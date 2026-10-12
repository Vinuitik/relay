package api

import (
	"errors"
	"net/http"
	"strconv"

	"relay/runner/internal/session"
)

// handleListSubagents: GET /v1/sessions/{id}/agents - background agents the
// chat spawned (read from Claude transcripts, see session/subagents.go).
func (s *Server) handleListSubagents(w http.ResponseWriter, r *http.Request) {
	list, err := s.Sessions.Subagents(r.PathValue("sessionId"))
	if err != nil {
		writeSubagentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleSubagentMessages: GET /v1/sessions/{id}/agents/{agentId}/messages?after=n
func (s *Server) handleSubagentMessages(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	page, err := s.Sessions.SubagentMessages(r.PathValue("sessionId"), r.PathValue("agentId"), after)
	if err != nil {
		writeSubagentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func writeSubagentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "session not found")
	case errors.Is(err, session.ErrSubagentNotFound):
		writeError(w, http.StatusNotFound, "agent not found")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}