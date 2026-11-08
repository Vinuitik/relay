package api

import (
	"errors"
	"net/http"

	"relay/runner/internal/collectors"
)

// Package ledger routes (/v1/packages*), see internal/collectors/packages.go.
// Server.Packages nil → 503.

func (s *Server) packagesOn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Packages == nil {
			writeError(w, http.StatusServiceUnavailable, "packages are not enabled on this runner")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleListPackages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Packages.List())
}

func (s *Server) handleUninstallPackage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Manager string `json:"manager"`
		ID      string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	err := s.Packages.Uninstall(req.Manager, req.ID)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]any{})
	case errors.Is(err, collectors.ErrInUse), errors.Is(err, collectors.ErrBusy):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeCollectorError(w, err)
	}
}
