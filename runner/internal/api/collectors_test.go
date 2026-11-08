package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relay/runner/internal/collectors"
	"relay/runner/internal/collectors/sandbox"
)

// apiSandbox answers {"value": <int in inputs/n.txt, or 7>}.
type apiSandbox struct{ unavailable error }

func (apiSandbox) Kind() string       { return "fake" }
func (f apiSandbox) Available() error { return f.unavailable }
func (apiSandbox) Run(_ context.Context, s sandbox.Spec) (sandbox.Result, error) {
	v := "7"
	if b, err := os.ReadFile(filepath.Join(s.InputsDir, "n.txt")); err == nil {
		v = strings.TrimSpace(string(b))
	}
	return sandbox.Result{Stdout: []byte(`{"value": ` + v + `, "unit": "min", "evidence": [{"label": "x", "value": 1}]}`)}, nil
}

func withCollectors(t *testing.T, s *Server, sb collectors.Sandbox) *collectors.Engine {
	aw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/0/buckets/" {
			fmt.Fprint(w, `{"aw-watcher-window_h": {}}`)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(aw.Close)
	e := collectors.NewEngine(t.TempDir())
	e.Sandbox, e.AWBase = sb, aw.URL
	e.Python = func() (string, error) { return "/usr/bin/python3", nil }
	s.Collectors, s.Sensors = e, collectors.NewSensors(e)
	return e
}

func TestCollectorsDisabled(t *testing.T) {
	s := newTestServer(t)
	for _, p := range []string{"/v1/collectors", "/v1/sensors"} {
		if rec := doRequest(t, s.Routes(), http.MethodGet, p, testKey, nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s = %d, want 503", p, rec.Code)
		}
	}
}

func TestCollectorsLifecycle(t *testing.T) {
	s := newTestServer(t)
	withCollectors(t, s, apiSandbox{})
	h := s.Routes()
	manifest := json.RawMessage(`{"schedule":"daily 00:10","window":"1d","sources":[{"type":"activitywatch","buckets":["window"]}],"unit":"min","aggregation":"sum"}`)
	body := map[string]any{"id": "yt", "kpiName": "YouTube", "manifest": manifest, "files": map[string]string{
		"collector.py": "print(1)", "fixtures/a/expected.json": `{"value": 3}`, "fixtures/a/inputs/n.txt": "3",
	}}

	if rec := doRequest(t, h, http.MethodPost, "/v1/collectors", testKey, map[string]any{"id": "yt"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad install = %d", rec.Code)
	}
	rec := doRequest(t, h, http.MethodPost, "/v1/collectors", testKey, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("install = %d %s", rec.Code, rec.Body)
	}
	var inst struct {
		ID           string
		Version      int
		ContractHash string
		Approved     bool
	}
	decodeBody(t, rec, &inst)
	if inst.ID != "yt" || inst.Version != 1 || inst.Approved || len(inst.ContractHash) != 64 {
		t.Fatalf("install resp = %+v", inst)
	}

	rec = doRequest(t, h, http.MethodPost, "/v1/collectors/yt/test", testKey, nil)
	var tr collectors.TestResult
	decodeBody(t, rec, &tr)
	if rec.Code != http.StatusOK || !tr.Passed || tr.Sandbox != "fake" || len(tr.Cases) != 1 {
		t.Fatalf("test = %d %s", rec.Code, rec.Body)
	}

	window := map[string]string{"from": "2026-11-03T00:00:00+01:00", "to": "2026-11-04T00:00:00+01:00"}
	if rec := doRequest(t, h, http.MethodPost, "/v1/collectors/yt/run", testKey, window); rec.Code != http.StatusForbidden {
		t.Fatalf("run unapproved = %d", rec.Code)
	}
	if rec := doRequest(t, h, http.MethodPost, "/v1/collectors/yt/approve", testKey, map[string]string{"contractHash": "x"}); rec.Code != http.StatusConflict {
		t.Fatalf("approve wrong hash = %d", rec.Code)
	}
	if rec := doRequest(t, h, http.MethodPost, "/v1/collectors/yt/approve", testKey, map[string]string{"contractHash": inst.ContractHash}); rec.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body)
	}
	rec = doRequest(t, h, http.MethodPost, "/v1/collectors/yt/run", testKey, window)
	var out collectors.Output
	decodeBody(t, rec, &out)
	if rec.Code != http.StatusOK || out.Value != 7 || out.Unit != "min" || out.Sandbox != "fake" || len(out.Evidence) != 1 {
		t.Fatalf("run = %d %s", rec.Code, rec.Body)
	}
	if rec := doRequest(t, h, http.MethodPost, "/v1/collectors/yt/run", testKey, map[string]string{"from": "x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("run bad body = %d", rec.Code)
	}
	if rec := doRequest(t, h, http.MethodPost, "/v1/collectors/yt/activate", testKey, nil); rec.Code != http.StatusOK {
		t.Fatalf("activate = %d %s", rec.Code, rec.Body)
	}

	rec = doRequest(t, h, http.MethodGet, "/v1/collectors", testKey, nil)
	var list []collectors.Info
	decodeBody(t, rec, &list)
	if len(list) != 1 || list[0].State != collectors.StateActive || !list[0].Approved || list[0].KPIName != "YouTube" {
		t.Fatalf("list = %s", rec.Body)
	}

	s.Collectors.Tick(context.Background())
	rec = doRequest(t, h, http.MethodGet, "/v1/collectors/yt/readings", testKey, nil)
	var rs []map[string]any
	decodeBody(t, rec, &rs)
	if rec.Code != http.StatusOK || len(rs) == 0 || rs[0]["value"] != 7.0 || rs[0]["unit"] != "min" || rs[0]["from"] == nil {
		t.Fatalf("readings = %d %s", rec.Code, rec.Body)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	rec = doRequest(t, h, http.MethodGet, "/v1/collectors/yt/readings?since="+future, testKey, nil)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("readings since future = %s", rec.Body)
	}
	if rec := doRequest(t, h, http.MethodGet, "/v1/collectors/yt/readings?since=yesterday", testKey, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad since = %d", rec.Code)
	}

	if rec := doRequest(t, h, http.MethodDelete, "/v1/collectors/yt", testKey, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodDelete, "/v1/collectors/yt"}, {http.MethodGet, "/v1/collectors/yt/readings"},
		{http.MethodPost, "/v1/collectors/yt/test"}, {http.MethodPost, "/v1/collectors/yt/activate"},
	} {
		if rec := doRequest(t, h, c.method, c.path, testKey, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s after delete = %d", c.method, c.path, rec.Code)
		}
	}
}

func TestCollectorsNoSandbox(t *testing.T) {
	s := newTestServer(t)
	withCollectors(t, s, apiSandbox{unavailable: sandbox.ErrUnavailable})
	h := s.Routes()
	body := map[string]any{"id": "c", "kpiName": "k", "files": map[string]string{"collector.py": ""},
		"manifest": json.RawMessage(`{"schedule":"hourly","window":"1h","sources":[{"type":"activitywatch","buckets":["window"]}],"unit":"min","aggregation":"sum"}`)}
	rec := doRequest(t, h, http.MethodPost, "/v1/collectors", testKey, body)
	var inst struct{ ContractHash string }
	decodeBody(t, rec, &inst)
	if rec := doRequest(t, h, http.MethodPost, "/v1/collectors/c/test", testKey, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("test without sandbox = %d", rec.Code)
	}
	doRequest(t, h, http.MethodPost, "/v1/collectors/c/approve", testKey, map[string]string{"contractHash": inst.ContractHash})
	if rec := doRequest(t, h, http.MethodPost, "/v1/collectors/c/run", testKey, map[string]string{"from": "2026-11-03T00:00:00Z", "to": "2026-11-03T01:00:00Z"}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("run without sandbox = %d", rec.Code)
	}
	rec = doRequest(t, h, http.MethodGet, "/v1/sensors", testKey, nil)
	var st collectors.SensorsState
	decodeBody(t, rec, &st)
	if st.Sandbox.Available || st.Sandbox.Error == "" || st.Sandbox.Kind != "fake" || !st.ActivityWatch.Running || len(st.ActivityWatch.Buckets) != 1 {
		t.Fatalf("sensors = %s", rec.Body)
	}
}

func TestSensorsInstallEndpoint(t *testing.T) {
	s := newTestServer(t)
	withCollectors(t, s, apiSandbox{})
	done := make(chan struct{})
	s.Sensors.Install = func(context.Context, string) error { <-done; return nil }
	h := s.Routes()
	if rec := doRequest(t, h, http.MethodPost, "/v1/sensors/install", testKey, map[string]string{"name": "x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown sensor = %d", rec.Code)
	}
	rec := doRequest(t, h, http.MethodPost, "/v1/sensors/install", testKey, map[string]string{"name": "activitywatch"})
	if rec.Code == http.StatusNotImplemented {
		t.Skipf("install unsupported on this OS: %s", rec.Body)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("install = %d %s", rec.Code, rec.Body)
	}
	if rec := doRequest(t, h, http.MethodPost, "/v1/sensors/install", testKey, map[string]string{"name": "activitywatch"}); rec.Code != http.StatusConflict {
		t.Fatalf("second install = %d", rec.Code)
	}
	close(done)
}

func TestBusyWhileCollectorRuns(t *testing.T) {
	s := newTestServer(t)
	if s.anyBusy() {
		t.Fatal("busy at start")
	}
	release := make(chan struct{})
	started := make(chan struct{})
	e := withCollectors(t, s, blockingSandbox{started, release})
	info, err := e.Install(collectors.InstallRequest{ID: "c", KPIName: "k", Files: map[string]string{"collector.py": ""},
		Manifest: json.RawMessage(`{"schedule":"hourly","window":"1h","sources":[{"type":"activitywatch","buckets":["window"]}],"unit":"min","aggregation":"sum"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.Approve("c", info.ContractHash)
	go e.Run(context.Background(), "c", time.Now().Add(-time.Hour), time.Now()) //nolint:errcheck
	<-started
	if !s.anyBusy() {
		t.Fatal("not busy while a collector runs")
	}
	if rec := doRequest(t, s.Routes(), http.MethodPost, "/v1/suspend", testKey, nil); rec.Code != http.StatusConflict {
		t.Fatalf("suspend during a collector run = %d", rec.Code)
	}
	close(release)
}

type blockingSandbox struct{ started, release chan struct{} }

func (blockingSandbox) Kind() string     { return "fake" }
func (blockingSandbox) Available() error { return nil }
func (b blockingSandbox) Run(context.Context, sandbox.Spec) (sandbox.Result, error) {
	close(b.started)
	<-b.release
	return sandbox.Result{Stdout: []byte(`{"value": 1}`)}, nil
}

func TestPackagesAPI(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()
	if rec := doRequest(t, h, "GET", "/v1/packages", testKey, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled status = %d", rec.Code)
	}
	e := withCollectors(t, s, apiSandbox{})
	s.Packages = collectors.NewPackages(e.DataDir, s.Sensors)
	s.Packages.Sync = true
	s.Sensors.Install = func(ctx context.Context, dir string) error { return nil }
	if rec := doRequest(t, h, "POST", "/v1/packages/uninstall", testKey, map[string]string{"manager": "relay", "id": "activitywatch"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d", rec.Code)
	}
	// AW is "running" (fake server) → preexisting → installedByRelay=false → 400.
	if _, err := s.Packages.Require("c1", []collectors.Require{{Manager: "relay", ID: "activitywatch", Why: "w"}}); err != nil {
		t.Fatal(err)
	}
	rec := doRequest(t, h, "GET", "/v1/packages", testKey, nil)
	var list []collectors.PackageInfo
	decodeBody(t, rec, &list)
	if len(list) != 1 || list[0].InstalledByRelay || list[0].State != "installed" || len(list[0].UsedBy) != 1 {
		t.Fatalf("list = %+v", list)
	}
	body := map[string]string{"manager": "relay", "id": "activitywatch"}
	if rec := doRequest(t, h, "POST", "/v1/packages/uninstall", testKey, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("preexisting status = %d, want 400", rec.Code)
	}
	var info runnerInfo
	decodeBody(t, doRequest(t, h, "GET", "/v1/runner/info", testKey, nil), &info)
	if info.OS == "" || info.Arch == "" || len(info.Managers) == 0 || info.Managers[0] != "relay" {
		t.Errorf("info = %+v", info)
	}
}
