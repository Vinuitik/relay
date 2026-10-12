package vscodehook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateFor(t *testing.T) {
	cases := map[[2]string]string{
		{"UserPromptSubmit", ""}: "busy", {"Stop", ""}: "idle",
		{"Notification", "permission_prompt"}: "waiting", {"Notification", "idle_prompt"}: "",
		{"Other", ""}: "",
	}
	for in, want := range cases {
		if got := StateFor(in[0], in[1]); got != want {
			t.Errorf("%v: %q want %q", in, got, want)
		}
	}
}

func TestParseBusyStaleness(t *testing.T) {
	now := time.Now()
	mk := func(state string, age time.Duration) []byte {
		b, _ := json.Marshal(State{State: state, At: now.Add(-age).UTC().Format(time.RFC3339)})
		return b
	}
	if !ParseBusy(mk("busy", time.Minute), now) || !ParseBusy(mk("waiting", time.Minute), now) {
		t.Error("fresh busy/waiting should be busy")
	}
	if ParseBusy(mk("busy", 31*time.Minute), now) {
		t.Error("stale busy should be idle")
	}
	if ParseBusy(mk("idle", 0), now) || ParseBusy([]byte("junk"), now) {
		t.Error("idle/junk should not be busy")
	}
}

func TestShouldBlock(t *testing.T) {
	if !ShouldBlock(false, true) || ShouldBlock(true, true) || ShouldBlock(false, false) || ShouldBlock(true, false) {
		t.Error("guard decision wrong")
	}
}

func TestHandleWritesStateAndBlocks(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	tr := filepath.Join(home, "t.jsonl")
	in := func(ev string) []byte {
		b, _ := json.Marshal(Input{SessionID: "abc-1", TranscriptPath: tr, HookEventName: ev})
		return b
	}
	if code, _ := Handle("UserPromptSubmit", in("UserPromptSubmit"), home, now); code != 0 {
		t.Fatal("new chat must not be blocked")
	}
	if !Busy(home, "abc-1", now) {
		t.Fatal("expected busy")
	}
	Handle("Stop", in("Stop"), home, now)
	if Busy(home, "abc-1", now) {
		t.Fatal("expected idle")
	}
	os.MkdirAll(filepath.Join(home, "handed-off"), 0o700)
	os.WriteFile(filepath.Join(home, "handed-off", "abc-1.jsonl"), []byte("x"), 0o600)
	if code, msg := Handle("UserPromptSubmit", in("UserPromptSubmit"), home, now); code != 2 || msg != MovedMessage {
		t.Fatalf("expected block, got %d %q", code, msg)
	}
	if code, _ := Handle("Stop", []byte("garbage"), home, now); code != 0 {
		t.Fatal("must fail open")
	}
	if _, ok := LastEvent(home); !ok {
		t.Error("LastEvent missing")
	}
}

func TestInstallPreservesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	orig := `{"model":"opus","big":12345678901234,"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo mine"}]}],"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"x"}]}]}}`
	p := filepath.Join(dir, "settings.json")
	os.WriteFile(p, []byte(orig), 0o644)
	if err := Install(dir, "C:\bin\runnerd.exe"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if !Installed(dir) || !strings.Contains(s, `"model": "opus"`) {
		t.Fatalf("lost keys or not installed: %s", s)
	}
	for _, want := range []string{"echo mine", "PreToolUse", "12345678901234"} {
		if !strings.Contains(s, want) {
			t.Fatalf("existing content %q lost: %s", want, s)
		}
	}
	if bak, _ := os.ReadFile(p + ".relay-bak"); string(bak) != orig {
		t.Fatal("backup wrong")
	}
	Install(dir, "C:\bin\runnerd.exe")
	if after, _ := os.ReadFile(p); string(after) != s {
		t.Fatal("not idempotent")
	}
	Install(dir, "/opt/runnerd")
	after, _ := os.ReadFile(p)
	if strings.Contains(string(after), "C:/bin") || !strings.Contains(string(after), "/opt/runnerd") {
		t.Fatalf("path not updated: %s", after)
	}
	m := map[string]any{}
	json.Unmarshal(after, &m)
	if n := len(m["hooks"].(map[string]any)["Stop"].([]any)); n != 2 {
		t.Fatalf("Stop groups = %d", n)
	}
}

func TestInstallLeavesBadJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	os.WriteFile(p, []byte("{not json"), 0o644)
	if Install(dir, "x") == nil {
		t.Fatal("expected error")
	}
	if b, _ := os.ReadFile(p); string(b) != "{not json" {
		t.Fatal("file modified")
	}
}
