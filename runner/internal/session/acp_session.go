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
//	startACP:   spawn agent → initialize → session/new(cwd) → set_mode(DefaultMode) → idle
//	sendACP:    idle → busy, runTurn goroutine:
//	              ensureAgent (dormant? spawn → session/resume → re-apply mode)
//	              session/prompt; streamed session/update → transcript (onUpdate)
//	              session/request_permission → waiting + OnNeedsInput (onRequest)
//	              RespondPermission → busy again
//	            prompt returns (end_turn/cancelled/...) → idle + OnFinished
//	Cancel:     session/cancel → prompt returns "cancelled"
//	agent dies: awaitACPExit → dormant (idle, no process); next message resumes
//
// The agent process can come and go during one session's life (restored
// from disk, crashed, resumed), so rec.acp/rec.cmd are only read and
// written under rec.mu.

// clientName/Version identify the runner to the agent in initialize.
const (
	clientName    = "relay-runner"
	clientVersion = "v1"
)

// isACP reports whether rec is an ACP session (live or dormant).
func (rec *record) isACP() bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.acpSession != "" || rec.acp != nil
}

// client returns rec's live agent connection, nil if dormant.
func (rec *record) client() *acp.Client {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.acp
}

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
	client, sid, modes, err := m.spawnAgent(rec, pc, dir, "")
	if err != nil {
		return Session{}, fmt.Errorf("start provider %q: %w", provider, err)
	}
	m.attach(rec, client, sid, modes, m.DefaultMode)

	m.mu.Lock()
	m.sessions[id] = rec
	m.mu.Unlock()
	return rec.snapshot(), nil
}

// spawnAgent starts the agent process and opens the conversation: a new one
// (resumeID == "") or an existing one via session/resume.
func (m *Manager) spawnAgent(rec *record, pc ProviderCommand, dir, resumeID string) (*acp.Client, string, *acp.Modes, error) {
	client, err := acp.Start(pc.Name, pc.Args, dir, acp.Handlers{
		OnNotification: func(method string, params json.RawMessage) { m.onUpdate(rec, method, params) },
		OnRequest: func(c *acp.Client, reqID json.RawMessage, method string, params json.RawMessage) {
			m.onRequest(rec, c, reqID, method, params)
		},
	})
	if err != nil {
		return nil, "", nil, err
	}
	fail := func(step string, err error) (*acp.Client, string, *acp.Modes, error) {
		_ = client.Cmd().Process.Kill()
		<-client.Done()
		_ = client.Cmd().Wait()
		msg := fmt.Sprintf("%s: %v", step, err)
		if tail := client.StderrTail(); tail != "" {
			msg += ": " + lastLine(tail)
		}
		return nil, "", nil, fmt.Errorf("%s", msg)
	}
	if err := client.Initialize(clientName, clientVersion); err != nil {
		return fail("initialize", err)
	}
	if resumeID == "" {
		sid, modes, err := client.NewSession(dir)
		if err != nil {
			return fail("session/new", err)
		}
		return client, sid, modes, nil
	}
	modes, err := client.ResumeSession(resumeID, dir)
	if err != nil {
		return fail("session/resume", err)
	}
	return client, resumeID, modes, nil
}

// attach wires a freshly spawned/resumed agent into rec and switches it to
// wantMode if the agent offers it.
func (m *Manager) attach(rec *record, client *acp.Client, sid string, modes *acp.Modes, wantMode string) {
	mode := ""
	if modes != nil {
		mode = modes.CurrentModeID
		if wantMode != "" && wantMode != mode && hasMode(modes.AvailableModes, wantMode) {
			if err := client.SetMode(sid, wantMode); err != nil {
				log.Printf("session %s: set mode %q: %v", rec.data.ID, wantMode, err)
			} else {
				mode = wantMode
			}
		}
	}
	rec.mu.Lock()
	rec.acp = client
	rec.cmd = client.Cmd()
	rec.acpSession = sid
	rec.dormant = false
	if modes != nil {
		rec.data.Modes = modes.AvailableModes
		rec.data.Mode = mode
	}
	rec.mu.Unlock()
	go m.awaitACPExit(rec, client)
}

// ensureAgent returns rec's live agent, resuming a dormant session first.
func (m *Manager) ensureAgent(rec *record) (*acp.Client, error) {
	rec.mu.Lock()
	client, sid, provider, projectID, mode := rec.acp, rec.acpSession, rec.data.Provider, rec.data.ProjectID, rec.data.Mode
	rec.mu.Unlock()
	if client != nil {
		return client, nil
	}
	dir, ok := m.resolveDir(projectID)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrProjectNotFound, projectID)
	}
	m.mu.Lock()
	pc, err := m.resolveProvider(provider)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	client, sid, modes, err := m.spawnAgent(rec, pc, dir, sid)
	if err != nil {
		return nil, fmt.Errorf("resume session: %w", err)
	}
	m.attach(rec, client, sid, modes, mode)
	return client, nil
}

// awaitACPExit turns an agent process exit into a dormant session (unless
// Stop() ended the session): the conversation lives on in the agent's own
// storage and the next message resumes it.
func (m *Manager) awaitACPExit(rec *record, client *acp.Client) {
	<-client.Done() // let the reader drain stdout before Wait closes the pipe
	err := client.Cmd().Wait()

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.acp != client || rec.stopped || isTerminal(rec.data.State) {
		return
	}
	rec.acp = nil
	rec.cmd = nil
	rec.dormant = true
	rec.permID = nil
	rec.data.PendingPermission = nil
	if !rec.turnActive {
		// Mid-turn, runTurn reports the failure itself.
		note := "(agent process exited - your next message resumes the session)"
		if err != nil {
			note = "(agent process exited: " + err.Error() + " - your next message resumes the session)"
		}
		rec.data.Messages = append(rec.data.Messages, Message{Role: "agent", Text: note, At: now()})
	}
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
	var stopReason string
	client, err := m.ensureAgent(rec)
	if err == nil {
		rec.mu.Lock()
		sid := rec.acpSession
		rec.mu.Unlock()
		stopReason, err = client.Prompt(sid, text)
	}

	rec.mu.Lock()
	rec.turnActive = false
	if rec.stopped || isTerminal(rec.data.State) {
		// Stop() already finalized the session.
		rec.mu.Unlock()
		return
	}
	switch {
	case err != nil:
		rec.data.Messages = append(rec.data.Messages, Message{Role: "agent", Text: "Error: " + err.Error(), Kind: problemKind(err, ""), At: now()})
	case stopReason == "cancelled":
		rec.data.Messages = append(rec.data.Messages, Message{Role: "agent", Text: "(stopped)", At: now()})
	case stopReason != "end_turn" && stopReason != "":
		rec.data.Messages = append(rec.data.Messages, Message{Role: "agent", Text: "(turn ended: " + stopReason + ")", At: now()})
	}
	if err == nil {
		markProblemReply(rec.data.Messages)
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

// markProblemReply tags this turn's last agent reply with a problem Kind
// when the agent reported a quota/auth problem as ordinary text (Claude
// Code says "You've hit your limit · resets 5pm" as a normal reply, not an
// error).
func markProblemReply(msgs []Message) {
	for i := len(msgs) - 1; i >= 0 && msgs[i].Role != "user"; i-- {
		if msgs[i].Role == "agent" {
			msgs[i].Kind = problemKind(nil, msgs[i].Text)
			return
		}
	}
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
func (m *Manager) onRequest(rec *record, c *acp.Client, reqID json.RawMessage, method string, params json.RawMessage) {
	if method != "session/request_permission" {
		_ = c.RespondError(reqID, -32601, "method not supported: "+method)
		return
	}
	var p acp.PermissionRequestParams
	if err := json.Unmarshal(params, &p); err != nil || len(p.Options) == 0 {
		_ = c.RespondError(reqID, -32602, "invalid permission request")
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
	if !rec.isACP() {
		return Session{}, ErrNotSupported
	}
	rec.mu.Lock()
	pp, client := rec.data.PendingPermission, rec.acp
	if pp == nil || rec.permID == nil || client == nil {
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

	if err := client.Respond(reqID, acp.PermissionSelected(optionID)); err != nil {
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
	if !rec.isACP() {
		return Session{}, ErrNotSupported
	}
	rec.mu.Lock()
	active, client, sid := rec.turnActive, rec.acp, rec.acpSession
	reqID := rec.permID
	rec.permID = nil
	rec.data.PendingPermission = nil
	if active && rec.data.State == StateWaiting {
		rec.data.State = StateBusy
	}
	snapshot := cloneSession(rec.data)
	rec.mu.Unlock()

	if !active || client == nil {
		// Nothing running (or still resuming - the turn ends on its own).
		return snapshot, nil
	}
	// The spec requires answering an open permission request with
	// "cancelled" before/alongside session/cancel.
	if reqID != nil {
		_ = client.Respond(reqID, acp.PermissionCancelled())
	}
	if err := client.Cancel(sid); err != nil {
		return Session{}, err
	}
	return snapshot, nil
}

// SetMode switches an ACP session's mode (e.g. "default" to be asked before
// changes, "bypassPermissions" for no prompts). On a dormant session it's
// just recorded and applied when the session resumes.
func (m *Manager) SetMode(sessionID, modeID string) (Session, error) {
	rec, err := m.lookup(sessionID)
	if err != nil {
		return Session{}, err
	}
	if !rec.isACP() {
		return Session{}, ErrNotSupported
	}
	rec.mu.Lock()
	ok := hasMode(rec.data.Modes, modeID)
	terminal := isTerminal(rec.data.State)
	client, sid := rec.acp, rec.acpSession
	rec.mu.Unlock()
	if terminal {
		return Session{}, ErrSessionFinished
	}
	if !ok {
		return Session{}, fmt.Errorf("%w: unknown mode %q", ErrInvalidChoice, modeID)
	}
	if client != nil {
		if err := client.SetMode(sid, modeID); err != nil {
			return Session{}, err
		}
	}
	rec.mu.Lock()
	rec.data.Mode = modeID
	snapshot := cloneSession(rec.data)
	rec.mu.Unlock()
	return snapshot, nil
}

// Shutdown kills every live agent process - called when the runner exits, so
// agents don't outlive it (on Windows children aren't killed with their
// parent). Sessions go dormant and resume after the next start.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	recs := make([]*record, 0, len(m.sessions))
	for _, rec := range m.sessions {
		recs = append(recs, rec)
	}
	m.mu.Unlock()
	for _, rec := range recs {
		rec.mu.Lock()
		cmd := rec.cmd
		isACP := rec.acp != nil
		rec.mu.Unlock()
		if isACP && cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
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
