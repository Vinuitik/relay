// Package uptime records this runner's own up/down intervals to a small
// local JSON file, so GET /v1/uptime can report them. This is deliberately
// a short-term buffer, not the dashboard's system of record - the phone app
// pulls it whenever a runner happens to be reachable and persists the merged
// history itself (a runner going to sleep is exactly when it becomes
// unreachable, so the phone - not the runner - is the natural place to keep
// a durable weekly history; see android/FLOWS.md). Purged the same way
// session history is: entries older than MaxAge are dropped on load/save.
package uptime

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// MaxAge is how long an interval is kept in the local buffer before being
// purged. Short by design (see package doc) - the phone is expected to have
// synced well before this, and "lost a stretch of history if it didn't" is
// an accepted tradeoff, not a bug to engineer around.
const MaxAge = 14 * 24 * time.Hour

// Interval is one continuous stretch of this runner being up. End is nil
// while the runner is still running (the "current" open interval).
type Interval struct {
	Start time.Time  `json:"start"`
	End   *time.Time `json:"end,omitempty"`
}

// Store is a mutex-guarded, disk-persisted list of this runner's up/down
// intervals.
type Store struct {
	mu        sync.Mutex
	file      string
	intervals []Interval
}

// Open loads the interval history at file (creating it empty if absent) and
// closes out any interval left open by a previous run - an unclean shutdown
// (crash, power loss, physical unplug) never gets a chance to call Close, so
// its interval would otherwise claim uptime all the way to the next boot.
// Closing it "now" at load time undercounts that one interval slightly, but
// per the package doc that's an accepted tradeoff over adding real crash
// detection for a personal dashboard.
func Open(file string) (*Store, error) {
	s := &Store{file: file}

	data, err := os.ReadFile(file)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s.intervals); err != nil {
			return nil, fmt.Errorf("parse uptime file %q: %w", file, err)
		}
	case os.IsNotExist(err):
		s.intervals = []Interval{}
	default:
		return nil, fmt.Errorf("read uptime file %q: %w", file, err)
	}

	now := time.Now().UTC()
	if n := len(s.intervals); n > 0 && s.intervals[n-1].End == nil {
		s.intervals[n-1].End = &now
	}
	s.intervals = append(s.intervals, Interval{Start: now})

	s.purgeLocked(now)
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close marks the current open interval as ended now, so a clean shutdown
// (idle-suspend, or any future graceful-stop path) records accurate uptime
// instead of relying on Open's crash-recovery fallback next boot. Best
// effort: a failure to persist here is logged by the caller, never fatal -
// see idle.Monitor's BeforeShutdown wiring in cmd/runnerd/main.go.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if n := len(s.intervals); n > 0 && s.intervals[n-1].End == nil {
		s.intervals[n-1].End = &now
	}
	return s.persistLocked()
}

// List returns a snapshot of all known intervals, oldest first.
func (s *Store) List() []Interval {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Interval, len(s.intervals))
	copy(out, s.intervals)
	return out
}

func (s *Store) purgeLocked(now time.Time) {
	cutoff := now.Add(-MaxAge)
	kept := s.intervals[:0]
	for _, iv := range s.intervals {
		if iv.End == nil || iv.End.After(cutoff) {
			kept = append(kept, iv)
		}
	}
	s.intervals = kept
}

func (s *Store) persistLocked() error {
	data, err := json.MarshalIndent(s.intervals, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal uptime intervals: %w", err)
	}
	if err := os.WriteFile(s.file, data, 0o600); err != nil {
		return fmt.Errorf("write uptime file %q: %w", s.file, err)
	}
	return nil
}
