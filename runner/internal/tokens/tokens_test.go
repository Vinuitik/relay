package tokens

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func asst(id, model, ts string, out, cw, cr int, tools ...string) string {
	var blocks []string
	for _, t := range tools {
		blocks = append(blocks, fmt.Sprintf(`{"type":"tool_use","name":%q}`, t))
	}
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"cwd":"C:/Users/x/Relay","message":{"id":%q,"model":%q,"content":[%s],"usage":{"input_tokens":3,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`,
		ts, id, model, strings.Join(blocks, ","), out, cw, cr)
}

func write(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
}

func scanner(dir string) *Scanner {
	s := New(func() string { return dir })
	s.Now = func() time.Time { return now }
	return s
}

func TestReportDedupeSubagentsAndWindow(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "projects", "enc-relay")
	write(t, filepath.Join(proj, "chat1.jsonl"),
		`{"type":"user","timestamp":"2026-10-18T10:00:00Z","message":{"content":"fix the wifi thing"}}`,
		asst("m1", "claude-opus-5-5", "2026-10-18T10:00:05Z", 1, 100, 1000, "Bash"),  // partial line...
		asst("m1", "claude-opus-5-5", "2026-10-18T10:00:06Z", 50, 100, 1000, "Read"), // ...same call, final counts
		`{"type":"user","timestamp":"2026-10-18T10:00:07Z","message":{"content":[{"type":"tool_result"}]}}`,
		asst("m2", "claude-opus-5-5", "2026-10-18T10:01:00Z", 10, 0, 2000, "Bash"),
		asst("old", "claude-opus-5-5", "2026-10-01T10:00:00Z", 999, 999, 999), // outside a 3-day window
	)
	write(t, filepath.Join(proj, "chat1", "subagents", "agent-a.jsonl"),
		asst("s1", "claude-haiku-4-5", "2026-10-18T10:02:00Z", 5, 50, 500, "Grep"))

	rep := scanner(dir).Report(3)

	if rep.Totals.Calls != 3 {
		t.Fatalf("calls = %d, want 3 (m1 deduped, old out of window)", rep.Totals.Calls)
	}
	if rep.Totals.Output != 65 { // 50 (max of m1 lines) + 10 + 5
		t.Errorf("output = %d, want 65", rep.Totals.Output)
	}
	if len(rep.ByDay) != 3 || rep.ByDay[2].Date != "2026-10-18" || rep.ByDay[2].Calls != 3 || rep.ByDay[0].Calls != 0 {
		t.Errorf("byDay = %+v", rep.ByDay)
	}
	if len(rep.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1 (subagent folded into chat1)", len(rep.Sessions))
	}
	s := rep.Sessions[0]
	if s.ID != "chat1" || s.Project != "Relay" || s.Title != "fix the wifi thing" {
		t.Errorf("session meta = %+v", s)
	}
	if s.Prompts != 1 || s.SubagentCalls != 1 || s.ToolCalls != 4 || s.ShellCalls != 2 {
		t.Errorf("counts prompts=%d sub=%d tools=%d shell=%d", s.Prompts, s.SubagentCalls, s.ToolCalls, s.ShellCalls)
	}
	if s.Model != "claude-opus-5-5" || s.MaxContext != 2003 {
		t.Errorf("model=%q maxCtx=%d", s.Model, s.MaxContext)
	}
	if len(rep.ByModel) != 2 || rep.ByModel[0].Name != "claude-opus-5-5" {
		t.Errorf("byModel = %+v", rep.ByModel)
	}
}

func TestFlags(t *testing.T) {
	long := SessionStat{Prompts: 1, ShellCalls: 70, ToolCalls: 100, MaxContext: 300_000, Model: "claude-opus-5-5", Counts: Counts{Calls: 120}}
	got := strings.Join(flagsFor(long), ",")
	for _, want := range []string{FlagLongChain, FlagHugeContext, FlagManyShell, FlagOpusMechanics} {
		if !strings.Contains(got, want) {
			t.Errorf("flags %q missing %s", got, want)
		}
	}
	calm := SessionStat{Prompts: 10, ShellCalls: 3, ToolCalls: 20, MaxContext: 50_000, Model: "claude-sonnet-5-5", Counts: Counts{Calls: 30}}
	if f := flagsFor(calm); len(f) != 0 {
		t.Errorf("calm chat flagged: %v", f)
	}
}

func TestCacheReparsesOnChange(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "projects", "enc", "c.jsonl")
	write(t, p, asst("a", "claude-sonnet-5-5", "2026-10-18T09:00:00Z", 1, 0, 10))
	s := scanner(dir)
	if n := s.Report(1).Totals.Calls; n != 1 {
		t.Fatalf("calls = %d", n)
	}
	write(t, p, asst("a", "claude-sonnet-5-5", "2026-10-18T09:00:00Z", 1, 0, 10), asst("b", "claude-sonnet-5-5", "2026-10-18T09:01:00Z", 1, 0, 10))
	if n := s.Report(1).Totals.Calls; n != 2 {
		t.Fatalf("after append calls = %d, want 2", n)
	}
}

func TestEmptyDirAndClamp(t *testing.T) {
	rep := scanner(t.TempDir()).Report(9999)
	if rep.Days != MaxDays || len(rep.ByDay) != MaxDays || rep.Totals.Calls != 0 || rep.Sessions == nil || rep.ByModel == nil || rep.ByProject == nil {
		t.Errorf("rep = days %d byDay %d calls %d", rep.Days, len(rep.ByDay), rep.Totals.Calls)
	}
}

func TestLastSegment(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Users\x\Relay`: "Relay", "/home/v/Documents/Relay/": "Relay", "/h/Obsidian/.claude/worktrees/agent-ab12": "Obsidian", "": "", "solo": "solo",
	} {
		if got := lastSegment(in); got != want {
			t.Errorf("lastSegment(%q) = %q, want %q", in, got, want)
		}
	}
}
