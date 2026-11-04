package collectors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"relay/runner/internal/collectors/sandbox"
)

// fakeSandbox computes the output in Go: value = number of events in
// <inputs>/aw/window.json, or the int in <inputs>/n.txt.
type fakeSandbox struct {
	unavailable error
	runs        atomic.Int32
	out         func(s sandbox.Spec) (sandbox.Result, error)
}

func (f *fakeSandbox) Kind() string     { return "fake" }
func (f *fakeSandbox) Available() error { return f.unavailable }
func (f *fakeSandbox) Run(ctx context.Context, s sandbox.Spec) (sandbox.Result, error) {
	f.runs.Add(1)
	if f.out != nil {
		return f.out(s)
	}
	if b, err := os.ReadFile(filepath.Join(s.InputsDir, "n.txt")); err == nil {
		return sandbox.Result{Stdout: []byte(`{"value": ` + strings.TrimSpace(string(b)) + `, "unit": "min", "evidence": []}`)}, nil
	}
	var evs []json.RawMessage
	b, err := os.ReadFile(filepath.Join(s.InputsDir, "aw", "window.json"))
	if err != nil {
		return sandbox.Result{Stderr: []byte(err.Error()), ExitCode: 1}, nil
	}
	_ = json.Unmarshal(b, &evs)
	return sandbox.Result{Stdout: []byte(fmt.Sprintf("debug line\n{\"value\": %d, \"evidence\": [{\"label\": \"events\", \"value\": %d}]}", len(evs), len(evs)))}, nil
}

// fakeAW serves two buckets; each events query returns 3 events.
func fakeAW(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/0/buckets/":
			fmt.Fprint(w, `{"aw-watcher-window_host": {}, "aw-watcher-afk_host": {}}`)
		case strings.HasSuffix(r.URL.Path, "/events"):
			if r.URL.Query().Get("start") == "" || r.URL.Query().Get("end") == "" {
				http.Error(w, "no window", 400)
				return
			}
			fmt.Fprint(w, `[{"duration": 60}, {"duration": 30}, {"duration": 5}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestEngine(t *testing.T, sb Sandbox, aw string) *Engine {
	e := &Engine{DataDir: t.TempDir(), Sandbox: sb, AWBase: aw,
		Python: func() (string, error) { return "/usr/bin/python3", nil },
		Loc:    time.UTC, Now: time.Now}
	return e
}

const dailyManifest = `{"schedule":"daily 00:10","window":"1d","sources":[{"type":"activitywatch","buckets":["window"]}],"unit":"min","aggregation":"sum"}`

func install(t *testing.T, e *Engine, id, manifest string, files map[string]string) Info {
	t.Helper()
	if files == nil {
		files = map[string]string{"collector.py": "print(1)"}
	}
	info, err := e.Install(InstallRequest{ID: id, KPIName: "Coding", Manifest: json.RawMessage(manifest), Files: files})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	return info
}

func TestContractHashCanonical(t *testing.T) {
	canon := `{"schedule":"daily 00:10","sources":[{"buckets":["window"],"type":"activitywatch"}],"window":"1d"}`
	sum := sha256.Sum256([]byte(canon))
	want := hex.EncodeToString(sum[:])
	_, got, err := ParseManifest(json.RawMessage(dailyManifest))
	if err != nil || got != want {
		t.Fatalf("hash = %s, %v; want %s", got, err, want)
	}
	// Key order, whitespace, unit and aggregation don't matter.
	other := `{ "unit": "h", "aggregation": "max", "window": "1d",
	  "sources": [ {"buckets": ["window"], "type": "activitywatch"} ], "schedule": "daily 00:10" }`
	if _, h, _ := ParseManifest(json.RawMessage(other)); h != want {
		t.Errorf("reordered manifest hash differs: %s", h)
	}
	// No HTML escaping.
	h, _ := ContractHash(json.RawMessage(`{"schedule":"a<b&c","window":"1d","sources":[]}`))
	sum = sha256.Sum256([]byte(`{"schedule":"a<b&c","sources":[],"window":"1d"}`))
	if h != hex.EncodeToString(sum[:]) {
		t.Errorf("HTML-escaped canonical JSON")
	}
	// Any contract change → new hash.
	wider := strings.Replace(dailyManifest, `["window"]`, `["window","web"]`, 1)
	if _, h, _ := ParseManifest(json.RawMessage(wider)); h == want {
		t.Errorf("wider contract has the same hash")
	}
}

func TestManifestValidation(t *testing.T) {
	ok := []string{
		dailyManifest,
		`{"schedule":"hourly","window":"1h","sources":[{"type":"file","glob":"~/.config/app/*.db","why":"usage db"}],"unit":"n","aggregation":"last"}`,
	}
	for _, m := range ok {
		if _, _, err := ParseManifest(json.RawMessage(m)); err != nil {
			t.Errorf("ParseManifest(%s) = %v", m, err)
		}
	}
	bad := []string{
		``, `{`,
		`{"schedule":"daily 25:00","window":"1d","sources":[{"type":"activitywatch","buckets":["window"]}],"unit":"min","aggregation":"sum"}`,
		`{"schedule":"hourly","window":"1d","sources":[{"type":"activitywatch","buckets":["window"]}],"unit":"min","aggregation":"sum"}`,
		`{"schedule":"daily 00:10","window":"1d","sources":[],"unit":"min","aggregation":"sum"}`,
		`{"schedule":"daily 00:10","window":"1d","sources":[{"type":"activitywatch","buckets":[]}],"unit":"min","aggregation":"sum"}`,
		`{"schedule":"daily 00:10","window":"1d","sources":[{"type":"activitywatch","buckets":["window"]}],"unit":"","aggregation":"sum"}`,
		`{"schedule":"daily 00:10","window":"1d","sources":[{"type":"activitywatch","buckets":["window"]}],"unit":"min","aggregation":"avg"}`,
		`{"schedule":"daily 00:10","window":"1d","sources":[{"type":"net","buckets":["window"]}],"unit":"min","aggregation":"sum"}`,
		`{"schedule":"daily 00:10","window":"1d","sources":[{"type":"file","glob":"rel/*.db","why":"x"}],"unit":"min","aggregation":"sum"}`,
		`{"schedule":"daily 00:10","window":"1d","sources":[{"type":"file","glob":"~/a/*.db"}],"unit":"min","aggregation":"sum"}`,
		`{"schedule":"daily 00:10","window":"1d","sources":[{"type":"file","glob":"~/.ssh/*","why":"x"}],"unit":"min","aggregation":"sum"}`,
		`{"schedule":"daily 00:10","window":"1d","sources":[{"type":"file","glob":"~/.config/google-chrome/*/Cookies","why":"x"}],"unit":"min","aggregation":"sum"}`,
		`{"schedule":"daily 00:10","window":"1d","sources":[{"type":"file","glob":"~/**/x.db","why":"x"}],"unit":"min","aggregation":"sum"}`,
	}
	for _, m := range bad {
		if _, _, err := ParseManifest(json.RawMessage(m)); !errors.Is(err, ErrBadRequest) {
			t.Errorf("ParseManifest(%s) = %v, want ErrBadRequest", m, err)
		}
	}
}

func TestDenied(t *testing.T) {
	for _, p := range []string{"/home/u/.ssh/id_rsa", "/home/u/proj/.env", "/home/u/proj/.env.local", "/x/server.pem",
		"/home/u/.local/share/keyrings/login.keyring", "/home/u/.config/google-chrome/Default/Login Data",
		"/home/u/.config/google-chrome/Default/Cookies", "/home/u/.mozilla/firefox/p/key4.db",
		`C:\Users\u\AppData\Local\Google\Chrome\User Data\Default\Network\Cookies`, "/home/u/.aws/credentials"} {
		if denied(p) == "" {
			t.Errorf("denied(%q) = allowed", p)
		}
	}
	for _, p := range []string{"/home/u/.config/google-chrome/Default/History", "/home/u/notes/todo.txt", "/home/u/.local/share/app/data.db"} {
		if why := denied(p); why != "" {
			t.Errorf("denied(%q) = %s", p, why)
		}
	}
}

func TestStoreLifecycle(t *testing.T) {
	e := newTestEngine(t, &fakeSandbox{}, "")
	info := install(t, e, "coding", dailyManifest, nil)
	if info.Version != 1 || info.Approved || info.State != StatePending {
		t.Fatalf("v1 = %+v", info)
	}
	if err := e.Approve("coding", "nope"); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("approve wrong hash = %v", err)
	}
	if err := e.Activate("coding"); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("activate unapproved = %v", err)
	}
	if err := e.Approve("coding", info.ContractHash); err != nil {
		t.Fatal(err)
	}
	if err := e.Activate("coding"); err != nil {
		t.Fatal(err)
	}
	// Repair inside the contract: stays approved and active.
	v2 := install(t, e, "coding", strings.Replace(dailyManifest, `"unit":"min"`, `"unit":"minutes"`, 1), map[string]string{"collector.py": "print(2)"})
	if v2.Version != 2 || !v2.Approved || v2.State != StateActive || v2.ContractHash != info.ContractHash {
		t.Fatalf("v2 = %+v", v2)
	}
	// Wider contract: needs a new approval, back to pending.
	v3 := install(t, e, "coding", strings.Replace(dailyManifest, `["window"]`, `["window","web"]`, 1), nil)
	if v3.Version != 3 || v3.Approved || v3.State != StatePending {
		t.Fatalf("v3 = %+v", v3)
	}
	if _, err := e.Run(context.Background(), "coding", time.Now().Add(-time.Hour), time.Now()); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("run unapproved = %v", err)
	}
	for _, files := range []map[string]string{
		{},
		{"collector.py": "", "../x": ""},
		{"collector.py": "", "fixtures/a/other.txt": ""},
		{"collector.py": "", "fixtures/../../x/expected.json": ""},
		{"collector.py": "", "/etc/x": ""},
	} {
		if _, err := e.Install(InstallRequest{ID: "x", KPIName: "k", Manifest: json.RawMessage(dailyManifest), Files: files}); !errors.Is(err, ErrBadRequest) {
			t.Errorf("Install files %v = %v", files, err)
		}
	}
	if _, err := e.Install(InstallRequest{ID: "../x", KPIName: "k", Manifest: json.RawMessage(dailyManifest), Files: map[string]string{"collector.py": ""}}); !errors.Is(err, ErrBadRequest) {
		t.Errorf("bad id accepted: %v", err)
	}
	for i := 0; i < 6; i++ {
		install(t, e, "coding", dailyManifest, nil)
	}
	vs, _ := os.ReadDir(filepath.Join(e.DataDir, "collectors", "coding", "versions"))
	if len(vs) != keepVersions {
		t.Errorf("kept %d versions, want %d", len(vs), keepVersions)
	}
	if err := e.Delete("coding"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Get("coding"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
	if err := e.Delete("coding"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

func TestDueWindows(t *testing.T) {
	loc := time.FixedZone("X", 2*3600)
	daily, _, _ := ParseManifest(json.RawMessage(dailyManifest))
	at := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, loc) }

	ws := dueWindows(daily, at(4, 0, 5), loc) // before 00:10: yesterday not due yet
	if len(ws) != 6 || !ws[len(ws)-1].To.Equal(at(3, 0, 0)) {
		t.Fatalf("00:05 → %d windows, last %v", len(ws), ws[len(ws)-1])
	}
	ws = dueWindows(daily, at(4, 0, 15), loc)
	if len(ws) != 7 || !ws[6].From.Equal(at(3, 0, 0)) || !ws[6].To.Equal(at(4, 0, 0)) || !ws[0].From.Equal(at(3, 0, 0).AddDate(0, 0, -6)) {
		t.Fatalf("00:15 → %d windows: first %v last %v", len(ws), ws[0], ws[6])
	}
	for i := 1; i < len(ws); i++ {
		if !ws[i].From.Equal(ws[i-1].To) {
			t.Fatalf("windows not contiguous/ordered: %v", ws)
		}
	}

	hourly, _, _ := ParseManifest(json.RawMessage(`{"schedule":"hourly","window":"1h","sources":[{"type":"activitywatch","buckets":["afk"]}],"unit":"min","aggregation":"sum"}`))
	ws = dueWindows(hourly, at(4, 10, 30), loc)
	if len(ws) != 168 || !ws[167].From.Equal(at(4, 9, 0)) || !ws[167].To.Equal(at(4, 10, 0)) {
		t.Fatalf("hourly → %d windows, last %v", len(ws), ws[len(ws)-1])
	}
}

func TestSchedulerTick(t *testing.T) {
	aw := fakeAW(t)
	sb := &fakeSandbox{}
	e := newTestEngine(t, sb, aw.URL)
	now := time.Date(2026, 11, 4, 0, 15, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	ctx := context.Background()

	info := install(t, e, "c", dailyManifest, nil)
	e.Tick(ctx)
	if sb.runs.Load() != 0 {
		t.Fatalf("pending collector ran")
	}
	_ = e.Approve("c", info.ContractHash)
	e.Tick(ctx)
	if sb.runs.Load() != 0 {
		t.Fatalf("approved but inactive collector ran")
	}
	_ = e.Activate("c")
	e.Tick(ctx)
	rs, _ := e.Readings("c", time.Time{})
	if len(rs) != 7 || rs[0].Value != 3 || rs[0].Unit != "min" {
		t.Fatalf("backfill readings = %+v", rs)
	}
	e.Tick(ctx)
	if rs, _ := e.Readings("c", time.Time{}); len(rs) != 7 {
		t.Fatalf("second tick duplicated: %d", len(rs))
	}
	now = now.Add(24 * time.Hour)
	e.Tick(ctx)
	rs, _ = e.Readings("c", time.Time{})
	if len(rs) != 8 || !rs[7].To.Equal(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("next day readings = %d, last %+v", len(rs), rs[len(rs)-1])
	}
	if got, _ := e.Readings("c", rs[6].RecordedAt); len(got) != 1 {
		t.Fatalf("since filter → %d readings", len(got))
	}

	// Sandbox unavailable: nothing runs, error recorded.
	sb.unavailable = sandbox.ErrUnavailable
	before := sb.runs.Load()
	now = now.Add(24 * time.Hour)
	e.Tick(ctx)
	if sb.runs.Load() != before {
		t.Fatalf("ran without a sandbox")
	}
	if i, _ := e.Get("c"); i.State != StateActive || !strings.Contains(i.LastError, "sandbox unavailable") {
		t.Fatalf("after unavailable: %+v", i)
	}
	sb.unavailable = nil

	// Source down: stays active, retried after backoff.
	aw.Close()
	e.Tick(ctx)
	if i, _ := e.Get("c"); i.State != StateActive || !strings.Contains(i.LastError, "activitywatch") {
		t.Fatalf("after source error: %+v", i)
	}

	// Collector failure → failed, no more scheduling.
	aw2 := fakeAW(t)
	e.AWBase = aw2.URL
	now = now.Add(sourceRetry + time.Minute)
	sb.out = func(sandbox.Spec) (sandbox.Result, error) {
		return sandbox.Result{Stdout: []byte(`{"value": NaN}`)}, nil
	}
	e.Tick(ctx)
	if i, _ := e.Get("c"); i.State != StateFailed || i.LastError == "" {
		t.Fatalf("after collector error: %+v", i)
	}
	before = sb.runs.Load()
	e.Tick(ctx)
	if sb.runs.Load() != before {
		t.Fatalf("failed collector still scheduled")
	}
}

func TestRunAndFixtures(t *testing.T) {
	aw := fakeAW(t)
	sb := &fakeSandbox{}
	e := newTestEngine(t, sb, aw.URL)
	ctx := context.Background()
	info := install(t, e, "c", dailyManifest, map[string]string{
		"collector.py":                 "print(1)",
		"fixtures/empty/expected.json": `{"value": 0}`,
		"fixtures/empty/inputs/n.txt":  "0",
		"fixtures/day/expected.json":   `{"value": 47, "from": "2026-11-03T00:00:00Z", "to": "2026-11-04T00:00:00Z"}`,
		"fixtures/day/inputs/n.txt":    "47",
		"fixtures/wrong/expected.json": `{"value": 10}`,
		"fixtures/wrong/inputs/n.txt":  "11",
	})
	res, err := e.Test(ctx, "c")
	if err != nil || res.Passed || len(res.Cases) != 3 || res.Sandbox != "fake" {
		t.Fatalf("Test = %+v, %v", res, err)
	}
	for _, c := range res.Cases {
		if want := c.Name != "wrong"; c.Passed != want {
			t.Errorf("case %+v passed=%v", c, c.Passed)
		}
	}
	_ = e.Approve("c", info.ContractHash)
	out, err := e.Run(ctx, "c", time.Now().Add(-24*time.Hour), time.Now())
	if err != nil || out.Value != 3 || out.Unit != "min" || out.Sandbox != "fake" || len(out.Evidence) != 1 {
		t.Fatalf("Run = %+v, %v", out, err)
	}
	if _, err := e.Run(ctx, "c", time.Now(), time.Now().Add(-time.Hour)); !errors.Is(err, ErrBadRequest) {
		t.Errorf("reversed window = %v", err)
	}
	sb.unavailable = errors.New("no bwrap")
	if _, err := e.Run(ctx, "c", time.Now().Add(-time.Hour), time.Now()); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Run without sandbox = %v", err)
	}
	if _, err := e.Test(ctx, "c"); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Test without sandbox = %v", err)
	}
}

func TestParseOutput(t *testing.T) {
	for _, s := range []string{`{"value": NaN}`, `{"unit": "x"}`, `not json`, `{"value": "12"}`, `{"value": 1e999}`} {
		if _, err := parseOutput([]byte(s)); err == nil {
			t.Errorf("parseOutput(%s) accepted", s)
		}
	}
	ev := make([]string, 30)
	for i := range ev {
		ev[i] = fmt.Sprintf(`{"label": "%s", "value": %d}`, strings.Repeat("x", 300), i)
	}
	out, err := parseOutput([]byte(`{"value": 2.5, "unit": "h", "evidence": [` + strings.Join(ev, ",") + `]}`))
	if err != nil || out.Value != 2.5 || len(out.Evidence) != maxEvidence || len(out.Evidence[0].Label) != maxLabel {
		t.Fatalf("parseOutput = %+v, %v", out, err)
	}
}

func TestFileSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, p := range []string{"app/p1/usage.db", "app/p2/usage.db", "app/p2/other.txt"} {
		full := filepath.Join(home, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(p), 0o644)
	}
	out := t.TempDir()
	n, err := fileSnapshot("~/app/*/usage.db", out, MaxFileBytes)
	if err != nil || n == 0 {
		t.Fatalf("fileSnapshot = %d, %v", n, err)
	}
	for _, p := range []string{"p1/usage.db", "p2/usage.db"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(p))); err != nil {
			t.Errorf("missing copy %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "p2", "other.txt")); err == nil {
		t.Errorf("copied a non-matching file")
	}
	if _, err := fileSnapshot("~/app/*/usage.db", t.TempDir(), 5); !errors.Is(err, ErrBadRequest) {
		t.Errorf("size cap: %v", err)
	}
	// A secret reached through a broad glob is refused at copy time.
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".ssh", "id_rsa"), []byte("k"), 0o600)
	if _, err := fileSnapshot("~/*/id_rsa", t.TempDir(), MaxFileBytes); !errors.Is(err, ErrBadRequest) {
		t.Errorf("secret glob: %v", err)
	}
	// ...also through a symlink with an innocent name.
	_ = os.Symlink(filepath.Join(home, ".ssh", "id_rsa"), filepath.Join(home, "app", "p1", "innocent.db"))
	if _, err := fileSnapshot("~/app/p1/*.db", t.TempDir(), MaxFileBytes); !errors.Is(err, ErrBadRequest) {
		t.Errorf("symlink to secret: %v", err)
	}
}

func TestSensorsInstallState(t *testing.T) {
	aw := fakeAW(t)
	e := newTestEngine(t, &fakeSandbox{}, aw.URL)
	s := NewSensors(e)
	release := make(chan struct{})
	s.Install = func(ctx context.Context, dir string) error { <-release; return errors.New("boom") }
	st := s.State(context.Background())
	if !st.ActivityWatch.Running || !st.ActivityWatch.Installed || len(st.ActivityWatch.Buckets) != 2 || !st.Sandbox.Available || st.Sandbox.Kind != "fake" {
		t.Fatalf("state = %+v", st)
	}
	if err := s.StartInstall("nope"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("unknown sensor = %v", err)
	}
	if err := installSupported(); err != nil {
		t.Skipf("install unsupported here: %v", err)
	}
	if err := s.StartInstall("activitywatch"); err != nil {
		t.Fatal(err)
	}
	if err := s.StartInstall("activitywatch"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second install = %v", err)
	}
	if st := s.State(context.Background()); st.Operation != "installing" {
		t.Fatalf("operation = %q", st.Operation)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		st = s.State(context.Background())
		if st.Operation == "" || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.Operation != "" || st.ActivityWatch.Error != "boom" {
		t.Fatalf("after install: %+v", st)
	}
}

func TestBusyWhileRunning(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	sb := &fakeSandbox{out: func(sandbox.Spec) (sandbox.Result, error) {
		close(started)
		<-block
		return sandbox.Result{Stdout: []byte(`{"value": 1}`)}, nil
	}}
	e := newTestEngine(t, sb, fakeAW(t).URL)
	info := install(t, e, "c", dailyManifest, nil)
	_ = e.Approve("c", info.ContractHash)
	done := make(chan error)
	go func() {
		_, err := e.Run(context.Background(), "c", time.Now().Add(-time.Hour), time.Now())
		done <- err
	}()
	<-started
	if busy, _ := e.IdleStatus(); !busy || e.Active() != 1 {
		t.Fatalf("not busy during a run")
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if busy, since := e.IdleStatus(); busy || since.IsZero() {
		t.Fatalf("still busy after run")
	}
}
