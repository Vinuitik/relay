package api

import (
	"errors"
	"net/http"
	"strconv"

	"relay/runner/internal/auth"
	"relay/runner/internal/jobs"
)

// One-shot headless claude jobs for the hub (/v1/jobs*, see internal/jobs).
// Server.Jobs nil → 503.

func (s *Server) jobsOn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Jobs == nil {
			writeError(w, http.StatusServiceUnavailable, "jobs are not enabled on this runner")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req jobs.Request
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.Jobs.Submit(req)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]string{"jobId": id})
	case errors.Is(err, jobs.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, jobs.ErrProjectRequired), errors.Is(err, jobs.ErrBadRequest):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrCLINotFound):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	after := 0
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "after must be a non-negative integer")
			return
		}
		after = n
	}
	writeJobResult(w)(s.Jobs.Get(r.PathValue("id"), after))
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	writeJobResult(w)(s.Jobs.Cancel(r.PathValue("id")))
}

func writeJobResult(w http.ResponseWriter) func(jobs.Job, error) {
	return func(j jobs.Job, err error) {
		if errors.Is(err, jobs.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, j)
	}
}
