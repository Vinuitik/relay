package idle

import (
	"errors"
	"os"
	"testing"
	"time"
)

// fakeStatus is a fake SessionStatus for tests - never touches real sessions.
type fakeStatus struct {
	busy      bool
	idleSince time.Time
}

func (f fakeStatus) IdleStatus() (bool, time.Time) {
	return f.busy, f.idleSince
}

// fakeShutdowner records calls instead of ever invoking a real shutdown
// command - a test must NEVER exercise osShutdowner.
type fakeShutdowner struct {
	calls int
	err   error
}

func (f *fakeShutdowner) Shutdown() error {
	f.calls++
	return f.err
}

func TestTick_BusyNeverTriggers(t *testing.T) {
	status := fakeStatus{busy: true, idleSince: time.Now().Add(-24 * time.Hour)}
	shutdown := &fakeShutdowner{}
	mon := NewMonitor(status, shutdown, Config{Timeout: time.Minute, CheckInterval: time.Second})

	for i := 0; i < 5; i++ {
		mon.tick()
	}

	if shutdown.calls != 0 {
		t.Fatalf("shutdown.calls = %d, want 0 (busy should never trigger)", shutdown.calls)
	}
}

func TestTick_IdleBelowThresholdDoesNotTrigger(t *testing.T) {
	status := fakeStatus{busy: false, idleSince: time.Now().Add(-10 * time.Second)}
	shutdown := &fakeShutdowner{}
	mon := NewMonitor(status, shutdown, Config{Timeout: time.Minute, CheckInterval: time.Second})

	mon.tick()

	if shutdown.calls != 0 {
		t.Fatalf("shutdown.calls = %d, want 0 (idle duration below threshold)", shutdown.calls)
	}
}

func TestTick_IdleAtOrPastThresholdTriggersOnce(t *testing.T) {
	status := fakeStatus{busy: false, idleSince: time.Now().Add(-time.Hour)}
	shutdown := &fakeShutdowner{}
	mon := NewMonitor(status, shutdown, Config{Timeout: time.Minute, CheckInterval: time.Second})

	// Multiple ticks after the threshold is crossed must still only shut
	// down once - see tick's doc comment.
	for i := 0; i < 5; i++ {
		mon.tick()
	}

	if shutdown.calls != 1 {
		t.Fatalf("shutdown.calls = %d, want exactly 1", shutdown.calls)
	}
}

func TestTick_FailedShutdownRetriesOnNextTick(t *testing.T) {
	status := fakeStatus{busy: false, idleSince: time.Now().Add(-time.Hour)}
	shutdown := &fakeShutdowner{err: errors.New("boom")}
	mon := NewMonitor(status, shutdown, Config{Timeout: time.Minute, CheckInterval: time.Second})

	mon.tick()
	mon.tick()

	if shutdown.calls != 2 {
		t.Fatalf("shutdown.calls = %d, want 2 (failed shutdown should retry)", shutdown.calls)
	}
	if mon.triggered {
		t.Fatalf("triggered = true after only failed Shutdown calls, want false")
	}
}

func TestLoadConfig_DisabledByDefault(t *testing.T) {
	for _, k := range []string{"RELAY_IDLE_SUSPEND_ENABLED", "RELAY_IDLE_TIMEOUT", "RELAY_IDLE_CHECK_INTERVAL"} {
		old, had := os.LookupEnv(k)
		os.Unsetenv(k)
		if had {
			defer os.Setenv(k, old)
		}
	}

	cfg := LoadConfig()

	if cfg.Enabled {
		t.Fatal("Enabled = true with no env vars set, want false (must be opt-in - see package doc)")
	}
	if cfg.Timeout != DefaultTimeout {
		t.Fatalf("Timeout = %s, want default %s", cfg.Timeout, DefaultTimeout)
	}
	if cfg.CheckInterval != DefaultCheckInterval {
		t.Fatalf("CheckInterval = %s, want default %s", cfg.CheckInterval, DefaultCheckInterval)
	}
}

func TestLoadConfig_EnvOverrides(t *testing.T) {
	t.Setenv("RELAY_IDLE_SUSPEND_ENABLED", "true")
	t.Setenv("RELAY_IDLE_TIMEOUT", "5m")
	t.Setenv("RELAY_IDLE_CHECK_INTERVAL", "10s")

	cfg := LoadConfig()

	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if cfg.Timeout != 5*time.Minute {
		t.Fatalf("Timeout = %s, want 5m", cfg.Timeout)
	}
	if cfg.CheckInterval != 10*time.Second {
		t.Fatalf("CheckInterval = %s, want 10s", cfg.CheckInterval)
	}
}

func TestLoadConfig_MalformedDurationFallsBackToDefault(t *testing.T) {
	t.Setenv("RELAY_IDLE_TIMEOUT", "not-a-duration")

	cfg := LoadConfig()

	if cfg.Timeout != DefaultTimeout {
		t.Fatalf("Timeout = %s, want default %s on malformed input", cfg.Timeout, DefaultTimeout)
	}
}
