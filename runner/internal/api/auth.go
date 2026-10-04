package api

import (
	"errors"
	"net/http"

	"relay/runner/internal/auth"
)

// Claude re-login from the phone (see internal/auth and runner/FLOWS.md
// "Claude re-login"): start → open URL on the phone → paste the code from
// the callback page → finish.

func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrCLINotFound):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, auth.ErrEmptyCode):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrNoLogin):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// handleClaudeAuthStatus → GET /v1/auth/claude.
func (s *Server) handleClaudeAuthStatus(w http.ResponseWriter, r *http.Request) {
	if s.ClaudeAuth == nil {
		writeAuthError(w, auth.ErrCLINotFound)
		return
	}
	st, err := s.ClaudeAuth.Status(r.Context())
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleClaudeAuthStart → POST /v1/auth/claude/start. Can take up to ~20s
// (waiting for the CLI to print its login URL).
func (s *Server) handleClaudeAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.ClaudeAuth == nil {
		writeAuthError(w, auth.ErrCLINotFound)
		return
	}
	res, err := s.ClaudeAuth.Start()
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type claudeAuthFinishRequest struct {
	Code string `json:"code"`
}

// handleClaudeAuthFinish → POST /v1/auth/claude/finish. Can take up to ~60s.
func (s *Server) handleClaudeAuthFinish(w http.ResponseWriter, r *http.Request) {
	if s.ClaudeAuth == nil {
		writeAuthError(w, auth.ErrCLINotFound)
		return
	}
	var req claudeAuthFinishRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.ClaudeAuth.Finish(req.Code)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
