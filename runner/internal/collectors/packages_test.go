package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
