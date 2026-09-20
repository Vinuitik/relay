// Package waker implements the wake daemon (wakerd) that runs on an
// always-on Raspberry Pi sitting on the LAN. Its only job is to send
// Wake-on-LAN magic packets on request and report whether the target is up.
//
// It is a distinct role from the runner: no projects, no sessions, no agent
// CLI. It exists because a WoL magic packet is an L2 broadcast, so only a
// device physically on the target's LAN can send one - and every other
// machine on that LAN is asleep.
package waker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultListenAddr is used when WAKER_LISTEN_ADDR is not set.
	//
	// Loopback-only by default, same reasoning as the runner's config: a
	// real deployment binds the Tailscale interface IP by passing
	// WAKER_LISTEN_ADDR, which this package does not enforce.
	DefaultListenAddr = "127.0.0.1:7778"

	// DefaultProbePort is the port reachability probes dial when a machine
	// doesn't set its own - the runner's listen port, since every machine
	// this daemon wakes is a runner.
	DefaultProbePort = 7777

	// DefaultWakeTimeout is how long a wake request waits for the target to
	// answer before giving up.
	DefaultWakeTimeout = 90 * time.Second

	// DefaultPollInterval is how often the target is probed while waiting.
	DefaultPollInterval = 3 * time.Second

	// DefaultProbeTimeout bounds a single TCP dial. Short on purpose: a
	// sleeping host doesn't answer at all, and we'd rather poll again than
	// block.
	DefaultProbeTimeout = 2 * time.Second

	configFileName = "config.json"
)

// Machine is one wakeable host, edited by hand in config.json for v1.
type Machine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	MAC  string `json:"mac"`
	// ProbeHost is the IP or hostname reachability probes dial. Falls back
	// to ID when empty (so `{"id":"dell"}` works if "dell" resolves).
	ProbeHost string `json:"probeHost,omitempty"`
	// ProbePort is the TCP port probes dial. Falls back to DefaultProbePort.
	ProbePort int `json:"probePort,omitempty"`
}

// Host returns the hostname/IP to dial for this machine.
func (m Machine) Host() string {
	if m.ProbeHost != "" {
		return m.ProbeHost
	}
	return m.ID
}

// Port returns the TCP port to dial for this machine.
func (m Machine) Port() int {
	if m.ProbePort > 0 {
		return m.ProbePort
	}
	return DefaultProbePort
}

// File is the on-disk shape of ~/.waker/config.json.
type File struct {
	Key      string    `json:"key"`
	Machines []Machine `json:"machines"`
}

// Config is resolved runtime configuration for wakerd.
type Config struct {
	WakerHome  string
	ConfigFile string
	ListenAddr string
	Key        string
	Machines   []Machine
	// FirstRun is true when config.json was created during this Load call.
	FirstRun bool
}

// Machine returns the machine with the given id.
func (c *Config) Machine(id string) (Machine, bool) {
	for _, m := range c.Machines {
		if m.ID == id {
			return m, true
		}
	}
	return Machine{}, false
}

// Load reads, or creates on first run, ~/.waker/config.json (or
// $WAKER_HOME/config.json - mainly so tests don't touch the real home dir).
// A freshly created file holds a new random key and an empty machine list.
//
// WAKER_LISTEN_ADDR overrides the HTTP listen address.
func Load() (*Config, error) {
	home, err := wakerHome()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, fmt.Errorf("create waker home %q: %w", home, err)
	}

	path := filepath.Join(home, configFileName)
	f, firstRun, err := loadOrCreateFile(path)
	if err != nil {
		return nil, err
	}

	listenAddr := os.Getenv("WAKER_LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = DefaultListenAddr
	}

	return &Config{
		WakerHome:  home,
		ConfigFile: path,
		ListenAddr: listenAddr,
		Key:        f.Key,
		Machines:   f.Machines,
		FirstRun:   firstRun,
	}, nil
}

func wakerHome() (string, error) {
	if h := os.Getenv("WAKER_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home dir: %w", err)
	}
	return filepath.Join(home, ".waker"), nil
}

// loadOrCreateFile returns the existing config at path, or writes a new one
// with a freshly generated key. It never overwrites an existing file - the
// machine list is hand-edited, and clobbering it would be unrecoverable.
func loadOrCreateFile(path string) (File, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		var f File
		if err := json.Unmarshal(data, &f); err != nil {
			return File{}, false, fmt.Errorf("parse waker config %q: %w", path, err)
		}
		if f.Key == "" {
			return File{}, false, fmt.Errorf("waker config %q has no key", path)
		}
		return f, false, nil
	}
	if !os.IsNotExist(err) {
		return File{}, false, fmt.Errorf("read waker config %q: %w", path, err)
	}

	key, err := generateKey()
	if err != nil {
		return File{}, false, err
	}
	f := File{Key: key, Machines: []Machine{}}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return File{}, false, err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return File{}, false, fmt.Errorf("write waker config %q: %w", path, err)
	}
	return f, true, nil
}

// generateKey returns 32 random bytes as hex - same scheme as the runner's
// internal/config key file.
func generateKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
