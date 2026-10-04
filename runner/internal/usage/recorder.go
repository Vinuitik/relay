package usage

import (
	"log"
	"os"
	"sync"
	"time"
)

// Recorder samples the activity sources once per wall-clock minute and
// appends what it saw to the day files. The source funcs are injected
// (wired in cmd/runnerd/main.go) so usage imports neither session nor
// activity, and tests can fake them.
type Recorder struct {
	Dir string

	// Busy reports whether any agent turn is running right now
	// (session.Manager.IdleStatus).
	Busy func() bool
	// AppLast is the last phone-app foreground ping
	// (activity.Tracker.LastActive). Zero time = never.
	AppLast func() time.Time
	// LocalIdle is time since the last local keyboard/mouse input
	// (activity.LocalIdleTime). An error means "no signal" (Linux).
	LocalIdle func() (time.Duration, error)

	now func() time.Time

	mu       sync.Mutex
	turnSeen bool // a turn ended since the last sample
}

// NewRecorder creates dir if needed and prunes old day files.
func NewRecorder(dir string) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	r := &Recorder{Dir: dir, now: time.Now}
	if err := Prune(dir, r.now()); err != nil {
		log.Printf("usage: prune: %v", err)
	}
	return r, nil
}

// RecordTurn logs one completed agent turn. Wired to
// session.Manager.OnTurn.
func (r *Recorder) RecordTurn(sessionID string, start, end time.Time) {
	r.mu.Lock()
	r.turnSeen = true
	r.mu.Unlock()
	l := line{K: "t", Turn: Turn{SessionID: sessionID, Start: start.UnixMilli(), End: end.UnixMilli()}}
	if err := appendLine(r.Dir, end, l); err != nil {
		log.Printf("usage: record turn: %v", err)
	}
}

// Run samples at every minute boundary until stop closes. After the
// machine sleeps (or the process is paused) it records only the minute
// just finished - the gap stays unrecorded, which Report counts as
// "runner not up".
func (r *Recorder) Run(stop <-chan struct{}) {
	var last int64
	day := r.now().UTC().YearDay()
	for {
		now := r.now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-stop:
			return
		case <-time.After(next.Sub(now)):
		}
		t := r.now()
		minute := t.Add(-30 * time.Second).Truncate(time.Minute)
		if minute.Unix() == last {
			continue
		}
		last = minute.Unix()
		r.sample(minute, t)
		if d := t.UTC().YearDay(); d != day {
			day = d
			if err := Prune(r.Dir, t); err != nil {
				log.Printf("usage: prune: %v", err)
			}
		}
	}
}

// sample records the minute starting at minute, observed at now.
func (r *Recorder) sample(minute, now time.Time) {
	r.mu.Lock()
	turn := r.turnSeen
	r.turnSeen = false
	r.mu.Unlock()

	var a []byte
	if turn || (r.Busy != nil && r.Busy()) {
		a = append(a, FlagTurn)
	}
	if r.AppLast != nil && !r.AppLast().Before(minute) {
		a = append(a, FlagApp)
	}
	if r.LocalIdle != nil {
		if idle, err := r.LocalIdle(); err == nil && idle <= now.Sub(minute) {
			a = append(a, FlagLocal)
		}
	}
	l := line{K: "m", Minute: Minute{T: minute.Unix(), Active: string(a)}}
	if err := appendLine(r.Dir, minute, l); err != nil {
		log.Printf("usage: record minute: %v", err)
	}
}

// Report loads the last days of data and runs the simulation for each
// idle limit (minutes). Hour-of-day buckets use the runner's local zone.
func (r *Recorder) Report(days int, limits []int) (Report, error) {
	to := r.now()
	from := to.AddDate(0, 0, -days)
	mins, turns, err := Load(r.Dir, from, to)
	if err != nil {
		return Report{}, err
	}
	return Simulate(mins, turns, from, to, limits, time.Local), nil
}
