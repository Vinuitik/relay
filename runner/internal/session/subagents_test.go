package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const subFixture = `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"Find the bug"}}
{"type":"attachment","timestamp":"2026-01-01T10:00:01Z"}
{"type":"assistant","timestamp":"2026-01-01T10:00:02Z","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"hmm"}]}}
{"type":"assistant","timestamp":"2026-01-01T10:00:03Z","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"Looking."},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a/b/foo.go"}}]}}
{"type":"user","timestamp":"2026-01-01T10:00:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"go test"}}]}}
`

const subFinal = `{"type":"assistant","timestamp":"2026-01-01T10:00:09Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Final report"}]}}
`

func writeSub(t *testing.T, dir, id, body, desc string) string {
	t.Helper()
	p := filepath.Join(dir, "agent-"+id+".jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := `{"agentType":"general-purpose","description":"` + desc + `","toolUseId":"toolu_x"}`
	if err := os.WriteFile(filepath.Join(dir, "agent-"+id+".meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSubagents_ParseMessages(t *testing.T) {
	dir := t.TempDir()
	p := writeSub(t, dir, "abc1", subFixture, "Fix bug")
	tr, err := parseSubagent(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.msgs) != 4 {
		t.Fatalf("got %d msgs: %+v", len(tr.msgs), tr.msgs)
	}
	if m := tr.msgs[0]; m.Role != "user" || m.Text != "Find the bug" {
		t.Errorf("prompt = %+v", m)
	}
	if m := tr.msgs[1]; m.Role != "agent" || m.Text != "Looking." {
		t.Errorf("text = %+v", m)
	}
	if m := tr.msgs[2]; m.Role != "tool" || m.Text != "Read foo.go" || m.ToolKind != "read" || m.Status != "completed" {
		t.Errorf("read tool = %+v", m)
	}
	if m := tr.msgs[3]; m.Text != "Bash: go test" || m.Status != "in_progress" {
		t.Errorf("bash tool = %+v", m)
	}
	pg := tr.page(2)
	if len(pg.Messages) != 2 || pg.Next != 4 || pg.Total != 4 || pg.Status != "running" {
		t.Errorf("page(2) = %+v", pg)
	}
	if pg := tr.page(99); len(pg.Messages) != 0 || pg.Next != 4 {
		t.Errorf("page(99) = %+v", pg)
	}
}

func TestSubagents_ListStatus(t *testing.T) {
	dir := t.TempDir()
	writeSub(t, dir, "run1", subFixture, "Still going")
	writeSub(t, dir, "done1", subFixture+subFinal, "Finished")
	old := writeSub(t, dir, "stale1", subFixture, "Abandoned")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	got := map[string]SubagentInfo{}
	for _, a := range listSubagents(dir) {
		got[a.AgentID] = a
	}
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	if a := got["run1"]; a.Status != "running" || a.Description != "Still going" || a.MessageCount != 4 || a.StartedAt != "2026-01-01T10:00:00Z" {
		t.Errorf("run1 = %+v", a)
	}
	if a := got["done1"]; a.Status != "done" || a.MessageCount != 5 || a.LastActivityAt != "2026-01-01T10:00:09Z" {
		t.Errorf("done1 = %+v", a)
	}
	if got["stale1"].Status != "done" {
		t.Errorf("stale1 = %+v", got["stale1"])
	}
	if l := listSubagents(filepath.Join(dir, "nope")); len(l) != 0 {
		t.Errorf("missing dir = %+v", l)
	}
}