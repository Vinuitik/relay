package api

import (
	"errors"
	"net/http"
	"sync"

	"relay/runner/internal/auth"
)

// Sign-in relay from the phone (see internal/auth and runner/FLOWS.md
// "Sign-in relay"): start → open the URL on the phone → either paste the
// code from the callback page (finish) or approve with the one-time code and
// poll status until signed_in.

func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrCLINotFound):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, auth.ErrEmptyCode), errors.Is(err, auth.ErrCodeNotNeeded):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrNoLogin):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, auth.ErrUnknownProvider):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// authManager resolves {provider}. No managers wired at all → the provider
// is treated as known but CLI-less (503), matching the pre-relay behavior.
func (s *Server) authManager(r *http.Request) (*auth.Manager, error) {
	id := r.PathValue("provider")
	for _, m := range s.Auth {
		if m.Provider.ID == id {
			return m, nil
		}
	}
	if len(s.Auth) == 0 {
		return nil, auth.ErrCLINotFound
	}
	return nil, auth.ErrUnknownProvider
}

// handleAuthList → GET /v1/auth: every provider's status (statuses run in
// parallel; each is bounded by the provider's StatusTimeout).
func (s *Server) handleAuthList(w http.ResponseWriter, r *http.Request) {
	out := make([]auth.Status, len(s.Auth))
	var wg sync.WaitGroup
	for i, m := range s.Auth {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], _ = m.Status(r.Context()) // CLI missing → CLIFound false
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

// handleAuthStatus → GET /v1/auth/{provider}.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	m, err := s.authManager(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	st, err := m.Status(r.Context())
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleAuthStart → POST /v1/auth/{provider}/start. Can take up to ~20s
// (waiting for the CLI to print its login URL).
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	m, err := s.authManager(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	res, err := m.Start()
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type authFinishRequest struct {
	Code string `json:"code"`
}

// handleAuthFinish → POST /v1/auth/{provider}/finish. Can take up to ~60s.
func (s *Server) handleAuthFinish(w http.ResponseWriter, r *http.Request) {
	m, err := s.authManager(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	var req authFinishRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := m.Finish(req.Code)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
