package uptime

import (
	"path/filepath"
	"testing"
)

func TestOpen_StartsAnOpenInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uptime.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	intervals := s.List()
	if len(intervals) != 1 {
		t.Fatalf("List() = %d intervals, want 1", len(intervals))
	}
	if intervals[0].End != nil {
		t.Fatalf("newly opened interval has End = %v, want nil", intervals[0].End)
	}
}

func TestClose_EndsTheOpenInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uptime.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	intervals := s.List()
	if len(intervals) != 1 || intervals[0].End == nil {
		t.Fatalf("List() after Close = %+v, want one closed interval", intervals)
	}
}

func TestOpen_ClosesOrphanedIntervalFromPreviousRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uptime.json")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open (first run): %v", err)
	}
	if got := len(first.List()); got != 1 {
		t.Fatalf("first run intervals = %d, want 1", got)
	}
	// Simulate a crash: no Close() call before "restarting".

	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open (second run): %v", err)
	}
	intervals := second.List()
	if len(intervals) != 2 {
		t.Fatalf("List() after second Open = %d intervals, want 2 (closed orphan + new open one)", len(intervals))
	}
	if intervals[0].End == nil {
		t.Fatalf("first run's interval should have been closed by the second Open, got End = nil")
	}
	if intervals[1].End != nil {
		t.Fatalf("second run's interval should still be open, got End = %v", intervals[1].End)
	}
}
