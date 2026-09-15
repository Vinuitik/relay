package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"relay/runner/internal/notify"
	"relay/runner/internal/project"
	"relay/runner/internal/session"
	"relay/runner/internal/uptime"
)

const testKey = "test-key-123"

func newTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	projects, err := project.NewRegistry(filepath.Join(root, "projects"), filepath.Join(root, "projects.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	sessions := session.NewManager(projects.Dir)
	devices := notify.NewRegistry()
	return NewServer(testKey, projects, sessions, ComposeFuncs{
		Start: func(string) error { return nil },
		Stop:  func(string) error { return nil },
	}, devices, &fakeSender{}, &fakeUptimeStore{})
}

// fakeUptimeStore is a no-op UptimeStore for tests that don't care about
// uptime reporting specifically.
type fakeUptimeStore struct {
	intervals []uptime.Interval
}

func (f *fakeUptimeStore) List() []uptime.Interval {
	return f.intervals
}

// fakeSender is a no-op wol.PacketSender for tests that don't care about
// wake behavior specifically (see wake_test.go for dedicated wake tests).
type fakeSender struct {
	sent [][]byte
	err  error
}

func (f *fakeSender) SendBroadcast(payload []byte) error {
	f.sent = append(f.sent, payload)
	return f.err
}

func doRequest(t *testing.T, h http.Handler, method, path, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if key != "" {
		req.Header.Set("X-Relay-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
}

func TestHealthRequiresNoAuth(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), "GET", "/v1/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]bool
	decodeBody(t, rec, &body)
	if !body["ok"] {
		t.Errorf("body = %v, want ok:true", body)
	}
}

func TestWrongKeyIs401(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), "GET", "/v1/projects", "wrong-key", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestMissingKeyIs401(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), "GET", "/v1/projects", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestCreateAndListProjects(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()

	rec := doRequest(t, h, "POST", "/v1/projects", testKey, map[string]string{"name": "Demo"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}
	var p project.Project
	decodeBody(t, rec, &p)
	if p.ID == "" || p.Name != "Demo" {
		t.Fatalf("unexpected project: %+v", p)
	}

	rec = doRequest(t, h, "GET", "/v1/projects", testKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	var list []project.Project
	decodeBody(t, rec, &list)
	if len(list) != 1 || list[0].ID != p.ID {
		t.Fatalf("list = %+v, want single project %+v", list, p)
	}
}

func TestRunnerInfo(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), "GET", "/v1/runner/info", testKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var info runnerInfo
	decodeBody(t, rec, &info)
	if info.Hostname == "" || info.Version != Version {
		t.Errorf("unexpected info: %+v", info)
	}
}

// createProject is a test helper used by session-flow tests below.
func createProject(t *testing.T, h http.Handler, name string) project.Project {
	t.Helper()
	rec := doRequest(t, h, "POST", "/v1/projects", testKey, map[string]string{"name": name})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var p project.Project
	decodeBody(t, rec, &p)
	return p
}

func TestSessionLifecycle(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()

	p := createProject(t, h, "Session Project")

	// Start a session with the trivial echo-agent provider (`sh -c cat`),
	// which just echoes stdin back on stdout - no real agent CLI needed.
	rec := doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/sessions", testKey, map[string]string{"provider": "echo-agent"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("start session status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}
	var sess session.Session
	decodeBody(t, rec, &sess)
	if sess.ID == "" || sess.ProjectID != p.ID || sess.Provider != "echo-agent" {
		t.Fatalf("unexpected session: %+v", sess)
	}
	if sess.State != session.StateBusy {
		t.Errorf("initial state = %q, want %q", sess.State, session.StateBusy)
	}

	// Send a message; the echo-agent should reflect it back into the
	// transcript as an agent message.
	rec = doRequest(t, h, "POST", "/v1/sessions/"+sess.ID+"/message", testKey, map[string]string{"text": "hello"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send message status = %d, want 202, body=%s", rec.Code, rec.Body.String())
	}

	var got session.Session
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec = doRequest(t, h, "GET", "/v1/sessions/"+sess.ID, testKey, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get session status = %d, want 200", rec.Code)
		}
		decodeBody(t, rec, &got)
		if len(got.Messages) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got.Messages) < 2 {
		t.Fatalf("transcript = %+v, want at least a user + echoed agent message", got.Messages)
	}
	if got.Messages[0].Role != "user" || got.Messages[0].Text != "hello" {
		t.Errorf("first message = %+v, want user/hello", got.Messages[0])
	}
	foundEcho := false
	for _, m := range got.Messages[1:] {
		if m.Role == "agent" && m.Text == "hello" {
			foundEcho = true
		}
	}
	if !foundEcho {
		t.Errorf("expected an echoed agent message, got %+v", got.Messages)
	}

	// Stop the session.
	rec = doRequest(t, h, "POST", "/v1/sessions/"+sess.ID+"/stop", testKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var stopped session.Session
	decodeBody(t, rec, &stopped)
	if stopped.State != session.StateFinished {
		t.Errorf("state after stop = %q, want %q", stopped.State, session.StateFinished)
	}

	// List sessions for the project should include this one.
	rec = doRequest(t, h, "GET", "/v1/projects/"+p.ID+"/sessions", testKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list sessions status = %d, want 200", rec.Code)
	}
	var list []session.Session
	decodeBody(t, rec, &list)
	if len(list) != 1 || list[0].ID != sess.ID {
		t.Fatalf("session list = %+v, want single session %+v", list, sess)
	}
}

func TestUnknownProjectIs404(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()

	rec := doRequest(t, h, "GET", "/v1/projects/does-not-exist/sessions", testKey, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	rec = doRequest(t, h, "POST", "/v1/projects/does-not-exist/sessions", testKey, map[string]string{"provider": "echo-agent"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("start session on unknown project status = %d, want 404", rec.Code)
	}
}

func TestUnknownSessionIs404(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()

	rec := doRequest(t, h, "GET", "/v1/sessions/does-not-exist", testKey, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	rec = doRequest(t, h, "POST", "/v1/sessions/does-not-exist/message", testKey, map[string]string{"text": "hi"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("message to unknown session status = %d, want 404", rec.Code)
	}

	rec = doRequest(t, h, "POST", "/v1/sessions/does-not-exist/stop", testKey, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("stop unknown session status = %d, want 404", rec.Code)
	}
}

func TestUnknownProviderIs400(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()
	p := createProject(t, h, "Bad Provider Project")

	rec := doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/sessions", testKey, map[string]string{"provider": "nope"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestContainersStartStop(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()
	p := createProject(t, h, "Compose Project")

	rec := doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/containers/start", testKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("containers/start status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/containers/stop", testKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("containers/stop status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}
