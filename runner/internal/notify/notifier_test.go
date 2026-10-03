package notify

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewNotifier_NoopWhenEnvUnset(t *testing.T) {
	n := NewNotifier("")
	if _, ok := n.(noopNotifier); !ok {
		t.Fatalf("NewNotifier(\"\") = %T, want noopNotifier", n)
	}
	// Must never error the caller.
	if err := n.NotifySessionFinished(Device{ID: "d1"}, Session{ID: "s1"}); err != nil {
		t.Fatalf("noop NotifySessionFinished returned error: %v", err)
	}
}

func TestNewNotifier_NoopWhenFileMissing(t *testing.T) {
	n := NewNotifier(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if _, ok := n.(noopNotifier); !ok {
		t.Fatalf("NewNotifier(missing file) = %T, want noopNotifier", n)
	}
}

func TestNewNotifier_NoopWhenFileMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	if err := os.WriteFile(path, []byte(`{"not": "a real credential file"}`), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}

	n := NewNotifier(path)
	if _, ok := n.(noopNotifier); !ok {
		t.Fatalf("NewNotifier(malformed file) = %T, want noopNotifier", n)
	}
}

func TestPushData_IncludesRunnerRef(t *testing.T) {
	dev := Device{FCMToken: "tok", RunnerRef: "my-pc"}
	sess := Session{ID: "s1", ProjectID: "p1", Problem: "quota"}
	cases := map[string]map[string]string{
		"session_finished":    sessionFinishedData(dev, sess),
		"session_needs_input": sessionNeedsInputData(dev, sess),
		"runner_suspending":   runnerSuspendingData(dev, "host"),
	}
	for typ, data := range cases {
		if data["type"] != typ {
			t.Errorf("%s: type = %q", typ, data["type"])
		}
		if data["runnerRef"] != "my-pc" {
			t.Errorf("%s: runnerRef = %q, want my-pc", typ, data["runnerRef"])
		}
	}
	if d := sessionFinishedData(dev, sess); d["sessionId"] != "s1" || d["projectId"] != "p1" || d["problem"] != "quota" {
		t.Errorf("session_finished data = %v", d)
	}
	if d := runnerSuspendingData(dev, "host"); d["hostname"] != "host" {
		t.Errorf("runner_suspending data = %v", d)
	}

	// Never registered a runnerRef -> key present, empty.
	d, ok := sessionFinishedData(Device{FCMToken: "tok"}, sess)["runnerRef"]
	if !ok || d != "" {
		t.Errorf("runnerRef without registration = %q (present %v), want present and empty", d, ok)
	}
}
