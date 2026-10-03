package session

import (
	"sort"
	"strings"
)

// Rune caps for Session.Title / Session.Preview - list-row sized, so the
// app can show them without fetching or rendering the whole transcript.
const (
	titleMaxRunes   = 80
	previewMaxRunes = 120
)

// summarize derives Session.Title (first user message) and Session.Preview
// (last non-empty agent message) from a transcript.
func summarize(msgs []Message) (title, preview string) {
	for _, m := range msgs {
		if m.Role == "user" && strings.TrimSpace(m.Text) != "" {
			title = oneLine(m.Text, titleMaxRunes)
			break
		}
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "agent" && strings.TrimSpace(msgs[i].Text) != "" {
			preview = oneLine(msgs[i].Text, previewMaxRunes)
			break
		}
	}
	return title, preview
}

// oneLine collapses all whitespace runs (newlines included) to single spaces
// and truncates to max runes, ending in "…" when cut.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}

// ListAll returns sessions across every project, optionally restricted to
// the given states (nil/empty = all). Sorted for the app's "Needs you"
// strip: waiting first, then busy, then everything else; within a group,
// most recently active (lastActiveAt, else createdAt) first.
func (m *Manager) ListAll(states []string) []Session {
	m.mu.Lock()
	recs := make([]*record, 0, len(m.sessions))
	for _, rec := range m.sessions {
		recs = append(recs, rec)
	}
	m.mu.Unlock()

	want := make(map[string]bool, len(states))
	for _, st := range states {
		want[st] = true
	}
	out := make([]Session, 0, len(recs))
	for _, rec := range recs {
		s := rec.snapshot()
		if len(want) == 0 || want[s.State] {
			out = append(out, s)
		}
	}
	SortForAttention(out)
	return out
}

// SortForAttention orders sessions waiting > busy > rest, then by recency
// desc. Timestamps are UTC RFC3339 strings, so string order is time order.
func SortForAttention(sessions []Session) {
	rank := func(state string) int {
		switch state {
		case StateWaiting:
			return 0
		case StateBusy:
			return 1
		}
		return 2
	}
	recency := func(s Session) string {
		if s.LastActiveAt != "" {
			return s.LastActiveAt
		}
		return s.CreatedAt
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		ri, rj := rank(sessions[i].State), rank(sessions[j].State)
		if ri != rj {
			return ri < rj
		}
		return recency(sessions[i]) > recency(sessions[j])
	})
}
