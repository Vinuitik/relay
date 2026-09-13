package history

import (
	"testing"
	"time"
)

func at(t *testing.T, s string) *time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return &tm
}

func TestPurgeOlderThan(t *testing.T) {
	cutoff, err := time.Parse(time.RFC3339, "2026-09-06T00:00:00Z") // one week before "now"
	if err != nil {
		t.Fatal(err)
	}

	sessions := []SessionInfo{
		{ID: "busy-1", State: "busy"},                                     // never purged, no FinishedAt
		{ID: "old-finished", State: "finished", FinishedAt: at(t, "2026-08-01T00:00:00Z")}, // older than cutoff -> purged
		{ID: "recent-finished", State: "finished", FinishedAt: at(t, "2026-09-10T00:00:00Z")}, // newer -> kept
		{ID: "old-error", State: "error", FinishedAt: at(t, "2026-07-01T00:00:00Z")}, // older -> purged
		{ID: "idle-1", State: "idle"}, // never purged
	}

	kept := PurgeOlderThan(sessions, cutoff)

	keptIDs := make(map[string]bool)
	for _, s := range kept {
		keptIDs[s.ID] = true
	}

	wantKept := []string{"busy-1", "recent-finished", "idle-1"}
	for _, id := range wantKept {
		if !keptIDs[id] {
			t.Errorf("expected %q to be kept, was purged", id)
		}
	}
	wantPurged := []string{"old-finished", "old-error"}
	for _, id := range wantPurged {
		if keptIDs[id] {
			t.Errorf("expected %q to be purged, was kept", id)
		}
	}
	if len(kept) != len(wantKept) {
		t.Errorf("kept = %d sessions, want %d (%+v)", len(kept), len(wantKept), kept)
	}
}

func TestPurgeOlderThanExactCutoffIsKept(t *testing.T) {
	cutoff := time.Now()
	sessions := []SessionInfo{
		{ID: "exact", State: "finished", FinishedAt: &cutoff},
	}
	kept := PurgeOlderThan(sessions, cutoff)
	if len(kept) != 1 {
		t.Fatalf("expected session exactly at cutoff to be kept, got %+v", kept)
	}
}

func TestPurgeOlderThanNilFinishedAtIsKept(t *testing.T) {
	sessions := []SessionInfo{
		{ID: "weird", State: "finished", FinishedAt: nil},
	}
	kept := PurgeOlderThan(sessions, time.Now())
	if len(kept) != 1 {
		t.Fatalf("expected session with nil FinishedAt to be kept defensively, got %+v", kept)
	}
}
