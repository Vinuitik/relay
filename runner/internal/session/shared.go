package session

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"relay/runner/internal/acp"
)

// Shared chats: the same conversation continued from the phone and from VS
// Code on the runner's machine (see runner/FLOWS.md "Shared chats").
//
// The agent's own transcript (Claude: ~/.claude/projects/<dir>/<id>.jsonl)
// is the single source of truth; data.Messages is a cache rebuilt from it
// by replaying with session/load. The runner never parses that file - it
// finds it by id, watches its size, and checks appended bytes for a turn
// marker (hasTurn):
//
//	ListAgentChats: spawn agent → session/list(cwd) → kill (linked ones marked)
//	Adopt:          spawn agent → session/load (replay → Messages) → idle
//	ensureAgent:    transcript grew since markTranscript? → kill agent → session/load
//	Sync (GET):     same check while idle, so the phone shows VS Code's turns on open
//
// Forks (both sides writing at once) are tolerated, not detected - see FLOWS.

// AgentChat is one conversation the agent has saved for a project folder.
type AgentChat struct {
	AgentSessionID string `json:"agentSessionId"`
	Title          string `json:"title"`
	UpdatedAt      string `json:"updatedAt"`
	// SessionID is the Relay session already showing this chat, "" if none.
	SessionID string `json:"sessionId,omitempty"`
}

// maxListPages bounds session/list paging (claude-agent-acp pages hold 1000).
const maxListPages = 5

// ListAgentChats asks provider's agent for every conversation saved for
// projectID's folder - the ones started in VS Code as well as Relay's own.
// It spawns a short-lived agent process just for the listing.
func (m *Manager) ListAgentChats(projectID, provider string) ([]AgentChat, error) {
	dir, ok := m.resolveDir(projectID)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrProjectNotFound, projectID)
	}
	m.mu.Lock()
	pc, err := m.resolveProvider(provider)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !pc.ACP {
		return nil, ErrNotSupported
	}
	client, err := acp.Start(pc.Name, pc.Args, dir, acp.Handlers{})
	if err != nil {
		return nil, fmt.Errorf("start provider %q: %w", provider, err)
	}
	defer func() {
		if p := client.Cmd().Process; p != nil {
			_ = p.Kill()
		}
		<-client.Done()
		_ = client.Cmd().Wait()
	}()
	if err := client.Initialize(clientName, clientVersion); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	var infos []acp.SessionInfo
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		got, next, err := client.ListSessions(dir, cursor)
		if err != nil {
			return nil, fmt.Errorf("session/list: %w", err)
		}
		infos = append(infos, got...)
		if next == "" {
			break
		}
		cursor = next
	}

	linked := m.linkedSessions()
	chats := make([]AgentChat, 0, len(infos))
	for _, in := range infos {
		chats = append(chats, AgentChat{
			AgentSessionID: in.SessionID,
			Title:          in.Title,
			UpdatedAt:      in.UpdatedAt,
			SessionID:      linked[in.SessionID],
		})
	}
	return chats, nil
}

// linkedSessions maps agent session id → the live (non-finished) Relay
// session showing it.
func (m *Manager) linkedSessions() map[string]string {
	m.mu.Lock()
	recs := make([]*record, 0, len(m.sessions))
	for _, rec := range m.sessions {
		recs = append(recs, rec)
	}
	m.mu.Unlock()
	out := map[string]string{}
	for _, rec := range recs {
		rec.mu.Lock()
		if rec.acpSession != "" && !isTerminal(rec.data.State) {
			out[rec.acpSession] = rec.data.ID
		}
		rec.mu.Unlock()
	}
	return out
}

// Adopt opens one of the agent's saved conversations (e.g. started in VS
// Code) as a Relay session, its history replayed into the transcript. If a
// Relay session already shows it, that one is returned with existing=true.
func (m *Manager) Adopt(projectID, provider, agentSessionID string) (sess Session, existing bool, err error) {
	if id, ok := m.linkedSessions()[agentSessionID]; ok {
		s, err := m.Get(id)
		return s, true, err
	}
	dir, ok := m.resolveDir(projectID)
	if !ok {
		return Session{}, false, fmt.Errorf("%w: %q", ErrProjectNotFound, projectID)
	}
	m.mu.Lock()
	pc, err := m.resolveProvider(provider)
	if err != nil {
		m.mu.Unlock()
		return Session{}, false, err
	}
	id := m.newID()
	m.mu.Unlock()
	if !pc.ACP {
		return Session{}, false, ErrNotSupported
	}

	startTime := nowT()
	rec := &record{
		data: Session{
			ID:        id,
			ProjectID: projectID,
			Provider:  provider,
			State:     StateIdle,
			CreatedAt: startTime.Format(time.RFC3339),
			Messages:  []Message{},
		},
		idleSince: startTime,
	}
	client, sid, setup, err := m.spawnAgent(rec, pc, dir, agentSessionID, true)
	if err != nil {
		return Session{}, false, fmt.Errorf("open chat %q: %w", agentSessionID, err)
	}
	m.attach(rec, client, sid, setup, m.DefaultMode, m.configDefaults())
	m.markTranscript(rec)

	m.mu.Lock()
	m.sessions[id] = rec
	m.mu.Unlock()
	return rec.snapshot(), false, nil
}

// Sync catches an idle ACP session up with writes made elsewhere (VS Code)
// before the phone reads it: if the transcript grew, the conversation is
// reloaded (a few seconds - it spawns the agent). No-op while a turn runs,
// for raw sessions, or when another Sync is already reloading.
func (m *Manager) Sync(sessionID string) error {
	rec, err := m.lookup(sessionID)
	if err != nil {
		return err
	}
	rec.mu.Lock()
	skip := rec.acpSession == "" || rec.turnActive || rec.syncing || isTerminal(rec.data.State)
	if !skip {
		rec.syncing = true
	}
	rec.mu.Unlock()
	if skip || !m.transcriptStale(rec) {
		if !skip {
			rec.mu.Lock()
			rec.syncing = false
			rec.mu.Unlock()
		}
		return nil
	}
	_, err = m.ensureAgent(rec)
	rec.mu.Lock()
	rec.syncing = false
	rec.mu.Unlock()
	return err
}

// replay runs session/load on a fresh agent, building the transcript aside
// (onUpdate writes to rec.replayMsgs while rec.replaying) and swapping it in
// on success. A turn waiting to send keeps its user message at the end -
// it isn't in the agent's transcript yet.
func (m *Manager) replay(rec *record, client *acp.Client, sessionID, dir string) (acp.SessionSetup, error) {
	rec.mu.Lock()
	rec.replaying = true
	rec.replayMsgs = []Message{}
	rec.mu.Unlock()

	setup, err := client.LoadSession(sessionID, dir)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	msgs := rec.replayMsgs
	rec.replaying = false
	rec.replayMsgs = nil
	if err != nil {
		return acp.SessionSetup{}, err
	}
	for i := range msgs {
		if msgs[i].Role == "tool" && (msgs[i].Status == "pending" || msgs[i].Status == "in_progress") {
			msgs[i].Status = "failed"
		}
	}
	if old := rec.data.Messages; rec.turnActive && len(old) > 0 && old[len(old)-1].Role == "user" {
		msgs = append(msgs, old[len(old)-1])
	}
	rec.data.Messages = msgs
	return setup, nil
}

// transcriptStale reports whether a conversation turn was appended to rec's
// agent transcript since markTranscript - i.e. someone else continued the
// chat. Growth that is only bookkeeping (Claude appends "mode"/"cost-state"
// lines on load and on exit) is caught up silently, as is a session first
// seen without a known size (persisted before this feature).
func (m *Manager) transcriptStale(rec *record) bool {
	size, ok := m.transcriptFileSize(rec)
	if !ok {
		return false
	}
	rec.mu.Lock()
	known, from, path := rec.transcriptKnown, rec.transcriptSize, rec.transcriptPath
	rec.mu.Unlock()
	if known && size <= from {
		return false
	}
	if known && size > from && hasTurn(path, from, size) {
		return true
	}
	rec.mu.Lock()
	rec.transcriptSize, rec.transcriptKnown = size, true
	rec.mu.Unlock()
	return false
}

// turnMarkers are the line types Claude Code writes for conversation turns.
// The only knowledge of the transcript's format the runner has: if Claude
// renamed them, laptop turns would stop being picked up (never corrupted).
var turnMarkers = [][]byte{[]byte(`"type":"user"`), []byte(`"type":"assistant"`)}

// maxScan bounds how much appended transcript hasTurn reads; more than that
// is assumed to contain a turn.
const maxScan = 32 << 20

// hasTurn reports whether bytes [from, to) of path contain a turn line.
func hasTurn(path string, from, to int64) bool {
	if to-from > maxScan {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, to-from)
	n, _ := f.ReadAt(buf, from)
	for _, mk := range turnMarkers {
		if bytes.Contains(buf[:n], mk) {
			return true
		}
	}
	return false
}

// markTranscript records the transcript's current size as caught up.
func (m *Manager) markTranscript(rec *record) {
	size, ok := m.transcriptFileSize(rec)
	if !ok {
		return
	}
	rec.mu.Lock()
	rec.transcriptSize, rec.transcriptKnown = size, true
	rec.mu.Unlock()
}

// transcriptFileSize stats rec's agent transcript, locating it on first use.
func (m *Manager) transcriptFileSize(rec *record) (int64, bool) {
	rec.mu.Lock()
	sid, path := rec.acpSession, rec.transcriptPath
	rec.mu.Unlock()
	if sid == "" {
		return 0, false
	}
	if path == "" {
		if path = findTranscript(sid); path == "" {
			return 0, false
		}
		rec.mu.Lock()
		rec.transcriptPath = path
		rec.mu.Unlock()
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return fi.Size(), true
}

var agentSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// findTranscript locates Claude Code's transcript for a session id:
// <config dir>/projects/<encoded cwd>/<id>.jsonl, config dir being
// $CLAUDE_CONFIG_DIR or ~/.claude. Matching by id (ids are UUIDs) avoids
// re-implementing Claude's folder-name encoding. "" if not found - other
// agents simply never look stale.
func findTranscript(sessionID string) string {
	if !agentSessionIDPattern.MatchString(sessionID) {
		return ""
	}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".claude")
	}
	matches, err := filepath.Glob(filepath.Join(dir, "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	if len(matches) > 1 {
		log.Printf("sessions: %d transcripts for %s, using %s", len(matches), sessionID, matches[0])
	}
	return matches[0]
}
