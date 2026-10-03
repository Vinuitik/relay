package api

import (
	"net/http"
	"testing"

	"relay/runner/internal/session"
)

func TestListAllSessions_AcrossProjectsWithFilter(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()
	for _, name := range []string{"alpha", "beta"} {
		if rec := doRequest(t, h, "POST", "/v1/projects", testKey, map[string]string{"name": name}); rec.Code != http.StatusCreated {
			t.Fatalf("create project %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	a, err := s.Sessions.Start("alpha", "echo-agent")
	if err != nil {
		t.Fatalf("Start alpha: %v", err)
	}
	b, err := s.Sessions.Start("beta", "echo-agent")
	if err != nil {
		t.Fatalf("Start beta: %v", err)
	}
	if _, err := s.Sessions.Stop(b.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	defer s.Sessions.Stop(a.ID)

	rec := doRequest(t, h, "GET", "/v1/sessions", testKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var all []session.Session
	decodeBody(t, rec, &all)
	if len(all) != 2 || all[0].ID != a.ID {
		t.Fatalf("all = %+v, want 2 sessions with busy %s first", all, a.ID)
	}

	rec = doRequest(t, h, "GET", "/v1/sessions?state=waiting,busy", testKey, nil)
	var filtered []session.Session
	decodeBody(t, rec, &filtered)
	if len(filtered) != 1 || filtered[0].ID != a.ID {
		t.Fatalf("filtered = %+v, want only %s", filtered, a.ID)
	}

	if rec := doRequest(t, h, "GET", "/v1/sessions", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no key: status = %d, want 401", rec.Code)
	}
}
