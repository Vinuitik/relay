package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"relay/runner/internal/git"
	"relay/runner/internal/handoff"
	"relay/runner/internal/project"
)

// projectView is a project plus its git identity, for GET /v1/projects:
// two runners' projects are "the same" when their RemoteKey matches.
type projectView struct {
	project.Project
	// RemoteURL is the origin URL with credentials stripped; RemoteKey the
	// normalized host/owner/repo form used for matching. Both omitted when
	// the folder has no git remote.
	RemoteURL string `json:"remoteUrl,omitempty"`
	RemoteKey string `json:"remoteKey,omitempty"`
}

func (s *Server) projectViews(ctx context.Context) []projectView {
	list := s.Projects.List()
	out := make([]projectView, len(list))
	for i, p := range list {
		out[i] = projectView{Project: p}
		if u := git.Origin(ctx, p.Path); u != "" {
			out[i].RemoteURL, out[i].RemoteKey = u, handoff.NormalizeRemote(u)
		}
	}
	return out
}

// handoffService returns the service or writes 503.
func (s *Server) handoffService(w http.ResponseWriter) *handoff.Service {
	if s.Handoff == nil {
		writeError(w, http.StatusServiceUnavailable, "chat handoff not configured")
		return nil
	}
	return s.Handoff
}

// writeHandoffError maps handoff errors to a status and a machine-readable
// "code" next to the message (runners read it back from each other).
func writeHandoffError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, handoff.ErrBusy):
		status, code = http.StatusConflict, "busy"
	case errors.Is(err, handoff.ErrMismatch):
		status, code = http.StatusConflict, "mismatch"
	case errors.Is(err, handoff.ErrExists):
		status, code = http.StatusConflict, "exists"
	case errors.Is(err, handoff.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, handoff.ErrNoPeer):
		status, code = http.StatusNotFound, "no_peer"
	case errors.Is(err, handoff.ErrNoProject):
		status, code = http.StatusNotFound, "no_project"
	case errors.Is(err, handoff.ErrBadID):
		status, code = http.StatusBadRequest, "bad_id"
	case errors.Is(err, handoff.ErrNoRemote):
		status, code = http.StatusUnprocessableEntity, "no_remote"
	case errors.Is(err, handoff.ErrUnreachable):
		status, code = http.StatusBadGateway, "unreachable"
	}
	writeJSON(w, status, map[string]string{"error": err.Error(), "code": code})
}

type peerView struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
}

func (s *Server) handleListPeers(w http.ResponseWriter, r *http.Request) {
	h := s.handoffService(w)
	if h == nil {
		return
	}
	peers := h.Peers.List()
	out := make([]peerView, len(peers))
	for i, p := range peers {
		out[i] = peerView{Name: p.Name, BaseURL: p.BaseURL}
	}
	writeJSON(w, http.StatusOK, out)
}

type setPeerRequest struct {
	BaseURL string `json:"baseUrl"`
	Key     string `json:"key"`
}

func (s *Server) handleSetPeer(w http.ResponseWriter, r *http.Request) {
	h := s.handoffService(w)
	if h == nil {
		return
	}
	var req setPeerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := r.PathValue("name")
	if name == "" || !strings.HasPrefix(req.BaseURL, "http") || req.Key == "" {
		writeError(w, http.StatusBadRequest, "name, http(s) baseUrl and key are required")
		return
	}
	if err := h.Peers.Set(handoff.Peer{Name: name, BaseURL: req.BaseURL, Key: req.Key}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, peerView{Name: name, BaseURL: req.BaseURL})
}

func (s *Server) handleDeletePeer(w http.ResponseWriter, r *http.Request) {
	h := s.handoffService(w)
	if h == nil {
		return
	}
	if err := h.Peers.Delete(r.PathValue("name")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type releaseRequest struct {
	AgentChatID string `json:"agentChatId"`
	// Remote, if set, must match the chat's project git remote.
	Remote string `json:"remote"`
}

// handleHandoffRelease is called by the *target* runner: returns the
// transcript bytes (application/x-ndjson) and moves them out of Claude's
// folder here. 409 code=busy while a turn runs.
func (s *Server) handleHandoffRelease(w http.ResponseWriter, r *http.Request) {
	h := s.handoffService(w)
	if h == nil {
		return
	}
	var req releaseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := h.Release(r.Context(), req.AgentChatID, req.Remote)
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	_, _ = w.Write(data)
}

type statusRequest struct {
	ChatIDs []string `json:"chatIds"`
}

func (s *Server) handleHandoffStatus(w http.ResponseWriter, r *http.Request) {
	h := s.handoffService(w)
	if h == nil {
		return
	}
	var req statusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": h.Status(req.ChatIDs)})
}

func (s *Server) handleHandoffCopy(w http.ResponseWriter, r *http.Request) {
	h := s.handoffService(w)
	if h == nil {
		return
	}
	data, saved, err := h.Copy(r.PathValue("agentChatId"))
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Relay-Copy-At", saved.UTC().Format(time.RFC3339))
	_, _ = w.Write(data)
}

type pullRequest struct {
	Peer        string `json:"peer"`
	AgentChatID string `json:"agentChatId"`
	ProjectID   string `json:"projectId"`
	// Link skips the git-remote check (projects linked by hand).
	Link bool `json:"link"`
}

func (s *Server) handleHandoffPull(w http.ResponseWriter, r *http.Request) {
	h := s.handoffService(w)
	if h == nil {
		return
	}
	var req pullRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.Pull(r.Context(), req.Peer, req.AgentChatID, req.ProjectID, req.Link)
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type takeoverRequest struct {
	// Peer is whose handed-off copy to use; "" = this runner's own.
	Peer        string `json:"peer"`
	AgentChatID string `json:"agentChatId"`
	ProjectID   string `json:"projectId"`
}

func (s *Server) handleHandoffTakeover(w http.ResponseWriter, r *http.Request) {
	h := s.handoffService(w)
	if h == nil {
		return
	}
	var req takeoverRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.Takeover(r.Context(), req.Peer, req.AgentChatID, req.ProjectID)
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleHandoffLent(w http.ResponseWriter, r *http.Request) {
	h := s.handoffService(w)
	if h == nil {
		return
	}
	writeJSON(w, http.StatusOK, h.Lent.List())
}
