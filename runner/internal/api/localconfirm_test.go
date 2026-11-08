package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"relay/runner/internal/collectors"
	"relay/runner/internal/uplink"
)

func confirmServer(t *testing.T) (*Server, http.Handler, *int, *[]string) {
	s := newTestServer(t)
	e := withCollectors(t, s, apiSandbox{})
	s.Packages = collectors.NewPackages(e.DataDir, s.Sensors)
	s.Packages.Sync = true
	installs, declined := 0, []string{}
	s.Sensors.Install = func(ctx context.Context, dir string) error { installs++; return nil }
	s.Packages.Managers = map[string]collectors.Manager{"relay": nil}
	s.Packages.Managers["relay"] = stubMgr{&installs}
	s.Packages.Confirm = collectors.NewConfirms()
	s.Packages.OnDeclined = func(id string) { declined = append(declined, id) }
	return s, s.Routes(), &installs, &declined
}

type stubMgr struct{ n *int }

func (stubMgr) Present(context.Context, string) bool { return false }
func (m stubMgr) Install(context.Context, string) error {
	*m.n++
	return nil
}
func (stubMgr) Uninstall(context.Context, string) error { return nil }

func localReq(method, path, body string, mut func(*http.Request)) *http.Request {
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	r.RemoteAddr = "127.0.0.1:5555"
	r.Host = "localhost:7421"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if mut != nil {
		mut(r)
	}
	return r
}

func TestLocalConfirmRefusals(t *testing.T) {
	_, h, _, _ := confirmServer(t)
	for name, mut := range map[string]func(*http.Request){
		"uplink":       func(r *http.Request) { *r = *r.WithContext(uplink.WithUplink(r.Context())) },
		"non-loopback": func(r *http.Request) { r.RemoteAddr = "100.64.0.5:4000" },
		"hub":          func(r *http.Request) { r.RemoteAddr = "relay-hub:0" },
		"forwarded":    func(r *http.Request) { r.Header.Set("X-Forwarded-For", "1.2.3.4") },
		"bad host":     func(r *http.Request) { r.Host = "evil.example" },
	} {
		for _, req := range []*http.Request{
			localReq("GET", "/local/confirm", "", mut),
			localReq("POST", "/local/confirm/abc", `{"decision":"allow"}`, mut),
		} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, req.Method, req.URL.Path, rec.Code)
			}
		}
	}
}

func TestLocalConfirmAllowDeny(t *testing.T) {
	s, h, installs, declined := confirmServer(t)
	body := map[string]string{"contractHash": ""}
	_ = body
	reqs := []collectors.Require{{Manager: "relay", ID: "activitywatch", Why: "w"}}
	if _, err := s.Packages.Require("kpi1", reqs); err == nil {
		t.Fatal("want awaiting")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, localReq("GET", "/local/confirm", "", nil))
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("kpi1")) || !bytes.Contains(rec.Body.Bytes(), []byte("activitywatch")) {
		t.Fatalf("page = %d %s", rec.Code, rec.Body)
	}
	tok := s.Packages.Confirm.List()[0].Token
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, localReq("POST", "/local/confirm/"+tok, `{"decision":"maybe"}`, nil))
	if rec.Code != 400 {
		t.Fatalf("bad decision = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, localReq("POST", "/local/confirm/"+tok, `{"decision":"allow"}`, nil))
	if rec.Code != 200 || *installs != 1 || s.Packages.RequiresState(reqs) != "ready" {
		t.Fatalf("allow = %d installs=%d state=%s", rec.Code, *installs, s.Packages.RequiresState(reqs))
	}
	// deny path
	s.Packages.Release("kpi1")
	_, _ = s.Packages.Require("kpi2", []collectors.Require{{Manager: "relay", ID: "activitywatch", Why: "w"}})
	s.Packages.Release("kpi1")
	// kpi2 requires an already-installed pkg → no prompt; use a fresh ledger instead.
	s2, h2, _, declined2 := confirmServer(t)
	_, _ = s2.Packages.Require("kpi3", reqs)
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, localReq("POST", "/local/confirm/"+s2.Packages.Confirm.List()[0].Token, `{"decision":"deny"}`, nil))
	if rec.Code != 200 || len(*declined2) != 1 || (*declined2)[0] != "kpi3" || s2.Packages.RequiresState(reqs) != "awaiting_confirm" {
		t.Fatalf("deny = %d declined=%v", rec.Code, *declined2)
	}
	_ = declined
}

func TestLocalURLAndListener(t *testing.T) {
	for in, want := range map[string]string{"100.64.1.2:7777": "http://127.0.0.1:7777/local/confirm", "127.0.0.1:7777": "http://127.0.0.1:7777/local/confirm", "0.0.0.0:9": "http://127.0.0.1:9/local/confirm", ":9": "http://127.0.0.1:9/local/confirm"} {
		if got := LocalConfirmURL(in); got != want {
			t.Errorf("url(%s) = %s", in, got)
		}
	}
	for in, want := range map[string]string{"100.64.1.2:7777": "127.0.0.1:7777", "127.0.0.1:7777": "", "[::1]:7777": "", "0.0.0.0:7777": "", "[::]:7777": "", ":7777": ""} {
		if got := LocalListenAddr(in); got != want {
			t.Errorf("listen(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestLocalRoutesOnlyLocal(t *testing.T) {
	_, _, _, _ = confirmServer(t)
	s, _, _, _ := confirmServer(t)
	h := s.LocalRoutes()
	for _, p := range []string{"/v1/health", "/v1/packages", "/v1/runner/info"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, localReq("GET", p, "", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, localReq("GET", "/local/confirm", "", nil))
	if rec.Code != 200 {
		t.Errorf("/local/confirm = %d", rec.Code)
	}
}
