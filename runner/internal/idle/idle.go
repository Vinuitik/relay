// Package idle implements the runner's half of the "Sleep path" described in
// ARCHITECTURE.md: a ticker that checks this runner's own busy/idle state
// and, once idle past a threshold with no active agent task, suspends the
// machine to sleep (S3 on Linux via `systemctl suspend`, Modern Standby on
// Windows via SetSuspendState - not S5/full poweroff; the power delta
// between S3 and S5 is only ~1W, while S5 costs a ~60s boot and is far more
// fragile to wake than a machine already sitting in S3).
//
// SAFETY: this is disabled by default (see LoadConfig / RELAY_IDLE_SUSPEND_ENABLED).
// Do not enable it in local dev or in a container/CI - it will genuinely try
// to suspend whatever host it can reach a shutdown command on. It's a
// separate opt-in gate from the idle timeout itself, so an accidental short
// RELAY_IDLE_TIMEOUT can't cause a surprise suspend either.
package idle

import (
	"log"
	"os"
	"time"
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

	// triggered is set once Shutdown has been called successfully, so a
	// live Monitor never calls it a second time. See tick's doc comment for
	// why, and what happens on a failed Shutdown call.
	triggered bool
}

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
	if mon.triggered {
		return
	}

	busy, idleSince := mon.status.IdleStatus()
	if busy {
		return
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
