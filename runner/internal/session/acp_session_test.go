package session

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain doubles as a fake ACP agent: when RELAY_FAKE_ACP=1 the test
// binary, re-run as a provider subprocess, speaks just enough ACP for these
// tests instead of running tests - no Node/claude-agent-acp needed.
func TestMain(m *testing.M) {
	if os.Getenv("RELAY_FAKE_ACP") == "1" {
		runFakeAgent()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runFakeAgent scripts replies by prompt text:
//   - "perm": a tool call that asks permission; allowed → end_turn,
//     cancelled → "cancelled".
//   - "hang": works until session/cancel arrives → "cancelled".
//   - anything else: streams "Hel"+"lo" plus one completed tool call.
func runFakeAgent() {
	var writeMu sync.Mutex
	send := func(v map[string]any) {
		v["jsonrpc"] = "2.0"
		b, _ := json.Marshal(v)
		writeMu.Lock()
		os.Stdout.Write(append(b, '\n'))
		writeMu.Unlock()
	}
	update := func(u map[string]any) {
		send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "fs1", "update": u}})
	}
	permReplies := make(chan string, 1) // outcome of the permission request
	cancelled := make(chan struct{}, 1)

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			send(map[string]any{"id": m.ID, "result": map[string]any{"protocolVersion": 1}})
		case "session/new":
			send(map[string]any{"id": m.ID, "result": map[string]any{
				"sessionId": "fs1",
				"modes": map[string]any{"currentModeId": "default", "availableModes": []map[string]string{
					{"id": "default", "name": "Manual"}, {"id": "bypassPermissions", "name": "Bypass permissions"},
				}},
			}})
		case "session/resume":
			var r struct {
				SessionID string `json:"sessionId"`
			}
			json.Unmarshal(m.Params, &r)
			if r.SessionID != "fs1" {
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32602, "message": "unknown session"}})
				continue
			}
			send(map[string]any{"id": m.ID, "result": map[string]any{
				"modes": map[string]any{"currentModeId": "default", "availableModes": []map[string]string{
					{"id": "default", "name": "Manual"}, {"id": "bypassPermissions", "name": "Bypass permissions"},
				}},
			}})
		case "session/load":
			// Replays a short history: one question, one answer, one tool call.
			update(map[string]any{"sessionUpdate": "user_message_chunk", "messageId": "u0", "content": map[string]string{"type": "text", "text": "earlier question"}})
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "a0", "content": map[string]string{"type": "text", "text": "earlier "}})
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "a0", "content": map[string]string{"type": "text", "text": "answer"}})
			update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t0", "title": "Read b.go", "kind": "read", "status": "completed"})
			send(map[string]any{"id": m.ID, "result": map[string]any{
				"modes": map[string]any{"currentModeId": "default", "availableModes": []map[string]string{
					{"id": "default", "name": "Manual"}, {"id": "bypassPermissions", "name": "Bypass permissions"},
				}},
			}})
		case "session/list":
			send(map[string]any{"id": m.ID, "result": map[string]any{"sessions": []map[string]string{
				{"sessionId": "vs1", "cwd": "/p", "title": "From VS Code", "updatedAt": "2026-10-04T10:00:00Z"},
				{"sessionId": "fs1", "cwd": "/p", "title": "From Relay", "updatedAt": "2026-10-04T09:00:00Z"},
			}}})
		case "session/set_mode":
			send(map[string]any{"id": m.ID, "result": map[string]any{}})
		case "session/cancel":
			cancelled <- struct{}{}
		case "session/prompt":
			var p struct {
				Prompt []struct{ Text string } `json:"prompt"`
			}
			json.Unmarshal(m.Params, &p)
			text := p.Prompt[0].Text
			id := m.ID
			go func() {
				stop := "end_turn"
				switch {
				case strings.Contains(text, "perm"):
					update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Write x.txt", "kind": "edit", "status": "pending"})
					send(map[string]any{"id": 100, "method": "session/request_permission", "params": map[string]any{
						"sessionId": "fs1",
						"toolCall":  map[string]any{"toolCallId": "t1", "title": "Write x.txt", "kind": "edit"},
						"options": []map[string]string{
							{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
							{"optionId": "reject", "name": "Reject", "kind": "reject_once"},
						},
					}})
					if <-permReplies == "cancelled" {
						<-cancelled
						stop = "cancelled"
					} else {
						update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed"})
					}
				case strings.Contains(text, "hang"):
					<-cancelled
					stop = "cancelled"
				default:
					time.Sleep(100 * time.Millisecond)
					update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "m1", "content": map[string]string{"type": "text", "text": "Hel"}})
					update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "m1", "content": map[string]string{"type": "text", "text": "lo"}})
					update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t2", "title": "Preparing…", "kind": "read", "status": "pending"})
					update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t2", "title": "Read a.go", "status": "completed", "content": []any{}})
				}
				send(map[string]any{"id": id, "result": map[string]any{"stopReason": stop}})
			}()
		case "":
			// A response from the client - only the permission reply is expected.
			var r struct {
				Outcome struct{ Outcome string } `json:"outcome"`
			}
			json.Unmarshal(m.Result, &r)
			permReplies <- r.Outcome.Outcome
		}
	}
}

func newFakeACPManager(t *testing.T) *Manager {
	t.Helper()
	t.Setenv("RELAY_FAKE_ACP", "1")
	m := NewManager(testResolver(t.TempDir()))
	m.providers["fake-acp"] = ProviderCommand{Name: os.Args[0], Args: []string{"-test.run=^$"}, ACP: true}
	return m
}

func TestACP_StartsIdleInDefaultMode(t *testing.T) {
	m := newFakeACPManager(t)
	sess, err := m.Start("proj", "fake-acp")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)
	if sess.State != StateIdle {
		t.Errorf("State = %q, want idle", sess.State)
	}
	if sess.Mode != "bypassPermissions" {
		t.Errorf("Mode = %q, want bypassPermissions (the Manager default)", sess.Mode)
	}
	if len(sess.Modes) != 2 {
		t.Errorf("Modes = %v, want the agent's 2 modes", sess.Modes)
	}
}

func TestACP_TurnStreamsTextAndToolCalls(t *testing.T) {
	m := newFakeACPManager(t)
	var finished sync.WaitGroup
	finished.Add(1)
	m.OnFinished = func(Session) { finished.Done() }
	sess, err := m.Start("proj", "fake-acp")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { m.OnFinished = nil; m.Stop(sess.ID) }()

	if err := m.SendMessage(sess.ID, "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got, _ := m.Get(sess.ID); got.State != StateBusy {
		t.Errorf("State right after SendMessage = %q, want busy", got.State)
	}
	if err := m.SendMessage(sess.ID, "again"); err == nil {
		t.Error("second SendMessage during a turn succeeded, want ErrTurnInProgress")
	}
	got := waitForState(t, m, sess.ID, StateIdle, 5*time.Second)
	finished.Wait()

	if len(got.Messages) != 3 {
		t.Fatalf("Messages = %+v, want user + agent + tool", got.Messages)
	}
	if got.Messages[1].Role != "agent" || got.Messages[1].Text != "Hello" {
		t.Errorf("agent message = %+v, want chunks merged into \"Hello\"", got.Messages[1])
	}
	tool := got.Messages[2]
	if tool.Role != "tool" || tool.Text != "Read a.go" || tool.Status != "completed" || tool.ToolKind != "read" {
		t.Errorf("tool message = %+v, want updated title/status in place", tool)
	}
}

func TestACP_PermissionWaitsThenResumes(t *testing.T) {
	m := newFakeACPManager(t)
	needsInput := make(chan Session, 1)
	m.OnNeedsInput = func(s Session) { needsInput <- s }
	sess, err := m.Start("proj", "fake-acp")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	if err := m.SendMessage(sess.ID, "perm"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	waiting := waitForState(t, m, sess.ID, StateWaiting, 5*time.Second)
	if waiting.PendingPermission == nil || waiting.PendingPermission.Title != "Write x.txt" || len(waiting.PendingPermission.Options) != 2 {
		t.Fatalf("PendingPermission = %+v", waiting.PendingPermission)
	}
	select {
	case <-needsInput:
	case <-time.After(2 * time.Second):
		t.Fatal("OnNeedsInput not called")
	}
	if busy, _ := m.IdleStatus(); busy {
		t.Error("IdleStatus busy while waiting on permission, want idle (waiting on the human)")
	}

	if _, err := m.RespondPermission(sess.ID, "nope"); err == nil {
		t.Error("unknown option accepted")
	}
	if _, err := m.RespondPermission(sess.ID, "allow"); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
	done := waitForState(t, m, sess.ID, StateIdle, 5*time.Second)
	if done.PendingPermission != nil {
		t.Error("PendingPermission still set after the turn ended")
	}
	if last := done.Messages[len(done.Messages)-1]; last.Status != "completed" {
		t.Errorf("tool status = %q, want completed", last.Status)
	}
}

func TestACP_CancelStopsTurnButKeepsSession(t *testing.T) {
	m := newFakeACPManager(t)
	sess, err := m.Start("proj", "fake-acp")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	if err := m.SendMessage(sess.ID, "hang"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, err := m.Cancel(sess.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got := waitForState(t, m, sess.ID, StateIdle, 5*time.Second)
	if last := got.Messages[len(got.Messages)-1]; last.Text != "(stopped)" {
		t.Errorf("last message = %+v, want (stopped)", last)
	}
	// Session is still usable.
	if err := m.SendMessage(sess.ID, "hi"); err != nil {
		t.Fatalf("SendMessage after cancel: %v", err)
	}
	waitForState(t, m, sess.ID, StateIdle, 5*time.Second)
}

func TestACP_CancelWhileWaitingOnPermission(t *testing.T) {
	m := newFakeACPManager(t)
	sess, err := m.Start("proj", "fake-acp")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	if err := m.SendMessage(sess.ID, "perm"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	waitForState(t, m, sess.ID, StateWaiting, 5*time.Second)
	if _, err := m.Cancel(sess.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got := waitForState(t, m, sess.ID, StateIdle, 5*time.Second)
	if got.Messages[len(got.Messages)-2].Status != "failed" {
		t.Errorf("unfinished tool call status = %q, want failed", got.Messages[len(got.Messages)-2].Status)
	}
}

func TestACP_SetMode(t *testing.T) {
	m := newFakeACPManager(t)
	sess, err := m.Start("proj", "fake-acp")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)

	got, err := m.SetMode(sess.ID, "default")
	if err != nil || got.Mode != "default" {
		t.Fatalf("SetMode = %q, %v; want default", got.Mode, err)
	}
	if _, err := m.SetMode(sess.ID, "yolo"); err == nil {
		t.Error("unknown mode accepted")
	}
}

func TestACP_NotSupportedOnRawProvider(t *testing.T) {
	m := NewManager(testResolver(t.TempDir()))
	sess, err := m.Start("proj", "echo-agent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(sess.ID)
	if _, err := m.Cancel(sess.ID); err == nil {
		t.Error("Cancel on raw provider succeeded, want ErrNotSupported")
	}
}

func TestACP_PersistRestartResume(t *testing.T) {
	store := t.TempDir()
	projectDir := t.TempDir()

	m1 := newFakeACPManager(t)
	m1.resolveDir = testResolver(projectDir)
	if err := m1.SetStoreDir(store); err != nil {
		t.Fatal(err)
	}
	sess, err := m1.Start("proj", "fake-acp")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m1.SetMode(sess.ID, "default"); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if err := m1.SendMessage(sess.ID, "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	waitForState(t, m1, sess.ID, StateIdle, 5*time.Second)
	m1.SaveAll()
	saved, _ := m1.Get(sess.ID)
	if saved.LastActiveAt == "" {
		t.Error("LastActiveAt not set after save")
	}
	m1.Shutdown()
	time.Sleep(300 * time.Millisecond)

	// "Restart": a new Manager over the same store.
	m2 := newFakeACPManager(t)
	m2.resolveDir = testResolver(projectDir)
	if err := m2.SetStoreDir(store); err != nil {
		t.Fatal(err)
	}
	if err := m2.LoadPersisted(); err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}
	got, err := m2.Get(sess.ID)
	if err != nil {
		t.Fatalf("restored session missing: %v", err)
	}
	if got.State != StateIdle || len(got.Messages) != 3 {
		t.Fatalf("restored = state %q, %d messages; want idle with the 3 saved messages", got.State, len(got.Messages))
	}
	if got.LastActiveAt != saved.LastActiveAt {
		t.Errorf("LastActiveAt = %q after restore, want the saved %q", got.LastActiveAt, saved.LastActiveAt)
	}

	// The next message resumes the agent conversation and re-applies the mode.
	if err := m2.SendMessage(sess.ID, "hi again"); err != nil {
		t.Fatalf("SendMessage after restore: %v", err)
	}
	after := waitForState(t, m2, sess.ID, StateIdle, 5*time.Second)
	defer m2.Stop(sess.ID)
	if last := after.Messages[len(after.Messages)-2]; last.Role != "agent" || last.Text != "Hello" {
		t.Errorf("reply after resume = %+v, want the agent's Hello", last)
	}
	if after.Mode != "default" {
		t.Errorf("Mode after resume = %q, want the saved \"default\"", after.Mode)
	}
}

func TestRestore_RawSessionComesBackFinished(t *testing.T) {
	rec := restoreRecord(persisted{Session: Session{ID: "x", State: StateBusy, Provider: "echo-agent"}})
	if rec.data.State != StateFinished || rec.dormant {
		t.Errorf("raw session restored as %q (dormant=%v), want finished", rec.data.State, rec.dormant)
	}
}
