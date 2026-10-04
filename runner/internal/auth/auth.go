// Package auth is the sign-in relay: it runs a CLI's login on the runner and
// hands the browser part to the phone, so an expired or missing login
// (Claude, GitHub, Google Cloud, ...) never needs someone at the PC (see
// runner/FLOWS.md "Sign-in relay"). Each CLI is a Provider recipe
// (providers.go); one Manager per provider.
//
//	Start:  spawn the login (stdin = pipe) → parse URL (+ device code) from
//	        its output → awaiting_code (paste-code recipes) or
//	        awaiting_approval (device flow); process kept alive, killed after
//	        CodeTimeout
//	Finish: (paste-code only) write code+"\n" to stdin → wait for exit
//	Device flow: the CLI exits by itself once the user approved → watched in
//	        the background (awaitApproval)
//	Either way the exited CLI → complete: AfterLogin commands → signed_in +
//	        OnSignedIn | failed
//	Status: current state + the provider's status command (loggedIn, account)
//
// Only one login per provider runs at a time. Everything external goes
// through Runner so tests never spawn the real CLI.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// States reported by Status / Start / Finish.
const (
	StateIdle         = "idle"          // no login attempted since the runner started
	StateAwaitingCode = "awaiting_code" // URL handed out, waiting for the code from the phone
	// Device flow: URL + UserCode handed out, waiting for the user to approve
	// in the browser; the phone polls Status until signed_in/failed.
	StateAwaitingApproval = "awaiting_approval"
	StateSignedIn         = "signed_in" // last login attempt succeeded
	StateFailed           = "failed"    // last login attempt failed / timed out (see Message)
)

var (
	ErrCLINotFound     = errors.New("CLI not found on this runner")
	ErrNoLogin         = errors.New("no login in progress - start one first")
	ErrEmptyCode       = errors.New("code is required")
	ErrCodeNotNeeded   = errors.New("this sign-in finishes in the browser - there is no code to send")
	ErrUnknownProvider = errors.New("unknown sign-in provider")
)

// Proc is a running login CLI.
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
// the CLIs.
type Runner interface {
	// Start launches name with args, stdin as a pipe.
	Start(name string, args ...string) (Proc, error)
	// Output runs name to completion and returns its combined stdout+stderr
	// (gh prints its status to stderr).
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Status is the GET /v1/auth/{provider} response.
type Status struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	State    string `json:"state"`
	URL      string `json:"url,omitempty"`
	UserCode string `json:"userCode,omitempty"`
	Message  string `json:"message,omitempty"`
	LoggedIn bool   `json:"loggedIn"`
	// Account is the signed-in email/username. Email repeats it for claude
	// only (the Claude sheet predates Account).
	Account string `json:"account,omitempty"`
	Email   string `json:"email,omitempty"`
	// CLIFound is false when the provider's CLI isn't installed (GET /v1/auth
	// lists every provider; the single-provider GET answers 503 instead).
	CLIFound bool `json:"cliFound"`
}

// Result is what Start/Finish return.
type Result struct {
	State    string `json:"state"`
	URL      string `json:"url,omitempty"`
	UserCode string `json:"userCode,omitempty"`
	Message  string `json:"message,omitempty"`
}

// Manager runs at most one login for its Provider at a time.
type Manager struct {
	Provider Provider
	Runner   Runner
	// FindCLI resolves the binary; ErrCLINotFound if absent.
	FindCLI func() (string, error)
	// OnSignedIn, if set, runs (in its own goroutine) after a successful
	// login - claude: wired to session.Manager.RestartIdleAgents in runnerd.
	OnSignedIn func()

	URLTimeout    time.Duration // waiting for the login URL to be printed
	CodeTimeout   time.Duration // awaiting_* lifetime before the process is killed
	FinishTimeout time.Duration // waiting for exit after the code was written
	StatusTimeout time.Duration // the status command, and each AfterLogin command

	opMu sync.Mutex // serializes Start/Finish/expire/awaitApproval

	mu      sync.Mutex
	state   string
	message string
	cur     *login
}

// NewManager returns a Manager for p using the real CLI.
func NewManager(p Provider) *Manager {
	return &Manager{
		Provider:      p,
		Runner:        execRunner{},
		FindCLI:       p.findCLI,
		URLTimeout:    20 * time.Second,
		CodeTimeout:   10 * time.Minute,
		FinishTimeout: 60 * time.Second,
		StatusTimeout: 10 * time.Second,
		state:         StateIdle,
	}
}

// NewManagers returns one Manager per known provider, in Providers() order.
func NewManagers() []*Manager {
	var ms []*Manager
	for _, p := range Providers() {
		ms = append(ms, NewManager(p))
	}
	return ms
}

// login is one login CLI process.
type login struct {
	proc     Proc
	url      string
	userCode string
	started  time.Time
	timer    *time.Timer

	outMu sync.Mutex
	out   strings.Builder

	urlCh   chan [2]string // receives URL + device code once both appear in the output
	done    chan struct{}  // closed after output EOF + Wait
	exitErr error          // valid after done
}

func (l *login) output() string {
	l.outMu.Lock()
	defer l.outMu.Unlock()
	return l.out.String()
}

// watch drains the output (looking for the URL + code) and reaps the
// process.
func (l *login) watch(p Provider) {
	buf := make([]byte, 4096)
	sent := false
	r := l.proc.Output()
	for {
		n, err := r.Read(buf)
		if n > 0 {
			l.outMu.Lock()
			l.out.Write(buf[:n])
			all := l.out.String()
			l.outMu.Unlock()
			if !sent {
				if u, c, ok := p.parse(all); ok {
					sent = true
					l.urlCh <- [2]string{u, c}
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
		log.Printf("auth: login CLI did not exit 10s after kill")
	}
}

// waitState is the state a started login waits in.
func (p Provider) waitState() string {
	if p.PasteCode {
		return StateAwaitingCode
	}
	return StateAwaitingApproval
}

// parse finds the URL (and, for device flow, the code) in the output so
// far; ok once everything the recipe needs is there.
func (p Provider) parse(out string) (url, code string, ok bool) {
	out = ansiRe.ReplaceAllString(out, "")
	if url = p.URLRe.FindString(out); url == "" {
		return "", "", false
	}
	if p.CodeRe != nil {
		m := p.CodeRe.FindStringSubmatch(out)
		if m == nil {
			return "", "", false
		}
		code = m[1]
	}
	return url, code, true
}

// cmdName is "claude auth login" etc., for messages.
func (p Provider) cmdName() string {
	return strings.Join(append([]string{p.CLINames[0]}, p.LoginArgs[:min(2, len(p.LoginArgs))]...), " ")
}

// Start begins a login, or returns the one already waiting if it is younger
// than CodeTimeout.
func (m *Manager) Start() (Result, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	p := m.Provider

	m.mu.Lock()
	if l := m.cur; l != nil {
		if m.state == p.waitState() && time.Since(l.started) < m.CodeTimeout {
			m.mu.Unlock()
			return Result{State: m.state, URL: l.url, UserCode: l.userCode}, nil
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
	proc, err := m.Runner.Start(bin, p.LoginArgs...)
	if err != nil {
		return Result{}, fmt.Errorf("start %s: %w", p.cmdName(), err)
	}
	l := &login{proc: proc, started: time.Now(), urlCh: make(chan [2]string, 1), done: make(chan struct{})}
	go l.watch(p)

	fail := func(msg string) (Result, error) {
		log.Printf("auth: %s; full %s output:\n%s", msg, p.ID, l.output())
		msg = withTail(msg, l.output())
		m.setState(StateFailed, msg, nil)
		return Result{State: StateFailed, Message: msg}, nil
	}

	select {
	case uc := <-l.urlCh:
		l.url, l.userCode = uc[0], uc[1]
	case <-l.done:
		if _, _, ok := p.parse(l.output()); !ok {
			return fail(p.cmdName() + " exited without printing a login URL")
		}
		return fail(p.cmdName() + " exited before the sign-in could be finished")
	case <-time.After(m.URLTimeout):
		l.kill()
		return fail(fmt.Sprintf("%s printed no login URL within %s", p.cmdName(), m.URLTimeout))
	}

	l.timer = time.AfterFunc(m.CodeTimeout, func() { m.expire(l) })
	m.setState(p.waitState(), "", l)
	if !p.PasteCode {
		go m.awaitApproval(l)
	}
	return Result{State: p.waitState(), URL: l.url, UserCode: l.userCode}, nil
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
	m.message = fmt.Sprintf("login timed out: not finished within %s", m.CodeTimeout)
	m.mu.Unlock()
	l.kill()
}

// awaitApproval finishes a device-flow login once its CLI exits by itself
// (the user approved, or the device code expired).
func (m *Manager) awaitApproval(l *login) {
	<-l.done
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	if m.cur != l { // expired, or replaced by a new Start
		m.mu.Unlock()
		return
	}
	m.cur = nil
	m.mu.Unlock()
	l.timer.Stop()
	m.complete(l)
}

// Finish feeds the code from the callback page to the waiting login
// (paste-code recipes only).
func (m *Manager) Finish(code string) (Result, error) {
	if !m.Provider.PasteCode {
		return Result{}, ErrCodeNotNeeded
	}
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

	name := m.Provider.cmdName()
	if _, err := io.WriteString(l.proc.Stdin(), code+"\n"); err != nil {
		l.kill()
		return m.fail(l, "could not send the code to "+name+": "+err.Error())
	}
	select {
	case <-l.done:
	case <-time.After(m.FinishTimeout):
		l.kill()
		return m.fail(l, fmt.Sprintf("%s did not finish within %s", name, m.FinishTimeout))
	}
	return m.complete(l)
}

// complete judges an exited login: success → AfterLogin commands →
// signed_in + OnSignedIn; otherwise failed. Caller holds opMu.
func (m *Manager) complete(l *login) (Result, error) {
	p := m.Provider
	if l.exitErr != nil && (p.SuccessText == "" || !strings.Contains(l.output(), p.SuccessText)) {
		return m.fail(l, p.cmdName()+" failed ("+l.exitErr.Error()+")")
	}
	if len(p.AfterLogin) > 0 {
		bin, err := m.FindCLI()
		if err != nil {
			return m.fail(l, err.Error())
		}
		for _, args := range p.AfterLogin {
			ctx, cancel := context.WithTimeout(context.Background(), m.StatusTimeout)
			out, err := m.Runner.Output(ctx, bin, args...)
			cancel()
			if err != nil {
				msg := fmt.Sprintf("signed in, but `%s %s` failed (%v)", p.CLINames[0], strings.Join(args, " "), err)
				log.Printf("auth: %s; output:\n%s", msg, out)
				msg = withTail(msg, string(out))
				m.setState(StateFailed, msg, nil)
				return Result{State: StateFailed, Message: msg}, nil
			}
		}
	}
	m.setState(StateSignedIn, "Login successful.", nil)
	if m.OnSignedIn != nil {
		go m.OnSignedIn()
	}
	return Result{State: StateSignedIn, Message: "Login successful."}, nil
}

// fail records a failed attempt, message = msg + the CLI's last lines.
func (m *Manager) fail(l *login, msg string) (Result, error) {
	out := l.output()
	log.Printf("auth: %s; full %s output:\n%s", msg, m.Provider.ID, out)
	msg = withTail(msg, out)
	m.setState(StateFailed, msg, nil)
	return Result{State: StateFailed, Message: msg}, nil
}

func (m *Manager) setState(state, msg string, cur *login) {
	m.mu.Lock()
	m.state, m.message, m.cur = state, msg, cur
	m.mu.Unlock()
}

// Status reports the login state plus what the provider's status command
// says (best effort: any failure → loggedIn false).
func (m *Manager) Status(ctx context.Context) (Status, error) {
	p := m.Provider
	m.mu.Lock()
	st := Status{Provider: p.ID, Name: p.Name, State: m.state, Message: m.message}
	if m.cur != nil && m.state == p.waitState() {
		st.URL, st.UserCode = m.cur.url, m.cur.userCode
	}
	m.mu.Unlock()

	bin, err := m.FindCLI()
	if err != nil {
		return st, err
	}
	st.CLIFound = true
	ctx, cancel := context.WithTimeout(ctx, m.StatusTimeout)
	defer cancel()
	out, err := m.Runner.Output(ctx, bin, p.StatusArgs...)
	// A logged-out CLI may exit non-zero but still print its status - parse anyway.
	st.LoggedIn, st.Account = p.ParseStatus(out, err)
	if p.ID == "claude" {
		st.Email = st.Account
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
	// Decoder, not Unmarshal: stderr may follow the JSON.
	if json.NewDecoder(strings.NewReader(s)).Decode(&v) != nil {
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
// the CLI output (ANSI escapes stripped), or "" - the claude recipe's URL.
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
	// A killed claude.cmd / gcloud.cmd can leave its child holding the
	// output pipe open; don't let Wait hang on it forever.
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
	return cmd.CombinedOutput()
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
