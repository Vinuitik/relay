package acp

import "encoding/json"

// Typed wrappers for the ACP methods the runner uses. Field names follow
// the v1 spec and were checked against a live claude-agent-acp 0.84.0
// (2026-09-30) - see runner/FLOWS.md "Sessions (ACP)".

// Mode is one permission/behavior mode an agent offers, e.g. Claude's
// "default" (ask first), "acceptEdits", "plan", "auto", "bypassPermissions".
type Mode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Modes is session/new's "modes" field.
type Modes struct {
	CurrentModeID  string `json:"currentModeId"`
	AvailableModes []Mode `json:"availableModes"`
}

// Initialize performs the ACP handshake.
func (c *Client) Initialize(clientName, clientVersion string) error {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		// No fs/terminal: the agent uses its own tools on the local disk,
		// which is exactly where the runner lives anyway.
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]string{"name": clientName, "version": clientVersion},
	}
	return c.Call("initialize", params, nil)
}

// NewSession opens a conversation rooted at cwd (absolute path).
func (c *Client) NewSession(cwd string) (sessionID string, modes *Modes, err error) {
	var res struct {
		SessionID string `json:"sessionId"`
		Modes     *Modes `json:"modes"`
	}
	if err := c.Call("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}, &res); err != nil {
		return "", nil, err
	}
	return res.SessionID, res.Modes, nil
}

// SetMode switches the session's mode.
func (c *Client) SetMode(sessionID, modeID string) error {
	return c.Call("session/set_mode", map[string]string{"sessionId": sessionID, "modeId": modeID}, nil)
}

// Prompt sends one user turn and blocks until the agent finishes it,
// returning the stop reason: "end_turn", "cancelled", "max_tokens",
// "max_turn_requests" or "refusal". Progress arrives meanwhile as
// session/update notifications.
func (c *Client) Prompt(sessionID, text string) (stopReason string, err error) {
	var res struct {
		StopReason string `json:"stopReason"`
	}
	params := map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": text}},
	}
	if err := c.Call("session/prompt", params, &res); err != nil {
		return "", err
	}
	return res.StopReason, nil
}

// Cancel asks the agent to stop the current turn; the pending Prompt call
// then returns with stopReason "cancelled".
func (c *Client) Cancel(sessionID string) error {
	return c.Notify("session/cancel", map[string]string{"sessionId": sessionID})
}

// SessionUpdate is the "update" object of a session/update notification.
// Only the fields the runner uses are decoded.
type SessionUpdate struct {
	// Kind: "agent_message_chunk", "agent_thought_chunk", "user_message_chunk",
	// "tool_call", "tool_call_update", "plan", "current_mode_update",
	// "usage_update", "available_commands_update", ...
	Kind      string        `json:"sessionUpdate"`
	MessageID string        `json:"messageId,omitempty"`
	Content   *ContentBlock `json:"-"` // for *_message_chunk; see UnmarshalJSON
	// Tool calls. Status/Title are absent on updates that don't change them.
	ToolCallID string `json:"toolCallId,omitempty"`
	Title      string `json:"title,omitempty"`
	ToolKind   string `json:"kind,omitempty"`   // read, edit, delete, move, search, execute, think, fetch, other
	Status     string `json:"status,omitempty"` // pending, in_progress, completed, failed
	// current_mode_update
	CurrentModeID string `json:"currentModeId,omitempty"`
}

// ContentBlock is a text content block (other block types decode with an
// empty Text).
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// UnmarshalJSON decodes "content" only for message chunks: on tool calls
// the same key holds an array of tool output blocks, not a single block.
func (u *SessionUpdate) UnmarshalJSON(b []byte) error {
	type plain SessionUpdate
	var aux struct {
		plain
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*u = SessionUpdate(aux.plain)
	if len(aux.Content) > 0 && aux.Content[0] == '{' {
		var cb ContentBlock
		if err := json.Unmarshal(aux.Content, &cb); err == nil {
			u.Content = &cb
		}
	}
	return nil
}

// SessionUpdateParams is the params of a session/update notification.
type SessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

// PermissionOption is one button offered by a permission request.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // allow_once, allow_always, reject_once, reject_always
}

// PermissionRequestParams is the params of session/request_permission.
type PermissionRequestParams struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
		Kind       string `json:"kind"`
	} `json:"toolCall"`
	Options []PermissionOption `json:"options"`
}

// PermissionSelected is the result answering a permission request with the
// user's choice.
func PermissionSelected(optionID string) any {
	return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": optionID}}
}

// PermissionCancelled is the result answering a permission request that was
// abandoned because the turn was cancelled.
func PermissionCancelled() any {
	return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
}
