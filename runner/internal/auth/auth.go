// Package auth lets the phone re-log the runner's Claude Code CLI in when
// its login expires, without anyone at the PC (see runner/FLOWS.md "Claude
// re-login").
//
//	Start:  spawn `claude auth login` (stdin = pipe) → parse the oauth/authorize
//	        URL from its output → awaiting_code (process kept alive, killed
//	        after CodeTimeout without a code)
//	Finish: write code+"\n" to its stdin → wait for exit → signed_in | failed
//	        → OnSignedIn (restart idle agents so they pick up the new login)
//	Status: current state + `claude auth status --json` (loggedIn, email)
//
// Only one login runs at a time. Everything external goes through Runner so
// tests never spawn the real CLI.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// States reported by Status / Start / Finish.
const (
	StateIdle         = "idle"          // no login attempted since the runner started
	StateAwaitingCode = "awaiting_code" // URL handed out, waiting for the code from the phone
	StateSignedIn     = "signed_in"     // last login attempt succeeded
	StateFailed       = "failed"        // last login attempt failed / timed out (see Message)
)

var (
	ErrCLINotFound = errors.New("claude CLI not found on this runner (install Claude Code, or set RELAY_CLAUDE_CLI to its full path)")
	ErrNoLogin     = errors.New("no login in progress - start one first")
	ErrEmptyCode   = errors.New("code is required")
)

// Proc is a running `claude auth login`.
type Proc interface {
	// Stdin is the process's stdin pipe.
	Stdin() io.Writer
	// Output streams combined stdout+stderr; EOF once the process exited.
	Output() io.Reader
	// Wait blocks until the process exited and is reaped.
	Wait() error
	Kill() error
}

// Runner abstracts process execution (cf. compose.Runner) so tests can fake
// the claude CLI.
type Runner interface {
	// Start launches name with args, stdin as a pipe.
	Start(name string, args ...string) (Proc, error)
	// Output runs name to completion and returns its stdout.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Status is the GET /v1/auth/claude response.
type Status struct {
	State    string `json:"state"`
	URL      string `json:"url,omitempty"`
	Message  string `json:"message,omitempty"`
	LoggedIn bool   `json:"loggedIn"`
	Email    string `json:"email,omitempty"`
}

// Result is what Start/Finish return.
type Result struct {
	State   string `json:"state"`
	URL     string `json:"url,omitempty"`
	Message string `json:"message,omitempty"`
}

// Manager runs at most one `claude auth login` at a time.
type Manager struct {
	Runner Runner
	// FindCLI resolves the claude binary; ErrCLINotFound if absent.
	FindCLI func() (string, error)
	// OnSignedIn, if set, runs (in its own goroutine) after a successful
	// login - wired to session.Manager.RestartIdleAgents in runnerd.
	OnSignedIn func()

	URLTimeout    time.Duration // waiting for the login URL to be printed
	CodeTimeout   time.Duration // awaiting_code lifetime before the process is killed
	FinishTimeout time.Duration // waiting for exit after the code was written
	StatusTimeout time.Duration // `claude auth status --json`

	opMu sync.Mutex // serializes Start/Finish

	mu      sync.Mutex
	state   string
	message string
	cur     *login
}

// NewManager returns a Manager using the real CLI.
func NewManager() *Manager {
	return &Manager{
		Runner:        execRunner{},
		FindCLI:       FindCLI,
		URLTimeout:    20 * time.Second,
		CodeTimeout:   10 * time.Minute,
		FinishTimeout: 60 * time.Second,
		StatusTimeout: 10 * time.Second,
		state:         StateIdle,
	}
}

// login is one `claude auth login` process.
type login struct {
	proc    Proc
	url     string
	started time.Time
	timer   *time.Timer

	outMu sync.Mutex
	out   strings.Builder

	urlCh   chan string   // receives the URL once it appears in the output
	done    chan struct{} // closed after output EOF + Wait
	exitErr error         // valid after done
}

func (l *login) output() string {
	l.outMu.Lock()
	defer l.outMu.Unlock()
	return l.out.String()
}

// watch drains the output (looking for the URL) and reaps the process.
func (l *login) watch() {
	buf := make([]byte, 4096)
	sentURL := false
	r := l.proc.Output()
	for {
		n, err := r.Read(buf)
		if n > 0 {
			l.outMu.Lock()
			l.out.Write(buf[:n])
			all := l.out.String()
			l.outMu.Unlock()
			if !sentURL {
				if u := ParseLoginURL(all); u != "" {
					sentURL = true
					l.urlCh <- u
				}
			}
		}
		if err != nil {
			break
		}
	}
	l.exitErr = l.proc.Wait()
	close(l.done)
}

// kill kills the process and waits (bounded) for it to be reaped.
func (l *login) kill() {
	if l.timer != nil {
		l.timer.Stop()
	}
	_ = l.proc.Kill()
	select {
	case <-l.done:
	case <-time.After(10 * time.Second):
		log.Printf("auth: claude auth login did not exit 10s after kill")
	}
}

// Start begins a login, or returns the one already awaiting a code if it is
// younger than CodeTimeout.
func (m *Manager) Start() (Result, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if l := m.cur; l != nil {
		if m.state == StateAwaitingCode && time.Since(l.started) < m.CodeTimeout {
			m.mu.Unlock()
			return Result{State: StateAwaitingCode, URL: l.url}, nil
		}
		m.cur = nil
		m.mu.Unlock()
		l.kill()
	} else {
		m.mu.Unlock()
	}

	bin, err := m.FindCLI()
	if err != nil {
		return Result{}, err
	}
	proc, err := m.Runner.Start(bin, "auth", "login")
	if err != nil {
		return Result{}, fmt.Errorf("start claude auth login: %w", err)
	}
	l := &login{proc: proc, started: time.Now(), urlCh: make(chan string, 1), done: make(chan struct{})}
	go l.watch()

	fail := func(msg string) (Result, error) {
		log.Printf("auth: %s; full claude output:\n%s", msg, l.output())
		msg = withTail(msg, l.output())
		m.setState(StateFailed, msg, nil)
		return Result{State: StateFailed, Message: msg}, nil
	}

	select {
	case u := <-l.urlCh:
		l.url = u
	case <-l.done:
		if ParseLoginURL(l.output()) == "" {
			return fail("claude auth login exited without printing a login URL")
		}
		return fail("claude auth login exited before a code could be entered")
	case <-time.After(m.URLTimeout):
		l.kill()
		return fail(fmt.Sprintf("claude auth login printed no login URL within %s", m.URLTimeout))
	}

	l.timer = time.AfterFunc(m.CodeTimeout, func() { m.expire(l) })
	m.setState(StateAwaitingCode, "", l)
	return Result{State: StateAwaitingCode, URL: l.url}, nil
}

// expire kills a login nobody finished within CodeTimeout.
func (m *Manager) expire(l *login) {
	m.opMu.Lock() // never while Finish is feeding this login a code
	defer m.opMu.Unlock()
	m.mu.Lock()
	if m.cur != l {
		m.mu.Unlock()
		return
	}
	m.cur = nil
	m.state = StateFailed
	m.message = fmt.Sprintf("login timed out: no code entered within %s", m.CodeTimeout)
	m.mu.Unlock()
	l.kill()
}

// Finish feeds the code from the callback page to the waiting login.
func (m *Manager) Finish(code string) (Result, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return Result{}, ErrEmptyCode
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	l := m.cur
	if l == nil || m.state != StateAwaitingCode {
		m.mu.Unlock()
		return Result{}, ErrNoLogin
	}
	m.cur = nil // this attempt ends here, whatever the outcome
	m.mu.Unlock()
	l.timer.Stop()

	fail := func(msg string) (Result, error) {
		out := l.output()
		log.Printf("auth: %s; full claude output:\n%s", msg, out)
		msg = withTail(msg, out)
		m.setState(StateFailed, msg, nil)
		return Result{State: StateFailed, Message: msg}, nil
	}

	if _, err := io.WriteString(l.proc.Stdin(), code+"\n"); err != nil {
		l.kill()
		return fail("could not send the code to claude auth login: " + err.Error())
	}
	select {
	case <-l.done:
	case <-time.After(m.FinishTimeout):
		l.kill()
		return fail(fmt.Sprintf("claude auth login did not finish within %s", m.FinishTimeout))
	}
	out := l.output()
	if l.exitErr != nil && !strings.Contains(out, "Login successful") {
		return fail("claude auth login failed (" + l.exitErr.Error() + ")")
	}
	m.setState(StateSignedIn, "Login successful.", nil)
	if m.OnSignedIn != nil {
		go m.OnSignedIn()
	}
	return Result{State: StateSignedIn, Message: "Login successful."}, nil
}

func (m *Manager) setState(state, msg string, cur *login) {
	m.mu.Lock()
	m.state, m.message, m.cur = state, msg, cur
	m.mu.Unlock()
}

// Status reports the login state plus what `claude auth status --json`
// says (best effort: any failure → loggedIn false).
func (m *Manager) Status(ctx context.Context) (Status, error) {
	m.mu.Lock()
	st := Status{State: m.state, Message: m.message}
	if m.cur != nil && m.state == StateAwaitingCode {
		st.URL = m.cur.url
	}
	m.mu.Unlock()

	bin, err := m.FindCLI()
	if err != nil {
		return st, err
	}
	ctx, cancel := context.WithTimeout(ctx, m.StatusTimeout)
	defer cancel()
	out, err := m.Runner.Output(ctx, bin, "auth", "status", "--json")
	// A logged-out CLI may exit non-zero but still print JSON - parse anyway.
	st.LoggedIn, st.Email = ParseAuthStatus(out)
	if err != nil && !st.LoggedIn {
		log.Printf("auth: claude auth status: %v", err)
	}
	return st, nil
}

// ParseAuthStatus reads `claude auth status --json` defensively: loggedIn
// must be literally true; email is the first "email" string found anywhere.
func ParseAuthStatus(out []byte) (loggedIn bool, email string) {
	s := string(out)
	if i := strings.IndexByte(s, '{'); i >= 0 {
		s = s[i:]
	}
	var v map[string]any
	if json.Unmarshal([]byte(s), &v) != nil {
		return false, ""
	}
	loggedIn, _ = v["loggedIn"].(bool)
	return loggedIn, findEmail(v)
}

func findEmail(v any) string {
	switch t := v.(type) {
	case map[string]any:
		if e, ok := t["email"].(string); ok && e != "" {
			return e
		}
		for _, c := range t {
			if e := findEmail(c); e != "" {
				return e
			}
		}
	case []any:
		for _, c := range t {
			if e := findEmail(c); e != "" {
				return e
			}
		}
	}
	return ""
}

var (
	ansiRe  = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[ -/]*[@-~]`)
	loginRe = regexp.MustCompile(`https://[^\s"'<>]*oauth/authorize[^\s"'<>]*`)
)

// ParseLoginURL returns the first https URL containing "oauth/authorize" in
// the CLI output (ANSI escapes stripped), or "".
func ParseLoginURL(out string) string {
	return loginRe.FindString(ansiRe.ReplaceAllString(out, ""))
}

// withTail appends the last ~5 non-empty output lines to msg.
func withTail(msg, out string) string {
	out = ansiRe.ReplaceAllString(out, "")
	var lines []string
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", "\n"), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	if len(lines) == 0 {
		return msg
	}
	return msg + ": " + strings.Join(lines, " | ")
}

// FindCLI resolves the claude binary: $RELAY_CLAUDE_CLI, else PATH
// ("claude", plus claude.cmd/claude.exe on Windows).
func FindCLI() (string, error) {
	if p := strings.TrimSpace(os.Getenv("RELAY_CLAUDE_CLI")); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%w: RELAY_CLAUDE_CLI=%q: %v", ErrCLINotFound, p, err)
		}
		return p, nil
	}
	names := []string{"claude"}
	if runtime.GOOS == "windows" {
		names = append(names, "claude.cmd", "claude.exe")
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", ErrCLINotFound
}

// execRunner is the real Runner.
type execRunner struct{}

type execProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *io.PipeReader
	done  chan struct{}
	err   error
}

func (execRunner) Start(name string, args ...string) (Proc, error) {
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	// A killed claude.cmd can leave its node child holding the output pipe
	// open; don't let Wait hang on it forever.
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &execProc{cmd: cmd, stdin: stdin, out: pr, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		pw.Close()
		close(p.done)
	}()
	return p, nil
}

func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	return cmd.Output()
}

func (p *execProc) Stdin() io.Writer  { return p.stdin }
func (p *execProc) Output() io.Reader { return p.out }
func (p *execProc) Wait() error       { <-p.done; return p.err }
func (p *execProc) Kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	return p.cmd.Process.Kill()
}
