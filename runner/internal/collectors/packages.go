package collectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Package ledger: which packages a collector's manifest "requires" are
// installed on this machine, which of them Relay installed (only those may
// ever be uninstalled) and which collectors use them (refcount). Persisted
// at $RELAY_HOME/packages.json (0600). Package managers sit behind Manager;
// only "relay" (ActivityWatch via sensors.go) exists so far.

var (
	// ErrInUse: uninstall refused, collectors still use the package → 409.
	ErrInUse = errors.New("package is used by collectors")
	// ErrNotConfirmed: the user denied the install → 403.
	ErrNotConfirmed = errors.New("install not confirmed")
	// ErrAwaitingConfirm: Require queued the install for the local confirm
	// page; nothing is installed yet → approve answers 202 awaiting_confirm.
	ErrAwaitingConfirm = errors.New("install awaiting local confirmation")
)

// Manager installs packages through one package manager.
type Manager interface {
	// Present: is the package installed on this machine, by anyone.
	Present(ctx context.Context, id string) bool
	Install(ctx context.Context, id string) error
	Uninstall(ctx context.Context, id string) error
}

// Package states in PackageInfo.State.
const (
	PkgInstalled       = "installed"
	PkgInstalling      = "installing"
	PkgFailed          = "failed"
	PkgAwaitingConfirm = "awaiting_confirm"
)

// Entry is one ledger row, exactly the packages.json shape.
type Entry struct {
	Manager          string   `json:"manager"`
	ID               string   `json:"id"`
	InstalledAt      string   `json:"installedAt"`
	InstalledByRelay bool     `json:"installedByRelay"`
	UsedBy           []string `json:"usedBy"`
}

// PackageInfo is the GET /v1/packages row.
type PackageInfo struct {
	Entry
	State string `json:"state"`
	Error string `json:"error"`
}

type pkg struct {
	Entry
	state, err string
	busy       bool // uninstall running
}

// Packages is the ledger plus the managers.
type Packages struct {
	Dir      string             // $RELAY_HOME
	Managers map[string]Manager // by manifest manager name
	// ConfirmInstall is THE hook for the local confirm gate (agent ⑥): called
	// before a collector approve installs anything; false → ErrNotConfirmed.
	// nil = allow (no gate yet).
	ConfirmInstall func(collectorID string, reqs []Require) bool
	// Confirm is the local confirm queue (confirm.go): unmet requires wait
	// there for the person at this computer. nil = no queue (install at once).
	Confirm *Confirms
	// OnDeclined is called when the local confirm was denied (the engine
	// marks the collector failed).
	OnDeclined func(collectorID string)
	Now        func() time.Time
	Sync       bool // run installs/uninstalls inline (tests)

	mu     sync.Mutex
	loaded bool
	pkgs   []*pkg
}

// NewPackages wires the built-in "relay" manager to sensors.
func NewPackages(home string, s *Sensors) *Packages {
	p := &Packages{Dir: home, Now: time.Now, Managers: map[string]Manager{"relay": relayManager{s}}}
	for _, name := range AvailableManagers() {
		if m := newCmdManager(name, home); m != nil {
			p.Managers[name] = m
		}
	}
	return p
}

func (p *Packages) path() string { return filepath.Join(p.Dir, "packages.json") }

func (p *Packages) load() {
	if p.loaded {
		return
	}
	p.loaded = true
	var es []Entry
	if b, err := os.ReadFile(p.path()); err == nil {
		_ = json.Unmarshal(b, &es)
	}
	for _, e := range es {
		if e.UsedBy == nil {
			e.UsedBy = []string{}
		}
		p.pkgs = append(p.pkgs, &pkg{Entry: e, state: PkgInstalled})
	}
}

// save writes installed rows atomically (tmp + rename), 0600. Caller holds mu.
func (p *Packages) save() error {
	es := []Entry{}
	for _, k := range p.pkgs {
		if k.state == PkgInstalled {
			es = append(es, k.Entry)
		}
	}
	b, err := json.MarshalIndent(es, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	tmp := p.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path())
}

func (p *Packages) find(manager, id string) *pkg {
	for _, k := range p.pkgs {
		if k.Manager == manager && k.ID == id {
			return k
		}
	}
	return nil
}

func (p *Packages) drop(k *pkg) {
	for i, x := range p.pkgs {
		if x == k {
			p.pkgs = append(p.pkgs[:i], p.pkgs[i+1:]...)
			return
		}
	}
}

func addUser(k *pkg, collector string) {
	for _, u := range k.UsedBy {
		if u == collector {
			return
		}
	}
	k.UsedBy = append(k.UsedBy, collector)
}

// List is GET /v1/packages.
func (p *Packages) List() []PackageInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	out := make([]PackageInfo, 0, len(p.pkgs))
	for _, k := range p.pkgs {
		e := k.Entry
		e.UsedBy = append([]string{}, e.UsedBy...)
		out = append(out, PackageInfo{Entry: e, State: k.state, Error: k.err})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Manager+"/"+out[i].ID < out[j].Manager+"/"+out[j].ID
	})
	return out
}

// Require is the approve hook: record collectorID as a user of every req
// and install what is missing. Returns pending=true while any install is
// still running (always false with Sync). To change the confirm gate:
// Packages.ConfirmInstall.
func (p *Packages) Require(collectorID string, reqs []Require) (pending bool, err error) {
	if len(reqs) == 0 {
		return false, nil
	}
	for _, r := range reqs {
		if p.Managers[r.Manager] == nil {
			return false, ErrUnsupported
		}
	}
	if unmet := p.unmet(reqs); len(unmet) > 0 {
		if p.ConfirmInstall != nil && !p.ConfirmInstall(collectorID, unmet) {
			return false, ErrNotConfirmed
		}
		if p.Confirm != nil {
			p.Confirm.Request(collectorID, unmet, func(allow bool) {
				if !allow {
					if p.OnDeclined != nil {
						p.OnDeclined(collectorID)
					}
					return
				}
				_, _ = p.install(collectorID, reqs)
			})
			return true, ErrAwaitingConfirm
		}
	}
	return p.install(collectorID, reqs)
}

// unmet: the reqs that are neither installed nor installing in the ledger.
func (p *Packages) unmet(reqs []Require) []Require {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	var out []Require
	for _, r := range reqs {
		if k := p.find(r.Manager, r.ID); k == nil || k.state == PkgFailed {
			out = append(out, r)
		}
	}
	return out
}

// install records collectorID as a user of every req and installs what is
// missing (the part of Require after the confirm gate).
func (p *Packages) install(collectorID string, reqs []Require) (pending bool, err error) {
	for _, r := range reqs {
		p.mu.Lock()
		p.load()
		k := p.find(r.Manager, r.ID)
		switch {
		case k != nil && k.state == PkgInstalled:
			addUser(k, collectorID)
			err = p.save()
			p.mu.Unlock()
			if err != nil {
				return false, err
			}
			continue
		case k != nil && k.state == PkgInstalling:
			addUser(k, collectorID)
			p.mu.Unlock()
			pending = true
			continue
		case k == nil:
			k = &pkg{Entry: Entry{Manager: r.Manager, ID: r.ID, UsedBy: []string{}}}
			p.pkgs = append(p.pkgs, k)
		}
		// new or retrying a failed one
		addUser(k, collectorID)
		k.state, k.err = PkgInstalling, ""
		p.mu.Unlock()
		if p.Sync {
			p.runInstall(k)
		} else {
			go p.runInstall(k)
			pending = true
		}
	}
	return pending, nil
}

// runInstall: a package that is already there is recorded as not ours
// (installedByRelay=false → never uninstalled); otherwise install it.
func (p *Packages) runInstall(k *pkg) {
	m := p.Managers[k.Manager]
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	present := m.Present(ctx, k.ID)
	var err error
	if !present {
		err = m.Install(ctx, k.ID)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		k.state, k.err = PkgFailed, err.Error()
		return
	}
	k.state, k.err = PkgInstalled, ""
	k.InstalledByRelay = !present
	k.InstalledAt = p.Now().UTC().Format(time.RFC3339)
	if e := p.save(); e != nil {
		k.err = e.Error()
	}
}

// Release removes collectorID from every usedBy (collector DELETE). Never
// uninstalls.
func (p *Packages) Release(collectorID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	for _, k := range append([]*pkg{}, p.pkgs...) {
		out := k.UsedBy[:0]
		for _, u := range k.UsedBy {
			if u != collectorID {
				out = append(out, u)
			}
		}
		k.UsedBy = out
		if len(out) == 0 && k.state == PkgFailed {
			p.drop(k)
		}
	}
	_ = p.save()
}

// Uninstall removes a package Relay installed and nobody uses. Errors:
// ErrNotFound; ErrBadRequest (not installed by Relay, checked first);
// ErrInUse. The removal itself is async (Sync: inline); a failure shows as
// state "failed" with error.
func (p *Packages) Uninstall(manager, id string) error {
	p.mu.Lock()
	p.load()
	k := p.find(manager, id)
	switch {
	case k == nil || k.state == PkgInstalling:
		p.mu.Unlock()
		return ErrNotFound
	case !k.InstalledByRelay:
		p.mu.Unlock()
		return badf("%s/%s was not installed by Relay; remove it yourself", manager, id)
	case len(k.UsedBy) > 0:
		p.mu.Unlock()
		return ErrInUse
	case k.busy:
		p.mu.Unlock()
		return ErrBusy
	}
	k.busy = true
	p.mu.Unlock()
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		err := p.Managers[manager].Uninstall(ctx, id)
		p.mu.Lock()
		defer p.mu.Unlock()
		k.busy = false
		if err != nil {
			k.state, k.err = PkgFailed, err.Error()
			return
		}
		p.drop(k)
		_ = p.save()
	}
	if p.Sync {
		run()
	} else {
		go run()
	}
	return nil
}

// RequiresState is the "requiresState" of a collector with these requires.
func (p *Packages) RequiresState(reqs []Require) string {
	if len(reqs) == 0 {
		return "none"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	state := "ready"
	for _, r := range reqs {
		k := p.find(r.Manager, r.ID)
		switch {
		case k == nil:
			if state == "ready" {
				state = PkgAwaitingConfirm
			}
		case k.state == PkgFailed:
			return "failed"
		case k.state == PkgInstalling:
			state = PkgInstalling
		}
	}
	return state
}

// Requires returns collector id's current manifest requires.
func (e *Engine) Requires(id string) ([]Require, error) {
	e.init()
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.st.load(id)
	if err != nil {
		return nil, err
	}
	mf, err := e.st.manifest(m)
	return mf.Requires, err
}

// AvailableManagers: "relay" always, plus the package managers found on PATH.
func AvailableManagers() []string {
	out := []string{"relay"}
	have := func(names ...string) bool {
		for _, n := range names {
			if _, err := exec.LookPath(n); err == nil {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		name string
		bins []string
	}{{"winget", []string{"winget"}}, {"brew", []string{"brew"}}, {"flatpak", []string{"flatpak"}}, {"pip", []string{"pip", "pip3"}}} {
		if have(c.bins...) {
			out = append(out, c.name)
		}
	}
	return out
}

// relayManager: the built-in installers (only "activitywatch"). Install and
// Uninstall take the same lock as Sensors.StartInstall (Sensors.acquire), so
// two AW installs never run at once.
type relayManager struct{ s *Sensors }

func (m relayManager) Present(ctx context.Context, id string) bool {
	return id == "activitywatch" && m.s.State(ctx).ActivityWatch.Installed
}

func (m relayManager) Install(ctx context.Context, id string) error {
	if id != "activitywatch" {
		return badf("relay has no package %q", id)
	}
	release, err := m.s.acquire(ctx, true)
	if err != nil {
		return err
	}
	err = m.s.Install(ctx, m.s.Dir)
	release(err)
	return err
}

// awProcs are the processes ActivityWatch leaves running.
var awProcs = []string{"aw-qt", "aw-server", "aw-server-rust", "aw-watcher-window", "aw-watcher-afk"}

// Uninstall stops the running ActivityWatch processes, then removes what
// the install put there: Linux dir + autostart entry; Windows the winget
// package or the Inno uninstaller, the HKCU Run value and our sensors dir.
func (m relayManager) Uninstall(ctx context.Context, id string) error {
	if id != "activitywatch" {
		return badf("relay has no package %q", id)
	}
	release, err := m.s.acquire(ctx, true)
	if err != nil {
		return err
	}
	err = m.uninstall(ctx)
	release(err)
	return err
}

func (m relayManager) uninstall(ctx context.Context) error {
	stopAW(ctx)
	if runtime.GOOS == "windows" {
		return uninstallWindows(ctx, m.s.Dir)
	}
	if runtime.GOOS != "linux" {
		return ErrUnsupported
	}
	if err := os.RemoveAll(filepath.Join(m.s.Dir, "activitywatch")); err != nil {
		return err
	}
	if cfg, err := os.UserConfigDir(); err == nil {
		_ = os.Remove(filepath.Join(cfg, "autostart", "relay-activitywatch.desktop"))
	}
	return nil
}

// stopAW kills the AW processes by exact name (best effort: none running is fine).
func stopAW(ctx context.Context) {
	for _, n := range awProcs {
		if runtime.GOOS == "windows" {
			_, _ = execRun(ctx, "taskkill", "/F", "/T", "/IM", n+".exe")
		} else {
			_, _ = execRun(ctx, "pkill", "-x", n)
		}
	}
}

func uninstallWindows(ctx context.Context, dir string) error {
	var first error
	if _, err := exec.LookPath("winget"); err == nil {
		_, first = execRun(ctx, "winget", "uninstall", "--id", "ActivityWatch.ActivityWatch", "--exact", "--silent", "--disable-interactivity")
	} else {
		first = err
	}
	if first != nil {
		if u := awUninstaller(); u != "" {
			if _, err := execRun(ctx, u, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART"); err == nil {
				first = nil
			}
		}
	}
	_, _ = execRun(ctx, "reg", "delete", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "ActivityWatch", "/f")
	_ = os.RemoveAll(filepath.Join(dir, "activitywatch"))
	return first
}

// awUninstaller: the Inno Setup uninstaller of a per-user setup.exe install.
func awUninstaller() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return ""
	}
	u := filepath.Join(base, "Programs", "ActivityWatch", "unins000.exe")
	if _, err := os.Stat(u); err != nil {
		return ""
	}
	return u
}

// ---- command-line package managers (winget, brew, flatpak, pip) ----

// mgrTimeout bounds every package-manager command.
var mgrTimeout = 10 * time.Minute

// execRun runs name with separate args (never a shell) and returns stdout.
// A failure becomes a one-sentence error from stderr. Tests replace it.
var execRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return stdout.Bytes(), fmt.Errorf("%s timed out or was cancelled", name)
		}
		return stdout.Bytes(), errors.New(oneSentence(name, stderr.Bytes(), err))
	}
	return stdout.Bytes(), nil
}

// oneSentence: "<tool> failed: <last stderr line>" (<= 200 chars).
func oneSentence(name string, stderr []byte, err error) string {
	line := ""
	for _, l := range strings.Split(strings.TrimSpace(string(stderr)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
		}
	}
	if line == "" {
		line = err.Error()
	}
	if len(line) > 200 {
		line = line[:200]
	}
	return name + " failed: " + strings.TrimRight(line, ".") + "."
}

// cmdManager drives one CLI package manager; argv builders are pure so
// tests assert them exactly.
type cmdManager struct {
	name string
	home string // $RELAY_HOME (pip venv)
}

func newCmdManager(name, home string) Manager {
	switch name {
	case "winget", "brew", "flatpak", "pip":
		return cmdManager{name, home}
	}
	return nil
}

func (m cmdManager) venv() string { return filepath.Join(m.home, "venv") }

func (m cmdManager) pipBin() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(m.venv(), "Scripts", "pip.exe")
	}
	return filepath.Join(m.venv(), "bin", "pip")
}

// pipName strips a version specifier / extras from a pip requirement.
func pipName(id string) string {
	if i := strings.IndexAny(id, "=<>!~[ ;"); i > 0 {
		return id[:i]
	}
	return id
}

// argv returns the command for op ("present", "install", "uninstall").
func (m cmdManager) argv(op, id string) (string, []string) {
	switch m.name {
	case "winget":
		switch op {
		case "present":
			return "winget", []string{"list", "--id", id, "--exact"}
		case "install":
			return "winget", []string{"install", "--id", id, "--exact", "--scope", "user", "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}
		}
		return "winget", []string{"uninstall", "--id", id, "--exact", "--silent", "--disable-interactivity"}
	case "brew":
		return "brew", []string{map[string]string{"present": "list", "install": "install", "uninstall": "uninstall"}[op], "--", id}
	case "flatpak":
		switch op {
		case "present":
			return "flatpak", []string{"info", "--user", "--", id}
		case "install":
			return "flatpak", []string{"install", "--user", "-y", "--noninteractive", "flathub", "--", id}
		}
		return "flatpak", []string{"uninstall", "--user", "-y", "--noninteractive", "--", id}
	}
	// pip
	switch op {
	case "present":
		return m.pipBin(), []string{"show", "--", pipName(id)}
	case "install":
		return m.pipBin(), []string{"install", "--", id}
	}
	return m.pipBin(), []string{"uninstall", "-y", "--", pipName(id)}
}

func (m cmdManager) Present(ctx context.Context, id string) bool {
	ctx, cancel := context.WithTimeout(ctx, mgrTimeout)
	defer cancel()
	name, args := m.argv("present", id)
	_, err := execRun(ctx, name, args...)
	return err == nil
}

func (m cmdManager) Install(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, mgrTimeout)
	defer cancel()
	switch m.name {
	case "flatpak":
		if _, err := execRun(ctx, "flatpak", "remote-add", "--user", "--if-not-exists", "flathub", "https://dl.flathub.org/repo/flathub.flatpakrepo"); err != nil {
			return err
		}
	case "pip":
		if _, err := os.Stat(m.pipBin()); err != nil {
			py := "python3"
			if runtime.GOOS == "windows" {
				py = "python"
			}
			if _, err := execRun(ctx, py, "-m", "venv", m.venv()); err != nil {
				return err
			}
		}
	}
	name, args := m.argv("install", id)
	_, err := execRun(ctx, name, args...)
	return err
}

func (m cmdManager) Uninstall(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, mgrTimeout)
	defer cancel()
	name, args := m.argv("uninstall", id)
	_, err := execRun(ctx, name, args...)
	return err
}
