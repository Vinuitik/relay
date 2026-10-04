package session

import (
	"strings"
	"testing"
	"time"
)

// After a re-login, idle agents are restarted silently (no transcript note)
// and resume on the next message; a session mid-turn is left alone.
func TestRestartIdleAgents_SkipsBusyAndAddsNoNote(t *testing.T) {
	m := newFakeACPManager(t)
	idle, err := m.Start("proj", "fake-acp")
	if err != nil {
		t.Fatalf("Start idle: %v", err)
	}
	defer m.Stop(idle.ID)
	busy, err := m.Start("proj", "fake-acp")
	if err != nil {
		t.Fatalf("Start busy: %v", err)
	}
	defer m.Stop(busy.ID)

	if err := m.SendMessage(busy.ID, "hang"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// Wait until the busy session's turn is really in flight.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, _ := m.lookup(busy.ID)
		rec.mu.Lock()
		active := rec.turnActive
		rec.mu.Unlock()
		if active || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	busyRec, _ := m.lookup(busy.ID)
	busyClient := busyRec.client()

	if n := m.RestartIdleAgents("fake-acp"); n != 1 {
		t.Fatalf("RestartIdleAgents = %d, want 1 (only the idle session)", n)
	}
	if n := m.RestartIdleAgents("other-provider"); n != 0 {
		t.Errorf("RestartIdleAgents(other) = %d, want 0", n)
	}
	idleRec, _ := m.lookup(idle.ID)
	if idleRec.client() != nil {
		t.Error("idle session still has a live agent after restart")
	}
	if busyRec.client() != busyClient {
		t.Error("busy session's agent was touched")
	}
	time.Sleep(300 * time.Millisecond) // let awaitACPExit observe the kill
	got, _ := m.Get(idle.ID)
	for _, msg := range got.Messages {
		if strings.Contains(msg.Text, "agent process exited") {
			t.Errorf("deliberate restart added a note: %q", msg.Text)
		}
	}
	if got.State != StateIdle {
		t.Errorf("idle session state = %q, want idle", got.State)
	}

	// Next message respawns + resumes.
	if err := m.SendMessage(idle.ID, "hi"); err != nil {
		t.Fatalf("SendMessage after restart: %v", err)
	}
	after := waitForState(t, m, idle.ID, StateIdle, 5*time.Second)
	if last := after.Messages[len(after.Messages)-2]; last.Role != "agent" || last.Text != "Hello" {
		t.Errorf("reply after restart = %+v, want Hello", last)
	}
	m.Cancel(busy.ID)
}
