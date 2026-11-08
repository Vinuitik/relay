package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"relay/runner/internal/credentials"
)

// KPI credentials (/v1/credentials*), see internal/credentials. Bodies of
// these routes are never logged or echoed: decode errors are generic and
// validation errors never contain the secret. Server.Credentials nil → 503.

func (s *Server) credentialsOn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Credentials == nil {
			writeError(w, http.StatusServiceUnavailable, "credentials are not enabled on this runner")
			return
		}
		next(w, r)
	}
}

func writeCredentialError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, credentials.ErrBadRequest):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, credentials.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	list, err := s.Credentials.List()
	if err != nil {
		writeCredentialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handlePutCredential(w http.ResponseWriter, r *http.Request) {
	var req credentials.PutRequest
	r.Body = http.MaxBytesReader(w, r.Body, credentials.MaxBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "body must be JSON {host, auth, secret, headerName?, queryParam?} up to 64 KiB")
		return
	}
	info, err := s.Credentials.Put(r.PathValue("name"), req)
	if err != nil {
		writeCredentialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	if err := s.Credentials.Delete(r.PathValue("name")); err != nil {
		writeCredentialError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
