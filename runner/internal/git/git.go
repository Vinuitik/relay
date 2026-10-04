// Package git is the phone's Git tab: status, diff, stage/unstage, commit,
// branches, log, push/pull/fetch - by shelling out to the machine's own
// `git`, so pushes use the runner user's credentials (Git Credential
// Manager, gh, ssh keys) exactly as a terminal on that PC would. See
// runner/FLOWS.md "Git".
//
// Every command runs non-interactively (GIT_TERMINAL_PROMPT=0,
// GCM_INTERACTIVE=never, stdin closed) with a timeout: nobody is at the PC
// to answer a prompt, so a missing credential must fail fast and be relayed
// (AuthProvider → the phone's sign-in sheet), never hang.
//
// All commands run at the repository root (`git rev-parse --show-toplevel`
// of the project dir), so every path in and out is root-relative.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotRepo        = errors.New("not a git repository")
	ErrGitNotFound    = errors.New("git not found on this runner (install Git, or put it on the runner's PATH)")
	ErrNothingStaged  = errors.New("nothing staged to commit - stage some changes first")
	ErrEmptyMessage   = errors.New("commit message is required")
	ErrBadBranch      = errors.New("not a valid branch name")
	ErrNoRemote       = errors.New("this repository has no remote to push to")
	ErrPathOutsideRepo = errors.New("path is outside the repository")
)

// Timeouts: local commands should be instant; network ones may be slow
// (large push, slow link) but must still end.
var (
	LocalTimeout   = 30 * time.Second
	NetworkTimeout = 3 * time.Minute
)

// Limits on what is sent to the phone.
const (
	MaxFiles     = 500
	MaxDiffBytes = 256 << 10
	MaxLog       = 100
)

// Error is a failed git command; Output is its stderr (stdout if stderr was
// empty), trimmed.
type Error struct {
	Args   []string
	Output string
	Err    error
}

func (e *Error) Error() string {
	if e.Output != "" {
		return e.Output
	}
	return fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// env makes every git call non-interactive and its messages English (the
// auth classifier matches on them).
func env() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never",
		"GIT_OPTIONAL_LOCKS=0", // status/diff don't take index.lock from under an agent
		"LC_ALL=C",
		"LANGUAGE=C",
	)
}

// run executes git in dir and returns stdout. okCodes are non-zero exit
// codes that still count as success (diff --no-index exits 1 on changes).
func run(ctx context.Context, dir string, timeout time.Duration, args []string, okCodes ...int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env()
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", ErrGitNotFound
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			for _, c := range okCodes {
				if ee.ExitCode() == c {
					return stdout.String(), nil
				}
			}
		}
		out := strings.TrimSpace(stderr.String())
		if out == "" {
			out = strings.TrimSpace(stdout.String())
		}
		if ctx.Err() == context.DeadlineExceeded {
			out = strings.TrimSpace(fmt.Sprintf("git %s timed out after %s. %s", args[0], timeout, out))
		}
		if strings.Contains(out, "not a git repository") {
			return "", ErrNotRepo
		}
		return "", &Error{Args: args, Output: out, Err: err}
	}
	return stdout.String(), nil
}

// Root returns the repository root containing dir.
func Root(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, LocalTimeout, []string{"rev-parse", "--show-toplevel"})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// File is one changed path. Index/Worktree are git's XY status letters
// ("M", "A", "D", "R", "C", "T", "U", or "." for unchanged).
type File struct {
	Path       string `json:"path"`
	OrigPath   string `json:"origPath,omitempty"` // renames/copies
	Index      string `json:"index"`
	Worktree   string `json:"worktree"`
	Untracked  bool   `json:"untracked,omitempty"`
	Conflicted bool   `json:"conflicted,omitempty"`
}

// Staged reports whether f has changes in the index.
func (f File) Staged() bool { return !f.Untracked && f.Index != "." }

// Status is the working-tree summary.
type Status struct {
	Branch    string `json:"branch"` // "" when detached
	Detached  bool   `json:"detached"`
	Upstream  string `json:"upstream,omitempty"`
	Ahead     int    `json:"ahead"`
	Behind    int    `json:"behind"`
	Files     []File `json:"files"`
	Truncated bool   `json:"truncated,omitempty"` // more than MaxFiles changed paths
	// RemoteURL is the push remote's URL with any embedded credentials
	// removed; "" when there is no remote.
	RemoteURL string `json:"remoteUrl,omitempty"`
}

// GetStatus reads `git status --porcelain=v2 --branch -z`.
func GetStatus(ctx context.Context, root string) (Status, error) {
	out, err := run(ctx, root, LocalTimeout, []string{"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all"})
	if err != nil {
		return Status{}, err
	}
	st := ParseStatus(out)
	st.RemoteURL = RedactURL(remoteURL(ctx, root, st.Upstream))
	return st, nil
}

// ParseStatus parses porcelain v2 -z output.
func ParseStatus(out string) Status {
	st := Status{Files: []File{}}
	recs := strings.Split(out, "\x00")
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if rec == "" {
			continue
		}
		switch rec[0] {
		case '#':
			switch {
			case strings.HasPrefix(rec, "# branch.head "):
				h := strings.TrimPrefix(rec, "# branch.head ")
				if h == "(detached)" {
					st.Detached = true
				} else {
					st.Branch = h
				}
			case strings.HasPrefix(rec, "# branch.upstream "):
				st.Upstream = strings.TrimPrefix(rec, "# branch.upstream ")
			case strings.HasPrefix(rec, "# branch.ab "):
				var a, b int
				fmt.Sscanf(strings.TrimPrefix(rec, "# branch.ab "), "+%d -%d", &a, &b)
				st.Ahead, st.Behind = a, b
			}
			continue
		}
		var f File
		switch rec[0] {
		case '1':
			p := strings.SplitN(rec, " ", 9)
			if len(p) < 9 {
				continue
			}
			f = File{Path: p[8], Index: p[1][:1], Worktree: p[1][1:2]}
		case '2':
			p := strings.SplitN(rec, " ", 10)
			if len(p) < 10 {
				continue
			}
			f = File{Path: p[9], Index: p[1][:1], Worktree: p[1][1:2]}
			if i+1 < len(recs) {
				i++
				f.OrigPath = recs[i]
			}
		case 'u':
			p := strings.SplitN(rec, " ", 11)
			if len(p) < 11 {
				continue
			}
			f = File{Path: p[10], Index: "U", Worktree: "U", Conflicted: true}
		case '?':
			f = File{Path: rec[2:], Index: ".", Worktree: "?", Untracked: true}
		default:
			continue
		}
		if len(st.Files) >= MaxFiles {
			st.Truncated = true
			continue
		}
		st.Files = append(st.Files, f)
	}
	return st
}

// remoteURL is the URL of the upstream's remote, else "origin", else the
// first remote; "" if none.
func remoteURL(ctx context.Context, root, upstream string) string {
	name := pushRemote(ctx, root, upstream)
	if name == "" {
		return ""
	}
	out, err := run(ctx, root, LocalTimeout, []string{"remote", "get-url", name})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func pushRemote(ctx context.Context, root, upstream string) string {
	out, err := run(ctx, root, LocalTimeout, []string{"remote"})
	if err != nil {
		return ""
	}
	remotes := strings.Fields(out)
	if upstream != "" {
		for _, r := range remotes {
			if strings.HasPrefix(upstream, r+"/") {
				return r
			}
		}
	}
	for _, r := range remotes {
		if r == "origin" {
			return r
		}
	}
	if len(remotes) > 0 {
		return remotes[0]
	}
	return ""
}

// RedactURL drops userinfo (a token pasted into a remote URL) before the
// URL leaves the machine.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil || u.Scheme == "" {
		return raw // scp-like git@host:path has no secret in it
	}
	u.User = nil
	return u.String()
}

// Diff is one file's patch.
type Diff struct {
	Path      string `json:"path"`
	Staged    bool   `json:"staged"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated,omitempty"`
}

// GetDiff returns the staged (index vs HEAD) or unstaged (worktree vs
// index; whole file for an untracked one) patch of path.
func GetDiff(ctx context.Context, root, path string, staged bool) (Diff, error) {
	if err := checkPath(path); err != nil {
		return Diff{}, err
	}
	var out string
	var err error
	switch {
	case staged:
		out, err = run(ctx, root, LocalTimeout, []string{"diff", "--no-color", "--no-ext-diff", "--cached", "--", path})
	default:
		out, err = run(ctx, root, LocalTimeout, []string{"diff", "--no-color", "--no-ext-diff", "--", path})
		if err == nil && out == "" && isUntracked(ctx, root, path) {
			out, err = run(ctx, root, LocalTimeout, []string{"diff", "--no-color", "--no-ext-diff", "--no-index", "--", "/dev/null", path}, 1)
		}
	}
	if err != nil {
		return Diff{}, err
	}
	d := Diff{Path: path, Staged: staged, Diff: out}
	if len(d.Diff) > MaxDiffBytes {
		d.Diff, d.Truncated = d.Diff[:MaxDiffBytes], true
	}
	return d, nil
}

func isUntracked(ctx context.Context, root, path string) bool {
	out, err := run(ctx, root, LocalTimeout, []string{"ls-files", "--others", "--exclude-standard", "--", path})
	return err == nil && strings.TrimSpace(out) != ""
}

// checkPath rejects absolute and escaping paths; git itself also refuses
// paths outside the repo, this just fails earlier with a clear error.
func checkPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") || (len(p) > 1 && p[1] == ':') {
		return ErrPathOutsideRepo
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return ErrPathOutsideRepo
		}
	}
	return nil
}

// Stage adds paths (or everything, incl. deletions and untracked files,
// when all) to the index.
func Stage(ctx context.Context, root string, paths []string, all bool) error {
	if all {
		_, err := run(ctx, root, LocalTimeout, []string{"add", "--all"})
		return err
	}
	for _, p := range paths {
		if err := checkPath(p); err != nil {
			return err
		}
	}
	_, err := run(ctx, root, LocalTimeout, append([]string{"add", "--all", "--"}, paths...))
	return err
}

// Unstage removes paths (or everything) from the index, keeping the
// working-tree changes. `reset` also works before the first commit.
func Unstage(ctx context.Context, root string, paths []string, all bool) error {
	if all {
		_, err := run(ctx, root, LocalTimeout, []string{"reset", "-q"})
		return err
	}
	for _, p := range paths {
		if err := checkPath(p); err != nil {
			return err
		}
	}
	_, err := run(ctx, root, LocalTimeout, append([]string{"reset", "-q", "--"}, paths...))
	return err
}

// Commit commits what is staged and returns the new commit.
func Commit(ctx context.Context, root, message string) (LogEntry, error) {
	if strings.TrimSpace(message) == "" {
		return LogEntry{}, ErrEmptyMessage
	}
	// `diff --cached --quiet` exits 0 = nothing staged, 1 = something staged.
	if _, err := run(ctx, root, LocalTimeout, []string{"diff", "--cached", "--quiet"}); err == nil {
		return LogEntry{}, ErrNothingStaged
	} else if ge := (*Error)(nil); !errors.As(err, &ge) {
		return LogEntry{}, err
	}
	if _, err := run(ctx, root, LocalTimeout, []string{"commit", "-q", "-m", message}); err != nil {
		return LogEntry{}, err
	}
	entries, err := Log(ctx, root, 1)
	if err != nil || len(entries) == 0 {
		return LogEntry{}, err
	}
	return entries[0], nil
}

// LogEntry is one commit.
type LogEntry struct {
	Hash    string `json:"hash"`
	Short   string `json:"short"`
	Author  string `json:"author"`
	Time    int64  `json:"time"` // unix seconds
	Subject string `json:"subject"`
}

const logFormat = "%H%x1f%h%x1f%an%x1f%at%x1f%s%x1e"

// Log returns the last n commits of HEAD (empty before the first commit).
func Log(ctx context.Context, root string, n int) ([]LogEntry, error) {
	if n <= 0 || n > MaxLog {
		n = MaxLog
	}
	out, err := run(ctx, root, LocalTimeout, []string{"log", "-n", strconv.Itoa(n), "--format=" + logFormat})
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) && strings.Contains(ge.Output, "does not have any commits") {
			return []LogEntry{}, nil
		}
		return nil, err
	}
	return parseLog(out), nil
}

func parseLog(out string) []LogEntry {
	entries := []LogEntry{}
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\r\n")
		p := strings.Split(rec, "\x1f")
		if len(p) != 5 {
			continue
		}
		t, _ := strconv.ParseInt(p[3], 10, 64)
		entries = append(entries, LogEntry{Hash: p[0], Short: p[1], Author: p[2], Time: t, Subject: p[4]})
	}
	return entries
}

// Branch is a local or remote-tracking branch.
type Branch struct {
	Name     string `json:"name"`             // "main", or "origin/feature" for a remote one
	Remote   bool   `json:"remote"`
	Current  bool   `json:"current"`
	Upstream string `json:"upstream,omitempty"`
}

// Branches lists local branches, then remote-tracking ones that have no
// local branch tracking them (switching to one creates that local branch).
func Branches(ctx context.Context, root string) ([]Branch, error) {
	out, err := run(ctx, root, LocalTimeout, []string{"for-each-ref", "--format=%(HEAD)%1f%(refname)%1f%(upstream:short)", "refs/heads", "refs/remotes"})
	if err != nil {
		return nil, err
	}
	return parseBranches(out), nil
}

func parseBranches(out string) []Branch {
	var local, remote []Branch
	tracked := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		p := strings.Split(strings.TrimRight(line, "\r"), "\x1f")
		if len(p) != 3 {
			continue
		}
		switch {
		case strings.HasPrefix(p[1], "refs/heads/"):
			local = append(local, Branch{Name: strings.TrimPrefix(p[1], "refs/heads/"), Current: p[0] == "*", Upstream: p[2]})
			if p[2] != "" {
				tracked[p[2]] = true
			}
		case strings.HasPrefix(p[1], "refs/remotes/"):
			name := strings.TrimPrefix(p[1], "refs/remotes/")
			if strings.HasSuffix(name, "/HEAD") {
				continue
			}
			remote = append(remote, Branch{Name: name, Remote: true})
		}
	}
	out2 := append([]Branch{}, local...)
	for _, r := range remote {
		if !tracked[r.Name] {
			out2 = append(out2, r)
		}
	}
	return out2
}

// Switch checks out branch. create → new branch from HEAD; remote →
// `switch --track <remote>/<name>` (creates the local tracking branch).
// Uncommitted changes that would be overwritten make git refuse; its
// message is returned as is.
func Switch(ctx context.Context, root, branch string, create, remote bool) error {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return ErrBadBranch
	}
	if !remote {
		if _, err := run(ctx, root, LocalTimeout, []string{"check-ref-format", "--branch", branch}); err != nil {
			return ErrBadBranch
		}
	}
	args := []string{"switch", branch}
	switch {
	case create:
		args = []string{"switch", "-c", branch}
	case remote:
		args = []string{"switch", "--track", branch}
	}
	_, err := run(ctx, root, LocalTimeout, args)
	return err
}

// Push pushes the current branch; with no upstream yet it publishes it
// (`push -u <remote> HEAD`). Returns git's output for the phone.
func Push(ctx context.Context, root string) (string, error) {
	st, err := GetStatus(ctx, root)
	if err != nil {
		return "", err
	}
	if st.Detached {
		return "", errors.New("HEAD is detached - switch to a branch before pushing")
	}
	args := []string{"push"}
	if st.Upstream == "" {
		r := pushRemote(ctx, root, "")
		if r == "" {
			return "", ErrNoRemote
		}
		args = []string{"push", "-u", r, "HEAD"}
	}
	return runNet(ctx, root, args)
}

// Pull fast-forwards the current branch only: a pull from the phone never
// creates a merge commit or a conflict to resolve; if the branches have
// diverged git refuses and says so.
func Pull(ctx context.Context, root string) (string, error) {
	return runNet(ctx, root, []string{"pull", "--ff-only"})
}

// Fetch updates remote-tracking branches (and so ahead/behind).
func Fetch(ctx context.Context, root string) (string, error) {
	return runNet(ctx, root, []string{"fetch", "--prune"})
}

// runNet runs a network command; git reports progress on stderr, so that is
// what's returned on success too.
func runNet(ctx context.Context, root string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, NetworkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = env()
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", ErrGitNotFound
		}
		if ctx.Err() == context.DeadlineExceeded {
			text = strings.TrimSpace(fmt.Sprintf("git %s timed out after %s. %s", args[0], NetworkTimeout, text))
		}
		return text, &Error{Args: args, Output: text, Err: err}
	}
	return text, nil
}

var authFailRe = regexp.MustCompile(`(?i)authentication failed|could not read (username|password)|terminal prompts disabled|invalid username or password|cannot prompt because user interactivity has been disabled|permission denied \(publickey|the requested url returned error: 40[13]|support for password authentication was removed|fatal: credential`)

// IsAuthError reports whether a failed network command failed for lack of
// (valid) credentials.
func IsAuthError(output string) bool { return authFailRe.MatchString(output) }

// AuthProvider maps a remote URL to the sign-in relay provider that can fix
// an auth failure for it ("github"), or "" when none can (ssh keys, other
// hosts - the phone then shows git's own message).
func AuthProvider(remoteURL string) string {
	u := strings.ToLower(remoteURL)
	if strings.HasPrefix(u, "https://github.com/") || strings.HasPrefix(u, "http://github.com/") {
		return "github"
	}
	return ""
}
