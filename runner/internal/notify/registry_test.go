package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistry_RegisterNewToken(t *testing.T) {
	r := NewRegistryAt(filepath.Join(t.TempDir(), "devices.json"))
	d := r.Register("token-a")
	if d.FCMToken != "token-a" {
		t.Fatalf("FCMToken = %q, want %q", d.FCMToken, "token-a")
	}
	if d.ID == "" {
		t.Fatal("expected non-empty generated ID")
	}
	if d.RegisteredAt == "" {
		t.Fatal("expected non-empty RegisteredAt")
	}

	list := r.List()
	if len(list) != 1 {
		t.Fatalf("List() has %d entries, want 1", len(list))
	}
}

func TestRegistry_ReRegisterSameTokenUpdatesNotDuplicates(t *testing.T) {
	r := NewRegistryAt(filepath.Join(t.TempDir(), "devices.json"))
	first := r.Register("token-a")
	second := r.Register("token-a")

	if first.ID != second.ID {
		t.Fatalf("re-registering the same token changed ID: %q -> %q", first.ID, second.ID)
	}

	list := r.List()
	if len(list) != 1 {
		t.Fatalf("List() has %d entries after re-register, want 1 (no duplicate)", len(list))
	}
}

func TestRegistry_DistinctTokensProduceDistinctDevices(t *testing.T) {
	r := NewRegistryAt(filepath.Join(t.TempDir(), "devices.json"))
	a := r.Register("token-a")
	b := r.Register("token-b")

	if a.ID == b.ID {
		t.Fatal("distinct tokens got the same device ID")
	}
	if len(r.List()) != 2 {
		t.Fatalf("List() has %d entries, want 2", len(r.List()))
	}
}

func TestRegistry_PersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")

	r1 := NewRegistryAt(path)
	want := r1.Register("token-a")

	r2 := NewRegistryAt(path)
	list := r2.List()
	if len(list) != 1 {
		t.Fatalf("List() after reload has %d entries, want 1", len(list))
	}
	if list[0].FCMToken != want.FCMToken || list[0].ID != want.ID {
		t.Fatalf("reloaded device = %+v, want %+v", list[0], want)
	}
}

func TestRegistry_MissingFileTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "devices.json")
	r := NewRegistryAt(path)
	if len(r.List()) != 0 {
		t.Fatalf("List() on missing file has %d entries, want 0", len(r.List()))
	}

	// Registering should still succeed and create the file (and its parent
	// dir doesn't need to pre-exist for CreateTemp to work, since t.TempDir
	// already exists - this exercises the "no file yet" -> "first write"
	// path against an existing parent dir).
}

func TestRegistry_CorruptFileTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	r := NewRegistryAt(path)
	if len(r.List()) != 0 {
		t.Fatalf("List() on corrupt file has %d entries, want 0", len(r.List()))
	}

	// Registry should still be usable after tolerating corruption.
	d := r.Register("token-a")
	if d.FCMToken != "token-a" {
		t.Fatalf("FCMToken = %q, want %q", d.FCMToken, "token-a")
	}
}

func TestRegistry_DedupeSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")

	r1 := NewRegistryAt(path)
	first := r1.Register("token-a")
	second := r1.Register("token-a")
	if first.ID != second.ID {
		t.Fatalf("re-registering changed ID: %q -> %q", first.ID, second.ID)
	}

	r2 := NewRegistryAt(path)
	r2.Register("token-a")
	if len(r2.List()) != 1 {
		t.Fatalf("List() after reload+reregister has %d entries, want 1 (no duplicate)", len(r2.List()))
	}
}

func TestRegistry_AtomicWriteLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")

	r := NewRegistryAt(path)
	r.Register("token-a")
	r.Register("token-b")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") || strings.HasPrefix(e.Name(), ".devices-") {
			t.Fatalf("leftover temp file found: %s", e.Name())
		}
	}
	if len(entries) != 1 || entries[0].Name() != "devices.json" {
		t.Fatalf("dir entries = %v, want only devices.json", entries)
	}
}
