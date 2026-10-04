package auth

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// ghOutput is what `gh auth login --web` printed with no TTY (gh 2.95, 2026-10).
const ghOutput = "\n! First copy your one-time code: B139-6EAA\nOpen this URL to continue in your web browser: https://github.com/login/device\n"

// deviceRunner fakes a device-flow CLI: prints ghOutput, then exits with
// whatever arrives on exit. Output() records the commands it ran.
type deviceRunner struct {
	exit   chan error
	status string
	outErr error // returned by Output for AfterLogin commands

	mu   sync.Mutex
	ran  [][]string
	proc *fakeProc
}

func (r *deviceRunner) Start(name string, args ...string) (Proc, error) {
	p := &fakeProc{done: make(chan struct{})}
	p.stdinR, p.stdinW = io.Pipe()
	p.outR, p.outW = io.Pipe()
	r.mu.Lock()
	r.proc = p
	r.mu.Unlock()
	go func() {
		io.WriteString(p.outW, ghOutput)
		select {
		case err := <-r.exit:
			io.WriteString(p.outW, "✓ Authentication complete.\n")
			p.exit(err)
		case <-p.done: // killed
		}
	}()
	return p, nil
}

func (r *deviceRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.ran = append(r.ran, args)
	r.mu.Unlock()
	if args[0] == "auth" && args[1] == "status" {
		return []byte(r.status), nil
	}
	return []byte("setup-git output"), r.outErr
}

func newDeviceManager(r *deviceRunner) *Manager {
	m := NewManager(GitHub())
	m.Runner = r
	m.FindCLI = func() (string, error) { return "gh", nil }
	m.URLTimeout = 300 * time.Millisecond
	return m
}

func waitState(t *testing.T, m *Manager, want string) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, _ := m.Status(context.Background())
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %q (%s), want %q", st.State, st.Message, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDeviceFlowSuccess(t *testing.T) {
	r := &deviceRunner{exit: make(chan error, 1), status: "github.com\n  ✓ Logged in to github.com account Vinuitik (keyring)\n"}
	m := newDeviceManager(r)
	signedIn := make(chan struct{}, 1)
	m.OnSignedIn = func() { signedIn <- struct{}{} }

	res, err := m.Start()
	if err != nil || res.State != StateAwaitingApproval || res.URL != "https://github.com/login/device" || res.UserCode != "B139-6EAA" {
		t.Fatalf("Start = %+v, %v", res, err)
	}
	if _, err := m.Finish("x"); !errors.Is(err, ErrCodeNotNeeded) {
		t.Errorf("Finish on device flow: %v", err)
	}
	st, _ := m.Status(context.Background())
	if st.UserCode != "B139-6EAA" || st.URL == "" {
		t.Errorf("Status while awaiting = %+v", st)
	}

	r.exit <- nil // user approved in the browser
	st = waitState(t, m, StateSignedIn)
	if !st.LoggedIn || st.Account != "Vinuitik" || st.Email != "" || st.UserCode != "" {
		t.Errorf("Status after login = %+v", st)
	}
	select {
	case <-signedIn:
	case <-time.After(time.Second):
		t.Error("OnSignedIn not called")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ranSetup := false
	for _, args := range r.ran {
		ranSetup = ranSetup || strings.Join(args, " ") == "auth setup-git --hostname github.com"
	}
	if !ranSetup {
		t.Errorf("AfterLogin not run: %v", r.ran)
	}
}

func TestDeviceFlowCLIFails(t *testing.T) {
	r := &deviceRunner{exit: make(chan error, 1)}
	m := newDeviceManager(r)
	if _, err := m.Start(); err != nil {
		t.Fatal(err)
	}
	r.exit <- errors.New("exit status 1") // code expired / denied
	st := waitState(t, m, StateFailed)
	if !strings.Contains(st.Message, "gh auth login failed") {
		t.Errorf("message = %q", st.Message)
	}
}

func TestDeviceFlowAfterLoginFails(t *testing.T) {
	r := &deviceRunner{exit: make(chan error, 1), outErr: errors.New("exit status 1")}
	m := newDeviceManager(r)
	if _, err := m.Start(); err != nil {
		t.Fatal(err)
	}
	r.exit <- nil
	st := waitState(t, m, StateFailed)
	if !strings.Contains(st.Message, "setup-git") {
		t.Errorf("message = %q", st.Message)
	}
}

func TestDeviceFlowExpiryKills(t *testing.T) {
	r := &deviceRunner{exit: make(chan error, 1)}
	m := newDeviceManager(r)
	m.CodeTimeout = 100 * time.Millisecond
	if _, err := m.Start(); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, m, StateFailed)
	if !strings.Contains(st.Message, "timed out") || !r.proc.killed.Load() {
		t.Errorf("after expiry: %+v killed=%v", st, r.proc.killed.Load())
	}
}

func TestParseGHStatus(t *testing.T) {
	ok, acct := ParseGHStatus([]byte("github.com\n  ✓ Logged in to github.com account Vinuitik (keyring)\n"), nil)
	if !ok || acct != "Vinuitik" {
		t.Errorf("new format: %v %q", ok, acct)
	}
	ok, acct = ParseGHStatus([]byte("github.com\n  ✓ Logged in to github.com as octo (oauth_token)\n"), nil)
	if !ok || acct != "octo" {
		t.Errorf("old format: %v %q", ok, acct)
	}
	if ok, _ := ParseGHStatus([]byte("You are not logged into any GitHub hosts."), errors.New("exit status 1")); ok {
		t.Error("logged out parsed as logged in")
	}
}

func TestParseGcloudStatus(t *testing.T) {
	ok, acct := ParseGcloudStatus([]byte(`[{"account":"a@b.c","status":""},{"account":"me@x.y","status":"ACTIVE"}]`), nil)
	if !ok || acct != "me@x.y" {
		t.Errorf("active: %v %q", ok, acct)
	}
	if ok, _ := ParseGcloudStatus([]byte("[]\n"), nil); ok {
		t.Error("no accounts parsed as logged in")
	}
}

func TestGcloudParsesURL(t *testing.T) {
	out := "Go to the following link in your browser, and complete the sign-in prompts:\n\n    https://accounts.google.com/o/oauth2/auth?response_type=code&client_id=x\n\nOnce finished, enter the verification code provided in your browser: "
	u, c, ok := GoogleCloud().parse(out)
	if !ok || c != "" || u != "https://accounts.google.com/o/oauth2/auth?response_type=code&client_id=x" {
		t.Errorf("parse = %q %q %v", u, c, ok)
	}
	// Device flow needs both URL and code.
	if _, _, ok := GitHub().parse("Open this URL: https://github.com/login/device\n"); ok {
		t.Error("gh parse ok without a code")
	}
}
