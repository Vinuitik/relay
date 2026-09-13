package session

import (
	"testing"
	"time"
)

func TestIdleStatus_NoSessionsIsIdleSinceManagerCreation(t *testing.T) {
	before := time.Now()
	m := NewManager(testResolver(t.TempDir()))
	after := time.Now()

	busy, idleSince := m.IdleStatus()
	if busy {
		t.Fatal("busy = true with no sessions, want false")
	}
	if idleSince.Before(before) || idleSince.After(after) {
		t.Fatalf("idleSince = %v, want between %v and %v", idleSince, before, after)
	}
}

func TestIdleStatus_BusyWhileSessionRunning(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))

	sess, err := m.Start("proj", "echo-agent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	busy, _ := m.IdleStatus()
	if !busy {
		t.Fatal("busy = false with a freshly started session, want true")
	}
}

func TestIdleStatus_IdleAfterStopIsSinceFinish(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))

	sess, err := m.Start("proj", "echo-agent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	before := time.Now()
	if _, err := m.Stop(sess.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	after := time.Now()

	busy, idleSince := m.IdleStatus()
	if busy {
		t.Fatal("busy = true after Stop, want false")
	}
	if idleSince.Before(before.Add(-time.Second)) || idleSince.After(after.Add(time.Second)) {
		t.Fatalf("idleSince = %v, want close to the Stop() call (between %v and %v)", idleSince, before, after)
	}
}

func TestIdleStatus_BusyIfAnySessionStillBusy(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))

	finished, err := m.Start("proj", "echo-agent")
	if err != nil {
		t.Fatalf("Start finished: %v", err)
	}
	if _, err := m.Stop(finished.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	stillBusy, err := m.Start("proj", "echo-agent")
	if err != nil {
		t.Fatalf("Start stillBusy: %v", err)
	}
	defer m.Stop(stillBusy.ID)

	busy, _ := m.IdleStatus()
	if !busy {
		t.Fatal("busy = false while a second session is still busy, want true")
	}
}
