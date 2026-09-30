package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"

	"relay/runner/internal/compose"
	"relay/runner/internal/project"
)

// ComposeFuncs lets the compose package be injected, so tests don't need
// real docker/compose files.
type ComposeFuncs struct {
	Detect func(dir string) (compose.Detected, error)
	Status func(dir string) ([]compose.Container, error)
	Up     func(dir, file string) error
	Down   func(dir, file string) error
}

// ContainersStatus is the response of every /containers endpoint - see
// shared/API.md.
type ContainersStatus struct {
	ComposeFiles  []string            `json:"composeFiles"`
	ActiveFile    string              `json:"activeFile"` // "" = several files and none chosen yet
	HasDockerfile bool                `json:"hasDockerfile"`
	Containers    []compose.Container `json:"containers"`
	Operation     string              `json:"operation"` // "" | "starting" | "switching" | "stopping"
	LastError     string              `json:"lastError"` // from the last finished operation
	DockerError   string              `json:"dockerError"`
}

// containerOp is the in-memory state of a project's background compose
// operation. Lost on runner restart, which is fine: it only says "an
// up/down is in flight", and a restart kills that anyway.
type containerOp struct {
	operation string
	lastError string
}

type startContainersRequest struct {
	File string `json:"file"`
}

// activeComposeFile resolves which of files the project uses: the one last
// chosen from the phone if it still exists, else compose.DefaultFile.
func activeComposeFile(p project.Project, files []string) string {
	if p.ActiveComposeFile != "" && slices.Contains(files, p.ActiveComposeFile) {
		return p.ActiveComposeFile
	}
	return compose.DefaultFile(files)
}

func (s *Server) handleContainersStatus(w http.ResponseWriter, r *http.Request) {
	p, err := s.Projects.Get(r.PathValue("projectId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	st, err := s.containersStatus(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleContainersStart turns on the chosen compose file (body `{file}`, or
// the active one if omitted). If a different file was active, that one is
// brought down first - a switch, so dev and prod never run side by side.
// Runs in the background: `up` can pull/build for minutes, far longer than
// the phone's HTTP timeout, so this answers 202 and the app polls GET.
func (s *Server) handleContainersStart(w http.ResponseWriter, r *http.Request) {
	p, err := s.Projects.Get(r.PathValue("projectId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	var req startContainersRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d, err := s.Compose.Detect(p.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(d.ComposeFiles) == 0 {
		writeError(w, http.StatusBadRequest, "no compose file found in this project")
		return
	}
	prev := activeComposeFile(p, d.ComposeFiles)
	target := req.File
	if target == "" {
		target = prev
	}
	if target == "" {
		writeError(w, http.StatusBadRequest, "this project has several compose files and none is chosen yet - pick one")
		return
	}
	if !slices.Contains(d.ComposeFiles, target) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%q is not a compose file in this project", target))
		return
	}

	switching := prev != "" && prev != target
	opName := "starting"
	if switching {
		opName = "switching"
	}
	if !s.beginContainerOp(p.ID, opName) {
		writeError(w, http.StatusConflict, "a container operation is already running for this project")
		return
	}
	if err := s.Projects.SetActiveComposeFile(p.ID, target); err != nil {
		s.endContainerOp(p.ID, nil)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.ActiveComposeFile = target
	dir := p.Path
	s.runBackground(func() {
		var err error
		if switching {
			err = s.Compose.Down(dir, prev)
		}
		if err == nil {
			err = s.Compose.Up(dir, target)
		}
		s.endContainerOp(p.ID, err)
	})
	s.writeContainersAccepted(w, p)
}

// handleContainersStop brings down everything from this project's compose
// stack (compose.Down uses --remove-orphans, so containers from any of the
// project's compose files go, not just the active one's).
func (s *Server) handleContainersStop(w http.ResponseWriter, r *http.Request) {
	p, err := s.Projects.Get(r.PathValue("projectId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	d, err := s.Compose.Detect(p.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(d.ComposeFiles) == 0 {
		writeError(w, http.StatusBadRequest, "no compose file found in this project")
		return
	}
	file := activeComposeFile(p, d.ComposeFiles)
	if file == "" {
		file = d.ComposeFiles[0]
	}
	if !s.beginContainerOp(p.ID, "stopping") {
		writeError(w, http.StatusConflict, "a container operation is already running for this project")
		return
	}
	dir := p.Path
	s.runBackground(func() {
		s.endContainerOp(p.ID, s.Compose.Down(dir, file))
	})
	s.writeContainersAccepted(w, p)
}

func (s *Server) writeContainersAccepted(w http.ResponseWriter, p project.Project) {
	st, err := s.containersStatus(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) containersStatus(p project.Project) (ContainersStatus, error) {
	d, err := s.Compose.Detect(p.Path)
	if err != nil {
		return ContainersStatus{}, err
	}
	s.opsMu.Lock()
	op := s.containerOps[p.ID]
	s.opsMu.Unlock()
	st := ContainersStatus{
		ComposeFiles:  d.ComposeFiles,
		ActiveFile:    activeComposeFile(p, d.ComposeFiles),
		HasDockerfile: d.HasDockerfile,
		Containers:    []compose.Container{},
		Operation:     op.operation,
		LastError:     op.lastError,
	}
	// Only ask docker when there's something to ask about - a plain project
	// shouldn't show "docker isn't running" just for existing.
	if len(d.ComposeFiles) > 0 {
		cs, err := s.Compose.Status(p.Path)
		if err != nil {
			st.DockerError = err.Error()
		} else {
			st.Containers = cs
		}
	}
	return st, nil
}

// beginContainerOp marks an operation as running for project id, or returns
// false if one already is.
func (s *Server) beginContainerOp(id, operation string) bool {
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	if s.containerOps == nil {
		s.containerOps = map[string]containerOp{}
	}
	if s.containerOps[id].operation != "" {
		return false
	}
	s.containerOps[id] = containerOp{operation: operation}
	return true
}

func (s *Server) endContainerOp(id string, err error) {
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	op := containerOp{}
	if err != nil {
		op.lastError = err.Error()
	}
	s.containerOps[id] = op
}

func (s *Server) runBackground(f func()) {
	if s.runSync {
		f()
		return
	}
	go f()
}

// decodeOptionalJSON is decodeJSON for endpoints whose body may be omitted
// entirely - an empty body leaves v at its zero value.
func decodeOptionalJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return errors.New("read body")
	}
	if len(body) == 0 {
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return decodeJSON(r, v)
}
