package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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
	Now            func() time.Time
	Sync           bool // run installs/uninstalls inline (tests)

	mu     sync.Mutex
	loaded bool
	pkgs   []*pkg
}

// NewPackages wires the built-in "relay" manager to sensors.
func NewPackages(home string, s *Sensors) *Packages {
	return &Packages{Dir: home, Now: time.Now, Managers: map[string]Manager{"relay": relayManager{s}}}
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
	if p.ConfirmInstall != nil && !p.ConfirmInstall(collectorID, reqs) {
		return false, ErrNotConfirmed
	}
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

// relayManager: the built-in installers (only "activitywatch").
type relayManager struct{ s *Sensors }

func (m relayManager) Present(ctx context.Context, id string) bool {
	return id == "activitywatch" && m.s.State(ctx).ActivityWatch.Installed
}

func (m relayManager) Install(ctx context.Context, id string) error {
	if id != "activitywatch" {
		return badf("relay has no package %q", id)
	}
	return m.s.Install(ctx, m.s.Dir)
}

// Uninstall removes what installLinux put there (dir + autostart entry).
func (m relayManager) Uninstall(ctx context.Context, id string) error {
	if id != "activitywatch" {
		return badf("relay has no package %q", id)
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
