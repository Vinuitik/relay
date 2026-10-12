package vscodeperm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreAnswerWakesWaiter(t *testing.T) {
	s := NewStore()
	it := s.Add(Item{SessionID: "s1", ToolName: "Bash"})
	if !s.HasSession("s1") || len(s.List()) != 1 {
		t.Fatal("item should be pending")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		if !s.Answer(it.ID, Decision{Behavior: "allow"}) {
			t.Error("answer rejected")
		}
	}()
	d, ok := s.Wait(context.Background(), it.ID, 2*time.Second)
	if !ok || d.Behavior != "allow" {
		t.Fatalf("got %+v %v", d, ok)
	}
	if s.HasSession("s1") || len(s.List()) != 0 {
		t.Error("item should be gone after answer")
	}
}

func TestStoreTimeoutRemovesItem(t *testing.T) {
	s := NewStore()
	it := s.Add(Item{SessionID: "s1"})
	if _, ok := s.Wait(context.Background(), it.ID, 20*time.Millisecond); ok {
		t.Fatal("should time out")
	}
	if s.Answer(it.ID, Decision{Behavior: "allow"}) {
		t.Error("answer after timeout must fail (404)")
	}
}

func TestStoreDoubleAnswer(t *testing.T) {
	s := NewStore()
	it := s.Add(Item{})
	if !s.Answer(it.ID, Decision{Behavior: "deny"}) {
		t.Fatal("first answer should win")
	}
	if s.Answer(it.ID, Decision{Behavior: "allow"}) {
		t.Fatal("second answer must be rejected")
	}
	d, ok := s.Wait(context.Background(), it.ID, time.Second)
	if ok {
		// Wait found the entry already removed by Answer: fine either way, but
		// it must never report the losing answer.
		if d.Behavior != "deny" {
			t.Fatalf("got %+v", d)
		}
	}
}

func TestStoreHookGone(t *testing.T) {
	s := NewStore()
	it := s.Add(Item{SessionID: "s1"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := s.Wait(ctx, it.ID, time.Minute); ok {
		t.Fatal("cancelled wait should not answer")
	}
	if s.HasSession("s1") {
		t.Error("item should expire when the hook's poll ends")
	}
}

func TestSummarize(t *testing.T) {
	cases := []struct{ tool, in, want string }{
		{"Bash", `{"command":"npm test"}`, "Bash: npm test"},
		{"Bash", `{"command":"echo a\necho b"}`, "Bash: echo a ..."},
		{"Edit", `{"file_path":"/x/y/foo.go"}`, "Edit foo.go"},
		{"Write", `{"file_path":"C:\\x\\bar.txt"}`, "Write bar.txt"},
		{"WebFetch", `{"url":"https://a.b"}`, "WebFetch: https://a.b"},
		{"mcp__x__y", `{}`, "mcp__x__y"},
		{"", `{}`, "permission request"},
	}
	for _, c := range cases {
		if got := Summarize(c.tool, json.RawMessage(c.in)); got != c.want {
			t.Errorf("%s %s: %q want %q", c.tool, c.in, got, c.want)
		}
	}
	long := Summarize("Bash", json.RawMessage(`{"command":"`+strings.Repeat("a", 300)+`"}`))
	if len([]rune(long)) != maxSummary {
		t.Errorf("long summary has %d runes", len([]rune(long)))
	}
}

func TestMatchProject(t *testing.T) {
	paths := map[string]string{"a": `D:\Relay`, "b": `D:\Relay\runner`, "c": "/srv/other"}
	if got := MatchProject("d:/relay/runner/internal", paths); got != "b" {
		t.Errorf("got %q", got)
	}
	if got := MatchProject(`D:\RelayX`, paths); got != "" {
		t.Errorf("prefix without separator matched: %q", got)
	}
}

func TestOutputShape(t *testing.T) {
	var allow map[string]map[string]any
	if err := json.Unmarshal(Output(Decision{Behavior: "allow"}), &allow); err != nil {
		t.Fatal(err)
	}
	h := allow["hookSpecificOutput"]
	dec := h["decision"].(map[string]any)
	if h["hookEventName"] != "PermissionRequest" || dec["behavior"] != "allow" || len(dec) != 1 {
		t.Errorf("allow shape: %v", allow)
	}
	var deny map[string]map[string]any
	_ = json.Unmarshal(Output(Decision{Behavior: "deny", Message: "no"}), &deny)
	dd := deny["hookSpecificOutput"]["decision"].(map[string]any)
	if dd["behavior"] != "deny" || dd["message"] != "no" || dd["interrupt"] != false {
		t.Errorf("deny shape: %v", dd)
	}
}

func TestDebouncer(t *testing.T) {
	d := NewDebouncer(2 * time.Minute)
	now := time.Now()
	if !d.Allow("s1", now) || d.Allow("s1", now.Add(time.Minute)) {
		t.Fatal("second call inside window must be blocked")
	}
	if !d.Allow("s2", now) {
		t.Error("other session independent")
	}
	if !d.Allow("s1", now.Add(3*time.Minute)) {
		t.Error("allowed again after window")
	}
}

func TestRunPermissionHook(t *testing.T) {
	status := http.StatusOK
	var gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Relay-Key")
		if status == http.StatusOK {
			w.Write([]byte(`{"behavior":"allow"}`))
			return
		}
		w.WriteHeader(status)
	}))
	defer ts.Close()
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "key.txt"), []byte("k123\n"), 0o600)
	os.WriteFile(filepath.Join(home, AddrFile), []byte(strings.TrimPrefix(ts.URL, "http://")), 0o600)

	out := RunPermissionHook([]byte(`{"session_id":"s","tool_name":"Bash"}`), home)
	if gotKey != "k123" || !strings.Contains(string(out), `"behavior":"allow"`) {
		t.Fatalf("key=%q out=%s", gotKey, out)
	}
	status = http.StatusNoContent
	if out := RunPermissionHook([]byte(`{}`), home); out != nil {
		t.Errorf("204 must print nothing, got %s", out)
	}
	if RunPermissionHook([]byte(`not json`), home) != nil {
		t.Error("bad stdin must print nothing")
	}
	if RunPermissionHook([]byte(`{}`), t.TempDir()) != nil {
		t.Error("no key must print nothing (and create nothing)")
	}
}
