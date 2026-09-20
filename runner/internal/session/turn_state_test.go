package session

import (
	"sync"
	"testing"
	"time"
)

// fakeClaudeStreamJSONScript is a scripted stand-in for the real `claude`
// CLI's stream-json wire format - never a real `claude` binary. It reads
// one line from stdin per loop iteration (one line == one turn, matching
// encodeClaudeStreamJSONUserMessage's one-line-per-message framing) and, on
// each, emits an "assistant" text event followed by the end-of-turn
// "result" event. The `sleep` gives tests a reliable window to observe
// StateBusy before the turn completes, instead of racing a near-instant
// reply.
const fakeClaudeStreamJSONScript = `while IFS= read -r line; do
  sleep 0.2
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
  echo '{"type":"result"}'
done`

// fakeClaudeStreamJSONOneTurnScript is like fakeClaudeStreamJSONScript but
// exits cleanly (status 0) after exactly one turn, instead of looping
// forever - used to test that a natural process exit still finalizes the
// session to StateFinished on top of the per-turn idle transition.
const fakeClaudeStreamJSONOneTurnScript = `read -r line
sleep 0.2
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
echo '{"type":"result"}'`

// registerFakeProvider injects a claude-stream-json-codec provider backed
// by a scripted shell command - directly into Manager.providers (same
// package, so this reaches around resolveProvider's normal env-override/
// auto-detect paths, neither of which can produce a *custom command* with
// Codec: codecClaudeStreamJSON at once; see resolveProvider's doc comment).
func registerFakeProvider(m *Manager, name, script string) {
	m.providers[name] = ProviderCommand{
		Name:  "sh",
		Args:  []string{"-c", script},
		Codec: codecClaudeStreamJSON,
	}
}

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

func TestClaudeStreamJSON_IdleAtStart(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))
	registerFakeProvider(m, "fake-claude", fakeClaudeStreamJSONScript)

	sess, err := m.Start("proj", "fake-claude")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	if sess.State != StateIdle {
		t.Fatalf("State = %q right after Start, want %q (long-lived process, waiting for input)", sess.State, StateIdle)
	}
}

func TestClaudeStreamJSON_BusyOnSend(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))
	registerFakeProvider(m, "fake-claude", fakeClaudeStreamJSONScript)

	sess, err := m.Start("proj", "fake-claude")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	if err := m.SendMessage(sess.ID, "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	got, err := m.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateBusy {
		t.Fatalf("State = %q immediately after SendMessage, want %q", got.State, StateBusy)
	}
}

func TestClaudeStreamJSON_IdleOnResult(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))
	registerFakeProvider(m, "fake-claude", fakeClaudeStreamJSONScript)

	sess, err := m.Start("proj", "fake-claude")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	if err := m.SendMessage(sess.ID, "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	waitForState(t, m, sess.ID, StateIdle, 2*time.Second)
}

func TestClaudeStreamJSON_OnFinishedFiresOncePerTurn(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))
	registerFakeProvider(m, "fake-claude", fakeClaudeStreamJSONScript)

	var mu sync.Mutex
	var got []Session
	m.OnFinished = func(s Session) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, s)
	}

	sess, err := m.Start("proj", "fake-claude")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	if err := m.SendMessage(sess.ID, "one"); err != nil {
		t.Fatalf("SendMessage 1: %v", err)
	}
	waitForState(t, m, sess.ID, StateIdle, 2*time.Second)

	if err := m.SendMessage(sess.ID, "two"); err != nil {
		t.Fatalf("SendMessage 2: %v", err)
	}
	waitForState(t, m, sess.ID, StateIdle, 2*time.Second)

	// OnFinished runs in its own goroutine (see notifyFinished) - give the
	// second call a moment to land before asserting the final count.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("OnFinished called %d times across 2 turns, want exactly 2", len(got))
	}
	for i, s := range got {
		if s.State != StateIdle {
			t.Fatalf("OnFinished call %d snapshot State = %q, want %q", i, s.State, StateIdle)
		}
	}
}

func TestClaudeStreamJSON_ProcessExitStillFinishes(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))
	registerFakeProvider(m, "fake-claude-one-turn", fakeClaudeStreamJSONOneTurnScript)

	sess, err := m.Start("proj", "fake-claude-one-turn")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := m.SendMessage(sess.ID, "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// The script exits right after its one turn's "result" event, so the
	// session should settle at StateFinished (not stay at StateIdle) once
	// the subprocess itself exits cleanly.
	final := waitForState(t, m, sess.ID, StateFinished, 2*time.Second)
	if final.FinishedAt == nil {
		t.Fatal("FinishedAt = nil on a finished session, want set")
	}
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
