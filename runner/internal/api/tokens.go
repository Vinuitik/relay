package api

import (
	"net/http"
	"strconv"
)

// handleTokens: GET /v1/tokens?days=N (default 7, max 90) - token usage read
// from Claude's transcripts on this machine (see internal/tokens). nil Tokens
// -> 503.
func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	if s.Tokens == nil {
		writeError(w, http.StatusServiceUnavailable, "token stats not available")
		return
	}
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days < 1 {
		days = 7
	}
	writeJSON(w, http.StatusOK, s.Tokens.Report(days))
}
