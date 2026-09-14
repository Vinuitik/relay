// Package selfupdate implements pull-based self-updating for the runner: it
// periodically checks GitHub Releases for a newer build, downloads the
// binary for its own OS/arch, verifies it against a published checksum, and
// atomically replaces itself - see runner/FLOWS.md "Self-update".
//
// Deliberately pull, not push: the alternative (a GitHub Actions job SSHing
// into the deployment machine) would require storing an SSH private key
// with access to that machine as a GitHub secret - a leaked secret there
// means remote code execution on someone's home server. Pull needs no
// inbound access and no secret on the GitHub side at all; the worst a
// compromised GitHub release can do is what any binary the runner already
// executes (docker compose, the configured CLI agent) could already do.
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// releasesURL lists releases newest-first; the runner tags its own releases
// "runner-<short-sha>" (see .github/workflows/runner-release.yml) so this
// filters out any unrelated tags (e.g. future Android releases) rather than
// trusting whatever the repo's single "latest" release happens to be.
const releasesURL = "https://api.github.com/repos/Vinuitik/relay/releases"

const tagPrefix = "runner-"

// devVersion is the version string a plain `go build` (no -X ldflags)
// produces. Self-update is a no-op for it - a developer's local build
// should never be silently overwritten by a GitHub release. Only binaries
// built by runner-release.yml (which sets this via -ldflags) have a real
// version and participate.
const devVersion = "dev"

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

// assetName is the release asset for this process's own OS/arch, matching
// the names runner-release.yml publishes. Empty for anything else
// (self-update silently does nothing on an unsupported platform - the
// existing build matrix is linux/amd64 and windows/amd64 only, same as
// install/install.sh).
func assetName() string {
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "relay-runner-linux-amd64"
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		return "relay-runner-windows-amd64.exe"
	default:
		return ""
	}
}

// CheckOnce looks for a newer tagged release than currentVersion and, if
// found, downloads + verifies + installs it over the running executable.
// Returns updated=true if it replaced the binary - the caller must exit
// promptly afterward (the old binary is still what's executing in memory;
// systemd's Restart=always, see install/relay-runner.service, brings the
// new one up). Returns updated=false, err=nil for "already current" or "no
// releases yet", which is the expected steady state, not a failure.
func CheckOnce(currentVersion string) (updated bool, err error) {
	if currentVersion == devVersion {
		return false, nil
	}
	asset := assetName()
	if asset == "" {
		return false, fmt.Errorf("selfupdate: unsupported platform %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	release, err := latestRunnerRelease()
	if err != nil {
		return false, fmt.Errorf("selfupdate: check latest release: %w", err)
	}
	if release == nil || release.TagName == currentVersion {
		return false, nil
	}

	binURL, sumURL := "", ""
	for _, a := range release.Assets {
		switch a.Name {
		case asset:
			binURL = a.BrowserDownloadURL
		case "checksums.txt":
			sumURL = a.BrowserDownloadURL
		}
	}
	if binURL == "" {
		return false, fmt.Errorf("selfupdate: release %s has no asset %q", release.TagName, asset)
	}

	binBytes, err := download(binURL)
	if err != nil {
		return false, fmt.Errorf("selfupdate: download %s: %w", asset, err)
	}

	if sumURL != "" {
		if err := verifyChecksum(binBytes, sumURL, asset); err != nil {
			return false, fmt.Errorf("selfupdate: %w", err)
		}
	}

	if err := install(binBytes); err != nil {
		return false, fmt.Errorf("selfupdate: install: %w", err)
	}
	return true, nil
}

// RunPeriodically calls CheckOnce every interval until stop is closed,
// logging (via logf) whatever it finds. Runs in its own goroutine; the
// caller is expected to os.Exit shortly after logf reports an update, same
// as the startup check in cmd/runnerd/main.go.
func RunPeriodically(currentVersion string, interval time.Duration, stop <-chan struct{}, logf func(format string, args ...any)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			updated, err := CheckOnce(currentVersion)
			if err != nil {
				logf("selfupdate: check failed: %v", err)
				continue
			}
			if updated {
				logf("selfupdate: installed a new version, exiting for systemd to restart into it")
				os.Exit(0)
			}
		}
	}
}

func latestRunnerRelease() (*ghRelease, error) {
	req, err := http.NewRequest(http.MethodGet, releasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	var releases []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, err
	}
	// GitHub returns newest-first; take the first one tagged for the runner
	// rather than releases[0], which could be an unrelated release type.
	for i := range releases {
		if strings.HasPrefix(releases[i].TagName, tagPrefix) {
			return &releases[i], nil
		}
	}
	return nil, nil
}

// download is a var (not a plain func) so tests can swap it out - see
// selfupdate_test.go - without standing up a real HTTP server for every case.
var download = func(url string) ([]byte, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// verifyChecksum downloads checksums.txt (plain `sha256sum` output format:
// "<hex>  <filename>" per line) and confirms binBytes matches the line for
// assetName. A release with no matching line, or that fails to parse, is
// treated as a verification failure - fail closed rather than install an
// unverified binary.
func verifyChecksum(binBytes []byte, sumURL, assetName string) error {
	sumBytes, err := download(sumURL)
	if err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}
	var want string
	for _, line := range strings.Split(string(sumBytes), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == assetName {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum entry for %s in checksums.txt", assetName)
	}
	sum := sha256.Sum256(binBytes)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, got, want)
	}
	return nil
}

// install writes the new binary to a temp file in the same directory as the
// currently-running executable (so the final rename is same-filesystem,
// hence atomic) and renames it over the running binary. On Linux this
// works even while the old binary is executing - the running process keeps
// its already-open inode until it exits, per the self-update-by-rename
// convention many long-running Linux daemons use. On Windows this can fail
// with "file in use" since NTFS commonly locks a running executable's file
// - see runner/FLOWS.md "Self-update" Technology notes; Windows is not
// this feature's primary target (the Linux server is), so a failure here
// just means CheckOnce returns an error and tries again next interval.
func install(binBytes []byte) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, ".relay-runner-update-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(binBytes); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
