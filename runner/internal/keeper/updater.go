package keeper

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Release is the subset of GitHub's release JSON keeperd uses.
type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

// Asset is one downloadable release file.
type Asset struct {
	Name string `json:"name"`
	// URL is the API URL (works for private repos with a token and
	// Accept: application/octet-stream); BrowserURL the public one.
	URL        string `json:"url"`
	BrowserURL string `json:"browser_download_url"`
}

// RunnerStatus is what the updater needs from GET /v1/runner/info.
type RunnerStatus struct {
	Busy      bool   `json:"busy"`
	Build     string `json:"build"`
	IdleSince string `json:"idleSince"`
}

// Updater checks for and applies releases. The fields are injectable so
// tests can point it at fakes.
type Updater struct {
	Cfg        Config
	Version    string // this keeperd's build tag ("dev" if unstamped)
	Supervisor *Supervisor
	HTTP       *http.Client
	APIBase    string                        // https://api.github.com
	Status     func() (RunnerStatus, error)  // queries the running runner
	Health     func() bool                   // GET /v1/health succeeded
	Relaunch   func(keeperPath string) error // hand over to a new keeperd
	Now        func() time.Time
	// HealthTimeout/MinUptime: how long a restarted runner gets to answer
	// /v1/health, and how long it must stay up to count as healthy
	// (defaults 60s / 10s).
	HealthTimeout, MinUptime time.Duration
}

// latest fetches the newest release.
func (u *Updater) latest() (*Release, error) {
	req, err := http.NewRequest(http.MethodGet, u.APIBase+"/repos/"+u.Cfg.Repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if u.Cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.Cfg.Token)
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("latest release: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var r Release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	return &r, nil
}

func (r *Release) asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// download fetches asset into path, verifying it against the release's
// SHA256SUMS file.
func (u *Updater) download(r *Release, name, path string) error {
	a, ok := r.asset(name)
	if !ok {
		return fmt.Errorf("release %s has no asset %s", r.Tag, name)
	}
	sums, ok := r.asset("SHA256SUMS")
	if !ok {
		return fmt.Errorf("release %s has no SHA256SUMS", r.Tag)
	}
	sumsBody, err := u.fetch(sums)
	if err != nil {
		return err
	}
	want, err := findSum(sumsBody, name)
	if err != nil {
		return err
	}
	body, err := u.fetch(a)
	if err != nil {
		return err
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("%s: checksum mismatch", name)
	}
	if err := os.WriteFile(path, body, 0o755); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func (u *Updater) fetch(a Asset) ([]byte, error) {
	url := a.BrowserURL
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if u.Cfg.Token != "" && a.URL != "" {
		// Private repos: browser URLs need a session, the API URL takes the token.
		req, err = http.NewRequest(http.MethodGet, a.URL, nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+u.Cfg.Token)
			req.Header.Set("Accept", "application/octet-stream")
		}
	}
	if err != nil {
		return nil, err
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", a.Name, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// findSum returns the hex sha256 for name from `sha256sum`-format output.
func findSum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no entry for %s", name)
}

// Bootstrap makes sure there's a runner binary and an adapter before the
// first start - a fresh install downloads the latest release right away.
func (u *Updater) Bootstrap() {
	if _, err := os.Stat(u.Cfg.RunnerPath()); err != nil {
		log.Printf("keeper: no runner at %s, installing latest release", u.Cfg.RunnerPath())
		if err := os.MkdirAll(dirOf(u.Cfg.RunnerPath()), 0o755); err != nil {
			log.Printf("keeper: %v", err)
		}
		r, err := u.latest()
		if err == nil {
			err = u.download(r, assetName("relay-runner"), u.Cfg.RunnerPath())
		}
		if err != nil {
			log.Printf("keeper: bootstrap runner: %v", err)
		}
	}
	if _, err := os.Stat(u.Cfg.AdapterBin()); err != nil {
		u.updateAdapter()
	}
}

// ErrNotIdle means an update is waiting for the runner to go quiet.
var ErrNotIdle = errors.New("runner not idle long enough")

// Check runs one update pass: keeperd itself first (its successor then
// updates the runner), then the runner binary, then the adapter. Runner
// restarts only happen once it has been idle for Cfg.Quiet.
func (u *Updater) Check() error {
	r, err := u.latest()
	if err != nil {
		return err
	}
	st, statusErr := u.Status()
	// A runner that doesn't answer (crash-looping, or an old build without
	// a working API) gets the latest release without waiting for idle -
	// there's no work of its to interrupt.
	runnerOutdated := statusErr != nil || st.Build != r.Tag
	keeperOutdated := u.Version != r.Tag && u.Version != "dev"
	if !runnerOutdated && !keeperOutdated {
		u.updateAdapter()
		return nil
	}
	if statusErr == nil && !u.quiet(st) {
		return ErrNotIdle
	}
	u.updateAdapter()

	if keeperOutdated {
		return u.replaceKeeper(r)
	}
	return u.replaceRunner(r)
}

// quiet reports whether the runner has been idle for at least Cfg.Quiet.
func (u *Updater) quiet(st RunnerStatus) bool {
	if st.Busy {
		return false
	}
	since, err := time.Parse(time.RFC3339, st.IdleSince)
	if err != nil {
		return false
	}
	return u.Now().Sub(since) >= u.Cfg.Quiet
}

// replaceRunner swaps in r's runner binary and restarts it, rolling back
// to the previous binary if the new one doesn't come up healthy.
func (u *Updater) replaceRunner(r *Release) error {
	path := u.Cfg.RunnerPath()
	newPath, oldPath := path+".new", path+".old"
	if err := u.download(r, assetName("relay-runner"), newPath); err != nil {
		return err
	}
	log.Printf("keeper: updating runner to %s", r.Tag)
	u.Supervisor.Stop()
	_ = os.Remove(oldPath)
	if err := os.Rename(path, oldPath); err != nil && !os.IsNotExist(err) {
		u.Supervisor.Start()
		return fmt.Errorf("move old runner aside: %w", err)
	}
	if err := os.Rename(newPath, path); err != nil {
		_ = os.Rename(oldPath, path)
		u.Supervisor.Start()
		return fmt.Errorf("install new runner: %w", err)
	}
	u.Supervisor.Start()
	if u.healthy() {
		log.Printf("keeper: runner %s is up", r.Tag)
		return nil
	}
	log.Printf("keeper: runner %s failed to come up - rolling back", r.Tag)
	u.Supervisor.Stop()
	_ = os.Remove(path)
	if err := os.Rename(oldPath, path); err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	u.Supervisor.Start()
	return fmt.Errorf("runner %s unhealthy, rolled back", r.Tag)
}

// healthy waits for the restarted runner to answer /v1/health and stay up.
func (u *Updater) healthy() bool {
	timeout, minUp := u.HealthTimeout, u.MinUptime
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	if minUp == 0 {
		minUp = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if u.Health() && u.Supervisor.Uptime() >= minUp {
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}

// replaceKeeper installs r's keeperd over the running binary (renaming a
// running executable is allowed on both Linux and Windows) and hands over.
func (u *Updater) replaceKeeper(r *Release) error {
	path := u.Cfg.KeeperPath()
	newPath, oldPath := path+".new", path+".old"
	if err := u.download(r, assetName("keeperd"), newPath); err != nil {
		return err
	}
	log.Printf("keeper: updating keeperd %s -> %s", u.Version, r.Tag)
	_ = os.Remove(oldPath)
	if err := os.Rename(path, oldPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("move old keeperd aside: %w", err)
	}
	if err := os.Rename(newPath, path); err != nil {
		_ = os.Rename(oldPath, path)
		return fmt.Errorf("install new keeperd: %w", err)
	}
	u.Supervisor.Stop()
	return u.Relaunch(path)
}

// updateAdapter installs/updates the ACP adapter into Cfg.AdapterDir via
// npm (skipped with a log line if npm isn't available).
func (u *Updater) updateAdapter() {
	npm, err := exec.LookPath("npm")
	if err != nil {
		log.Printf("keeper: npm not found, not managing the ACP adapter")
		return
	}
	if err := os.MkdirAll(u.Cfg.AdapterDir(), 0o755); err != nil {
		log.Printf("keeper: %v", err)
		return
	}
	cmd := exec.Command(npm, "install", "--prefix", u.Cfg.AdapterDir(), "--no-fund", "--no-audit", u.Cfg.AdapterPackage+"@latest")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("keeper: npm install adapter: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func dirOf(path string) string {
	i := strings.LastIndexAny(path, `/\`)
	if i < 0 {
		return "."
	}
	return path[:i]
}
