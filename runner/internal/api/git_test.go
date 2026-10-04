package api

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"relay/runner/internal/git"
)

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestGitEndpoints(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "T")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@x.y")
	t.Setenv("GIT_COMMITTER_NAME", "T")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@x.y")
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())

	srv := newTestServer(t)
	srv.runSync = true
	h := srv.Routes()
	p, err := srv.Projects.Create("repo")
	if err != nil {
		t.Fatal(err)
	}
	base := "/v1/projects/" + p.ID + "/git"

	// Plain folder: isRepo false, not an error; repo-only endpoints → 409.
	var st GitStatus
	st = GitStatus{}
	decodeBody(t, doRequest(t, h, http.MethodGet, base, testKey, nil), &st)
	if st.IsRepo {
		t.Fatalf("plain folder reported as repo: %+v", st)
	}
	if rec := doRequest(t, h, http.MethodGet, base+"/log", testKey, nil); rec.Code != http.StatusConflict {
		t.Errorf("log outside repo = %d, want 409", rec.Code)
	}

	origin := filepath.Join(t.TempDir(), "origin.git")
	gitCmd(t, filepath.Dir(origin), "init", "-q", "--bare", "-b", "main", origin)
	gitCmd(t, p.Path, "init", "-q", "-b", "main")
	gitCmd(t, p.Path, "remote", "add", "origin", origin)
	os.WriteFile(filepath.Join(p.Path, "a.txt"), []byte("hi\n"), 0o644)

	st = GitStatus{}
	decodeBody(t, doRequest(t, h, http.MethodGet, base, testKey, nil), &st)
	if !st.IsRepo || st.Branch != "main" || len(st.Files) != 1 || !st.Files[0].Untracked {
		t.Fatalf("status = %+v", st)
	}
	var d git.Diff
	decodeBody(t, doRequest(t, h, http.MethodGet, base+"/diff?path=a.txt", testKey, nil), &d)
	if d.Diff == "" {
		t.Error("empty diff for untracked file")
	}
	if rec := doRequest(t, h, http.MethodGet, base+"/diff?path=../x", testKey, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("escaping diff path = %d, want 400", rec.Code)
	}

	if rec := doRequest(t, h, http.MethodPost, base+"/commit", testKey, map[string]string{"message": "x"}); rec.Code != http.StatusBadRequest {
		t.Errorf("commit with nothing staged = %d, want 400", rec.Code)
	}
	st = GitStatus{}
	decodeBody(t, doRequest(t, h, http.MethodPost, base+"/stage", testKey, map[string]any{"paths": []string{"a.txt"}}), &st)
	if len(st.Files) != 1 || !st.Files[0].Staged() {
		t.Fatalf("after stage = %+v", st)
	}
	rec := doRequest(t, h, http.MethodPost, base+"/commit", testKey, map[string]string{"message": "first"})
	st = GitStatus{}
	decodeBody(t, rec, &st)
	if rec.Code != http.StatusOK || len(st.Files) != 0 {
		t.Fatalf("commit = %d %+v", rec.Code, st)
	}

	// No upstream → push publishes; runSync makes the 202 body already final.
	rec = doRequest(t, h, http.MethodPost, base+"/push", testKey, nil)
	st = GitStatus{}
	decodeBody(t, rec, &st)
	if rec.Code != http.StatusAccepted || st.LastOp != "push" || st.LastError != "" || st.Upstream != "origin/main" {
		t.Fatalf("push = %d %+v", rec.Code, st)
	}

	var log []git.LogEntry
	decodeBody(t, doRequest(t, h, http.MethodGet, base+"/log", testKey, nil), &log)
	if len(log) != 1 || log[0].Subject != "first" {
		t.Errorf("log = %+v", log)
	}
	st = GitStatus{}
	decodeBody(t, doRequest(t, h, http.MethodPost, base+"/switch", testKey, map[string]any{"branch": "feat", "create": true}), &st)
	if st.Branch != "feat" {
		t.Errorf("after switch -c = %+v", st)
	}
	var bs []git.Branch
	decodeBody(t, doRequest(t, h, http.MethodGet, base+"/branches", testKey, nil), &bs)
	if len(bs) != 2 {
		t.Errorf("branches = %+v", bs)
	}

	// Push to a remote that doesn't exist: lastError set, not an auth failure.
	gitCmd(t, p.Path, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	st = GitStatus{}
	decodeBody(t, doRequest(t, h, http.MethodPost, base+"/push", testKey, nil), &st)
	if st.LastError == "" || st.AuthFailed || st.NeedsAuth != "" {
		t.Errorf("failed push = %+v", st)
	}

	// An op in flight blocks others.
	srv.beginGitOp(p.ID, "pushing")
	if rec := doRequest(t, h, http.MethodPost, base+"/stage", testKey, map[string]any{"all": true}); rec.Code != http.StatusConflict {
		t.Errorf("stage during push = %d, want 409", rec.Code)
	}
}
