package auth

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testURL = "https://claude.com/cai/oauth/authorize?code=true&client_id=x&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&state=abc"

func TestParseLoginURL(t *testing.T) {
	cases := map[string]string{
		"Opening browser to sign in…\nIf the browser didn't open, visit: " + testURL + "\nPaste code here if prompted > ": testURL,
		"visit: \x1b[4m" + testURL + "\x1b[0m\n":                              testURL,
		"\x1b]8;;" + testURL + "\x07link\x1b]8;;\x07 then " + testURL + " ":   testURL,
		"see https://example.com/docs and https://x.test/oauth/authorize?a=1": "https://x.test/oauth/authorize?a=1",
		"no url here":                         "",
		"http://insecure/oauth/authorize?a=1": "",
	}
	for in, want := range cases {
		if got := ParseLoginURL(in); got != want {
			t.Errorf("ParseLoginURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseAuthStatus(t *testing.T) {
	in, email := ParseAuthStatus([]byte(`{"loggedIn":true,"authMethod":"claude.ai","email":"a@b.c"}`))
	if !in || email != "a@b.c" {
		t.Errorf("got %v %q", in, email)
	}
	in, email = ParseAuthStatus([]byte(`{"loggedIn":false}`))
	if in || email != "" {
		t.Errorf("logged out: got %v %q", in, email)
	}
	in, _ = ParseAuthStatus([]byte(`garbage`))
	if in {
		t.Error("garbage parsed as logged in")
	}
	in, email = ParseAuthStatus([]byte(`warning\n{"loggedIn":"yes","account":{"email":"n@e.st"}}`))
	if in || email != "n@e.st" {
		t.Errorf("defensive parse: got %v %q", in, email)
	}
}

// fakeProc is a scripted `claude auth login`.
type fakeProc struct {
	stdinR *io.PipeReader
	stdinW *io.PipeWriter
	outR   *io.PipeReader
	outW   *io.PipeWriter
	done   chan struct{}
	once   sync.Once
	err    error
	killed atomic.Bool
}

func (p *fakeProc) Stdin() io.Writer  { return p.stdinW }
func (p *fakeProc) Output() io.Reader { return p.outR }
func (p *fakeProc) Wait() error       { <-p.done; return p.err }
func (p *fakeProc) exit(err error) {
	p.once.Do(func() {
		p.err = err
		p.outW.Close()
		p.stdinR.Close()
		close(p.done)
	})
}
func (p *fakeProc) Kill() error {
	p.killed.Store(true)
	p.exit(errors.New("killed"))
	return nil
}

// fakeRunner behaves like the CLI: prints the URL (unless silent), then
// "Login successful." + exit 0 for code "good", an error + exit 1 otherwise.
type fakeRunner struct {
	silent bool
	starts atomic.Int32
	mu     sync.Mutex
	procs  []*fakeProc
	status string
}

func (r *fakeRunner) Start(name string, args ...string) (Proc, error) {
	r.starts.Add(1)
	p := &fakeProc{done: make(chan struct{})}
	p.stdinR, p.stdinW = io.Pipe()
	p.outR, p.outW = io.Pipe()
	r.mu.Lock()
	r.procs = append(r.procs, p)
	r.mu.Unlock()
	go func() {
		if !r.silent {
			io.WriteString(p.outW, "Opening browser to sign in…\nIf the browser didn't open, visit: "+testURL+"\nPaste code here if prompted > ")
		}
		line, err := bufio.NewReader(p.stdinR).ReadString('\n')
		if err != nil {
			return // killed
		}
		if strings.TrimSpace(line) == "good" {
			io.WriteString(p.outW, "\nLogin successful.\n")
			p.exit(nil)
			return
		}
		io.WriteString(p.outW, "\nOAuth error: invalid code\nline2\n")
		p.exit(errors.New("exit status 1"))
	}()
	return p, nil
}

func (r *fakeRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return []byte(r.status), nil
}

func newTestManager(r *fakeRunner) *Manager {
	m := NewManager(Claude())
	m.Runner = r
	m.FindCLI = func() (string, error) { return "claude", nil }
	m.URLTimeout = 300 * time.Millisecond
	return m
}

func TestStartFinishSuccess(t *testing.T) {
	r := &fakeRunner{status: `{"loggedIn":true,"email":"me@x.y"}`}
	m := newTestManager(r)
	signedIn := make(chan struct{}, 1)
	m.OnSignedIn = func() { signedIn <- struct{}{} }

	res, err := m.Start()
	if err != nil || res.State != StateAwaitingCode || res.URL != testURL {
		t.Fatalf("Start = %+v, %v", res, err)
	}
	again, _ := m.Start()
	if again.URL != testURL || r.starts.Load() != 1 {
		t.Errorf("second Start spawned again (starts=%d) or changed URL", r.starts.Load())
	}
	st, _ := m.Status(context.Background())
	if st.State != StateAwaitingCode || st.URL != testURL {
		t.Errorf("Status while awaiting = %+v", st)
	}

	res, err = m.Finish("  good \n")
	if err != nil || res.State != StateSignedIn {
		t.Fatalf("Finish = %+v, %v", res, err)
	}
	select {
	case <-signedIn:
	case <-time.After(time.Second):
		t.Error("OnSignedIn not called")
	}
	st, _ = m.Status(context.Background())
	if st.State != StateSignedIn || st.URL != "" || !st.LoggedIn || st.Email != "me@x.y" {
		t.Errorf("Status after login = %+v", st)
	}
	if _, err := m.Finish("good"); !errors.Is(err, ErrNoLogin) {
		t.Errorf("Finish after completion: %v, want ErrNoLogin", err)
	}
}

func TestFinishFailure(t *testing.T) {
	r := &fakeRunner{}
	m := newTestManager(r)
	called := false
	m.OnSignedIn = func() { called = true }
	if _, err := m.Start(); err != nil {
		t.Fatal(err)
	}
	res, err := m.Finish("bad")
	if err != nil || res.State != StateFailed {
		t.Fatalf("Finish = %+v, %v", res, err)
	}
	if !strings.Contains(res.Message, "invalid code") {
		t.Errorf("message %q lacks the CLI's last lines", res.Message)
	}
	st, _ := m.Status(context.Background())
	if st.State != StateFailed || st.LoggedIn {
		t.Errorf("Status = %+v", st)
	}
	time.Sleep(50 * time.Millisecond)
	if called {
		t.Error("OnSignedIn called after a failure")
	}
	// A new Start spawns a fresh process.
	if _, err := m.Start(); err != nil || r.starts.Load() != 2 {
		t.Errorf("restart: err=%v starts=%d", err, r.starts.Load())
	}
}

func TestFinishErrors(t *testing.T) {
	m := newTestManager(&fakeRunner{})
	if _, err := m.Finish("   "); !errors.Is(err, ErrEmptyCode) {
		t.Errorf("empty code: %v", err)
	}
	if _, err := m.Finish("good"); !errors.Is(err, ErrNoLogin) {
		t.Errorf("no login: %v", err)
	}
	m.FindCLI = func() (string, error) { return "", ErrCLINotFound }
	if _, err := m.Start(); !errors.Is(err, ErrCLINotFound) {
		t.Errorf("missing CLI: %v", err)
	}
}

func TestNoURLTimesOut(t *testing.T) {
	r := &fakeRunner{silent: true}
	m := newTestManager(r)
	res, err := m.Start()
	if err != nil || res.State != StateFailed {
		t.Fatalf("Start = %+v, %v; want failed", res, err)
	}
	if !r.procs[0].killed.Load() {
		t.Error("process not killed after URL timeout")
	}
}

func TestCodeTimeoutKillsProcess(t *testing.T) {
	r := &fakeRunner{}
	m := newTestManager(r)
	m.CodeTimeout = 100 * time.Millisecond
	if _, err := m.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !r.procs[0].killed.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !r.procs[0].killed.Load() {
		t.Fatal("process not killed after CodeTimeout")
	}
	st, _ := m.Status(context.Background())
	if st.State != StateFailed || st.URL != "" || !strings.Contains(st.Message, "timed out") {
		t.Errorf("Status after expiry = %+v", st)
	}
	if _, err := m.Finish("good"); !errors.Is(err, ErrNoLogin) {
		t.Errorf("Finish after expiry: %v, want ErrNoLogin", err)
	}
}
