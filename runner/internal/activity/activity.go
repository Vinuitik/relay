// Package activity tracks liveness signals that idle-suspend treats as
// "someone is using this machine" beyond Relay's own session state - see
// internal/idle's package doc for how these feed the suspend decision. Two
// independent signals live here:
//
//   - Tracker: source (b), the phone app's foreground heartbeat (POST
//     /v1/activity, sent every 30s while the Android app is in the
//     foreground - never while backgrounded).
//   - LocalIdleTime: source (c), local keyboard/mouse input on this
//     machine itself (Windows only - see local_windows.go / local_unix.go).
//
// Both are liveness only, not state worth persisting: on restart, "no
// recent signal" is exactly "nobody's around right now", which is already
// the correct default.
package activity

import (
	"errors"
	"sync"
	"time"
)

// DefaultWindow is how long a phone-app activity ping (source b) stays
// "fresh" after it arrives. The Android app pings every 30s while in the
// foreground; DefaultWindow is deliberately several times that so one
// dropped ping on a flaky mobile connection doesn't make idle.Monitor think
// the app closed and the machine suddenly suspendable.
const DefaultWindow = 90 * time.Second

// ErrUnsupported is returned by LocalIdleTime on platforms with no
// portable, dependency-free way to read local keyboard/mouse input -
// currently Linux/unix. Callers must treat it as "no local input signal
// available", not as a hard failure: a headless Linux service has no
// console user in the first place, which is exactly the case where this
// doesn't matter - idle.Monitor falls back to sources (a) session state and
// (b) phone activity pings alone.
var ErrUnsupported = errors.New("activity: local input detection is unsupported on this platform")

// Tracker records the most recent moment the phone app confirmed it was in
// the foreground (see api.Server's POST /v1/activity handler). In-memory
// only, mutex-guarded for concurrent use from the HTTP handler goroutine
// and idle.Monitor's ticker goroutine.
type Tracker struct {
	mu   sync.Mutex
	last time.Time
}

// NewTracker builds an empty Tracker. LastActive returns the zero Time
// until the first Mark.
func NewTracker() *Tracker {
	return &Tracker{}
}

// Mark records that the phone app confirmed foreground activity right now.
func (t *Tracker) Mark() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last = time.Now()
}

// LastActive returns the last time Mark was called, or the zero Time if it
// never has been.
func (t *Tracker) LastActive() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}
