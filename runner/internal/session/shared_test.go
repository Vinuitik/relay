package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShared_ListAgentChatsMarksLinked(t *testing.T) {
	m := newFakeACPManager(t)
	sess, err := m.Start("proj", "fake-acp") // the fake's new session is "fs1"
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	chats, err := m.ListAgentChats("proj", "fake-acp")
	if err != nil {
		t.Fatalf("ListAgentChats: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("got %d chats, want 2: %+v", len(chats), chats)
	}
	if c := chats[0]; c.AgentSessionID != "vs1" || c.Title != "From VS Code" || c.SessionID != "" {
		t.Errorf("chats[0] = %+v, want the unlinked VS Code chat", c)
	}
	if c := chats[1]; c.AgentSessionID != "fs1" || c.SessionID != sess.ID {
		t.Errorf("chats[1] = %+v, want fs1 linked to %s", c, sess.ID)
	}
}

func TestShared_AdoptReplaysHistory(t *testing.T) {
	m := newFakeACPManager(t)
	sess, existing, err := m.Adopt("proj", "fake-acp", "vs1")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	defer m.Stop(sess.ID)
	if existing {
		t.Error("first Adopt reported existing")
	}
	want := []struct{ role, text string }{{"user", "earlier question"}, {"agent", "earlier answer"}, {"tool", "Read b.go"}}
	if len(sess.Messages) != len(want) {
		t.Fatalf("messages = %+v, want %d replayed", sess.Messages, len(want))
	}
	for i, w := range want {
		if got := sess.Messages[i]; got.Role != w.role || got.Text != w.text {
			t.Errorf("message %d = %s %q, want %s %q", i, got.Role, got.Text, w.role, w.text)
		}
	}

	again, existing, err := m.Adopt("proj", "fake-acp", "vs1")
	if err != nil || !existing || again.ID != sess.ID {
		t.Errorf("second Adopt = %s existing=%v err=%v, want the same session %s", again.ID, existing, err, sess.ID)
	}

	if err := m.SendMessage(sess.ID, "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	after := waitForState(t, m, sess.ID, StateIdle, 5*time.Second)
	if len(after.Messages) != 6 || after.Messages[4].Text != "Hello" {
		t.Errorf("after a turn = %+v, want history + hi + Hello + tool", after.Messages)
	}
}

func TestShared_ReloadsWhenTranscriptGrows(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	transcript := filepath.Join(cfg, "projects", "C--p", "vs1.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newFakeACPManager(t)
	sess, _, err := m.Adopt("proj", "fake-acp", "vs1")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	defer m.Stop(sess.ID)
	if err := m.SendMessage(sess.ID, "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	waitForState(t, m, sess.ID, StateIdle, 5*time.Second)

	// Unchanged transcript: Sync keeps the phone's transcript as is.
	if err := m.Sync(sess.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got, _ := m.Get(sess.ID); len(got.Messages) != 6 {
		t.Fatalf("Sync without changes rebuilt the transcript: %d messages", len(got.Messages))
	}

	appendLine := func(line string) {
		f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(line + "\n")
		f.Close()
	}

	// Bookkeeping only (Claude writes these on load/exit): no reload.
	appendLine(`{"type":"cost-state","sessionId":"vs1"}`)
	if err := m.Sync(sess.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got, _ := m.Get(sess.ID); len(got.Messages) != 6 {
		t.Fatalf("bookkeeping line rebuilt the transcript: %d messages", len(got.Messages))
	}

	// "VS Code" appends a turn: Sync reloads from the agent's transcript.
	appendLine(`{"type":"user","message":{"role":"user","content":"from vscode"}}`)
	if err := m.Sync(sess.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	got, _ := m.Get(sess.ID)
	if len(got.Messages) != 3 || got.Messages[0].Text != "earlier question" {
		t.Fatalf("after VS Code wrote, messages = %+v, want the replayed 3", got.Messages)
	}
	// Caught up now: another Sync is a no-op.
	if err := m.Sync(sess.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if again, _ := m.Get(sess.ID); len(again.Messages) != 3 {
		t.Errorf("second Sync changed the transcript: %d messages", len(again.Messages))
	}
}
