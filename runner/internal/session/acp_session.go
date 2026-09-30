package session

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"relay/runner/internal/acp"
)

// ACP session lifecycle (see runner/FLOWS.md "Sessions (ACP)"):
//
//	startACP: spawn agent → initialize → session/new(cwd) → set_mode(DefaultMode) → idle
//	sendACP:  idle → busy, session/prompt runs in runTurn's goroutine
//	          streamed session/update → transcript (onUpdate)
//	          session/request_permission → waiting + OnNeedsInput (onRequest)
//	          RespondPermission → busy again
//	runTurn:  prompt returns (end_turn/cancelled/...) → idle + OnFinished
//	Cancel:   session/cancel → prompt returns "cancelled"

// clientName/Version identify the runner to the agent in initialize.
const (
	clientName    = "relay-runner"
	clientVersion = "v1"
)

func (m *Manager) startACP(id, projectID, provider, dir string, pc ProviderCommand) (Session, error) {
	startTime := nowT()
	rec := &record{
		data: Session{
			ID:        id,
			ProjectID: projectID,
			Provider:  provider,
			State:     StateIdle,
			CreatedAt: startTime.Format(time.RFC3339),
			Messages:  []Message{},
		},
		idleSince: startTime,
	}

	client, err := acp.Start(pc.Name, pc.Args, dir, acp.Handlers{
		OnNotification: func(method string, params json.RawMessage) { m.onUpdate(rec, method, params) },
		OnRequest:      func(reqID json.RawMessage, method string, params json.RawMessage) { m.onRequest(rec, reqID, method, params) },
	})
	if err != nil {
		return Session{}, fmt.Errorf("start provider %q: %w", provider, err)
	}
	fail := func(step string, err error) (Session, error) {
		_ = client.Cmd().Process.Kill()
		<-client.Done()
		_ = client.Cmd().Wait()
		msg := fmt.Sprintf("start provider %q: %s: %v", provider, step, err)
		if tail := client.StderrTail(); tail != "" {
			msg += ": " + lastLine(tail)
		}
		return Session{}, fmt.Errorf("%s", msg)
	}

	if err := client.Initialize(clientName, clientVersion); err != nil {
		return fail("initialize", err)
	}
	sid, modes, err := client.NewSession(dir)
	if err != nil {
		return fail("session/new", err)
	}

	rec.acp = client
	rec.acpSession = sid
	rec.cmd = client.Cmd()
	if modes != nil {
		rec.data.Mode = modes.CurrentModeID
		rec.data.Modes = modes.AvailableModes
		if m.DefaultMode != "" && m.DefaultMode != modes.CurrentModeID && hasMode(modes.AvailableModes, m.DefaultMode) {
			if err := client.SetMode(sid, m.DefaultMode); err != nil {
				log.Printf("session %s: set default mode %q: %v", id, m.DefaultMode, err)
			} else {
				rec.data.Mode = m.DefaultMode
			}
		}
	}

	m.mu.Lock()
	m.sessions[id] = rec
	m.mu.Unlock()

	go m.awaitExit(rec)
	return rec.snapshot(), nil
}

// sendACP starts one turn. The session/prompt call blocks until the agent
// finishes, so it runs in its own goroutine (runTurn); the HTTP request
// returns immediately and the phone polls the transcript.
func (m *Manager) sendACP(rec *record, text string) error {
	rec.mu.Lock()
	if isTerminal(rec.data.State) {
		rec.mu.Unlock()
		return ErrSessionFinished
	}
	if rec.turnActive {
		rec.mu.Unlock()
		return ErrTurnInProgress
	}
	rec.turnActive = true
	rec.data.State = StateBusy
	rec.data.Messages = append(rec.data.Messages, Message{Role: "user", Text: text, At: now()})
	rec.mu.Unlock()

	go m.runTurn(rec, text)
	return nil
}

func (m *Manager) runTurn(rec *record, text string) {
	stopReason, err := rec.acp.Prompt(rec.acpSession, text)

	rec.mu.Lock()
	rec.turnActive = false
	if rec.stopped || isTerminal(rec.data.State) {
		// Stop() or process exit already finalized the session.
		rec.mu.Unlock()
		return
	}
	switch {
	case err != nil:
		rec.data.Messages = append(rec.data.Messages, Message{Role: "agent", Text: "Error: " + err.Error(), At: now()})
	case stopReason == "cancelled":
		rec.data.Messages = append(rec.data.Messages, Message{Role: "agent", Text: "(stopped)", At: now()})
	case stopReason != "end_turn" && stopReason != "":
		rec.data.Messages = append(rec.data.Messages, Message{Role: "agent", Text: "(turn ended: " + stopReason + ")", At: now()})
	}
	// Tools still marked running when the turn ended never finish now.
	for i := range rec.data.Messages {
		msg := &rec.data.Messages[i]
		if msg.Role == "tool" && (msg.Status == "pending" || msg.Status == "in_progress") {
			msg.Status = "failed"
		}
	}
	rec.data.State = StateIdle
	rec.data.PendingPermission = nil
	rec.permID = nil
	rec.idleSince = nowT()
	snapshot := cloneSession(rec.data)
	rec.mu.Unlock()

	m.notifyFinished(snapshot)
}

// onUpdate folds a session/update notification into the transcript:
// streamed text chunks extend the current agent message, tool calls become
// "tool" entries whose title/status later updates rewrite in place.
func (m *Manager) onUpdate(rec *record, method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var p acp.SessionUpdateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	u := p.Update

	rec.mu.Lock()
	defer rec.mu.Unlock()
	msgs := rec.data.Messages
	switch u.Kind {
	case "agent_message_chunk":
		if u.Content == nil || u.Content.Text == "" {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == "agent" && msgs[n-1].ref == u.MessageID {
			msgs[n-1].Text += u.Content.Text
			return
		}
		rec.data.Messages = append(msgs, Message{Role: "agent", Text: u.Content.Text, At: now(), ref: u.MessageID})
	case "tool_call", "tool_call_update":
		if u.ToolCallID == "" {
			return
		}
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "tool" && msgs[i].ref == u.ToolCallID {
				if u.Title != "" {
					msgs[i].Text = u.Title
				}
				if u.ToolKind != "" {
					msgs[i].ToolKind = u.ToolKind
				}
				if u.Status != "" {
					msgs[i].Status = u.Status
				}
				return
			}
		}
		status := u.Status
		if status == "" {
			status = "pending"
		}
		rec.data.Messages = append(msgs, Message{
			Role: "tool", Text: u.Title, ToolKind: u.ToolKind, Status: status, At: now(), ref: u.ToolCallID,
		})
	case "current_mode_update":
		if u.CurrentModeID != "" {
			rec.data.Mode = u.CurrentModeID
		}
	}
}

// onRequest handles agent->client requests. Only permission requests are
// supported (the runner advertises no fs/terminal capabilities).
func (m *Manager) onRequest(rec *record, reqID json.RawMessage, method string, params json.RawMessage) {
	if method != "session/request_permission" {
		if rec.acp != nil {
			_ = rec.acp.RespondError(reqID, -32601, "method not supported: "+method)
		}
		return
	}
	var p acp.PermissionRequestParams
	if err := json.Unmarshal(params, &p); err != nil || len(p.Options) == 0 {
		if rec.acp != nil {
			_ = rec.acp.RespondError(reqID, -32602, "invalid permission request")
		}
		return
	}

	rec.mu.Lock()
	title := p.ToolCall.Title
	if title == "" {
		for i := len(rec.data.Messages) - 1; i >= 0; i-- {
			if rec.data.Messages[i].ref == p.ToolCall.ToolCallID {
				title = rec.data.Messages[i].Text
				break
			}
		}
	}
	rec.permID = reqID
	rec.data.PendingPermission = &PendingPermission{Title: title, ToolKind: p.ToolCall.Kind, Options: p.Options}
	rec.data.State = StateWaiting
	rec.idleSince = nowT()
	snapshot := cloneSession(rec.data)
	rec.mu.Unlock()

	if m.OnNeedsInput != nil {
		go m.OnNeedsInput(snapshot)
	}
}

// RespondPermission answers the session's open permission request with
// optionID (one of PendingPermission.Options).
func (m *Manager) RespondPermission(sessionID, optionID string) (Session, error) {
	rec, err := m.lookup(sessionID)
	if err != nil {
		return Session{}, err
	}
	if rec.acp == nil {
		return Session{}, ErrNotSupported
	}
	rec.mu.Lock()
	pp := rec.data.PendingPermission
	if pp == nil || rec.permID == nil {
		rec.mu.Unlock()
		return Session{}, fmt.Errorf("%w: no permission request is pending", ErrInvalidChoice)
	}
	valid := false
	for _, o := range pp.Options {
		if o.OptionID == optionID {
			valid = true
		}
	}
	if !valid {
		rec.mu.Unlock()
		return Session{}, fmt.Errorf("%w: unknown option %q", ErrInvalidChoice, optionID)
	}
	reqID := rec.permID
	rec.permID = nil
	rec.data.PendingPermission = nil
	rec.data.State = StateBusy
	snapshot := cloneSession(rec.data)
	rec.mu.Unlock()

	if err := rec.acp.Respond(reqID, acp.PermissionSelected(optionID)); err != nil {
		return Session{}, err
	}
	return snapshot, nil
}

// Cancel stops the current turn (the phone's red Stop button) without
// ending the session: the agent aborts, session/prompt returns "cancelled",
// and runTurn marks the session idle. No-op if no turn is running.
func (m *Manager) Cancel(sessionID string) (Session, error) {
	rec, err := m.lookup(sessionID)
	if err != nil {
		return Session{}, err
	}
	if rec.acp == nil {
		return Session{}, ErrNotSupported
	}
	rec.mu.Lock()
	active := rec.turnActive
	reqID := rec.permID
	rec.permID = nil
	rec.data.PendingPermission = nil
	if active && rec.data.State == StateWaiting {
		rec.data.State = StateBusy
	}
	snapshot := cloneSession(rec.data)
	rec.mu.Unlock()

	if !active {
		return snapshot, nil
	}
	// The spec requires answering an open permission request with
	// "cancelled" before/alongside session/cancel.
	if reqID != nil {
		_ = rec.acp.Respond(reqID, acp.PermissionCancelled())
	}
	if err := rec.acp.Cancel(rec.acpSession); err != nil {
		return Session{}, err
	}
	return snapshot, nil
}

// SetMode switches an ACP session's mode (e.g. "default" to be asked before
// changes, "bypassPermissions" for no prompts).
func (m *Manager) SetMode(sessionID, modeID string) (Session, error) {
	rec, err := m.lookup(sessionID)
	if err != nil {
		return Session{}, err
	}
	if rec.acp == nil {
		return Session{}, ErrNotSupported
	}
	rec.mu.Lock()
	ok := hasMode(rec.data.Modes, modeID)
	terminal := isTerminal(rec.data.State)
	rec.mu.Unlock()
	if terminal {
		return Session{}, ErrSessionFinished
	}
	if !ok {
		return Session{}, fmt.Errorf("%w: unknown mode %q", ErrInvalidChoice, modeID)
	}
	if err := rec.acp.SetMode(rec.acpSession, modeID); err != nil {
		return Session{}, err
	}
	rec.mu.Lock()
	rec.data.Mode = modeID
	snapshot := cloneSession(rec.data)
	rec.mu.Unlock()
	return snapshot, nil
}

func hasMode(modes []acp.Mode, id string) bool {
	for _, md := range modes {
		if md.ID == id {
			return true
		}
	}
	return false
}

func lastLine(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			return s[i+1:]
		}
	}
	return s
}
