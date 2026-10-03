package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSummarize_TitleAndPreview(t *testing.T) {
	msgs := []Message{
		{Role: "agent", Text: "hello, I'm ready"},
		{Role: "user", Text: "  fix the\nlogin   bug  "},
		{Role: "tool", Text: "Edit login.go"},
		{Role: "agent", Text: "first reply"},
		{Role: "user", Text: "second ask"},
		{Role: "agent", Text: "done:\n- fixed it"},
		{Role: "agent", Text: "   "},
	}
	title, preview := summarize(msgs)
	if title != "fix the login bug" {
		t.Errorf("title = %q", title)
	}
	if preview != "done: - fixed it" {
		t.Errorf("preview = %q", preview)
	}
}

func TestSummarize_EmptyWhenNoMessages(t *testing.T) {
	title, preview := summarize(nil)
	if title != "" || preview != "" {
		t.Fatalf("got %q / %q, want empty", title, preview)
	}
	title, preview = summarize([]Message{{Role: "tool", Text: "Read x"}})
	if title != "" || preview != "" {
		t.Fatalf("tool-only: got %q / %q, want empty", title, preview)
	}
}

func TestOneLine_TruncatesByRunes(t *testing.T) {
	long := strings.Repeat("я", 200) // multi-byte: must cut on runes, not bytes
	got := oneLine(long, titleMaxRunes)
	if n := utf8.RuneCountInString(got); n != titleMaxRunes {
		t.Errorf("rune count = %d, want %d", n, titleMaxRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("missing ellipsis: %q", got)
	}
	exact := strings.Repeat("a", previewMaxRunes)
	if got := oneLine(exact, previewMaxRunes); got != exact {
		t.Errorf("exact-length text was truncated: %q", got)
	}
}

func TestCloneSession_FillsTitlePreview(t *testing.T) {
	s := cloneSession(Session{Messages: []Message{{Role: "user", Text: "hi"}, {Role: "agent", Text: "yo"}}})
	if s.Title != "hi" || s.Preview != "yo" {
		t.Fatalf("Title/Preview = %q/%q", s.Title, s.Preview)
	}
}

func TestListAll_FilterAndSort(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))
	add := func(id, project, state, created, active string) {
		m.sessions[id] = &record{data: Session{ID: id, ProjectID: project, State: state, CreatedAt: created, LastActiveAt: active}}
	}
	add("idle-new", "p1", StateIdle, "2026-01-01T00:00:09Z", "")
	add("busy-old", "p1", StateBusy, "2026-01-01T00:00:01Z", "2026-01-01T00:00:02Z")
	add("wait", "p2", StateWaiting, "2026-01-01T00:00:00Z", "")
	add("busy-new", "p2", StateBusy, "2026-01-01T00:00:01Z", "2026-01-01T00:00:05Z")
	add("fin", "p3", StateFinished, "2026-01-01T00:00:03Z", "")

	ids := func(ss []Session) string {
		var out []string
		for _, s := range ss {
			out = append(out, s.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(m.ListAll(nil)); got != "wait,busy-new,busy-old,idle-new,fin" {
		t.Errorf("ListAll(nil) = %s", got)
	}
	if got := ids(m.ListAll([]string{StateWaiting, StateBusy})); got != "wait,busy-new,busy-old" {
		t.Errorf("ListAll(waiting,busy) = %s", got)
	}
	if got := ids(m.ListAll([]string{StateError})); got != "" {
		t.Errorf("ListAll(error) = %s, want none", got)
	}
}
