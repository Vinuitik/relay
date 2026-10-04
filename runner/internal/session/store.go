package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session persistence: each session is one JSON file under
// $RELAY_HOME/sessions/, on the same machine the agent runs on. A background
// loop (RunSaver) writes a session's file whenever its content changed, and
// while the agent is working it also bumps lastActiveAt every heartbeat so
// the file shows the session was alive until at least then.
//
// On startup LoadPersisted brings every file back: finished sessions stay
// finished, ACP sessions come back "dormant" (idle, no process) and are
// resumed with ACP session/resume on the next message - see
// acp_session.go's ensureAgent. Raw-provider sessions can't be resumed and
// come back finished.

// saveInterval is how often RunSaver checks for changes; heartbeatInterval
// is how often lastActiveAt is bumped while a session is busy.
const (
	saveInterval      = 2 * time.Second
	heartbeatInterval = 30 * time.Second
)

// persisted is the on-disk form: the API Session plus what's needed to
// resume the agent's own conversation.
type persisted struct {
	Session
	ACPSessionID string `json:"acpSessionId,omitempty"`
	// ACP marks a session that ran over ACP and can be resumed.
	ACP bool `json:"acp,omitempty"`
	// TranscriptSize: see record.transcriptSize (shared.go). Pointer so an
	// older file without it reads as "unknown", not "empty transcript".
	TranscriptSize *int64 `json:"transcriptSize,omitempty"`
}

// SetStoreDir enables persistence into dir (created if missing). Without
// it (tests, or an unwritable home) sessions stay memory-only.
func (m *Manager) SetStoreDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create sessions dir %q: %w", dir, err)
	}
	m.loadConfigDefaults(dir)
	m.mu.Lock()
	m.storeDir = dir
	m.mu.Unlock()
	return nil
}

// LoadPersisted reads every saved session back into the Manager. Call once
// at startup, after SetStoreDir and before serving requests.
func (m *Manager) LoadPersisted() error {
	m.mu.Lock()
	dir := m.storeDir
	m.mu.Unlock()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read sessions dir: %w", err)
	}
	loaded := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			log.Printf("sessions: skip %s: %v", e.Name(), err)
			continue
		}
		var p persisted
		if err := json.Unmarshal(data, &p); err != nil || p.ID == "" {
			log.Printf("sessions: skip unreadable %s: %v", e.Name(), err)
			continue
		}
		rec := restoreRecord(p)
		// Count the restored state as already saved, so the first save
		// pass doesn't stamp every old session with "last active: now".
		rec.saved = data
		rec.savedContent, _ = rec.content()
		m.mu.Lock()
		m.sessions[p.ID] = rec
		m.mu.Unlock()
		loaded++
	}
	if loaded > 0 {
		log.Printf("sessions: restored %d session(s) from %s", loaded, dir)
	}
	return nil
}

// restoreRecord turns a saved session into a live record without a
// process. Whatever was in flight when the runner stopped is over: a
// pending permission is dropped and running tool steps are marked failed.
func restoreRecord(p persisted) *record {
	s := p.Session
	s.PendingPermission = nil
	for i := range s.Messages {
		if s.Messages[i].Role == "tool" && (s.Messages[i].Status == "pending" || s.Messages[i].Status == "in_progress") {
			s.Messages[i].Status = "failed"
		}
	}
	rec := &record{data: s}
	if last, err := time.Parse(time.RFC3339, s.LastActiveAt); err == nil {
		rec.idleSince = last
	}
	switch {
	case isTerminal(s.State):
		// stays finished/error
	case p.ACP && p.ACPSessionID != "":
		rec.dormant = true
		rec.acpSession = p.ACPSessionID
		if p.TranscriptSize != nil {
			rec.transcriptSize, rec.transcriptKnown = *p.TranscriptSize, true
		}
		if s.State != StateIdle {
			s.Messages = append(s.Messages, Message{Role: "agent", Text: "(interrupted - the runner restarted)", At: now()})
		}
		s.State = StateIdle
	default:
		ts := now()
		s.State = StateFinished
		s.FinishedAt = &ts
	}
	rec.data = s
	return rec
}

// RunSaver persists changed sessions every saveInterval until stop is
// closed, then does a final flush. Run it in its own goroutine.
func (m *Manager) RunSaver(stop <-chan struct{}) {
	t := time.NewTicker(saveInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			m.SaveAll()
			return
		case <-t.C:
			m.SaveAll()
		}
	}
}

// SaveAll writes every session whose content changed since its last save,
// bumping lastActiveAt on change and, for busy sessions, every heartbeat.
func (m *Manager) SaveAll() {
	m.mu.Lock()
	dir := m.storeDir
	recs := make([]*record, 0, len(m.sessions))
	for _, rec := range m.sessions {
		recs = append(recs, rec)
	}
	m.mu.Unlock()
	if dir == "" {
		return
	}
	for _, rec := range recs {
		m.saveRecord(dir, rec)
	}
}

// content is the record's persisted form without lastActiveAt - what
// "changed since last save" compares, so bumping the timestamp doesn't
// itself count as a change. Caller holds rec.mu (or owns rec exclusively).
func (rec *record) content() ([]byte, persisted) {
	p := persisted{Session: cloneSession(rec.data), ACPSessionID: rec.acpSession, ACP: rec.acpSession != ""}
	if rec.transcriptKnown {
		size := rec.transcriptSize
		p.TranscriptSize = &size
	}
	p.LastActiveAt = ""
	b, err := json.Marshal(p)
	if err != nil {
		return nil, p
	}
	return b, p
}

func (m *Manager) saveRecord(dir string, rec *record) {
	rec.mu.Lock()
	content, p := rec.content()
	if content == nil {
		rec.mu.Unlock()
		return
	}
	changed := !bytes.Equal(content, rec.savedContent)
	heartbeat := rec.data.State == StateBusy && time.Since(rec.lastBeat) >= heartbeatInterval
	if !changed && !heartbeat && rec.saved != nil {
		rec.mu.Unlock()
		return
	}
	nowTS := nowT()
	rec.data.LastActiveAt = nowTS.Format(time.RFC3339)
	rec.lastBeat = nowTS
	p.LastActiveAt = rec.data.LastActiveAt
	full, err := json.MarshalIndent(p, "", " ")
	rec.savedContent = content
	rec.saved = full
	rec.mu.Unlock()
	if err != nil {
		return
	}

	path := filepath.Join(dir, p.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, full, 0o600); err != nil {
		log.Printf("sessions: save %s: %v", p.ID, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("sessions: save %s: %v", p.ID, err)
	}
}
