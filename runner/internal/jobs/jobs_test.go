package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as a fake `claude -p`: when RELAY_FAKE_CLAUDE=<mode> the
// test binary, re-run as the job subprocess, logs its args/cwd/stdin to
// RELAY_FAKE_CLAUDE_LOG and prints canned stream-json - no claude needed.
func TestMain(m *testing.M) {
	if mode := os.Getenv("RELAY_FAKE_CLAUDE"); mode != "" {
		os.Exit(fakeClaude(mode))
	}
	os.Exit(m.Run())
}

func fakeClaude(mode string) int {
	stdin, _ := io.ReadAll(os.Stdin)
	cwd, _ := os.Getwd()
	if p := os.Getenv("RELAY_FAKE_CLAUDE_LOG"); p != "" {
		b, _ := json.Marshal(map[string]any{"args": os.Args[1:], "cwd": cwd, "stdin": string(stdin)})
		_ = os.WriteFile(p, b, 0o600)
	}
	emit := func(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
	tool := func(parent any, name string, input map[string]any) {
		emit(map[string]any{"type": "assistant", "parent_tool_use_id": parent, "message": map[string]any{
			"content": []any{map[string]any{"type": "tool_use", "name": name, "input": input}}}})
	}
	text := func(parent any, s string) {
		emit(map[string]any{"type": "assistant", "parent_tool_use_id": parent, "message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": s}}}})
	}
	result := func(subtype string, isErr bool, res string) {
		emit(map[string]any{"type": "result", "subtype": subtype, "is_error": isErr, "result": res,
			"usage": map[string]any{"input_tokens": 10, "cache_creation_input_tokens": 100, "cache_read_input_tokens": 1000, "output_tokens": 7}})
	}
	emit(map[string]any{"type": "system", "subtype": "init"})
	switch mode {
	case "ok":
		tool(nil, "Read", map[string]any{"file_path": filepath.Join(cwd, "src", "a.go")})
		tool(nil, "Grep", map[string]any{"pattern": "foo"})
		tool(nil, "Agent", map[string]any{"description": "probe", "prompt": "x"})
		text("toolu_1", "subagent prompt, not shown")
		tool("toolu_1", "Read", map[string]any{"file_path": "/etc/hostname"})
		emit(map[string]any{"type": "system", "subtype": "permission_denied", "tool_name": "Read", "decision_reason": "outside cwd"})
		text(nil, "Here is the map.")
		result("success", false, "THE RESULT")
	case "fail":
		result("error_max_turns", true, "")
	case "noresult":
		fmt.Fprintln(os.Stderr, "boom")
		return 2
	case "hang":
		time.Sleep(time.Minute)
	case "many":
		for i := 0; i < MaxEvents+500; i++ {
			tool(nil, "Glob", map[string]any{"pattern": "*.go"})
		}
		result("success", false, "ok")
	case "big":
		result("success", false, strings.Repeat("é", MaxResult))
	}
	return 0
}

func newManager(t *testing.T, mode string) (*Manager, string, string) {
	t.Helper()
	t.Setenv("RELAY_FAKE_CLAUDE", mode)
	log := filepath.Join(t.TempDir(), "log.json")
	t.Setenv("RELAY_FAKE_CLAUDE_LOG", log)
	proj := t.TempDir()
	m := New(func(id string) (string, bool) {
		if id == "p1" {
			return proj, true
		}
		return "", false
	}, func() (string, error) { return os.Args[0], nil })
	m.ExtraArgs = []string{"-test.run=^$"}
	return m, proj, log
}

func waitState(t *testing.T, m *Manager, id string, want ...string) Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, err := m.Get(id, 0)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		for _, w := range want {
			if j.State == w {
				return j
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want %v; job = %+v", j.State, want, j)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readLog(t *testing.T, p string) (args []string, cwd, stdin string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("fake log: %v", err)
	}
	var l struct {
		Args  []string
		Cwd   string
		Stdin string
	}
	_ = json.Unmarshal(b, &l)
	return l.Args, l.Cwd, l.Stdin
}

func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestMapJobRunsConfinedAndTranslatesSteps(t *testing.T) {
	m, proj, log := newManager(t, "ok")
	id, err := m.Submit(Request{Kind: KindMap, ProjectID: "p1", Prompt: "map it", Model: "haiku", MaxTurns: 5})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	j := waitState(t, m, id, StateDone, StateFailed)
	if j.State != StateDone || j.Result != "THE RESULT" {
		t.Fatalf("job = %+v", j)
	}
	if j.Usage == nil || j.Usage.InputTokens != 1110 || j.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v", j.Usage)
	}
	var got []string
	for _, e := range j.Events {
		got = append(got, e.Kind+":"+e.Text)
	}
	want := []string{
		"step:Started",
		"step:Reading src/a.go",
		"step:Searching for foo",
		"step:Started subagent: probe",
		"step:(subagent) Reading /etc/hostname",
		"error:Blocked Read: outside cwd",
		"text:Here is the map.",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("events =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if j.NextSeq != len(want) {
		t.Errorf("NextSeq = %d", j.NextSeq)
	}

	args, cwd, stdin := readLog(t, log)
	if r1, _ := filepath.EvalSymlinks(cwd); r1 != mustEval(t, proj) {
		t.Errorf("cwd = %s, want %s", cwd, proj)
	}
	if stdin != "map it" {
		t.Errorf("stdin = %q", stdin)
	}
	joined := strings.Join(args, " ")
	for _, f := range []string{"-p", "--restricted", "--strict-mcp-config", "--no-session-persistence"} {
		if !strings.Contains(" "+joined+" ", " "+f+" ") {
			t.Errorf("missing %s in %s", f, joined)
		}
	}
	checks := map[string]string{"--tools": "Read,Grep,Glob,Agent", "--permission-mode": "dontAsk",
		"--permission-prompts": "none", "--output-format": "stream-json", "--model": "haiku", "--max-turns": "5",
		"--disallowedTools": "Bash,Edit,Write,NotebookEdit,WebFetch,WebSearch"}
	for f, v := range checks {
		if got, _ := flagValue(args, f); got != v {
			t.Errorf("%s = %q, want %q", f, got, v)
		}
	}

	// ?after filters events.
	j2, _ := m.Get(id, 5)
	if len(j2.Events) != 2 || j2.Events[0].Seq != 6 {
		t.Errorf("after=5 events = %+v", j2.Events)
	}
}

func mustEval(t *testing.T, p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestKPIJobHasNoToolsAndTempDir(t *testing.T) {
	m, proj, log := newManager(t, "ok")
	id, err := m.Submit(Request{Kind: KindKPI, Prompt: "write kpis"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitState(t, m, id, StateDone)
	args, cwd, _ := readLog(t, log)
	if v, ok := flagValue(args, "--tools"); !ok || v != "" {
		t.Errorf("--tools = %q (present %v), want empty", v, ok)
	}
	if cwd == proj || !strings.Contains(filepath.Base(cwd), "relay-job-") {
		t.Errorf("cwd = %s, want a relay-job- temp dir", cwd)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("temp dir not removed: %v", err)
	}
}

func TestSubmitValidation(t *testing.T) {
	m, _, _ := newManager(t, "ok")
	cases := []struct {
		req  Request
		want error
	}{
		{Request{Kind: KindMap, Prompt: "x"}, ErrProjectRequired},
		{Request{Kind: KindPlan, ProjectID: "nope", Prompt: "x"}, ErrProjectNotFound},
		{Request{Kind: "shell", Prompt: "x"}, ErrBadRequest},
		{Request{Kind: KindKPI, Prompt: "  "}, ErrBadRequest},
		{Request{Kind: KindKPI, Prompt: "x", MaxTurns: -1}, ErrBadRequest},
		{Request{Kind: KindKPI, Prompt: "x", Model: "--dangerously-skip-permissions"}, ErrBadRequest},
	}
	for _, c := range cases {
		if _, err := m.Submit(c.req); !errors.Is(err, c.want) {
			t.Errorf("Submit(%+v) = %v, want %v", c.req, err, c.want)
		}
	}
	if _, err := m.Get("job_missing", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v", err)
	}
	if _, err := m.Cancel("job_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Cancel missing = %v", err)
	}
}

func TestConcurrencyCapQueueAndCancel(t *testing.T) {
	m, _, _ := newManager(t, "hang")
	var ids []string
	for i := 0; i < 3; i++ {
		id, err := m.Submit(Request{Kind: KindKPI, Prompt: "x"})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		ids = append(ids, id)
	}
	waitState(t, m, ids[0], StateRunning)
	waitState(t, m, ids[1], StateRunning)
	if j, _ := m.Get(ids[2], 0); j.State != StateQueued {
		t.Fatalf("third job state = %s, want queued", j.State)
	}
	if m.Active() != 3 {
		t.Errorf("Active = %d", m.Active())
	}

	j, err := m.Cancel(ids[0])
	if err != nil || j.State != StateCancelled {
		t.Fatalf("Cancel = %+v, %v", j, err)
	}
	waitState(t, m, ids[2], StateRunning) // freed slot is taken
	if j, _ := m.Get(ids[0], 0); j.State != StateCancelled {
		t.Errorf("cancelled job became %s", j.State)
	}
	for _, id := range ids[1:] {
		m.Cancel(id)
	}
	deadline := time.Now().Add(10 * time.Second)
	for m.Active() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.Active() != 0 {
		t.Errorf("Active after cancel = %d", m.Active())
	}
}

func TestCancelQueuedJob(t *testing.T) {
	m, _, _ := newManager(t, "hang")
	for i := 0; i < MaxConcurrent; i++ {
		id, _ := m.Submit(Request{Kind: KindKPI, Prompt: "x"})
		defer m.Cancel(id)
	}
	id, _ := m.Submit(Request{Kind: KindKPI, Prompt: "x"})
	j, _ := m.Cancel(id)
	if j.State != StateCancelled {
		t.Fatalf("state = %s", j.State)
	}
	if m.Active() != MaxConcurrent {
		t.Errorf("Active = %d", m.Active())
	}
}

func TestFailedResults(t *testing.T) {
	m, _, _ := newManager(t, "fail")
	id, _ := m.Submit(Request{Kind: KindKPI, Prompt: "x"})
	j := waitState(t, m, id, StateFailed, StateDone)
	if j.State != StateFailed || !strings.Contains(j.Error, "error_max_turns") {
		t.Errorf("job = %+v", j)
	}

	m2, _, _ := newManager(t, "noresult")
	id, _ = m2.Submit(Request{Kind: KindKPI, Prompt: "x"})
	j = waitState(t, m2, id, StateFailed, StateDone)
	if j.State != StateFailed || !strings.Contains(j.Error, "boom") {
		t.Errorf("job = %+v", j)
	}
}

func TestEventCapAndResultTruncation(t *testing.T) {
	m, _, _ := newManager(t, "many")
	id, _ := m.Submit(Request{Kind: KindKPI, Prompt: "x"})
	j := waitState(t, m, id, StateDone)
	if len(j.Events) != MaxEvents || j.Events[MaxEvents-1].Kind != EventError {
		t.Errorf("events = %d, last = %+v", len(j.Events), j.Events[len(j.Events)-1])
	}

	m2, _, _ := newManager(t, "big")
	id, _ = m2.Submit(Request{Kind: KindKPI, Prompt: "x"})
	j = waitState(t, m2, id, StateDone)
	if len(j.Result) > MaxResult || !strings.HasSuffix(j.Result, truncMarker) {
		t.Errorf("result len = %d, suffix ok = %v", len(j.Result), strings.HasSuffix(j.Result, truncMarker))
	}
}

func TestFinishedJobsExpire(t *testing.T) {
	m, _, _ := newManager(t, "ok")
	id, _ := m.Submit(Request{Kind: KindKPI, Prompt: "x"})
	waitState(t, m, id, StateDone)
	now := time.Now()
	m.mu.Lock()
	m.now = func() time.Time { return now.Add(Retention + time.Minute) }
	m.mu.Unlock()
	if _, err := m.Get(id, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after retention = %v, want ErrNotFound", err)
	}
}
