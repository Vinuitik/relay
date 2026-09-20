// Package api implements the runner's HTTP API exactly as defined in
// shared/API.md.
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"relay/runner/internal/notify"
	"relay/runner/internal/project"
	"relay/runner/internal/session"
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
	// Suspend, if set, powers this machine off immediately on
	// POST /v1/suspend (see handleSuspend) - a manual counterpart to
	// idle.Monitor's automatic timeout, for "I'm done, don't wait 3
	// minutes." Left nil (not wired in cmd/runnerd/main.go) unless
	// RELAY_IDLE_SUSPEND_ENABLED=true, deliberately reusing idle-suspend's
	// existing opt-in gate rather than inventing a second one - a manual
	// trigger is still "run systemctl poweroff on this host," the same
	// real, hard-to-undo action the idle package's doc comment already
	// warns never to enable outside an intentional deployment.
	Suspend func() error
}

// NewServer builds a Server.
func NewServer(key string, projects *project.Registry, sessions *session.Manager, compose ComposeFuncs, devices *notify.Registry) *Server {
	return &Server{Key: key, Projects: projects, Sessions: sessions, Compose: compose, Devices: devices}
}

// Routes builds the HTTP handler for the v1 API, using Go 1.22's
// method-and-pattern aware http.ServeMux (stdlib only, no router dependency).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/runner/info", s.auth(s.handleRunnerInfo))
	mux.HandleFunc("GET /v1/projects", s.auth(s.handleListProjects))
	mux.HandleFunc("POST /v1/projects", s.auth(s.handleCreateProject))
	mux.HandleFunc("GET /v1/browse", s.auth(s.handleBrowse))
	mux.HandleFunc("GET /v1/projects/{projectId}/sessions", s.auth(s.handleListSessions))
	mux.HandleFunc("POST /v1/projects/{projectId}/sessions", s.auth(s.handleStartSession))
	mux.HandleFunc("GET /v1/sessions/{sessionId}", s.auth(s.handleGetSession))
	mux.HandleFunc("POST /v1/sessions/{sessionId}/message", s.auth(s.handleSendMessage))
	mux.HandleFunc("POST /v1/sessions/{sessionId}/stop", s.auth(s.handleStopSession))
	mux.HandleFunc("POST /v1/projects/{projectId}/containers/start", s.auth(s.handleContainersStart))
	mux.HandleFunc("POST /v1/projects/{projectId}/containers/stop", s.auth(s.handleContainersStop))
	mux.HandleFunc("POST /v1/devices", s.auth(s.handleRegisterDevice))
	mux.HandleFunc("POST /v1/suspend", s.auth(s.handleSuspend))
	mux.HandleFunc("GET /v1/projects/{projectId}/files", s.auth(s.handleListFiles))
	mux.HandleFunc("GET /v1/projects/{projectId}/files/content", s.auth(s.handleFileContent))

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

// anyBusy reports whether any session across any project is currently
// busy - shared by handleRunnerInfo's `busy` field and handleSuspend's
// refusal to power off out from under a running agent.
func (s *Server) anyBusy() bool {
	for _, p := range s.Projects.List() {
		for _, sess := range s.Sessions.ListByProject(p.ID) {
			if sess.State == session.StateBusy {
				return true
			}
		}
	}
	return false
}

func (s *Server) handleRunnerInfo(w http.ResponseWriter, r *http.Request) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	writeJSON(w, http.StatusOK, runnerInfo{Hostname: hostname, Busy: s.anyBusy(), Version: Version})
}

// handleSuspend powers this machine off immediately (POST /v1/suspend) - see
// Server.Suspend's doc comment for why it's gated behind the same
// RELAY_IDLE_SUSPEND_ENABLED flag as automatic idle-suspend, and never
// fires while a session is busy regardless of that flag.
func (s *Server) handleSuspend(w http.ResponseWriter, r *http.Request) {
	if s.anyBusy() {
		writeError(w, http.StatusConflict, "cannot suspend while a session is busy")
		return
	}
	if s.Suspend == nil {
		writeError(w, http.StatusServiceUnavailable, "manual suspend is not enabled on this runner (set RELAY_IDLE_SUSPEND_ENABLED=true)")
		return
	}
	if err := s.Suspend(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{})
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Projects.List())
}

type createProjectRequest struct {
	Name string `json:"name"`
	// Path, if set, registers this already-existing directory as a project
	// instead of scaffolding a new empty one - see project.RegisterExisting.
	Path string `json:"path"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var p project.Project
	var err error
	if req.Path != "" {
		p, err = s.Projects.RegisterExisting(req.Path, req.Name)
	} else {
		p, err = s.Projects.Create(req.Name)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// handleBrowse lists subdirectories of an arbitrary absolute path (or
// filesystem roots, if path is omitted) - unscoped, unlike the per-project
// file-viewing endpoints, since its purpose is finding a project directory
// to register before any project-level scoping exists. See
// project.BrowseDir.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	current, entries, err := project.BrowseDir(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": current, "entries": entries})
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

// maxViewableFileSize caps what handleFileContent will read into memory and
// return. Read-only file viewing is meant for skimming source/config, not
// downloading arbitrary large files - see ARCHITECTURE.md "File viewing".
const maxViewableFileSize = 1 << 20 // 1 MiB

type fileEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

// handleListFiles lists the contents of a directory within a project, per
// shared/API.md. path="" (or omitted) lists the project root.
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	full, err := s.Projects.ResolvePath(r.PathValue("projectId"), r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeError(w, http.StatusNotFound, "path not found")
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is not a directory")
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, fileEntry{Name: e.Name(), IsDir: e.IsDir(), Size: fi.Size()})
	}
	writeJSON(w, http.StatusOK, out)
}

type fileContent struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// handleFileContent returns a text file's contents, per shared/API.md. Read
// only, scoped to the project directory via Registry.ResolvePath - never
// serves a directory, a file above maxViewableFileSize, or anything that
// looks binary (a null byte in the first 512 bytes).
func (s *Server) handleFileContent(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}
	full, err := s.Projects.ResolvePath(r.PathValue("projectId"), relPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeError(w, http.StatusNotFound, "path not found")
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}
	if info.Size() > maxViewableFileSize {
		writeError(w, http.StatusRequestEntityTooLarge, "file too large to view (>1MiB)")
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if isBinary(data) {
		writeError(w, http.StatusUnsupportedMediaType, "binary file, not viewable")
		return
	}
	writeJSON(w, http.StatusOK, fileContent{Path: relPath, Content: string(data)})
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > 512 {
		n = 512
	}
	return bytes.IndexByte(data[:n], 0) != -1
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
