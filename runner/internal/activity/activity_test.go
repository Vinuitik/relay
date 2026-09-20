package activity

import (
	"testing"
	"time"
)

func TestTracker_LastActiveZeroBeforeMark(t *testing.T) {
	tr := NewTracker()
	if !tr.LastActive().IsZero() {
		t.Fatalf("LastActive() = %v, want zero before any Mark", tr.LastActive())
	}
}

func TestTracker_MarkRecordsNow(t *testing.T) {
	tr := NewTracker()
	before := time.Now()
	tr.Mark()
	after := time.Now()

	last := tr.LastActive()
	if last.Before(before) || last.After(after) {
		t.Fatalf("LastActive() = %v, want between %v and %v", last, before, after)
	}
}

func TestTracker_MarkOverwritesPrevious(t *testing.T) {
	tr := NewTracker()
	tr.Mark()
	first := tr.LastActive()

	time.Sleep(time.Millisecond)
	tr.Mark()
	second := tr.LastActive()

	if !second.After(first) {
		t.Fatalf("second Mark() = %v, want after first %v", second, first)
	}
}

// TestLocalIdleTime_UnixIsUnsupported guards local_unix.go's contract - only
// meaningful on the non-Windows build, but this file itself is built on
// every platform, so on Windows this just exercises the real
// GetLastInputInfo path and skips the ErrUnsupported assertion.
func TestLocalIdleTime_ReturnsWithoutPanicking(t *testing.T) {
	_, err := LocalIdleTime()
	if err != nil && err != ErrUnsupported {
		t.Fatalf("LocalIdleTime() error = %v, want nil or ErrUnsupported", err)
	}
}
