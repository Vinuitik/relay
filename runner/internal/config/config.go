// Package config loads (and creates, on first run) the runner's local state:
// its auth key, projects root directory, and project registry file.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DefaultListenAddr is used when RELAY_LISTEN_ADDR is not set.
	//
	// NOTE: this default (loopback only) is fine for local dev/tests. Per
	// ARCHITECTURE.md, a production deployment must bind only to the
	// Tailscale interface, never 0.0.0.0 - that's a deployment-time
	// concern (pass the Tailscale interface IP via RELAY_LISTEN_ADDR),
	// not something this package enforces.
	DefaultListenAddr = "127.0.0.1:7777"

	keyFileName      = "key.txt"
	projectsFileName = "projects.json"
	projectsDirName  = "projects"
)

// Config holds resolved runtime configuration for the runner.
type Config struct {
	RelayHome    string // root dir for runner state (key file, project registry)
	ProjectsRoot string // where project directories are scaffolded
	ProjectsFile string // path to the projects.json registry
	ListenAddr   string
	Key          string
	// FirstRun is true when the key was generated during this Load call
	// (i.e. this is the first time the runner has started on this machine).
	FirstRun bool
}

// Load reads, or creates on first run, the runner's home directory
// (~/.relay by default, or $RELAY_HOME - mainly so tests don't touch the
// real user's home directory), its key file, and an empty project registry.
//
// RELAY_LISTEN_ADDR overrides the HTTP listen address.
func Load() (*Config, error) {
	home, err := relayHome()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, fmt.Errorf("create relay home %q: %w", home, err)
	}

	projectsRoot := filepath.Join(home, projectsDirName)
	if err := os.MkdirAll(projectsRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create projects root %q: %w", projectsRoot, err)
	}

	keyPath := filepath.Join(home, keyFileName)
	key, firstRun, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}

	projectsFile := filepath.Join(home, projectsFileName)
	if err := ensureProjectsFile(projectsFile); err != nil {
		return nil, err
	}

	listenAddr := os.Getenv("RELAY_LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = DefaultListenAddr
	}

	return &Config{
		RelayHome:    home,
		ProjectsRoot: projectsRoot,
		ProjectsFile: projectsFile,
		ListenAddr:   listenAddr,
		Key:          key,
		FirstRun:     firstRun,
	}, nil
}

func relayHome() (string, error) {
	if h := os.Getenv("RELAY_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home dir: %w", err)
	}
	return filepath.Join(home, ".relay"), nil
}

// loadOrCreateKey returns the existing key at path, or generates and
// persists a new random one if none exists yet.
func loadOrCreateKey(path string) (key string, firstRun bool, err error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), false, nil
	}
	if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("read key file %q: %w", path, err)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, fmt.Errorf("generate key: %w", err)
	}
	key = hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		return "", false, fmt.Errorf("write key file %q: %w", path, err)
	}
	return key, true, nil
}

// ensureProjectsFile creates an empty JSON array registry file if one
// doesn't already exist. It never overwrites an existing registry.
func ensureProjectsFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat projects file %q: %w", path, err)
	}
	empty, err := json.Marshal([]struct{}{})
	if err != nil {
		return err
	}
	return os.WriteFile(path, empty, 0o600)
}
