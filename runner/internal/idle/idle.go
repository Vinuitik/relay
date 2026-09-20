// Package idle implements the runner's half of the "Sleep path" described in
// ARCHITECTURE.md: a ticker that checks this runner's own busy/idle state
// and, once idle past a threshold, suspends the machine to sleep (S3 on
// Linux via `systemctl suspend`, Modern Standby on Windows via
// SetSuspendState - not S5/full poweroff; the power delta between S3 and S5
// is only ~1W, while S5 costs a ~60s boot and is far more fragile to wake
// than a machine already sitting in S3).
//
// The idle decision considers three independent "someone's using this"
// signals, and never suspends while any of them is live:
//
//	(a) Relay session state - SessionStatus.IdleStatus(), an agent session
//	    actually busy on a turn.
//	(b) phone-app foreground activity - Monitor.Activity, a heartbeat POSTed
//	    to /v1/activity every 30s while the Android app is in the
//	    foreground (internal/activity.Tracker).
//	(c) local keyboard/mouse input - Monitor.LocalInput, Windows-only
//	    (internal/activity.LocalIdleTime); unsupported on Linux, where a
//	    headless system-service deployment has no console user to detect in
//	    the first place, so that absence is a no-op, not a gap.
//
// This exists because deciding "in use" from Relay session state alone is
// wrong on a dual-use laptop: the owner can be sat typing at the machine
// with no phone session open, and the runner would happily suspend it
// mid-work. (a) OR (b) OR (c) being recent blocks suspend; only when all
// three are quiet for cfg.Timeout does it fire.
//
// SAFETY: this is disabled by default (see LoadConfig / RELAY_IDLE_SUSPEND_ENABLED).
// Do not enable it in local dev or in a container/CI - it will genuinely try
// to suspend whatever host it can reach a shutdown command on. It's a
// separate opt-in gate from the idle timeout itself, so an accidental short
// RELAY_IDLE_TIMEOUT can't cause a surprise suspend either.
package idle

import (
	"errors"
	"log"
	"os"
	"time"

	"relay/runner/internal/activity"
)

// DefaultTimeout is how long the runner must be continuously idle (no busy
// session) before it suspends to sleep, unless overridden by RELAY_IDLE_TIMEOUT.
// This is a starting heuristic, not a measured value - kept as a named
// constant (and overridable via env) specifically so it's easy to tune once
// real usage shows whether 3 minutes is too eager or too lax.
const DefaultTimeout = 3 * time.Minute

// DefaultCheckInterval is how often the Monitor re-checks idle state.
const DefaultCheckInterval = 1 * time.Minute

// Config holds the idle-suspend feature's runtime configuration.
type Config struct {
	// Enabled gates the entire feature. Defaults to false - see the package
	// doc comment. Set RELAY_IDLE_SUSPEND_ENABLED=true to turn it on.
	Enabled bool
	// Timeout is how long the runner must be idle before suspending.
	// RELAY_IDLE_TIMEOUT overrides it, parsed as a Go duration (e.g. "30m").
	Timeout time.Duration
	// CheckInterval is how often the Monitor checks idle state.
	// RELAY_IDLE_CHECK_INTERVAL overrides it, parsed as a Go duration.
	CheckInterval time.Duration
}

// LoadConfig reads the idle-suspend config from the environment:
//
//	RELAY_IDLE_SUSPEND_ENABLED=true   opt in (default: disabled)
//	RELAY_IDLE_TIMEOUT=3m             idle threshold (default: DefaultTimeout)
//	RELAY_IDLE_CHECK_INTERVAL=1m      poll interval (default: DefaultCheckInterval)
//
// Malformed duration values are logged and ignored (fall back to default)
// rather than failing runner startup over a config typo.
func LoadConfig() Config {
	cfg := Config{
		Enabled:       os.Getenv("RELAY_IDLE_SUSPEND_ENABLED") == "true",
		Timeout:       DefaultTimeout,
		CheckInterval: DefaultCheckInterval,
	}

	if v := os.Getenv("RELAY_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Timeout = d
		} else {
			log.Printf("idle: invalid RELAY_IDLE_TIMEOUT %q (%v), using default %s", v, err, DefaultTimeout)
		}
	}
	if v := os.Getenv("RELAY_IDLE_CHECK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.CheckInterval = d
		} else {
			log.Printf("idle: invalid RELAY_IDLE_CHECK_INTERVAL %q (%v), using default %s", v, err, DefaultCheckInterval)
		}
	}

	return cfg
}

// SessionStatus is the read-only view of session state the Monitor needs.
// Satisfied by *session.Manager (session.Manager.IdleStatus) - defined here
// as an interface, rather than importing session directly into the Monitor's
// signature, purely so tests can fake it without spinning up real sessions.
type SessionStatus interface {
	// IdleStatus reports whether any session is currently busy and, if not,
	// the time since which none has been.
	IdleStatus() (busy bool, idleSince time.Time)
}

// Shutdowner powers the machine off. It's an interface so tests can fake it
// - mirrors wol.PacketSender / compose.Runner in this codebase. NEVER call a
// real Shutdowner from a test.
type Shutdowner interface {
	Shutdown() error
}

// ActivitySource is the read-only view of phone-app foreground activity
// (source b, POST /v1/activity) the Monitor needs. Satisfied by
// *activity.Tracker - defined here as an interface, like SessionStatus,
// purely so tests can fake it.
type ActivitySource interface {
	// LastActive returns the last time the phone app confirmed it was in
	// the foreground, or the zero Time if it never has.
	LastActive() time.Time
}

// LocalInputFunc reports how long since local keyboard/mouse input on this
// machine (source c), or activity.ErrUnsupported if this OS has no signal
// for it (see internal/activity/local_unix.go). Matches
// activity.LocalIdleTime's signature so it can be wired in directly.
type LocalInputFunc func() (time.Duration, error)

// Monitor periodically checks SessionStatus and calls Shutdowner.Shutdown
// once the runner has been continuously idle for cfg.Timeout.
type Monitor struct {
	status   SessionStatus
	shutdown Shutdowner
	cfg      Config

	// BeforeShutdown, if set, is called synchronously right before
	// Shutdown() - e.g. to close out the uptime buffer and fire a
	// best-effort "going down" push to registered devices (see
	// cmd/runnerd/main.go). Deliberately best-effort and fire-and-forget:
	// nothing here blocks or cancels the shutdown itself, and a failure
	// inside it should never stop the machine from actually suspending.
	BeforeShutdown func()

	// Activity, if set, is consulted alongside SessionStatus for source
	// (b): a phone app foreground ping within ActivityWindow (or
	// activity.DefaultWindow, if ActivityWindow is zero) counts as "in use
	// right now" and pushes the effective idle-since forward to the
	// current tick, regardless of how idle the session state itself looks.
	// Left nil in tests that don't care about this input (the phone-blind
	// existing behavior).
	Activity ActivitySource

	// ActivityWindow overrides activity.DefaultWindow for how fresh an
	// Activity ping must be to count. Zero means use the default.
	ActivityWindow time.Duration

	// LocalInput, if set, is consulted for source (c): local keyboard/mouse
	// input on this machine. A duration d back from now counts as "in use
	// since now-d", pushing the effective idle-since forward accordingly.
	// activity.ErrUnsupported (expected on Linux, see
	// internal/activity/local_unix.go) is treated as "no signal" and
	// ignored, not logged as a failure; any other error is logged and
	// otherwise ignored - a broken local-input check must never itself
	// block or force a suspend. Left nil in tests that don't care about
	// this input.
	LocalInput LocalInputFunc

	// triggered is set once Shutdown has been called successfully, so a
	// live Monitor never calls it a second time. See tick's doc comment for
	// why, and what happens on a failed Shutdown call. It is cleared again
	// on resume - see resumeFloor.
	triggered bool

	// lastTick is when tick() last ran, used to detect that this process
	// was suspended and has now resumed. Since the switch from S5 poweroff
	// to S3 sleep, the runner process SURVIVES a suspend/resume cycle -
	// under S5 it died and a fresh Monitor started at boot, which reset
	// triggered for free. Without resume detection triggered would latch
	// true for the life of the process and the machine would auto-suspend
	// exactly once, ever.
	lastTick time.Time

	// resumeFloor is the moment this process most recently resumed from
	// suspend. It acts as a lower bound on idleSince, so a machine that
	// just woke gets a full cfg.Timeout of grace before it may suspend
	// again - otherwise IdleStatus would still report the pre-suspend turn
	// end, already older than the timeout, and the runner would re-suspend
	// seconds after waking.
	resumeFloor time.Time
}

// resumeGraceFactor multiplies CheckInterval to decide whether a gap
// between ticks was a suspend rather than ordinary scheduler jitter. A
// wall-clock jump of more than this many check intervals could not have
// happened while the process was running normally.
const resumeGraceFactor = 3

// NewMonitor builds a Monitor. cfg.CheckInterval and cfg.Timeout should
// normally come from LoadConfig.
func NewMonitor(status SessionStatus, shutdown Shutdowner, cfg Config) *Monitor {
	return &Monitor{status: status, shutdown: shutdown, cfg: cfg}
}

// Run blocks forever, checking idle status every cfg.CheckInterval. Callers
// (main.go) should only invoke this in its own goroutine, and only when
// cfg.Enabled is true - Run itself doesn't check Enabled, that gate belongs
// at the call site so it's obvious from main.go whether the ticker was even
// started.
func (mon *Monitor) Run() {
	ticker := time.NewTicker(mon.cfg.CheckInterval)
	defer ticker.Stop()
	for range ticker.C {
		mon.tick()
	}
}

// tick runs one idle check.
//
// Behavior after Shutdown() is called once: if it succeeds, Monitor marks
// itself triggered and never calls Shutdown again - a successful suspend
// should freeze this process shortly afterward anyway (and it resumes from
// exactly where it left off on wake, still triggered, which is correct -
// the runner shouldn't immediately re-suspend itself the instant it wakes
// and ticks again). If Shutdown() itself returns an error (the command
// failed to even run - e.g. the polkit/sudoers gap shutdown_unix.go's
// Shutdown documents), triggered is left false so the next tick retries -
// the machine demonstrably didn't suspend, so there's nothing unsafe about
// trying again. This is the simplest behavior that avoids a shutdown-retry
// storm without silently giving up on a real failure.
func (mon *Monitor) tick() {
	now := time.Now()
	// Resume detection: a wall-clock gap far larger than CheckInterval means
	// this process was frozen by a suspend and has just come back. Clear the
	// one-shot latch and restart the idle countdown from now.
	if !mon.lastTick.IsZero() && now.Sub(mon.lastTick) > time.Duration(resumeGraceFactor)*mon.cfg.CheckInterval {
		log.Printf("idle: detected resume after %s of wall-clock gap, re-arming idle-suspend", now.Sub(mon.lastTick).Round(time.Second))
		mon.triggered = false
		mon.resumeFloor = now
	}
	mon.lastTick = now

	if mon.triggered {
		return
	}

	busy, idleSince := mon.status.IdleStatus()
	if idleSince.Before(mon.resumeFloor) {
		idleSince = mon.resumeFloor
	}
	if busy {
		return
	}

	// Effective idle-since is the LATEST of: session state (above), a
	// fresh phone-app activity ping (source b), local keyboard/mouse input
	// (source c), and resumeFloor (already folded in above) - never
	// suspend while any of them says someone's around. A session going
	// busy still short-circuits above regardless of these.
	if mon.Activity != nil {
		if last := mon.Activity.LastActive(); !last.IsZero() {
			window := mon.ActivityWindow
			if window <= 0 {
				window = activity.DefaultWindow
			}
			if now.Sub(last) <= window && now.After(idleSince) {
				idleSince = now
			}
		}
	}
	if mon.LocalInput != nil {
		switch d, err := mon.LocalInput(); {
		case err == nil:
			if localSince := now.Add(-d); localSince.After(idleSince) {
				idleSince = localSince
			}
		case errors.Is(err, activity.ErrUnsupported):
			// Expected on Linux - no local input signal, fall back to (a)
			// and (b) only. Not logged; this isn't a failure.
		default:
			log.Printf("idle: local input check failed: %v", err)
		}
	}

	if time.Since(idleSince) < mon.cfg.Timeout {
		return
	}

	log.Printf("idle: no active session for >= %s, suspending to sleep", mon.cfg.Timeout)
	if mon.BeforeShutdown != nil {
		mon.BeforeShutdown()
	}
	if err := mon.shutdown.Shutdown(); err != nil {
		log.Printf("idle: shutdown failed: %v", err)
		return
	}
	mon.triggered = true
}
