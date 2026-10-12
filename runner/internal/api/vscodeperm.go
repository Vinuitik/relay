package api

import (
	"encoding/json"
	"net/http"
	"time"

	"relay/runner/internal/vscodeperm"
)

// Phone-answerable VS Code permission prompts (see internal/vscodeperm).
//
//	POST /v1/vscode/permissions       hook → long-poll → 200 {behavior,message?} | 204
//	GET  /v1/vscode/permissions       phone → {"permissions":[Item...]}
//	POST /v1/vscode/permissions/{id}  phone → {behavior:"allow"|"deny",message?} → 200 | 404
//	POST /v1/vscode/notification      hook (Notification event) → 204, maybe a push

type permissionHookInput struct {
	SessionID string          `json:"session_id"`
	Cwd       string          `json:"cwd"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

func (s *Server) permHold() time.Duration {
	if s.PermHold > 0 {
		return s.PermHold
	}
	return vscodeperm.DefaultHoldSec * time.Second
}

func (s *Server) push(typ, title, body string, data map[string]string) {
	if s.Push != nil {
		s.Push(typ, title, body, data)
	}
}

func (s *Server) handleVSCodePermissionRequest(w http.ResponseWriter, r *http.Request) {
	if s.Perms == nil {
		writeError(w, http.StatusServiceUnavailable, "permissions not enabled")
		return
	}
	var in permissionHookInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	paths := map[string]string{}
	for _, p := range s.Projects.List() {
		paths[p.ID] = p.Path
	}
	item := s.Perms.Add(vscodeperm.Item{
		SessionID: in.SessionID,
		Cwd:       in.Cwd,
		Project:   vscodeperm.MatchProject(in.Cwd, paths),
		ToolName:  in.ToolName,
		Summary:   vscodeperm.Summarize(in.ToolName, in.ToolInput),
	})
	s.push("vscode_permission", "VS Code asks", item.Summary, map[string]string{
		"permissionId": item.ID, "sessionId": item.SessionID, "projectId": item.Project, "summary": item.Summary,
	})
	d, ok := s.Perms.Wait(r.Context(), item.ID, s.permHold())
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleVSCodePermissionList(w http.ResponseWriter, r *http.Request) {
	if s.Perms == nil {
		writeError(w, http.StatusServiceUnavailable, "permissions not enabled")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"permissions": s.Perms.List()})
}

func (s *Server) handleVSCodePermissionAnswer(w http.ResponseWriter, r *http.Request) {
	if s.Perms == nil {
		writeError(w, http.StatusServiceUnavailable, "permissions not enabled")
		return
	}
	var d vscodeperm.Decision
	if err := decodeJSON(r, &d); err != nil || !d.Valid() {
		writeError(w, http.StatusBadRequest, `behavior must be "allow" or "deny"`)
		return
	}
	if !s.Perms.Answer(r.PathValue("id"), d) {
		writeError(w, http.StatusNotFound, "permission request is gone (answered or timed out)")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type notificationHookInput struct {
	SessionID        string `json:"session_id"`
	Cwd              string `json:"cwd"`
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
}

// handleVSCodeNotification: VS Code is waiting on the user. If a permission
// request already covers that chat the phone has been pushed, so stay quiet;
// the check runs after NotifyDelay because the Notification and
// PermissionRequest hooks fire at nearly the same moment.
func (s *Server) handleVSCodeNotification(w http.ResponseWriter, r *http.Request) {
	var in notificationHookInput
	if err := decodeJSON(r, &in); err == nil &&
		(in.NotificationType == "permission_prompt" || in.NotificationType == "elicitation_dialog") && in.SessionID != "" {
		go s.maybePushWaiting(in)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) maybePushWaiting(in notificationHookInput) {
	delay := s.NotifyDelay
	if delay == 0 {
		delay = 2 * time.Second
	}
	time.Sleep(delay)
	if s.Perms != nil && s.Perms.HasSession(in.SessionID) {
		return
	}
	if s.WaitingDebounce == nil || !s.WaitingDebounce.Allow(in.SessionID, time.Now()) {
		return
	}
	s.push("vscode_waiting", "VS Code is waiting on you", in.Message, map[string]string{"sessionId": in.SessionID})
}
