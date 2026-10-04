// Package session implements session lifecycle: spawning a CLI agent
// subprocess per session, turning its output into a transcript, and feeding
// it user messages. Two kinds of provider:
//   - ACP providers (claude via claude-agent-acp, ...) - a structured
//     protocol with streaming text, tool-call progress, permission requests,
//     modes, and an explicit end-of-turn. See acp_session.go.
//   - raw providers (echo-agent, RELAY_PROVIDER_<NAME> overrides) - a line
//     in, lines out, no notion of turns.
//
// Limitation (by design, for this milestone): sessions live in memory only,
// guarded by a mutex. A runner restart loses all session state (transcripts,
// running processes). A real deployment would also persist sessions to
// disk, but that's out of scope here - keep it simple.
package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"relay/runner/internal/acp"
)

// Session states.
const (
	StateBusy     = "busy"
	StateIdle     = "idle"
	StateFinished = "finished"
	StateError    = "error"
	// StateWaiting: an ACP agent is paused on a permission request - waiting
	// on the human, not working, so it counts as idle for idle-suspend.
	StateWaiting = "waiting"
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
	// ErrTurnInProgress is returned by SendMessage while the agent is still
	// working on (or waiting for permission in) the previous turn.
	ErrTurnInProgress = errors.New("agent is still working on the previous message")
	// ErrNotSupported is returned by Cancel/SetMode/RespondPermission on a
	// raw (non-ACP) provider.
	ErrNotSupported = errors.New("not supported by this provider")
	// ErrInvalidChoice is returned for an unknown mode/permission option or
	// when no permission request is pending.
	ErrInvalidChoice = errors.New("invalid choice")
)

// Message is one transcript entry.
type Message struct {
	Role string `json:"role"` // "user" | "agent" | "tool"
	// Text is the message text, or for role "tool" a one-line title such as
	// "Write hello.txt" / "npm test".
	Text string `json:"text"`
	At   string `json:"at"` // RFC3339
	// ToolKind and Status are set for role "tool" only: kind is ACP's read,
	// edit, delete, move, search, execute, think, fetch, other; status is
	// pending, in_progress, completed, failed.
	ToolKind string `json:"toolKind,omitempty"`
	Status   string `json:"status,omitempty"`
	// Kind marks an agent message that's really a provider problem:
	// KindQuota or KindAuth (see problem.go). Empty for ordinary replies.
	Kind string `json:"kind,omitempty"`

	// ref is the ACP messageId (agent) or toolCallId (tool) that later
	// streamed updates are merged into. Internal only.
	ref string
}

// PendingPermission is an ACP agent's open permission request, answered via
// Manager.RespondPermission.
type PendingPermission struct {
	Title    string                 `json:"title"`
	ToolKind string                 `json:"toolKind"`
	Options  []acp.PermissionOption `json:"options"`
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
	// Mode/Modes: the agent's current and available permission modes (ACP
	// providers only), e.g. "bypassPermissions". Empty for raw providers.
	Mode              string             `json:"mode,omitempty"`
	Modes             []acp.Mode         `json:"modes,omitempty"`
	PendingPermission *PendingPermission `json:"pendingPermission,omitempty"`
	// ConfigOptions: model / effort / fast mode as the agent offers them
	// (ACP session config options), each with its current value.
	ConfigOptions []acp.ConfigOption `json:"configOptions,omitempty"`
	// LastActiveAt is when the session last changed, bumped every ~30s
	// while the agent works (see store.go). RFC3339.
	LastActiveAt string `json:"lastActiveAt,omitempty"`
	// Title and Preview are derived on every snapshot (see cloneSession /
	// summary.go), never set directly: the first user message and the last
	// agent message, each flattened to one line and truncated. "" if none.
	Title   string `json:"title"`
	Preview string `json:"preview"`
}

// ProviderCommand is the command run for a given provider name.
type ProviderCommand struct {
	Name string
	Args []string
	// ACP marks an Agent Client Protocol agent (see acp_session.go);
	// otherwise raw text, a line in and lines out.
	ACP bool
}

// record is the internal, mutable state for one session.
type record struct {
	mu   sync.Mutex
	data Session

	cmd   *exec.Cmd
	stdin io.WriteCloser // raw providers only; nil once the process has exited or Stop was called

	// ACP providers only (see acp_session.go). acp/acpSession are set once
	// at start and never change afterwards.
	acp        *acp.Client
	acpSession string
	turnActive bool            // a session/prompt call is in flight
	turnStart  time.Time       // when the in-flight turn began (sendACP); read by runTurn for OnTurn
	permID     json.RawMessage // JSON-RPC id of the open permission request, nil if none
	// dormant: an ACP session restored from disk (or whose agent process
	// died) with no process; the next message resumes it - see ensureAgent.
	dormant bool

	// Shared chats (shared.go). replaying: session/load is replaying the
	// conversation into data.Messages. transcriptSize/transcriptKnown: size
	// of the agent's own transcript file when the runner last caught up with
	// it; growth beyond it means someone else (VS Code) wrote to the chat.
	// transcriptPath caches where that file is. syncing guards Sync.
	// agentMu serializes ensureAgent (a turn and a Sync may both want to
	// spawn/reload the agent). Lock order: agentMu before mu, never the
	// other way round.
	agentMu         sync.Mutex
	replaying       bool
	replayMsgs      []Message
	transcriptSize  int64
	transcriptKnown bool
	transcriptPath  string
	syncing         bool

	// Persistence bookkeeping (store.go).
	saved        []byte    // last bytes written to disk
	savedContent []byte    // content() at that save, for change detection
	lastBeat     time.Time // last lastActiveAt bump
	// stopped marks that Stop() already finalized this session, so the
	// background reader goroutine (which observes process exit
	// independently) must not overwrite that final state.
	stopped bool
	// idleSince is the most recent moment this record stopped being busy -
	// a completed ACP turn or permission request (see acp_session.go),
	// a clean/errored process exit (awaitExit), or Stop() - whichever
	// happened most recently. Zero value means "still busy, never yet gone
	// idle." Read by Manager.IdleStatus; guarded by mu like the rest of the
	// record.
	idleSince time.Time
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
	// storeDir, if set, is where sessions are persisted (store.go).
	storeDir string
	// defaults/defaultsFile: config choices (model, effort) new sessions
	// start with - see config.go.
	defaults     []ConfigValue
	defaultsFile string

	// DefaultMode is the ACP mode new sessions are switched to right after
	// creation, if the agent offers it (env RELAY_DEFAULT_MODE, default
	// "bypassPermissions" - no permission prompts; the phone's Stop button
	// is the brake). Empty keeps the agent's own default.
	DefaultMode string

	// OnNeedsInput, if set, is called (in its own goroutine) when an ACP
	// agent pauses on a permission request - wired to a push notification
	// in cmd/runnerd/main.go.
	OnNeedsInput func(Session)

	// OnFinished, if set, is called (in its own goroutine, so it never
	// blocks or can fail the state transition itself) whenever a session
	// either completes a turn (an ACP session/prompt returning - once per
	// turn, not once per process lifetime) or reaches StateFinished (its
	// subprocess exited cleanly via awaitExit, or it was stopped via Stop).
	// This is how runner/internal/notify gets wired in to notify registered
	// devices of "your job is done"; session deliberately doesn't import
	// notify to avoid a dependency cycle/coupling - it just reports the fact
	// via this hook. For a long-lived ACP session the per-turn firing is the
	// meaningful one; a raw provider has no notion of turns, so it only ever
	// fires once, at process exit.
	OnFinished func(Session)

	// OnTurn, if set, is called (in its own goroutine) once per completed
	// ACP turn with when the user's message started it and when the agent
	// finished - however the turn ended (end_turn, cancelled, error). Wired
	// to internal/usage's recorder in cmd/runnerd/main.go; like OnFinished
	// it's a hook so session doesn't import usage.
	OnTurn func(sessionID string, start, end time.Time)
}

// NewManager creates a session Manager. resolveDir looks up a project's
// working directory by id; it's injected (rather than importing the
// project package directly) to keep session decoupled from project.
func NewManager(resolveDir ResolveProjectDir) *Manager {
	mode := os.Getenv("RELAY_DEFAULT_MODE")
	if mode == "" {
		mode = "bypassPermissions"
	}
	return &Manager{
		sessions:    make(map[string]*record),
		resolveDir:  resolveDir,
		providers:   defaultProviders(),
		createdAt:   time.Now(),
		DefaultMode: mode,
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

// autoDetectProviders maps a provider name the phone app is allowed to ask
// for to the command to run if it resolves on PATH right now - no manual
// setup needed as long as the CLI is installed. "claude" runs through the
// ACP adapter (`npm i -g @agentclientprotocol/claude-agent-acp`, needs Node
// >= 22), which uses the machine's existing `claude` login. "codex" is still
// the bare CLI over raw text - untested end-to-end; codex-acp is the planned
// replacement, see runner/FLOWS.md.
var autoDetectProviders = map[string]ProviderCommand{
	"claude": {Name: "claude-agent-acp", ACP: true},
	"codex":  {Name: "codex"},
}

// resolveProvider maps a provider name to a command, in order: (1)
// RELAY_ACP_<NAME> (name upper-cased, "-" -> "_") - path/command of an ACP
// agent, e.g. RELAY_ACP_CLAUDE=C:\tools\claude-agent-acp.cmd; (2)
// RELAY_PROVIDER_<NAME> as a raw shell command string; (3) a hardcoded
// provider (currently just "echo-agent", for tests); (4) autoDetectProviders
// - if the name is a known CLI and it resolves on PATH right now. Env
// overrides win over what's merely installed.
func (m *Manager) resolveProvider(name string) (ProviderCommand, error) {
	suffix := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	if acpCmd := os.Getenv("RELAY_ACP_" + suffix); acpCmd != "" {
		return ProviderCommand{Name: acpCmd, ACP: true}, nil
	}
	envKey := "RELAY_PROVIDER_" + suffix
	if cmdStr := os.Getenv(envKey); cmdStr != "" {
		return ProviderCommand{Name: "sh", Args: []string{"-c", cmdStr}}, nil
	}
	if pc, ok := m.providers[name]; ok {
		return pc, nil
	}
	if pc, ok := autoDetectProviders[name]; ok {
		if _, err := exec.LookPath(pc.Name); err == nil {
			return pc, nil
		}
		hint := envKey
		if pc.ACP {
			hint = "RELAY_ACP_" + suffix
		}
		return ProviderCommand{}, fmt.Errorf("%w: %q not found on PATH (install it, or set %s to its full path)", ErrUnknownProvider, pc.Name, hint)
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

func (m *Manager) start(projectID, provider, dir string) (Session, error) {
	m.mu.Lock()
	pc, err := m.resolveProvider(provider)
	if err != nil {
		m.mu.Unlock()
		return Session{}, err
	}
	id := m.newID()
	m.mu.Unlock()

	if pc.ACP {
		return m.startACP(id, projectID, provider, dir, pc)
	}

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

	// A raw provider has no per-turn signal, so it's busy from the moment
	// it's spawned until the process exits.
	startTime := nowT()
	rec := &record{
		data: Session{
			ID:        id,
			ProjectID: projectID,
			Provider:  provider,
			State:     StateBusy,
			CreatedAt: startTime.Format(time.RFC3339),
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

// pump reads a raw provider's combined stdout/stderr line by line,
// appending each line as an "agent" message.
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
	ts := nowT()
	tsStr := ts.Format(time.RFC3339)
	rec.data.FinishedAt = &tsStr
	if err != nil {
		rec.data.State = StateError
	} else {
		rec.data.State = StateFinished
	}
	rec.idleSince = ts
	rec.stdin = nil
	finished := cloneSession(rec.data)
	rec.mu.Unlock()

	if finished.State == StateFinished {
		m.notifyFinished(finished)
	}
}

// notifyFinished invokes OnFinished (if set) in its own goroutine, so a
// slow or failing notification path can never block session lifecycle
// transitions. Called both for a completed ACP turn and for a
// terminal StateFinished transition (awaitExit, Stop) - see OnFinished's
// doc comment for the full picture of when each fires.
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
// only hook runner/internal/idle uses to decide when to suspend to sleep
// (see ARCHITECTURE.md "Sleep path") - deliberately computed from existing
// session state rather than tracked as separate counters, so there's only
// one place session busy/idle/finished state lives.
//
// State is NOT monotonic busy -> {finished, error} for an ACP session: it
// goes busy -> idle (or waiting) -> busy ... once per turn (see
// acp_session.go), and may only ever reach
// finished/error when the subprocess itself exits or Stop is called - which,
// for a long-lived provider process, can be much later than its last busy
// period. So the moment the whole runner most recently stopped being busy
// isn't simply the latest FinishedAt among known sessions; each record
// tracks its own idleSince (updated on every busy exit - a completed turn,
// a clean/errored exit, or Stop; see record.idleSince), and the runner-wide
// idleSince is the max of those, or the time this Manager was constructed
// if no session has ever been created.
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
		recIdleSince := rec.idleSince
		rec.mu.Unlock()

		if state == StateBusy {
			return true, time.Time{}
		}
		if recIdleSince.After(idleSince) {
			idleSince = recIdleSince
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
	if rec.isACP() {
		return m.sendACP(rec, text)
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
	ts := nowT()
	tsStr := ts.Format(time.RFC3339)
	rec.data.State = StateFinished
	rec.data.FinishedAt = &tsStr
	rec.data.PendingPermission = nil
	rec.permID = nil
	rec.idleSince = ts
	rec.dormant = false
	var proc *os.Process
	if rec.cmd != nil {
		proc = rec.cmd.Process
	}
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
	// SetConfig updates option values in place; snapshots get their own copy.
	out.ConfigOptions = append([]acp.ConfigOption(nil), s.ConfigOptions...)
	if s.FinishedAt != nil {
		ts := *s.FinishedAt
		out.FinishedAt = &ts
	}
	out.Title, out.Preview = summarize(out.Messages)
	return out
}

func isTerminal(state string) bool {
	return state == StateFinished || state == StateError
}

// nowT is the time.Time counterpart to now() - used wherever a value needs
// to be both stored as a time.Time (e.g. record.idleSince, for comparisons)
// and formatted as an RFC3339 string (e.g. Session.CreatedAt/FinishedAt) from
// the exact same instant.
func nowT() time.Time {
	return time.Now().UTC()
}

func now() string {
	return nowT().Format(time.RFC3339)
}
