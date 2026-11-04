package api

import (
	"errors"
	"net/http"
	"time"

	"relay/runner/internal/collectors"
	"relay/runner/internal/collectors/sandbox"
)

// KPI collectors (/v1/collectors*) and local sensors (/v1/sensors*), see
// internal/collectors. Server.Collectors / Server.Sensors nil → 503.

func (s *Server) collectorsOn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Collectors == nil || s.Sensors == nil {
			writeError(w, http.StatusServiceUnavailable, "collectors are not enabled on this runner")
			return
		}
		next(w, r)
	}
}

// writeCollectorError maps engine errors to HTTP statuses.
func writeCollectorError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, collectors.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, collectors.ErrBadRequest):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, collectors.ErrNotApproved):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, collectors.ErrHashMismatch), errors.Is(err, collectors.ErrBusy):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, collectors.ErrCollector):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, collectors.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, sandbox.ErrUnavailable), errors.Is(err, collectors.ErrSource):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleListCollectors(w http.ResponseWriter, r *http.Request) {
	list, err := s.Collectors.List()
	if err != nil {
		writeCollectorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleInstallCollector(w http.ResponseWriter, r *http.Request) {
	var req collectors.InstallRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := s.Collectors.Install(req)
	if err != nil {
		writeCollectorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": info.ID, "version": info.Version,
		"contractHash": info.ContractHash, "approved": info.Approved})
}

func (s *Server) handleTestCollector(w http.ResponseWriter, r *http.Request) {
	res, err := s.Collectors.Test(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCollectorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleApproveCollector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ContractHash string `json:"contractHash"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Collectors.Approve(r.PathValue("id"), req.ContractHash); err != nil {
		writeCollectorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) handleRunCollector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From time.Time `json:"from"`
		To   time.Time `json:"to"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "body must be {from, to} as ISO-8601 times")
		return
	}
	out, err := s.Collectors.Run(r.Context(), r.PathValue("id"), req.From, req.To)
	if err != nil {
		writeCollectorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleActivateCollector(w http.ResponseWriter, r *http.Request) {
	if err := s.Collectors.Activate(r.PathValue("id")); err != nil {
		writeCollectorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) handleCollectorReadings(w http.ResponseWriter, r *http.Request) {
	var since time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be an ISO-8601 time")
			return
		}
		since = t
	}
	rs, err := s.Collectors.Readings(r.PathValue("id"), since)
	if err != nil {
		writeCollectorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

func (s *Server) handleDeleteCollector(w http.ResponseWriter, r *http.Request) {
	if err := s.Collectors.Delete(r.PathValue("id")); err != nil {
		writeCollectorError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSensors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Sensors.State(r.Context()))
}

func (s *Server) handleInstallSensor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Sensors.StartInstall(req.Name); err != nil {
		writeCollectorError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{})
}
