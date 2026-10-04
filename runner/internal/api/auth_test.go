package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"testing"

	"relay/runner/internal/auth"
)

// stubProc prints a login URL, then exits 0 with "Login successful." on any code.
type stubProc struct {
	inR  *io.PipeReader
	inW  *io.PipeWriter
	outR *io.PipeReader
	outW *io.PipeWriter
	done chan struct{}
}

func (p *stubProc) Stdin() io.Writer  { return p.inW }
func (p *stubProc) Output() io.Reader { return p.outR }
func (p *stubProc) Wait() error       { <-p.done; return nil }
func (p *stubProc) Kill() error       { p.inR.Close(); return nil }

type stubRunner struct{}

func (stubRunner) Start(string, ...string) (auth.Proc, error) {
	p := &stubProc{done: make(chan struct{})}
	p.inR, p.inW = io.Pipe()
	p.outR, p.outW = io.Pipe()
	go func() {
		defer close(p.done)
		defer p.outW.Close()
		io.WriteString(p.outW, "visit: https://claude.com/cai/oauth/authorize?state=s\nPaste code here if prompted > ")
		if _, err := bufio.NewReader(p.inR).ReadString('\n'); err == nil {
			io.WriteString(p.outW, "Login successful.\n")
		}
	}()
	return p, nil
}

func (stubRunner) Output(context.Context, string, ...string) ([]byte, error) {
	return []byte(`{"loggedIn":true,"email":"me@x.y"}`), nil
}

func TestClaudeAuthEndpoints(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Routes()

	if rec := doRequest(t, h, http.MethodGet, "/v1/auth/claude", testKey, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unwired status = %d, want 503", rec.Code)
	}

	m := auth.NewManager(auth.Claude())
	m.Runner = stubRunner{}
	m.FindCLI = func() (string, error) { return "claude", nil }
	gh := auth.NewManager(auth.GitHub())
	gh.FindCLI = func() (string, error) { return "", auth.ErrCLINotFound }
	srv.Auth = []*auth.Manager{m, gh}

	if rec := doRequest(t, h, http.MethodGet, "/v1/auth/nope", testKey, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown provider = %d, want 404", rec.Code)
	}
	if rec := doRequest(t, h, http.MethodPost, "/v1/auth/github/finish", testKey, map[string]string{"code": "x"}); rec.Code != http.StatusBadRequest {
		t.Errorf("finish on a device-flow provider = %d, want 400", rec.Code)
	}
	var list []auth.Status
	decodeBody(t, doRequest(t, h, http.MethodGet, "/v1/auth", testKey, nil), &list)
	if len(list) != 2 || list[0].Provider != "claude" || !list[0].CLIFound || !list[0].LoggedIn || list[1].Provider != "github" || list[1].CLIFound {
		t.Errorf("list = %+v", list)
	}

	if rec := doRequest(t, h, http.MethodPost, "/v1/auth/claude/start", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key = %d, want 401", rec.Code)
	}
	if rec := doRequest(t, h, http.MethodPost, "/v1/auth/claude/finish", testKey, map[string]string{"code": "x"}); rec.Code != http.StatusConflict {
		t.Errorf("finish without start = %d, want 409", rec.Code)
	}
	if rec := doRequest(t, h, http.MethodPost, "/v1/auth/claude/finish", testKey, map[string]string{"code": " "}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty code = %d, want 400", rec.Code)
	}

	rec := doRequest(t, h, http.MethodPost, "/v1/auth/claude/start", testKey, nil)
	var start auth.Result
	decodeBody(t, rec, &start)
	if rec.Code != http.StatusOK || start.State != "awaiting_code" || start.URL != "https://claude.com/cai/oauth/authorize?state=s" {
		t.Fatalf("start = %d %+v", rec.Code, start)
	}
	var st auth.Status
	decodeBody(t, doRequest(t, h, http.MethodGet, "/v1/auth/claude", testKey, nil), &st)
	if st.State != "awaiting_code" || st.URL == "" || !st.LoggedIn || st.Email != "me@x.y" {
		t.Errorf("status = %+v", st)
	}

	rec = doRequest(t, h, http.MethodPost, "/v1/auth/claude/finish", testKey, map[string]string{"code": "abc"})
	var fin auth.Result
	decodeBody(t, rec, &fin)
	if rec.Code != http.StatusOK || fin.State != "signed_in" {
		t.Errorf("finish = %d %+v", rec.Code, fin)
	}

	m.FindCLI = func() (string, error) { return "", auth.ErrCLINotFound }
	if rec := doRequest(t, h, http.MethodPost, "/v1/auth/claude/start", testKey, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("missing CLI = %d, want 503", rec.Code)
	}
}
