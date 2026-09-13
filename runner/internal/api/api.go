// Package api implements the runner's HTTP API exactly as defined in
// shared/API.md.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"relay/runner/internal/notify"
	"relay/runner/internal/project"
	"relay/runner/internal/session"
	"relay/runner/internal/wol"
)

// Version is reported by GET /v1/runner/info.
const Version = "v1"

// ComposeFuncs lets the compose package's Start/Stop be injected, so tests
// don't need real docker/compose files.
type ComposeFuncs struct {
	Start func(projectDir string) error
	Stop  func(projectDir string) error
}

// Server holds the dependencies needed to serve the v1 API.
type Server struct {
	Key      string
	Projects *project.Registry
	Sessions *session.Manager
	Compose  ComposeFuncs
	Devices  *notify.Registry
	Sender   wol.PacketSender
}

// NewServer builds a Server.
func NewServer(key string, projects *project.Registry, sessions *session.Manager, compose ComposeFuncs, devices *notify.Registry, sender wol.PacketSender) *Server {
	return &Server{Key: key, Projects: projects, Sessions: sessions, Compose: compose, Devices: devices, Sender: sender}
}

// Routes builds the HTTP handler for the v1 API, using Go 1.22's
// method-and-pattern aware http.ServeMux (stdlib only, no router dependency).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/runner/info", s.auth(s.handleRunnerInfo))
	mux.HandleFunc("GET /v1/projects", s.auth(s.handleListProjects))
	mux.HandleFunc("POST /v1/projects", s.auth(s.handleCreateProject))
	mux.HandleFunc("GET /v1/projects/{projectId}/sessions", s.auth(s.handleListSessions))
	mux.HandleFunc("POST /v1/projects/{projectId}/sessions", s.auth(s.handleStartSession))
	mux.HandleFunc("GET /v1/sessions/{sessionId}", s.auth(s.handleGetSession))
	mux.HandleFunc("POST /v1/sessions/{sessionId}/message", s.auth(s.handleSendMessage))
	mux.HandleFunc("POST /v1/sessions/{sessionId}/stop", s.auth(s.handleStopSession))
	mux.HandleFunc("POST /v1/projects/{projectId}/containers/start", s.auth(s.handleContainersStart))
	mux.HandleFunc("POST /v1/projects/{projectId}/containers/stop", s.auth(s.handleContainersStop))
	mux.HandleFunc("POST /v1/wake", s.auth(s.handleWake))
	mux.HandleFunc("POST /v1/devices", s.auth(s.handleRegisterDevice))

	return mux
}

// auth wraps a handler with the X-Relay-Key check. /v1/health is
// deliberately not wrapped - it requires no auth per shared/API.md.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Relay-Key") != s.Key {
			writeError(w, http.StatusUnauthorized, "invalid or missing X-Relay-Key")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type runnerInfo struct {
	Hostname string `json:"hostname"`
	Busy     bool   `json:"busy"`
	Version  string `json:"version"`
}

func (s *Server) handleRunnerInfo(w http.ResponseWriter, r *http.Request) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	busy := false
	for _, p := range s.Projects.List() {
		for _, sess := range s.Sessions.ListByProject(p.ID) {
			if sess.State == session.StateBusy {
				busy = true
			}
		}
	}

	writeJSON(w, http.StatusOK, runnerInfo{Hostname: hostname, Busy: busy, Version: Version})
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Projects.List())
}

type createProjectRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.Projects.Create(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if _, err := s.Projects.Get(projectID); err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, s.Sessions.ListByProject(projectID))
}

type startSessionRequest struct {
	Provider string `json:"provider"`
}

func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	var req startSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	sess, err := s.Sessions.Start(projectID, req.Provider)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrProjectNotFound):
			writeError(w, http.StatusNotFound, "project not found")
		case errors.Is(err, session.ErrUnknownProvider):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Sessions.Get(r.PathValue("sessionId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

type sendMessageRequest struct {
	Text string `json:"text"`
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var req sendMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	err := s.Sessions.SendMessage(r.PathValue("sessionId"), req.Text)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrSessionNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, session.ErrSessionFinished):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{})
}

func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Sessions.Stop(r.PathValue("sessionId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleContainersStart(w http.ResponseWriter, r *http.Request) {
	s.runCompose(w, r, s.Compose.Start)
}

func (s *Server) handleContainersStop(w http.ResponseWriter, r *http.Request) {
	s.runCompose(w, r, s.Compose.Stop)
}

func (s *Server) runCompose(w http.ResponseWriter, r *http.Request, action func(string) error) {
	p, err := s.Projects.Get(r.PathValue("projectId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if action == nil {
		writeError(w, http.StatusInternalServerError, "compose action not configured")
		return
	}
	if err := action(p.Path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

type wakeRequest struct {
	MAC string `json:"mac"`
}

// handleWake broadcasts a Wake-on-LAN magic packet on this runner's own
// local network, per shared/API.md's /v1/wake entry. It never targets a
// remote machine directly - the caller is expected to pick whichever known
// runner is on the same LAN segment as the machine being woken (see
// ARCHITECTURE.md "Relay device").
func (s *Server) handleWake(w http.ResponseWriter, r *http.Request) {
	var req wakeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	packet, err := wol.BuildMagicPacket(req.MAC)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if s.Sender == nil {
		writeError(w, http.StatusInternalServerError, "wake sender not configured")
		return
	}
	if err := s.Sender.SendBroadcast(packet); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{})
}

type registerDeviceRequest struct {
	FCMToken string `json:"fcmToken"`
}

// handleRegisterDevice registers/updates this phone's FCM push token, per
// shared/API.md's /v1/devices entry.
func (s *Server) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	var req registerDeviceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.FCMToken == "" {
		writeError(w, http.StatusBadRequest, "fcmToken is required")
		return
	}

	if s.Devices == nil {
		writeError(w, http.StatusInternalServerError, "device registry not configured")
		return
	}
	device := s.Devices.Register(req.FCMToken)
	writeJSON(w, http.StatusOK, device)
}

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("missing request body")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON body")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
