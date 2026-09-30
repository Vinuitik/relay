package session

import (
	"testing"
	"time"
)

// waitForState polls Get(sessionID) until it reports want or the deadline
// passes, failing the test on timeout - the pump goroutine that drives
// busy/idle transitions runs asynchronously, so tests can't assert on a
// transition the instant a call returns.
func waitForState(t *testing.T, m *Manager, sessionID, want string, timeout time.Duration) Session {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last Session
	for time.Now().Before(deadline) {
		sess, err := m.Get(sessionID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		last = sess
		if sess.State == want {
			return sess
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("State = %q after %s, want %q", last.State, timeout, want)
	return last
}

func TestRawCodec_StartsBusyNotIdle(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))

	sess, err := m.Start("proj", "echo-agent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	if sess.State != StateBusy {
		t.Fatalf("State = %q right after Start for a raw-codec provider, want %q (unchanged behavior)", sess.State, StateBusy)
	}
}

func TestRawCodec_SendMessageDoesNotGoIdle(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))

	sess, err := m.Start("proj", "echo-agent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	if err := m.SendMessage(sess.ID, "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// Give the echo-agent's pump goroutine time to process the echoed line
	// back - a raw-codec provider has no "result" event/turn concept, so
	// this must never transition the state to idle.
	time.Sleep(200 * time.Millisecond)

	got, err := m.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateBusy {
		t.Fatalf("raw-codec State = %q after SendMessage, want %q (unchanged behavior - no turn concept)", got.State, StateBusy)
	}
}
