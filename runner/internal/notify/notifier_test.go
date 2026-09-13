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
