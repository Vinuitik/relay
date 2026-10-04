package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var ctx = context.Background()

// gitT runs git in dir, failing the test on error.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// newRepo makes a repo with one commit on main, and a bare "origin" it
// tracks.
func newRepo(t *testing.T) (work, origin string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "T")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@x.y")
	t.Setenv("GIT_COMMITTER_NAME", "T")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@x.y")
	base := t.TempDir()
	origin = filepath.Join(base, "origin.git")
	work = filepath.Join(base, "work")
	gitT(t, base, "init", "-q", "--bare", "-b", "main", origin)
	gitT(t, base, "init", "-q", "-b", "main", work)
	write(t, work, "a.txt", "one\n")
	gitT(t, work, "add", ".")
	gitT(t, work, "commit", "-q", "-m", "first")
	gitT(t, work, "remote", "add", "origin", origin)
	gitT(t, work, "push", "-q", "-u", "origin", "main")
	return work, origin
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusStageCommitPush(t *testing.T) {
	work, origin := newRepo(t)
	sub := filepath.Join(work, "sub")
	write(t, work, "a.txt", "one\ntwo\n")
	write(t, work, "sub/new.txt", "fresh\n")

	root, err := Root(ctx, sub)
	if err != nil || filepath.Clean(root) != filepath.Clean(work) {
		// macOS/Windows temp dirs may differ by symlink/case; compare base names
		if err != nil || filepath.Base(root) != "work" {
			t.Fatalf("Root = %q, %v", root, err)
		}
	}
	st, err := GetStatus(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "main" || st.Upstream != "origin/main" || st.Ahead != 0 || len(st.Files) != 2 {
		t.Fatalf("status = %+v", st)
	}
	if st.RemoteURL != origin {
		t.Errorf("remote = %q, want %q", st.RemoteURL, origin)
	}

	d, err := GetDiff(ctx, root, "sub/new.txt", false)
	if err != nil || !strings.Contains(d.Diff, "+fresh") {
		t.Errorf("untracked diff = %+v, %v", d, err)
	}
	d, err = GetDiff(ctx, root, "a.txt", false)
	if err != nil || !strings.Contains(d.Diff, "+two") {
		t.Errorf("unstaged diff = %+v, %v", d, err)
	}

	if _, err := Commit(ctx, root, "nothing"); !errors.Is(err, ErrNothingStaged) {
		t.Errorf("commit with nothing staged: %v", err)
	}
	if err := Stage(ctx, root, []string{"a.txt"}, false); err != nil {
		t.Fatal(err)
	}
	d, _ = GetDiff(ctx, root, "a.txt", true)
	if !strings.Contains(d.Diff, "+two") {
		t.Errorf("staged diff = %q", d.Diff)
	}
	if err := Unstage(ctx, root, []string{"a.txt"}, false); err != nil {
		t.Fatal(err)
	}
	if err := Stage(ctx, root, nil, true); err != nil {
		t.Fatal(err)
	}
	st, _ = GetStatus(ctx, root)
	for _, f := range st.Files {
		if !f.Staged() {
			t.Errorf("not staged after stage-all: %+v", f)
		}
	}
	c, err := Commit(ctx, root, "second")
	if err != nil || c.Subject != "second" || c.Short == "" {
		t.Fatalf("commit = %+v, %v", c, err)
	}
	st, _ = GetStatus(ctx, root)
	if st.Ahead != 1 || len(st.Files) != 0 {
		t.Errorf("after commit: %+v", st)
	}
	if _, err := Push(ctx, root); err != nil {
		t.Fatal(err)
	}
	st, _ = GetStatus(ctx, root)
	if st.Ahead != 0 {
		t.Errorf("after push ahead=%d", st.Ahead)
	}
	log, err := Log(ctx, root, 10)
	if err != nil || len(log) != 2 || log[0].Subject != "second" || log[1].Subject != "first" {
		t.Errorf("log = %+v, %v", log, err)
	}
}

func TestBranchesSwitchPublishAndPull(t *testing.T) {
	work, origin := newRepo(t)
	if err := Switch(ctx, work, "feat", true, false); err != nil {
		t.Fatal(err)
	}
	write(t, work, "f.txt", "f\n")
	Stage(ctx, work, nil, true)
	Commit(ctx, work, "feat work")
	// No upstream yet → push publishes with -u.
	if _, err := Push(ctx, work); err != nil {
		t.Fatal(err)
	}
	st, _ := GetStatus(ctx, work)
	if st.Upstream != "origin/feat" {
		t.Errorf("upstream after publish = %q", st.Upstream)
	}
	if err := Switch(ctx, work, "main", false, false); err != nil {
		t.Fatal(err)
	}
	bs, _ := Branches(ctx, work)
	var names []string
	for _, b := range bs {
		names = append(names, b.Name)
		if b.Name == "main" && !b.Current {
			t.Error("main not current")
		}
	}
	if strings.Join(names, ",") != "feat,main" { // remote ones are all tracked → hidden
		t.Errorf("branches = %v", names)
	}

	// Another clone pushes; we fetch → behind 1 → pull fast-forwards.
	other := filepath.Join(filepath.Dir(work), "other")
	gitT(t, filepath.Dir(work), "clone", "-q", origin, other)
	write(t, other, "o.txt", "o\n")
	gitT(t, other, "add", ".")
	gitT(t, other, "commit", "-q", "-m", "from other")
	gitT(t, other, "push", "-q")
	gitT(t, other, "switch", "-q", "-c", "theirs")
	gitT(t, other, "push", "-q", "-u", "origin", "theirs")
	if _, err := Fetch(ctx, work); err != nil {
		t.Fatal(err)
	}
	st, _ = GetStatus(ctx, work)
	if st.Behind != 1 {
		t.Errorf("behind = %d", st.Behind)
	}
	if _, err := Pull(ctx, work); err != nil {
		t.Fatal(err)
	}
	st, _ = GetStatus(ctx, work)
	if st.Behind != 0 {
		t.Errorf("behind after pull = %d", st.Behind)
	}
	bs, _ = Branches(ctx, work)
	found := false
	for _, b := range bs {
		found = found || (b.Name == "origin/theirs" && b.Remote)
	}
	if !found {
		t.Errorf("untracked remote branch missing: %+v", bs)
	}
	if err := Switch(ctx, work, "origin/theirs", false, true); err != nil {
		t.Fatal(err)
	}
	st, _ = GetStatus(ctx, work)
	if st.Branch != "theirs" || st.Upstream != "origin/theirs" {
		t.Errorf("after remote switch: %+v", st)
	}
	if err := Switch(ctx, work, "bad..name", true, false); !errors.Is(err, ErrBadBranch) {
		t.Errorf("bad name: %v", err)
	}
}

func TestNotRepoAndPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	dir := t.TempDir()
	if _, err := Root(ctx, dir); !errors.Is(err, ErrNotRepo) {
		t.Errorf("Root of plain dir: %v", err)
	}
	for _, p := range []string{"", "/etc/passwd", "../x", "a/../../x", `C:\x`} {
		if checkPath(p) == nil {
			t.Errorf("checkPath(%q) accepted", p)
		}
	}
	if checkPath("a/b..c/d") != nil {
		t.Error("checkPath rejected a dotted name")
	}
}

func TestParseStatusRenameConflictTruncation(t *testing.T) {
	out := "# branch.oid abc\x00# branch.head (detached)\x00" +
		"2 R. N... 100644 100644 100644 h1 h2 R100 new name.txt\x00old.txt\x00" +
		"u UU N... 100644 100644 100644 100644 h1 h2 h3 c.txt\x00" +
		"? un tracked.txt\x00"
	st := ParseStatus(out)
	if !st.Detached || st.Branch != "" || len(st.Files) != 3 {
		t.Fatalf("%+v", st)
	}
	if f := st.Files[0]; f.Path != "new name.txt" || f.OrigPath != "old.txt" || f.Index != "R" || !f.Staged() {
		t.Errorf("rename = %+v", f)
	}
	if !st.Files[1].Conflicted || !st.Files[2].Untracked || st.Files[2].Path != "un tracked.txt" {
		t.Errorf("files = %+v", st.Files)
	}
	var b strings.Builder
	for i := 0; i < MaxFiles+3; i++ {
		b.WriteString("? f\x00")
	}
	if st := ParseStatus(b.String()); len(st.Files) != MaxFiles || !st.Truncated {
		t.Errorf("truncation: %d %v", len(st.Files), st.Truncated)
	}
}

func TestAuthClassification(t *testing.T) {
	for _, s := range []string{
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled",
		"remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/x/y.git/'",
		"git@github.com: Permission denied (publickey).",
		"fatal: Cannot prompt because user interactivity has been disabled.",
	} {
		if !IsAuthError(s) {
			t.Errorf("not classified as auth: %q", s)
		}
	}
	if IsAuthError("! [rejected] main -> main (fetch first)") {
		t.Error("non-fast-forward classified as auth")
	}
	if AuthProvider("https://github.com/Vinuitik/Relay.git") != "github" || AuthProvider("git@github.com:x/y.git") != "" || AuthProvider("https://gitlab.com/x") != "" {
		t.Error("AuthProvider mapping")
	}
	if got := RedactURL("https://user:ghp_secret@github.com/x/y.git"); got != "https://github.com/x/y.git" {
		t.Errorf("RedactURL = %q", got)
	}
	if got := RedactURL("git@github.com:x/y.git"); got != "git@github.com:x/y.git" {
		t.Errorf("RedactURL scp = %q", got)
	}
}
