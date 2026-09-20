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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
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
	// Codec selects how SendMessage/pump encode stdin and decode stdout for
	// this provider - "" means raw text (a line in, a line out), matching
	// the package doc comment's original assumption. See codecClaudeStreamJSON.
	Codec string
}

// codecClaudeStreamJSON is `claude`'s `--input-format=stream-json
// --output-format=stream-json` wire format - confirmed by hand (2026-09-19,
// piping one probe message through `claude --print --input-format=stream-json
// --output-format=stream-json --verbose`) rather than assumed. Plain
// `claude` with piped stdin and no flags is NOT this: it silently runs in
// one-shot `--print` mode and errors if no input arrives immediately,
// which is what a bare auto-detected `claude` did the first time this was
// tried - the flags plus this codec are both required together, not
// optional extras.
const codecClaudeStreamJSON = "claude-stream-json"

// record is the internal, mutable state for one session.
type record struct {
	mu   sync.Mutex
	data Session

	cmd   *exec.Cmd
	stdin io.WriteCloser // nil once the process has exited or Stop was called
	// codec is set once at Start and never changes for the session's
	// lifetime, so it's read without rec.mu held (same as cmd/data.Provider).
	codec string
	// stopped marks that Stop() already finalized this session, so the
	// background reader goroutine (which observes process exit
	// independently) must not overwrite that final state.
	stopped bool
	// idleSince is the most recent moment this record stopped being busy -
	// a completed turn (claude-stream-json "result" event, see finishTurn),
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

	// OnFinished, if set, is called (in its own goroutine, so it never
	// blocks or can fail the state transition itself) whenever a session
	// either completes a turn (a claude-stream-json "result" event - see
	// finishTurn, called once per turn, not once per process lifetime) or
	// reaches StateFinished (its subprocess exited cleanly via awaitExit, or
	// it was stopped via Stop). This is how runner/internal/notify gets
	// wired in to notify registered devices of "your job is done"; session
	// deliberately doesn't import notify to avoid a dependency
	// cycle/coupling - it just reports the fact via this hook. For a
	// persistent claude-stream-json session the per-turn firing is the
	// meaningful one, since the subprocess stays alive across turns and may
	// never reach StateFinished at all; a raw-codec provider has no notion
	// of turns, so it only ever fires once, at process exit.
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

// autoDetectProviders maps a provider name the phone app is allowed to ask
// for to the bare CLI command name to look up on PATH - no manual
// RELAY_PROVIDER_<NAME> setup needed on a fresh deployment as long as the
// CLI is actually installed. Bare names (no args) deliberately reuse
// autoDetectProviders maps a provider name the phone app is allowed to ask
// for to the command to run if it resolves on PATH right now - no manual
// RELAY_PROVIDER_<NAME> setup needed on a fresh deployment as long as the
// CLI is actually installed. "claude" needs the stream-json flags (see
// codecClaudeStreamJSON) to hold a persistent multi-turn conversation over
// one piped stdin instead of exiting after one one-shot --print call.
// "codex" has no confirmed equivalent wire format yet, so it stays raw
// text/no-args for now - untested end-to-end, see runner/FLOWS.md.
var autoDetectProviders = map[string]ProviderCommand{
	"claude": {
		Name:  "claude",
		Args:  []string{"--print", "--input-format=stream-json", "--output-format=stream-json", "--verbose"},
		Codec: codecClaudeStreamJSON,
	},
	"codex": {Name: "codex"},
}

// resolveProvider maps a provider name to a command, in order: (1)
// RELAY_PROVIDER_<NAME> (name upper-cased, "-" -> "_") as a shell command
// string, e.g. RELAY_PROVIDER_CLAUDE="claude --project ." - still the way
// to override the bare command, pass extra args, or opt out of the
// stream-json codec (an env override always gets Codec: "", raw text - see
// the doc comment above codecClaudeStreamJSON if you need to override
// *and* keep the codec, which isn't supported today); (2) a hardcoded
// provider (currently just "echo-agent", for tests); (3) autoDetectProviders
// - if the name is a known CLI and it resolves on PATH right now, use its
// pre-configured command+codec. Checked last, not first, so an explicit env
// override always wins over what's merely installed.
func (m *Manager) resolveProvider(name string) (ProviderCommand, error) {
	envKey := "RELAY_PROVIDER_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
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
		return ProviderCommand{}, fmt.Errorf("%w: %q CLI not found on PATH (install it, or set %s to its full path)", ErrUnknownProvider, pc.Name, envKey)
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

	// A claude-stream-json process is long-lived across turns: once spawned
	// it sits waiting for the first stdin message, so it starts idle, not
	// busy - only SendMessage (a turn starting) makes it busy. A raw-codec
	// provider (e.g. echo-agent, or an unconfigured CLI) keeps the original
	// behavior: busy from the moment it's spawned until the process exits,
	// since there's no per-turn signal (like the "result" event) to know it
	// ever goes idle in between.
	startTime := nowT()
	initialState := StateBusy
	if pc.Codec == codecClaudeStreamJSON {
		initialState = StateIdle
	}

	rec := &record{
		data: Session{
			ID:        id,
			ProjectID: projectID,
			Provider:  provider,
			State:     initialState,
			CreatedAt: startTime.Format(time.RFC3339),
			Messages:  []Message{},
		},
		cmd:   cmd,
		stdin: stdinPipe,
		codec: pc.Codec,
	}
	if initialState != StateBusy {
		rec.idleSince = startTime
	}

	m.mu.Lock()
	m.sessions[id] = rec
	m.mu.Unlock()

	go m.pump(rec, stdoutPipe)
	go m.awaitExit(rec)

	return rec.snapshot(), nil
}

// pump reads the subprocess's combined stdout/stderr line by line, decoding
// each line per rec.codec before appending it as an "agent" message. Raw
// codec (default) appends the line as-is; codecClaudeStreamJSON parses each
// line as a claude-cli stream-json event and only surfaces assistant text
// (see decodeClaudeStreamJSONLine) - a line that doesn't decode to visible
// text (e.g. the "system" event type, or malformed JSON) is dropped from
// the transcript rather than shown raw, so the phone doesn't get an
// unreadable JSON blob in the chat. The "result" event is handled
// separately, not dropped: it's the wire format's end-of-turn marker, so it
// drives finishTurn (busy -> idle, plus the per-turn OnFinished/job-done
// signal) instead of ever being appended to the transcript.
func (m *Manager) pump(rec *record, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if rec.codec == codecClaudeStreamJSON {
			if eventType, ok := decodeClaudeStreamJSONEventType(line); ok && eventType == "result" {
				m.finishTurn(rec)
				continue
			}
			if text, ok := decodeClaudeStreamJSONLine(line); ok {
				rec.appendMessage(Message{Role: "agent", Text: text, At: now()})
			}
			continue
		}
		rec.appendMessage(Message{Role: "agent", Text: line, At: now()})
	}
}

// finishTurn marks a claude-stream-json session idle again after a
// completed agent turn (a "result" event - see pump) and fires the
// OnFinished hook once for that turn - this is what actually sends the
// job-done push, see OnFinished's doc comment. Guarded on the record still
// being StateBusy, so a stray/duplicate "result" line (already idle) or one
// that races the process exiting (already finished/error) never re-fires
// the hook or clobbers a terminal state.
func (m *Manager) finishTurn(rec *record) {
	rec.mu.Lock()
	if rec.data.State != StateBusy {
		rec.mu.Unlock()
		return
	}
	rec.data.State = StateIdle
	rec.idleSince = nowT()
	snapshot := cloneSession(rec.data)
	rec.mu.Unlock()

	m.notifyFinished(snapshot)
}

// claudeStreamJSONContentBlock is one entry of a stream-json message's
// "content" array. Non-text blocks (e.g. tool_use) are silently dropped by
// both the encoder (never produced - user messages here are plain text)
// and the decoder (ignored - see decodeClaudeStreamJSONLine's doc comment)
// - `[NOT IMPLEMENTED]`: surfacing tool calls/results in the phone
// transcript, only the assistant's own text is shown for v1.
type claudeStreamJSONContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// encodeClaudeStreamJSONUserMessage builds one `claude --input-format
// stream-json` input line for a plain-text user message - the shape
// confirmed by hand against a real `claude --print --input-format=stream-json
// --output-format=stream-json` process (2026-09-19).
func encodeClaudeStreamJSONUserMessage(text string) (string, error) {
	payload := struct {
		Type    string `json:"type"`
		Message struct {
			Role    string                         `json:"role"`
			Content []claudeStreamJSONContentBlock `json:"content"`
		} `json:"message"`
	}{Type: "user"}
	payload.Message.Role = "user"
	payload.Message.Content = []claudeStreamJSONContentBlock{{Type: "text", Text: text}}

	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode claude stream-json user message: %w", err)
	}
	return string(b), nil
}

// decodeClaudeStreamJSONEventType extracts just the top-level "type" field
// from a `claude --output-format=stream-json` line - used by pump to detect
// the "result" event (the wire format's end-of-turn marker) before falling
// through to decodeClaudeStreamJSONLine's assistant-text-only parsing.
// ok=false means the line isn't valid JSON at all.
func decodeClaudeStreamJSONEventType(line string) (eventType string, ok bool) {
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return "", false
	}
	return event.Type, true
}

// decodeClaudeStreamJSONLine parses one `claude --output-format=stream-json`
// output line and, if it's an "assistant" event, returns its text content
// blocks concatenated. Every other observed event type ("system" init,
// "rate_limit_event", the final "result" summary - handled separately by
// decodeClaudeStreamJSONEventType/finishTurn, not here) and any line that
// fails to parse as JSON at all return ok=false - dropped from the
// transcript rather than shown as raw JSON (see pump's doc comment).
func decodeClaudeStreamJSONLine(line string) (text string, ok bool) {
	var event struct {
		Type    string `json:"type"`
		Message *struct {
			Content []claudeStreamJSONContentBlock `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return "", false
	}
	if event.Type != "assistant" || event.Message == nil {
		return "", false
	}
	var sb strings.Builder
	for _, block := range event.Message.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	if sb.Len() == 0 {
		return "", false
	}
	return sb.String(), true
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
// transitions. Called both for a completed turn (finishTurn) and for a
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
// State is NOT monotonic busy -> {finished, error} for a claude-stream-json
// session: it goes busy -> idle -> busy -> idle ... once per turn (see
// SendMessage and pump's "result"-event handling), and may only ever reach
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

	rec.mu.Lock()
	if isTerminal(rec.data.State) || rec.stdin == nil {
		rec.mu.Unlock()
		return ErrSessionFinished
	}
	stdin := rec.stdin
	codec := rec.codec
	// A turn is starting: mark busy now, before writing to stdin - not
	// after. A fast-replying subprocess could otherwise have pump observe
	// the matching "result" event (finishTurn) before this goroutine gets
	// back around to flipping the state post-write; finishTurn's StateBusy
	// guard would then see "not busy yet" and silently no-op, and this
	// write's later Busy transition would land *after* that, leaving the
	// session stuck busy forever for a turn that already finished. Setting
	// it first closes that race. No-op for raw-codec providers, which are
	// already busy from Start.
	rec.data.State = StateBusy
	rec.mu.Unlock()

	payload := text
	if codec == codecClaudeStreamJSON {
		encoded, err := encodeClaudeStreamJSONUserMessage(text)
		if err != nil {
			return err
		}
		payload = encoded
	}

	if _, err := io.WriteString(stdin, payload+"\n"); err != nil {
		return fmt.Errorf("write to session stdin: %w", err)
	}

	// The transcript always stores the plain text the user typed, never the
	// wire-format envelope - the phone should never see raw stream-json.
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
	rec.idleSince = ts
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
