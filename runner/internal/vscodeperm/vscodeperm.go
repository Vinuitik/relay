// Package vscodeperm lets the phone answer Claude-in-VS-Code permission
// prompts (HANDOFF-chat-sync.md stage 3). "Race" design: the VS Code dialog
// may show at the same time; whichever answer comes first wins. If nobody
// answers on the phone in time the hook prints nothing and VS Code's normal
// flow continues.
//
//	hook:   `runnerd vscode-hook PermissionRequest` → Hook → POST /v1/vscode/permissions (long-poll)
//	runner: Store holds pending items in memory; phone lists + answers them
//
// The hook side FAILS OPEN: any problem → no output, exit 0.
package vscodeperm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultHoldSec is how long a permission request waits for the phone.
const DefaultHoldSec = 120

// Item is one pending permission question.
type Item struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Project   string `json:"project,omitempty"` // registered project id, if cwd matched
	ToolName  string `json:"toolName"`
	Summary   string `json:"summary"`
	CreatedAt string `json:"createdAt"` // RFC3339
}

// Decision is the phone's answer.
type Decision struct {
	Behavior string `json:"behavior"` // allow | deny
	Message  string `json:"message,omitempty"`
}

// Valid reports whether d is an answer we accept.
func (d Decision) Valid() bool { return d.Behavior == "allow" || d.Behavior == "deny" }

type entry struct {
	item Item
	ch   chan Decision
}

// Store is the in-memory pending list (lost on runner restart - the hook's
// poll fails then and VS Code falls back to its own dialog).
type Store struct {
	mu    sync.Mutex
	items map[string]*entry
	now   func() time.Time
}

// NewStore builds an empty Store.
func NewStore() *Store { return &Store{items: map[string]*entry{}, now: time.Now} }

// Add registers a pending item (ID and CreatedAt are assigned here).
func (s *Store) Add(it Item) Item {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	it.ID = hex.EncodeToString(b)
	it.CreatedAt = s.now().UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	s.items[it.ID] = &entry{item: it, ch: make(chan Decision, 1)}
	s.mu.Unlock()
	return it
}

// Wait blocks until the item is answered, hold elapses or ctx ends (hook went
// away). The item is removed on return either way.
func (s *Store) Wait(ctx context.Context, id string, hold time.Duration) (Decision, bool) {
	s.mu.Lock()
	e := s.items[id]
	s.mu.Unlock()
	if e == nil {
		return Decision{}, false
	}
	t := time.NewTimer(hold)
	defer t.Stop()
	select {
	case d := <-e.ch:
		return d, true
	case <-t.C:
	case <-ctx.Done():
	}
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
	select { // answered in the instant before removal
	case d := <-e.ch:
		return d, true
	default:
	}
	return Decision{}, false
}

// Answer delivers d to a pending item. False if it is gone or already answered.
func (s *Store) Answer(id string, d Decision) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.items[id]
	if e == nil {
		return false
	}
	select {
	case e.ch <- d:
		delete(s.items, id) // a second answer finds nothing
		return true
	default:
		return false
	}
}

// List returns pending items, oldest first.
func (s *Store) List() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Item, 0, len(s.items))
	for _, e := range s.items {
		out = append(out, e.item)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt < out[j-1].CreatedAt; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// HasSession reports whether any pending item belongs to the chat.
func (s *Store) HasSession(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.items {
		if e.item.SessionID == sessionID {
			return true
		}
	}
	return false
}

// ---- summary ----

const maxSummary = 100

// Summarize renders a short phone-sized description: "Bash: npm test",
// "Edit foo.go", "WebFetch: https://...".
func Summarize(tool string, input json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	str := func(k string) string { v, _ := m[k].(string); return strings.TrimSpace(v) }
	var s string
	switch tool {
	case "Bash":
		cmd := str("command")
		if i := strings.IndexByte(cmd, '\n'); i >= 0 {
			cmd = cmd[:i] + " ..."
		}
		s = "Bash: " + cmd
	case "Edit", "MultiEdit", "Write", "Read", "NotebookEdit":
		p := str("file_path")
		if p == "" {
			p = str("notebook_path")
		}
		s = tool + " " + filepath.Base(strings.ReplaceAll(p, "\\", "/"))
	case "WebFetch":
		s = "WebFetch: " + str("url")
	case "WebSearch":
		s = "WebSearch: " + str("query")
	default:
		s = tool
	}
	s = strings.TrimSpace(s)
	if s == "" {
		s = "permission request"
	}
	if r := []rune(s); len(r) > maxSummary {
		s = string(r[:maxSummary-1]) + "…"
	}
	return s
}

// MatchProject returns the id of the registered project whose path is the
// longest prefix of cwd ("" if none). paths maps project id → path.
func MatchProject(cwd string, paths map[string]string) string {
	norm := func(p string) string {
		return strings.ToLower(strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/"))
	}
	c := norm(cwd)
	best, bestLen := "", -1
	for id, p := range paths {
		n := norm(p)
		if n == "" {
			continue
		}
		if (c == n || strings.HasPrefix(c, n+"/")) && len(n) > bestLen {
			best, bestLen = id, len(n)
		}
	}
	return best
}

// ---- debounce ----

// Debouncer allows one event per key per window.
type Debouncer struct {
	mu     sync.Mutex
	last   map[string]time.Time
	Window time.Duration
}

// NewDebouncer builds a Debouncer with the given window.
func NewDebouncer(w time.Duration) *Debouncer {
	return &Debouncer{last: map[string]time.Time{}, Window: w}
}

// Allow reports whether key may fire at now, and records it if so.
func (d *Debouncer) Allow(key string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.last[key]; ok && now.Sub(t) < d.Window {
		return false
	}
	d.last[key] = now
	return true
}
