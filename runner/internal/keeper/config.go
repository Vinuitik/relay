// Package keeper implements keeperd: the always-on process that keeps the
// runner running and up to date - the process-level counterpart of wakerd
// (wakerd wakes machines, keeperd (re)starts runnerd). It
//   - runs runnerd as its child and restarts it if it dies (Supervisor),
//   - checks GitHub Releases for a newer build and swaps the runner binary
//     in only when the runner has been idle for a while, rolling back if the
//     new one won't start (Updater),
//   - keeps the Claude ACP adapter installed/updated in its own directory,
//   - replaces itself when a newer keeperd is released.
//
// See runner/FLOWS.md "keeperd".
package keeper

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Config is keeperd's configuration, all from env vars (the same env the
// runner child inherits - /etc/relay/runner.env on Linux, the scheduled
// task's launcher script on Windows).
type Config struct {
	// Dir is the install dir: bin/ holds relay-runner and keeperd, acp/ the
	// adapter. RELAY_KEEPER_DIR; default /opt/relay (Linux) or
	// %LOCALAPPDATA%\Relay (Windows).
	Dir string
	// Repo is the GitHub repo releases come from (RELAY_UPDATE_REPO).
	Repo string
	// Token authenticates GitHub API calls - only needed once the repo is
	// private (RELAY_GITHUB_TOKEN, fine-grained, Contents read-only).
	Token string
	// CheckEvery is the release check interval (RELAY_UPDATE_INTERVAL).
	CheckEvery time.Duration
	// Quiet is how long the runner must have been idle before an update
	// restarts it (RELAY_UPDATE_QUIET).
	Quiet time.Duration
	// Disabled turns off all updating; supervision still runs
	// (RELAY_UPDATE_DISABLED=true).
	Disabled bool
	// AdapterPackage is the npm package installed into Dir/acp.
	AdapterPackage string
}

// LoadConfig reads Config from the environment.
func LoadConfig() Config {
	c := Config{
		Dir:            os.Getenv("RELAY_KEEPER_DIR"),
		Repo:           envOr("RELAY_UPDATE_REPO", "Vinuitik/relay"),
		Token:          strings.TrimSpace(os.Getenv("RELAY_GITHUB_TOKEN")),
		CheckEvery:     envDuration("RELAY_UPDATE_INTERVAL", 6*time.Hour),
		Quiet:          envDuration("RELAY_UPDATE_QUIET", 15*time.Minute),
		Disabled:       os.Getenv("RELAY_UPDATE_DISABLED") == "true",
		AdapterPackage: "@agentclientprotocol/claude-agent-acp",
	}
	if c.Dir == "" {
		c.Dir = defaultDir()
	}
	return c
}

func defaultDir() string {
	if runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			return filepath.Join(la, "Relay")
		}
	}
	return "/opt/relay"
}

// exe appends .exe on Windows.
func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// RunnerPath is where the runner binary lives.
func (c Config) RunnerPath() string { return filepath.Join(c.Dir, "bin", exe("relay-runner")) }

// KeeperPath is where keeperd's own binary lives.
func (c Config) KeeperPath() string { return filepath.Join(c.Dir, "bin", exe("keeperd")) }

// AdapterDir is the npm prefix the ACP adapter is installed into.
func (c Config) AdapterDir() string { return filepath.Join(c.Dir, "acp") }

// AdapterBin is the installed adapter's launcher (npm's .cmd shim on Windows).
func (c Config) AdapterBin() string {
	name := "claude-agent-acp"
	if runtime.GOOS == "windows" {
		name += ".cmd"
	}
	return filepath.Join(c.AdapterDir(), "node_modules", ".bin", name)
}

// assetName is the release asset for binary on this platform, e.g.
// "relay-runner-linux-amd64" or "keeperd-windows-amd64.exe".
func assetName(binary string) string {
	return exe(binary + "-" + runtime.GOOS + "-" + runtime.GOARCH)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// LoadEnvFile applies KEY=VALUE lines from path (# comments allowed) to the
// process environment, without overriding variables already set. It's how
// the Windows scheduled task - which can't set env vars itself - configures
// keeperd and, through it, the runner. A missing file is fine.
func LoadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"`)
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
	return nil
}
