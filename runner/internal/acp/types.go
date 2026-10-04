package acp

import (
	"encoding/json"
	"fmt"
)

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

// ConfigOption is one ACP session config option (model, effort, fast mode,
// mode...) as claude-agent-acp 0.85 reports it in session/new, session/resume
// and config_option_update. Category is "model", "thought_level", "mode"...
type ConfigOption struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	Category     string         `json:"category,omitempty"`
	Type         string         `json:"type"`
	CurrentValue any            `json:"currentValue"`
	Options      []ConfigChoice `json:"options,omitempty"`
}

// ConfigChoice is one selectable value. A group (ACP allows grouped
// options) has no Value and nests its choices in Options.
type ConfigChoice struct {
	Value       string         `json:"value,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Options     []ConfigChoice `json:"options,omitempty"`
}

// Current is the option's current value as a string ("" if unset).
func (o ConfigOption) Current() string {
	if o.CurrentValue == nil {
		return ""
	}
	return fmt.Sprint(o.CurrentValue)
}

// Has reports whether value is one of the option's choices (groups included).
func (o ConfigOption) Has(value string) bool {
	var walk func([]ConfigChoice) bool
	walk = func(cs []ConfigChoice) bool {
		for _, c := range cs {
			if c.Value == value || walk(c.Options) {
				return true
			}
		}
		return false
	}
	return walk(o.Options)
}

// SessionSetup is what session/new and session/resume return that the
// runner keeps: modes and config options.
type SessionSetup struct {
	Modes         *Modes         `json:"modes"`
	ConfigOptions []ConfigOption `json:"configOptions"`
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
func (c *Client) NewSession(cwd string) (sessionID string, setup SessionSetup, err error) {
	var res struct {
		SessionID string `json:"sessionId"`
		SessionSetup
	}
	if err := c.Call("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}, &res); err != nil {
		return "", SessionSetup{}, err
	}
	return res.SessionID, res.SessionSetup, nil
}

// ResumeSession reattaches to an existing conversation (e.g. after the agent
// process or the runner restarted) without replaying its history. Checked
// against claude-agent-acp 0.84.0: params {sessionId, cwd, mcpServers}, the
// result carries modes like session/new - but the mode is reset to the
// agent's default, so callers re-apply their own. Config options (model,
// effort) likewise come back at the agent's defaults.
func (c *Client) ResumeSession(sessionID, cwd string) (SessionSetup, error) {
	var res SessionSetup
	params := map[string]any{"sessionId": sessionID, "cwd": cwd, "mcpServers": []any{}}
	if err := c.Call("session/resume", params, &res); err != nil {
		return SessionSetup{}, err
	}
	return res, nil
}

// LoadSession reopens an existing conversation and replays its whole history
// as session/update notifications (user_message_chunk, agent_message_chunk,
// tool_call...). The read loop delivers notifications in order, so every
// replayed update has been handled before this returns. Checked against
// claude-agent-acp 0.85.1 (loadSession capability): params as session/resume,
// result carries modes/config options like session/new.
func (c *Client) LoadSession(sessionID, cwd string) (SessionSetup, error) {
	var res SessionSetup
	params := map[string]any{"sessionId": sessionID, "cwd": cwd, "mcpServers": []any{}}
	if err := c.Call("session/load", params, &res); err != nil {
		return SessionSetup{}, err
	}
	return res, nil
}

// SessionInfo is one conversation the agent has saved, from session/list.
type SessionInfo struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"` // RFC3339
}

// ListSessions returns one page of the agent's saved conversations rooted at
// cwd, newest first, and the cursor of the next page ("" if none). Checked
// against claude-agent-acp 0.85.1: pages of 1000, so one call is enough.
func (c *Client) ListSessions(cwd, cursor string) ([]SessionInfo, string, error) {
	var res struct {
		Sessions   []SessionInfo `json:"sessions"`
		NextCursor string        `json:"nextCursor"`
	}
	params := map[string]any{"cwd": cwd}
	if cursor != "" {
		params["cursor"] = cursor
	}
	if err := c.Call("session/list", params, &res); err != nil {
		return nil, "", err
	}
	return res.Sessions, res.NextCursor, nil
}

// SetConfigOption sets one config option (session/set_config_option) and
// returns the full, updated option list - changing the model can change
// which effort levels exist.
func (c *Client) SetConfigOption(sessionID, configID, value string) ([]ConfigOption, error) {
	var res struct {
		ConfigOptions []ConfigOption `json:"configOptions"`
	}
	params := map[string]string{"sessionId": sessionID, "configId": configID, "value": value}
	if err := c.Call("session/set_config_option", params, &res); err != nil {
		return nil, err
	}
	return res.ConfigOptions, nil
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
	// ConfigOptions: the full list, on "config_option_update".
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
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
