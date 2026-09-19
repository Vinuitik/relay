// Package session implements session lifecycle: spawning a CLI agent
// subprocess per session, capturing its stdout into a transcript, and
// feeding it user messages via stdin.
//
// Limitation (by design, for this milestone): sessions live in memory only,
// guarded by a mutex. A runner restart loses all session state (transcripts,
// running processes). A real deployment would also persist sessions to
// disk, but that's out of scope here - keep it simple.
package session

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"relay/runner/internal/history"
)

// Session states.
const (
	StateBusy     = "busy"
	StateIdle     = "idle"
	StateFinished = "finished"
	StateError    = "error"
)

// Errors returned by Manager methods, mapped to HTTP status codes by the
// api package.
var (
	// ErrSessionNotFound is returned by Get/SendMessage/Stop for an unknown session id.
	ErrSessionNotFound = errors.New("session not found")
	// ErrProjectNotFound is returned by Start when the project id doesn't resolve.
	ErrProjectNotFound = errors.New("project not found")
	// ErrUnknownProvider is returned by Start for an unconfigured provider.
	ErrUnknownProvider = errors.New("unknown provider")
	// ErrSessionFinished is returned by SendMessage/Stop on an already-terminal session.
	ErrSessionFinished = errors.New("session already finished")
)

// Message is one transcript entry.
type Message struct {
	Role string `json:"role"` // "user" | "agent"
	Text string `json:"text"`
	At   string `json:"at"` // RFC3339
}

// Session matches the shape defined in shared/API.md.
type Session struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"projectId"`
	Provider   string    `json:"provider"`
	State      string    `json:"state"`
	CreatedAt  string    `json:"createdAt"`
	FinishedAt *string   `json:"finishedAt"`
	Messages   []Message `json:"messages"`
}

// ProviderCommand is the command run for a given provider name.
type ProviderCommand struct {
	Name string
	Args []string
}

// record is the internal, mutable state for one session.
type record struct {
	mu   sync.Mutex
	data Session

	cmd   *exec.Cmd
	stdin io.WriteCloser // nil once the process has exited or Stop was called
	// stopped marks that Stop() already finalized this session, so the
	// background reader goroutine (which observes process exit
	// independently) must not overwrite that final state.
	stopped bool
}

// ResolveProjectDir resolves a project id to its working directory.
type ResolveProjectDir func(projectID string) (dir string, ok bool)

// Manager tracks all sessions across all projects.
type Manager struct {
	mu         sync.Mutex
	sessions   map[string]*record
	resolveDir ResolveProjectDir
	providers  map[string]ProviderCommand
	nextID     uint64
	// createdAt is used by IdleStatus as the idle-since time when no
	// session has ever been created yet.
	createdAt time.Time

	// OnFinished, if set, is called (in its own goroutine, so it never
	// blocks or can fail the state transition itself) whenever a session
	// transitions to StateFinished - either because its subprocess exited
	// cleanly (awaitExit) or because it was stopped via Stop. This is how
	// runner/internal/notify gets wired in to notify registered devices;
	// session deliberately doesn't import notify to avoid a dependency
	// cycle/coupling - it just reports the fact via this hook.
	OnFinished func(Session)
}

// NewManager creates a session Manager. resolveDir looks up a project's
// working directory by id; it's injected (rather than importing the
// project package directly) to keep session decoupled from project.
func NewManager(resolveDir ResolveProjectDir) *Manager {
	return &Manager{
		sessions:   make(map[string]*record),
		resolveDir: resolveDir,
		providers:  defaultProviders(),
		createdAt:  time.Now(),
	}
}

// defaultProviders gives tests a trivial provider ("echo-agent") that
// doesn't require any real CLI agent to be installed: it just echoes
// stdin back on stdout.
func defaultProviders() map[string]ProviderCommand {
	return map[string]ProviderCommand{
		"echo-agent": {Name: "sh", Args: []string{"-c", "cat"}},
	}
}

// resolveProvider maps a provider name to a command. RELAY_PROVIDER_<NAME>
// (name upper-cased, "-" -> "_") overrides/adds a provider as a shell
// command string, e.g. RELAY_PROVIDER_CLAUDE="claude --project ." - this is
// how real agent CLIs (claude/codex) get wired up without hardcoding them.
func (m *Manager) resolveProvider(name string) (ProviderCommand, error) {
	envKey := "RELAY_PROVIDER_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	if cmdStr := os.Getenv(envKey); cmdStr != "" {
		return ProviderCommand{Name: "sh", Args: []string{"-c", cmdStr}}, nil
	}
	if pc, ok := m.providers[name]; ok {
		return pc, nil
	}
	return ProviderCommand{}, fmt.Errorf("%w: %q (configure it via %s)", ErrUnknownProvider, name, envKey)
}

func (m *Manager) newID() string {
	m.nextID++
	return fmt.Sprintf("s%d-%d", time.Now().UnixNano(), m.nextID)
}

// Start spawns the configured command for provider, scoped to projectID's
// directory, and begins capturing its stdout as agent messages.
func (m *Manager) Start(projectID, provider string) (Session, error) {
	dir, ok := m.resolveDir(projectID)
	if !ok {
		return Session{}, fmt.Errorf("%w: %q", ErrProjectNotFound, projectID)
	}
	return m.start(projectID, provider, dir)
}

// authLoginProvider is a pseudo-provider name, not one a real project session
// ever uses - it exists only to hang RELAY_PROVIDER_AUTH_LOGIN's env-var
// override off the same resolveProvider lookup every other provider uses.
const authLoginProvider = "auth-login"

// defaultAuthLoginCommand is what runs when RELAY_PROVIDER_AUTH_LOGIN isn't
// set - the `claude` CLI's own interactive OAuth login, confirmed (by hand,
// in an isolated container, 2026-09-19) to print a URL to stdout and then
// block reading an auth code from stdin, which is exactly the shape this
// package's existing stdout-pump / stdin-write session machinery already
// handles - no new subprocess mechanism needed, just a different command.
var defaultAuthLoginCommand = ProviderCommand{Name: "claude", Args: []string{"auth", "login"}}

// StartAuthLogin spawns the configured auth-login command (see
// defaultAuthLoginCommand) as a session with no associated project - it's a
// one-time, per-machine admin action, not a coding session, so it isn't
// scoped to any project directory. The resulting Session behaves exactly
// like a project session for polling/transcript/SendMessage purposes: the
// OAuth URL the CLI prints shows up as an "agent" message, and the code you
// paste back on the phone goes through the ordinary SendMessage -> stdin
// path. dir is the working directory the command runs in (does not need to
// be a registered project - typically the runner's own home directory).
func (m *Manager) StartAuthLogin(dir string) (Session, error) {
	m.mu.Lock()
	if _, ok := m.providers[authLoginProvider]; !ok {
		if m.providers == nil {
			m.providers = map[string]ProviderCommand{}
		}
		m.providers[authLoginProvider] = defaultAuthLoginCommand
	}
	m.mu.Unlock()
	return m.start("", authLoginProvider, dir)
}

func (m *Manager) start(projectID, provider, dir string) (Session, error) {
	m.mu.Lock()
	pc, err := m.resolveProvider(provider)
	if err != nil {
		m.mu.Unlock()
		return Session{}, err
	}
	id := m.newID()
	m.mu.Unlock()

	cmd := exec.Command(pc.Name, pc.Args...)
	cmd.Dir = dir

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return Session{}, fmt.Errorf("open stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Session{}, fmt.Errorf("open stdout pipe: %w", err)
	}
	// Merge stderr into the same transcript stream as stdout.
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return Session{}, fmt.Errorf("start provider %q: %w", provider, err)
	}

	rec := &record{
		data: Session{
			ID:        id,
			ProjectID: projectID,
			Provider:  provider,
			State:     StateBusy,
			CreatedAt: now(),
			Messages:  []Message{},
		},
		cmd:   cmd,
		stdin: stdinPipe,
	}

	m.mu.Lock()
	m.sessions[id] = rec
	m.mu.Unlock()

	go m.pump(rec, stdoutPipe)
	go m.awaitExit(rec)

	return rec.snapshot(), nil
}

// pump reads the subprocess's combined stdout/stderr line by line, appending
// each line as an "agent" message.
func (m *Manager) pump(rec *record, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		rec.appendMessage(Message{Role: "agent", Text: scanner.Text(), At: now()})
	}
}

// awaitExit waits for the subprocess to exit and finalizes session state,
// unless Stop() already finalized it first.
func (m *Manager) awaitExit(rec *record) {
	err := rec.cmd.Wait()

	rec.mu.Lock()
	if rec.stopped {
		rec.mu.Unlock()
		return
	}
	ts := now()
	rec.data.FinishedAt = &ts
	if err != nil {
		rec.data.State = StateError
	} else {
		rec.data.State = StateFinished
	}
	rec.stdin = nil
	finished := cloneSession(rec.data)
	rec.mu.Unlock()

	if finished.State == StateFinished {
		m.notifyFinished(finished)
	}
}

// notifyFinished invokes OnFinished (if set) in its own goroutine, so a
// slow or failing notification path can never block session lifecycle
// transitions.
func (m *Manager) notifyFinished(sess Session) {
	if m.OnFinished == nil {
		return
	}
	go m.OnFinished(sess)
}

// Get returns the current transcript/state of a session.
func (m *Manager) Get(sessionID string) (Session, error) {
	rec, err := m.lookup(sessionID)
	if err != nil {
		return Session{}, err
	}
	return rec.snapshot(), nil
}

// ListByProject returns all sessions (running or finished) for a project.
func (m *Manager) ListByProject(projectID string) []Session {
	m.mu.Lock()
	recs := make([]*record, 0, len(m.sessions))
	for _, rec := range m.sessions {
		recs = append(recs, rec)
	}
	m.mu.Unlock()

	out := make([]Session, 0, len(recs))
	for _, rec := range recs {
		s := rec.snapshot()
		if s.ProjectID == projectID {
			out = append(out, s)
		}
	}
	return out
}

// IdleStatus reports whether any session is currently busy and, if not, the
// time since which the runner has had no busy session at all. It's the read
// only hook runner/internal/idle uses to decide when to suspend to S5 (see
// ARCHITECTURE.md "Sleep path") - deliberately computed from existing
// session state rather than tracked as separate counters, so there's only
// one place session busy/idle/finished state lives.
//
// A session's State only ever moves busy -> {finished, error} (never back to
// busy - see the state constants above), so the moment the whole runner most
// recently became idle is exactly the latest FinishedAt among all known
// sessions, or, if no session has ever been created, the time this Manager
// was constructed.
func (m *Manager) IdleStatus() (busy bool, idleSince time.Time) {
	m.mu.Lock()
	recs := make([]*record, 0, len(m.sessions))
	for _, rec := range m.sessions {
		recs = append(recs, rec)
	}
	m.mu.Unlock()

	idleSince = m.createdAt
	for _, rec := range recs {
		rec.mu.Lock()
		state := rec.data.State
		finishedAt := rec.data.FinishedAt
		rec.mu.Unlock()

		if state == StateBusy {
			return true, time.Time{}
		}
		if finishedAt != nil {
			if t, err := time.Parse(time.RFC3339, *finishedAt); err == nil && t.After(idleSince) {
				idleSince = t
			}
		}
	}
	return false, idleSince
}

// SendMessage writes text (plus a trailing newline) to the session's
// subprocess stdin and appends it to the transcript as a "user" message.
func (m *Manager) SendMessage(sessionID, text string) error {
	rec, err := m.lookup(sessionID)
	if err != nil {
		return err
	}

	rec.mu.Lock()
	if isTerminal(rec.data.State) || rec.stdin == nil {
		rec.mu.Unlock()
		return ErrSessionFinished
	}
	stdin := rec.stdin
	rec.mu.Unlock()

	if _, err := io.WriteString(stdin, text+"\n"); err != nil {
		return fmt.Errorf("write to session stdin: %w", err)
	}
	rec.appendMessage(Message{Role: "user", Text: text, At: now()})
	return nil
}

// Stop kills the session's subprocess and marks it finished.
func (m *Manager) Stop(sessionID string) (Session, error) {
	rec, err := m.lookup(sessionID)
	if err != nil {
		return Session{}, err
	}

	rec.mu.Lock()
	if isTerminal(rec.data.State) {
		s := rec.data
		rec.mu.Unlock()
		return cloneSession(s), nil
	}
	rec.stopped = true
	ts := now()
	rec.data.State = StateFinished
	rec.data.FinishedAt = &ts
	proc := rec.cmd.Process
	rec.stdin = nil
	snapshot := cloneSession(rec.data)
	rec.mu.Unlock()

	if proc != nil {
		_ = proc.Kill()
	}
	m.notifyFinished(snapshot)
	return snapshot, nil
}

func (m *Manager) lookup(sessionID string) (*record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSessionNotFound, sessionID)
	}
	return rec, nil
}

// PurgeFinishedBefore drops finished/error sessions that finished before
// cutoff, delegating the keep/drop decision to the history package. It
// returns the number of sessions removed.
func (m *Manager) PurgeFinishedBefore(cutoff time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	infos := make([]history.SessionInfo, 0, len(m.sessions))
	for id, rec := range m.sessions {
		rec.mu.Lock()
		info := history.SessionInfo{ID: id, State: rec.data.State}
		if rec.data.FinishedAt != nil {
			if t, err := time.Parse(time.RFC3339, *rec.data.FinishedAt); err == nil {
				info.FinishedAt = &t
			}
		}
		rec.mu.Unlock()
		infos = append(infos, info)
	}

	kept := history.PurgeOlderThan(infos, cutoff)
	keep := make(map[string]bool, len(kept))
	for _, k := range kept {
		keep[k.ID] = true
	}

	removed := 0
	for id := range m.sessions {
		if !keep[id] {
			delete(m.sessions, id)
			removed++
		}
	}
	return removed
}

func (r *record) appendMessage(msg Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data.Messages = append(r.data.Messages, msg)
}

func (r *record) snapshot() Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneSession(r.data)
}

func cloneSession(s Session) Session {
	out := s
	out.Messages = make([]Message, len(s.Messages))
	copy(out.Messages, s.Messages)
	if s.FinishedAt != nil {
		ts := *s.FinishedAt
		out.FinishedAt = &ts
	}
	return out
}

func isTerminal(state string) bool {
	return state == StateFinished || state == StateError
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
