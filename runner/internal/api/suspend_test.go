package api

import (
	"net/http"
	"testing"
)

func TestSuspend_NotConfiguredIs503(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), "POST", "/v1/suspend", testKey, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", rec.Code, rec.Body.String())
	}
}

func TestSuspend_CallsSuspendFuncAndReturns202(t *testing.T) {
	s := newTestServer(t)
	called := false
	s.Suspend = func() error {
		called = true
		return nil
	}

	rec := doRequest(t, s.Routes(), "POST", "/v1/suspend", testKey, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatalf("Suspend func was not called")
	}
}

func TestSuspend_RefusedWhileSessionBusy(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()
	s.Suspend = func() error {
		t.Fatalf("Suspend should not be called while a session is busy")
		return nil
	}

	p := createProject(t, h, "Suspend Project")
	rec := doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/sessions", testKey, map[string]string{"provider": "echo-agent"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("start session status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, "POST", "/v1/suspend", testKey, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}
}

func TestSuspend_RequiresAuth(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), "POST", "/v1/suspend", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
