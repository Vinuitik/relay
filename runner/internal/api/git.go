package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"relay/runner/internal/git"
	"relay/runner/internal/project"
)

// The phone's Git tab (see internal/git and runner/FLOWS.md "Git"). Local
// operations (stage, commit, switch, ...) answer synchronously; network
// ones (push/pull/fetch) run in the background like compose up/down: 202 +
// the app polls GET …/git until `operation` is "".

// GitStatus is GET /v1/projects/{id}/git - see shared/API.md.
type GitStatus struct {
	IsRepo bool `json:"isRepo"`
	git.Status
	Operation  string `json:"operation"`            // "" | "pushing" | "pulling" | "fetching"
	LastOp     string `json:"lastOp,omitempty"`     // "push" | "pull" | "fetch" - the last finished one
	LastError  string `json:"lastError,omitempty"`  // its error (git's own message), "" on success
	LastOutput string `json:"lastOutput,omitempty"` // its output on success
	// NeedsAuth names the sign-in relay provider that would fix LastError
	// ("github"); "" when it wasn't an auth failure or no provider can help.
	NeedsAuth string `json:"needsAuth,omitempty"`
	// AuthFailed is true when LastError was an auth failure at all (even
	// one no provider can fix, e.g. an ssh key).
	AuthFailed bool `json:"authFailed,omitempty"`
}

type gitOp struct {
	operation  string
	lastOp     string
	lastError  string
	lastOutput string
	needsAuth  string
	authFailed bool
}

type gitPathsRequest struct {
	Paths []string `json:"paths"`
	All   bool     `json:"all"`
}

type gitCommitRequest struct {
	Message string `json:"message"`
}

type gitSwitchRequest struct {
	Branch string `json:"branch"`
	Create bool   `json:"create"`
	Remote bool   `json:"remote"`
}

func writeGitError(w http.ResponseWriter, err error) {
	var ge *git.Error
	switch {
	case errors.Is(err, git.ErrNotRepo):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, git.ErrGitNotFound):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, git.ErrNothingStaged), errors.Is(err, git.ErrEmptyMessage),
		errors.Is(err, git.ErrBadBranch), errors.Is(err, git.ErrPathOutsideRepo), errors.Is(err, git.ErrNoRemote):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &ge):
		// git refused (e.g. switch with conflicting local changes): its own
		// message is the useful part.
		writeError(w, http.StatusConflict, ge.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// gitRoot resolves {projectId} → its repository root.
func (s *Server) gitRoot(w http.ResponseWriter, r *http.Request) (project.Project, string, bool) {
	p, err := s.Projects.Get(r.PathValue("projectId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return p, "", false
	}
	root, err := git.Root(r.Context(), p.Path)
	if err != nil {
		writeGitError(w, err)
		return p, "", false
	}
	return p, root, true
}

func (s *Server) gitStatus(ctx context.Context, p project.Project) (GitStatus, error) {
	s.opsMu.Lock()
	op := s.gitOps[p.ID]
	s.opsMu.Unlock()
	st := GitStatus{
		Operation: op.operation, LastOp: op.lastOp, LastError: op.lastError,
		LastOutput: op.lastOutput, NeedsAuth: op.needsAuth, AuthFailed: op.authFailed,
	}
	root, err := git.Root(ctx, p.Path)
	if errors.Is(err, git.ErrNotRepo) {
		st.Files = []git.File{}
		return st, nil // isRepo false - a plain project folder is not an error
	}
	if err != nil {
		return st, err
	}
	gs, err := git.GetStatus(ctx, root)
	if err != nil {
		return st, err
	}
	st.IsRepo, st.Status = true, gs
	return st, nil
}

// handleGitStatus → GET /v1/projects/{projectId}/git.
func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	p, err := s.Projects.Get(r.PathValue("projectId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	st, err := s.gitStatus(r.Context(), p)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleGitDiff → GET …/git/diff?path=<root-relative>&staged=true|false.
func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	_, root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	staged, _ := strconv.ParseBool(r.URL.Query().Get("staged"))
	d, err := git.GetDiff(r.Context(), root, r.URL.Query().Get("path"), staged)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// handleGitLog → GET …/git/log?limit=N (default 30, max git.MaxLog).
func (s *Server) handleGitLog(w http.ResponseWriter, r *http.Request) {
	_, root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		n = 30
	}
	entries, err := git.Log(r.Context(), root, n)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleGitBranches → GET …/git/branches.
func (s *Server) handleGitBranches(w http.ResponseWriter, r *http.Request) {
	_, root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	bs, err := git.Branches(r.Context(), root)
	if err != nil {
		writeGitError(w, err)
		return
	}
	if bs == nil {
		bs = []git.Branch{}
	}
	writeJSON(w, http.StatusOK, bs)
}

// gitLocal runs a quick mutating op (refused while a push/pull/fetch is in
// flight - they'd race on the index/HEAD), then answers with fresh status.
func (s *Server) gitLocal(w http.ResponseWriter, r *http.Request, do func(root string) error) {
	p, root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	if !s.beginGitOp(p.ID, "local") {
		writeError(w, http.StatusConflict, "a git operation is already running for this project")
		return
	}
	err := do(root)
	s.opsMu.Lock()
	op := s.gitOps[p.ID]
	op.operation = ""
	s.gitOps[p.ID] = op
	s.opsMu.Unlock()
	if err != nil {
		writeGitError(w, err)
		return
	}
	st, err := s.gitStatus(r.Context(), p)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleGitStage → POST …/git/stage {paths[] | all}.
func (s *Server) handleGitStage(w http.ResponseWriter, r *http.Request) {
	var req gitPathsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.gitLocal(w, r, func(root string) error { return git.Stage(r.Context(), root, req.Paths, req.All) })
}

// handleGitUnstage → POST …/git/unstage {paths[] | all}.
func (s *Server) handleGitUnstage(w http.ResponseWriter, r *http.Request) {
	var req gitPathsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.gitLocal(w, r, func(root string) error { return git.Unstage(r.Context(), root, req.Paths, req.All) })
}

// handleGitCommit → POST …/git/commit {message}. Commits what is staged.
func (s *Server) handleGitCommit(w http.ResponseWriter, r *http.Request) {
	var req gitCommitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.gitLocal(w, r, func(root string) error {
		_, err := git.Commit(r.Context(), root, req.Message)
		return err
	})
}

// handleGitSwitch → POST …/git/switch {branch, create?, remote?}.
func (s *Server) handleGitSwitch(w http.ResponseWriter, r *http.Request) {
	var req gitSwitchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.gitLocal(w, r, func(root string) error {
		return git.Switch(r.Context(), root, req.Branch, req.Create, req.Remote)
	})
}

func (s *Server) handleGitPush(w http.ResponseWriter, r *http.Request) {
	s.gitNetwork(w, r, "push", "pushing", git.Push)
}

func (s *Server) handleGitPull(w http.ResponseWriter, r *http.Request) {
	s.gitNetwork(w, r, "pull", "pulling", git.Pull)
}

func (s *Server) handleGitFetch(w http.ResponseWriter, r *http.Request) {
	s.gitNetwork(w, r, "fetch", "fetching", git.Fetch)
}

// gitNetwork starts push/pull/fetch in the background → 202 + status. On an
// auth failure the result names the sign-in provider that can fix it
// (needsAuth) so the phone can open that sign-in and retry.
func (s *Server) gitNetwork(w http.ResponseWriter, r *http.Request, name, operation string, do func(context.Context, string) (string, error)) {
	p, root, ok := s.gitRoot(w, r)
	if !ok {
		return
	}
	if !s.beginGitOp(p.ID, operation) {
		writeError(w, http.StatusConflict, "a git operation is already running for this project")
		return
	}
	s.runBackground(func() {
		out, err := do(context.Background(), root)
		op := gitOp{lastOp: name}
		if err != nil {
			op.lastError = err.Error()
			if git.IsAuthError(op.lastError) {
				op.authFailed = true
				st, _ := git.GetStatus(context.Background(), root)
				op.needsAuth = git.AuthProvider(st.RemoteURL)
			}
		} else {
			op.lastOutput = out
		}
		s.opsMu.Lock()
		s.gitOps[p.ID] = op
		s.opsMu.Unlock()
	})
	st, err := s.gitStatus(r.Context(), p)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

// beginGitOp marks an operation as running for project id, or returns false
// if one already is. Keeps the last finished op's result.
func (s *Server) beginGitOp(id, operation string) bool {
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	if s.gitOps == nil {
		s.gitOps = map[string]gitOp{}
	}
	op := s.gitOps[id]
	if op.operation != "" {
		return false
	}
	op.operation = operation
	s.gitOps[id] = op
	return true
}
