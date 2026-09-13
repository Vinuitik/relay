package notify

import "testing"

func TestRegistry_RegisterNewToken(t *testing.T) {
	r := NewRegistry()
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
	r := NewRegistry()
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
	r := NewRegistry()
	a := r.Register("token-a")
	b := r.Register("token-b")

	if a.ID == b.ID {
		t.Fatal("distinct tokens got the same device ID")
	}
	if len(r.List()) != 2 {
		t.Fatalf("List() has %d entries, want 2", len(r.List()))
	}
}
