package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequiresValidation(t *testing.T) {
	mk := func(req string) json.RawMessage {
		return json.RawMessage(strings.Replace(dailyManifest, `"unit"`, `"requires":`+req+`,"unit"`, 1))
	}
	ok := []string{
		`[{"manager":"relay","id":"activitywatch","why":"x"}]`,
		`[{"manager":"winget","id":"ActivityWatch.ActivityWatch","why":"x"}]`,
		`[{"manager":"brew","id":"homebrew/cask/foo","why":"x"}]`,
		`[{"manager":"flatpak","id":"net.example.App","why":"x"}]`,
		`[{"manager":"pip","id":"requests==2.*","why":"x"}]`,
		`[]`,
	}
	for _, r := range ok {
		if _, _, err := ParseManifest(mk(r)); err != nil {
			t.Errorf("%s: %v", r, err)
		}
	}
	bad := []string{
		`[{"manager":"apt","id":"foo","why":"x"}]`,
		`[{"manager":"relay","id":"other","why":"x"}]`,
		`[{"manager":"winget","id":"a b","why":"x"}]`,
		`[{"manager":"pip","id":"https://evil/x.whl","why":"x"}]`,
		`[{"manager":"pip","id":"--index-url","why":"x"}]`,
		`[{"manager":"brew","id":"Foo","why":"x"}]`,
		`[{"manager":"relay","id":"activitywatch","why":" "}]`,
		`[{"manager":"relay","id":"activitywatch"}]`,
		`[{"manager":"pip","id":"a","why":"x"},{"manager":"pip","id":"a","why":"x"}]`,
		`[{"manager":"pip","id":"a","why":"x"},{"manager":"pip","id":"b","why":"x"},{"manager":"pip","id":"c","why":"x"},{"manager":"pip","id":"d","why":"x"},{"manager":"pip","id":"e","why":"x"},{"manager":"pip","id":"f","why":"x"}]`,
	}
	for _, r := range bad {
		if _, _, err := ParseManifest(mk(r)); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: want ErrBadRequest, got %v", r, err)
		}
	}
}

func TestContractHashRequires(t *testing.T) {
	// Same canonical bytes as before `requires` existed (see TestContractHashCanonical): the hash of a manifest without requires must never change.
	_, base, err := ParseManifest(json.RawMessage(dailyManifest))
	if err != nil {
		t.Fatal(err)
	}
	for _, empty := range []string{`"requires":[],`, `"requires":null,`} {
		_, h, _ := ParseManifest(json.RawMessage(strings.Replace(dailyManifest, `"unit"`, empty+`"unit"`, 1)))
		if h != base {
			t.Errorf("%s changed the hash", empty)
		}
	}
	r1 := strings.Replace(dailyManifest, `"unit"`, `"requires":[{"manager":"relay","id":"activitywatch","why":"a"}],"unit"`, 1)
	r2 := strings.Replace(r1, `"why":"a"`, `"why":"b"`, 1)
	_, h1, _ := ParseManifest(json.RawMessage(r1))
	_, h2, _ := ParseManifest(json.RawMessage(r2))
	if h1 == base || h1 == h2 {
		t.Errorf("requires must be hashed: base=%s h1=%s h2=%s", base, h1, h2)
	}
}

type fakeMgr struct {
	present            map[string]bool
	installs, removals []string
	fail               error
}

func (f *fakeMgr) Present(_ context.Context, id string) bool { return f.present[id] }
func (f *fakeMgr) Install(_ context.Context, id string) error {
	if f.fail != nil {
		return f.fail
	}
	f.installs = append(f.installs, id)
	f.present[id] = true
	return nil
}
func (f *fakeMgr) Uninstall(_ context.Context, id string) error {
	f.removals = append(f.removals, id)
	delete(f.present, id)
	return nil
}

func newTestPackages(t *testing.T, m *fakeMgr) *Packages {
	p := &Packages{Dir: t.TempDir(), Managers: map[string]Manager{"relay": m}, Sync: true, Now: time.Now}
	return p
}

var aw = []Require{{Manager: "relay", ID: "activitywatch", Why: "w"}}

func TestLedgerRefcountAndUninstall(t *testing.T) {
	m := &fakeMgr{present: map[string]bool{}}
	p := newTestPackages(t, m)
	if _, err := p.Require("a", aw); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Require("b", aw); err != nil {
		t.Fatal(err)
	}
	if len(m.installs) != 1 {
		t.Fatalf("installs = %v, want one", m.installs)
	}
	l := p.List()
	if len(l) != 1 || !l[0].InstalledByRelay || l[0].State != PkgInstalled || len(l[0].UsedBy) != 2 {
		t.Fatalf("ledger = %+v", l)
	}
	fi, err := os.Stat(filepath.Join(p.Dir, "packages.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("packages.json: %v %v", fi, err)
	}
	if err := p.Uninstall("relay", "activitywatch"); !errors.Is(err, ErrInUse) {
		t.Fatalf("in use: %v", err)
	}
	p.Release("a")
	if err := p.Uninstall("relay", "activitywatch"); !errors.Is(err, ErrInUse) {
		t.Fatalf("still used by b: %v", err)
	}
	p.Release("b")
	if len(p.List()[0].UsedBy) != 0 || len(m.removals) != 0 {
		t.Fatal("Release must not uninstall")
	}
	// Survives a restart.
	p2 := &Packages{Dir: p.Dir, Managers: p.Managers, Sync: true}
	if l := p2.List(); len(l) != 1 || l[0].State != PkgInstalled {
		t.Fatalf("reloaded = %+v", l)
	}
	if err := p2.Uninstall("relay", "activitywatch"); err != nil {
		t.Fatal(err)
	}
	if len(m.removals) != 1 || len(p2.List()) != 0 {
		t.Fatalf("removals=%v list=%+v", m.removals, p2.List())
	}
	if err := p2.Uninstall("relay", "activitywatch"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("gone: %v", err)
	}
}

func TestLedgerNeverUninstallsPreexisting(t *testing.T) {
	m := &fakeMgr{present: map[string]bool{"activitywatch": true}}
	p := newTestPackages(t, m)
	if _, err := p.Require("a", aw); err != nil {
		t.Fatal(err)
	}
	if len(m.installs) != 0 {
		t.Fatal("must not install what is present")
	}
	if l := p.List(); l[0].InstalledByRelay {
		t.Fatalf("installedByRelay should be false: %+v", l[0])
	}
	p.Release("a")
	if err := p.Uninstall("relay", "activitywatch"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("want ErrBadRequest, got %v", err)
	}
	if len(m.removals) != 0 {
		t.Fatal("uninstalled a preexisting package")
	}
}

func TestLedgerFailureAndConfirmHook(t *testing.T) {
	m := &fakeMgr{present: map[string]bool{}, fail: errors.New("boom")}
	p := newTestPackages(t, m)
	p.ConfirmInstall = func(string, []Require) bool { return false }
	if _, err := p.Require("a", aw); !errors.Is(err, ErrNotConfirmed) || len(p.List()) != 0 {
		t.Fatalf("denied: %v %+v", err, p.List())
	}
	p.ConfirmInstall = nil
	if _, err := p.Require("a", aw); err != nil {
		t.Fatal(err)
	}
	if l := p.List(); l[0].State != PkgFailed || l[0].Error != "boom" {
		t.Fatalf("failed = %+v", l[0])
	}
	if got := p.RequiresState(aw); got != "failed" {
		t.Errorf("requiresState = %s", got)
	}
	m.fail = nil // retry on next approve
	if _, err := p.Require("a", aw); err != nil {
		t.Fatal(err)
	}
	if got := p.RequiresState(aw); got != "ready" {
		t.Errorf("requiresState = %s", got)
	}
	if got := p.RequiresState(nil); got != "none" {
		t.Errorf("requiresState = %s", got)
	}
}

// ---- managers: exact argv through the exec seam ----

type call struct {
	name string
	args []string
}

func fakeExec(t *testing.T, fn func(c call) error) *[]call {
	t.Helper()
	var calls []call
	old := execRun
	execRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		c := call{name, args}
		calls = append(calls, c)
		if fn != nil {
			return nil, fn(c)
		}
		return nil, nil
	}
	t.Cleanup(func() { execRun = old })
	return &calls
}

func eq(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

func TestManagerArgv(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		mgr, id string
		want    []call // install sequence
	}{
		{"winget", "A.B", []call{{"winget", []string{"install", "--id", "A.B", "--exact", "--scope", "user", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}}}},
		{"brew", "foo", []call{{"brew", []string{"install", "--", "foo"}}}},
		{"flatpak", "org.x.App", []call{
			{"flatpak", []string{"remote-add", "--user", "--if-not-exists", "flathub", "https://dl.flathub.org/repo/flathub.flatpakrepo"}},
			{"flatpak", []string{"install", "--user", "-y", "--noninteractive", "flathub", "--", "org.x.App"}}}},
	}
	for _, c := range cases {
		calls := fakeExec(t, nil)
		m := newCmdManager(c.mgr, "/home")
		if err := m.Install(ctx, c.id); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != len(c.want) {
			t.Fatalf("%s: calls = %v", c.mgr, *calls)
		}
		for i, w := range c.want {
			if (*calls)[i].name != w.name || !eq((*calls)[i].args, w.args) {
				t.Errorf("%s install[%d] = %v, want %v", c.mgr, i, (*calls)[i], w)
			}
		}
	}
	present := map[string]call{
		"winget":  {"winget", []string{"list", "--id", "A.B", "--exact"}},
		"brew":    {"brew", []string{"list", "--", "A.B"}},
		"flatpak": {"flatpak", []string{"info", "--user", "--", "A.B"}},
	}
	for mgr, w := range present {
		calls := fakeExec(t, nil)
		if !newCmdManager(mgr, "/home").Present(ctx, "A.B") || len(*calls) != 1 || (*calls)[0].name != w.name || !eq((*calls)[0].args, w.args) {
			t.Errorf("%s present: %v", mgr, *calls)
		}
	}
	calls := fakeExec(t, func(call) error { return errors.New("no") })
	if newCmdManager("brew", "/h").Present(ctx, "x") {
		t.Error("present on exec failure")
	}
	_ = calls
}

func TestPipManagerCreatesVenv(t *testing.T) {
	home := t.TempDir()
	calls := fakeExec(t, nil)
	m := newCmdManager("pip", home)
	if err := m.Install(context.Background(), "requests==2.*"); err != nil {
		t.Fatal(err)
	}
	pip := filepath.Join(home, "venv", "bin", "pip")
	if runtime.GOOS == "windows" {
		pip = filepath.Join(home, "venv", "Scripts", "pip.exe")
	}
	py := "python3"
	if runtime.GOOS == "windows" {
		py = "python"
	}
	if len(*calls) != 2 || (*calls)[0].name != py || !eq((*calls)[0].args, []string{"-m", "venv", filepath.Join(home, "venv")}) ||
		(*calls)[1].name != pip || !eq((*calls)[1].args, []string{"install", "--", "requests==2.*"}) {
		t.Fatalf("calls = %v", *calls)
	}
	// venv exists → no second venv creation.
	_ = os.MkdirAll(filepath.Dir(pip), 0o755)
	_ = os.WriteFile(pip, nil, 0o755)
	*calls = nil
	_ = m.Install(context.Background(), "requests==2.*")
	m.Present(context.Background(), "requests==2.*")
	_ = m.Uninstall(context.Background(), "requests==2.*")
	if len(*calls) != 3 || !eq((*calls)[0].args, []string{"install", "--", "requests==2.*"}) ||
		!eq((*calls)[1].args, []string{"show", "--", "requests"}) || !eq((*calls)[2].args, []string{"uninstall", "-y", "--", "requests"}) {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestManagerTimeoutAndError(t *testing.T) {
	old := mgrTimeout
	mgrTimeout = 20 * time.Millisecond
	defer func() { mgrTimeout = old }()
	var deadline bool
	oldExec := execRun
	execRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		_, deadline = ctx.Deadline()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	defer func() { execRun = oldExec }()
	start := time.Now()
	if err := newCmdManager("brew", "/h").Install(context.Background(), "x"); err == nil || !deadline || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v deadline=%v", err, deadline)
	}
	if got := oneSentence("brew", []byte("warn\nError: no such formula.\n"), errors.New("exit 1")); got != "brew failed: Error: no such formula." {
		t.Errorf("sentence = %q", got)
	}
}

func TestLockPreventsConcurrentAWInstalls(t *testing.T) {
	var cur, max int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	s := &Sensors{Dir: t.TempDir(), Install: func(ctx context.Context, dir string) error {
		n := atomic.AddInt32(&cur, 1)
		if n > atomic.LoadInt32(&max) {
			atomic.StoreInt32(&max, n)
		}
		started <- struct{}{}
		<-release
		atomic.AddInt32(&cur, -1)
		return nil
	}}
	m := relayManager{s}
	done := make(chan error, 1)
	go func() { done <- m.Install(context.Background(), "activitywatch") }()
	<-started
	if err := s.StartInstall("activitywatch"); !errors.Is(err, ErrBusy) && runtime.GOOS != "darwin" {
		if err == nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("StartInstall during manager install = %v, want ErrBusy", err)
		}
	}
	second := make(chan error, 1)
	go func() { second <- m.Install(context.Background(), "activitywatch") }()
	select {
	case <-started:
		t.Fatal("second install started while the first holds the lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&max) != 1 {
		t.Fatalf("max concurrent installs = %d", max)
	}
}

func TestRelayUninstallStopsAW(t *testing.T) {
	calls := fakeExec(t, nil)
	s := &Sensors{Dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(s.Dir, "activitywatch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" {
		t.Skip("linux removal path")
	}
	if err := (relayManager{s}).Uninstall(context.Background(), "activitywatch"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != len(awProcs) || (*calls)[0].name != "pkill" || !eq((*calls)[0].args, []string{"-x", "aw-qt"}) {
		t.Fatalf("calls = %v", *calls)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "activitywatch")); err == nil {
		t.Fatal("dir not removed")
	}
}

func TestConfirmQueueAllowDeny(t *testing.T) {
	m := &fakeMgr{present: map[string]bool{}}
	p := newTestPackages(t, m)
	p.Confirm = NewConfirms()
	var declined []string
	p.OnDeclined = func(id string) { declined = append(declined, id) }
	if _, err := p.Require("a", aw); !errors.Is(err, ErrAwaitingConfirm) {
		t.Fatalf("err = %v", err)
	}
	if len(m.installs) != 0 || p.RequiresState(aw) != PkgAwaitingConfirm {
		t.Fatal("installed before confirm")
	}
	pend := p.Confirm.List()
	if len(pend) != 1 || len(pend[0].Token) != 32 || pend[0].CollectorID != "a" {
		t.Fatalf("pending = %+v", pend)
	}
	if err := p.Confirm.Decide(pend[0].Token, true); err != nil {
		t.Fatal(err)
	}
	if len(m.installs) != 1 || p.RequiresState(aw) != "ready" {
		t.Fatalf("installs=%v state=%s", m.installs, p.RequiresState(aw))
	}
	if err := p.Confirm.Decide(pend[0].Token, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replay = %v", err)
	}
	// deny
	p2 := newTestPackages(t, &fakeMgr{present: map[string]bool{}})
	p2.Confirm, p2.OnDeclined = NewConfirms(), p.OnDeclined
	_, _ = p2.Require("b", aw)
	_ = p2.Confirm.Decide(p2.Confirm.List()[0].Token, false)
	if len(declined) != 1 || declined[0] != "b" || len(p2.List()) != 0 {
		t.Fatalf("declined=%v list=%+v", declined, p2.List())
	}
	// already installed → no prompt
	if _, err := p.Require("c", aw); err != nil || len(p.Confirm.List()) != 0 {
		t.Fatalf("installed requires prompted: %v", err)
	}
}
