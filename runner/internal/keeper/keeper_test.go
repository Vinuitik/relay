package keeper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMain doubles as a fake runner: with KEEPER_FAKE_RUNNER=1 the test
// binary just stays alive until killed, like a healthy relay-runner.
func TestMain(m *testing.M) {
	if os.Getenv("KEEPER_FAKE_RUNNER") == "1" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeGitHub serves one release whose runner asset is body.
func fakeGitHub(t *testing.T, tag string, body []byte, sumOverride string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(body)
	sumHex := hex.EncodeToString(sum[:])
	if sumOverride != "" {
		sumHex = sumOverride
	}
	name := assetName("relay-runner")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			json.NewEncoder(w).Encode(Release{Tag: tag, Assets: []Asset{
				{Name: name, BrowserURL: srv.URL + "/dl/runner"},
				{Name: "SHA256SUMS", BrowserURL: srv.URL + "/dl/sums"},
			}})
		case "/dl/runner":
			w.Write(body)
		case "/dl/sums":
			fmt.Fprintf(w, "%s  %s\n", sumHex, name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testBinary(t *testing.T) []byte {
	b, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestUpdater(t *testing.T, srv *httptest.Server, st RunnerStatus) (*Updater, *Supervisor) {
	t.Helper()
	cfg := Config{Dir: t.TempDir(), Repo: "o/r", Quiet: 15 * time.Minute}
	if err := os.MkdirAll(filepath.Join(cfg.Dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The "currently installed" runner: a copy of this test binary.
	if err := os.WriteFile(cfg.RunnerPath(), testBinary(t), 0o755); err != nil {
		t.Fatal(err)
	}
	sup := &Supervisor{Path: cfg.RunnerPath(), Env: append(os.Environ(), "KEEPER_FAKE_RUNNER=1")}
	u := &Updater{
		Cfg:           cfg,
		Version:       "dev",
		Supervisor:    sup,
		HTTP:          srv.Client(),
		APIBase:       srv.URL,
		Status:        func() (RunnerStatus, error) { return st, nil },
		Health:        func() bool { return sup.Uptime() > 0 },
		Now:           time.Now,
		HealthTimeout: 3 * time.Second,
		MinUptime:     500 * time.Millisecond,
	}
	t.Cleanup(sup.Stop)
	return u, sup
}

func idleFor(d time.Duration) RunnerStatus {
	return RunnerStatus{Build: "old", IdleSince: time.Now().Add(-d).UTC().Format(time.RFC3339)}
}

func TestCheck_WaitsForQuietRunner(t *testing.T) {
	srv := fakeGitHub(t, "new", testBinary(t), "")
	u, _ := newTestUpdater(t, srv, idleFor(time.Minute))
	if err := u.Check(); err != ErrNotIdle {
		t.Fatalf("Check = %v, want ErrNotIdle (idle only 1m of 15m)", err)
	}
	busy := RunnerStatus{Build: "old", Busy: true}
	u.Status = func() (RunnerStatus, error) { return busy, nil }
	if err := u.Check(); err != ErrNotIdle {
		t.Fatalf("Check while busy = %v, want ErrNotIdle", err)
	}
}

func TestCheck_UpToDateDoesNothing(t *testing.T) {
	srv := fakeGitHub(t, "same", []byte("x"), "")
	st := idleFor(time.Hour)
	st.Build = "same"
	u, _ := newTestUpdater(t, srv, st)
	before, _ := os.ReadFile(u.Cfg.RunnerPath())
	if err := u.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
	after, _ := os.ReadFile(u.Cfg.RunnerPath())
	if len(before) != len(after) {
		t.Error("runner binary changed although already on the latest build")
	}
}

func TestCheck_SwapsRunnerWhenQuiet(t *testing.T) {
	newBin := append(testBinary(t), []byte("v2")...) // still runnable, distinguishable
	srv := fakeGitHub(t, "new", newBin, "")
	u, sup := newTestUpdater(t, srv, idleFor(time.Hour))
	sup.Start()
	if err := u.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
	got, _ := os.ReadFile(u.Cfg.RunnerPath())
	if len(got) != len(newBin) {
		t.Error("runner binary not replaced")
	}
	if _, err := os.Stat(u.Cfg.RunnerPath() + ".old"); err != nil {
		t.Error("previous binary not kept as .old")
	}
	if sup.Uptime() == 0 {
		t.Error("runner not running after update")
	}
}

func TestCheck_RollsBackUnstartableRunner(t *testing.T) {
	broken := []byte("not an executable")
	srv := fakeGitHub(t, "new", broken, "")
	u, sup := newTestUpdater(t, srv, idleFor(time.Hour))
	orig, _ := os.ReadFile(u.Cfg.RunnerPath())
	sup.Start()
	if err := u.Check(); err == nil {
		t.Fatal("Check succeeded with a broken runner, want rollback error")
	}
	got, _ := os.ReadFile(u.Cfg.RunnerPath())
	if len(got) != len(orig) {
		t.Error("broken runner left installed, want the previous binary restored")
	}
	deadline := time.Now().Add(3 * time.Second)
	for sup.Uptime() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if sup.Uptime() == 0 {
		t.Error("previous runner not running after rollback")
	}
}

func TestCheck_RejectsChecksumMismatch(t *testing.T) {
	srv := fakeGitHub(t, "new", []byte("payload"), "0000")
	u, _ := newTestUpdater(t, srv, idleFor(time.Hour))
	orig, _ := os.ReadFile(u.Cfg.RunnerPath())
	if err := u.Check(); err == nil {
		t.Fatal("Check accepted a checksum mismatch")
	}
	got, _ := os.ReadFile(u.Cfg.RunnerPath())
	if len(got) != len(orig) {
		t.Error("runner replaced despite checksum mismatch")
	}
}

func TestSupervisor_RestartsCrashedChild(t *testing.T) {
	sup := &Supervisor{Path: os.Args[0], Env: append(os.Environ(), "KEEPER_FAKE_RUNNER=1")}
	sup.Start()
	defer sup.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for sup.Uptime() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	sup.mu.Lock()
	first := sup.cmd.Process.Pid
	sup.cmd.Process.Kill()
	sup.mu.Unlock()

	// Crashed within 30s of start → 10s backoff; wait for the new child.
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		sup.mu.Lock()
		cmd := sup.cmd
		sup.mu.Unlock()
		if cmd != nil && cmd.Process.Pid != first {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("supervisor did not restart the crashed child")
}

func TestFindSum(t *testing.T) {
	sums := []byte("abc  relay-runner-linux-amd64\ndef *keeperd-windows-amd64.exe\n")
	if got, _ := findSum(sums, "keeperd-windows-amd64.exe"); got != "def" {
		t.Errorf("findSum = %q, want def", got)
	}
	if _, err := findSum(sums, "missing"); err == nil {
		t.Error("findSum found a missing entry")
	}
}

func TestLoadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.env")
	os.WriteFile(path, []byte("# comment\nKEEPER_T_A=1\nKEEPER_T_B=\"two\"\nKEEPER_T_SET=file\n"), 0o600)
	t.Setenv("KEEPER_T_SET", "env")
	t.Setenv("KEEPER_T_A", "")
	os.Unsetenv("KEEPER_T_A")
	defer os.Unsetenv("KEEPER_T_B")
	if err := LoadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KEEPER_T_A") != "1" || os.Getenv("KEEPER_T_B") != "two" {
		t.Errorf("A=%q B=%q", os.Getenv("KEEPER_T_A"), os.Getenv("KEEPER_T_B"))
	}
	if os.Getenv("KEEPER_T_SET") != "env" {
		t.Error("env file overrode an already-set variable")
	}
	if err := LoadEnvFile(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("missing file: %v", err)
	}
}
