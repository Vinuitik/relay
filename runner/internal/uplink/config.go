// Package uplink connects this runner OUT to a Relay hub (HabitTracker) over
// a WebSocket and answers the hub's forwarded /v1/* requests with the
// runner's own http.Handler. Contract: shared/HUB.md. See FLOWS.md here.
package uplink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the pairing file, stored next to key.txt in RELAY_HOME.
const FileName = "hub.json"

// Config is the content of hub.json, written by `runnerd pair`.
type Config struct {
	Hub      string `json:"hub"`
	DeviceID string `json:"deviceId"`
	Token    string `json:"token"`
}

// Load reads dir/hub.json. A missing file returns (nil, nil): not paired,
// the uplink stays off.
func Load(dir string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", FileName, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", FileName, err)
	}
	if c.Hub == "" || c.Token == "" {
		return nil, fmt.Errorf("%s: missing hub or token", FileName)
	}
	if err := CheckHubURL(c.Hub); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes dir/hub.json with mode 0600 (it holds the raw device token).
func Save(dir string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// WriteFile keeps the mode of an existing file; force 0600 on re-pair.
	_ = os.Chmod(path, 0o600)
	return nil
}

// CheckHubURL refuses anything but https, except plain http to localhost
// (local hub dev / tests) - the token would otherwise travel in clear text.
func CheckHubURL(hub string) error {
	u, err := url.Parse(hub)
	if err != nil || u.Host == "" {
		return fmt.Errorf("hub %q: not an absolute URL", hub)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLocalhost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("hub %q: refusing plain http to a non-localhost host (use https)", hub)
	default:
		return fmt.Errorf("hub %q: scheme must be https", hub)
	}
}

func isLocalhost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// uplinkURL turns the hub base URL into the WebSocket endpoint.
func uplinkURL(hub string) string {
	base := strings.TrimRight(hub, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base + "/relay/uplink"
}
