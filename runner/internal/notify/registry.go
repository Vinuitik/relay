// Package notify registers phones' FCM push tokens and notifies them when a
// session finishes. See ARCHITECTURE.md "Notifications: Firebase Cloud
// Messaging (FCM)" and shared/API.md's /v1/devices entry.
package notify

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const devicesFileName = "devices.json"

// Device matches the shape defined in shared/API.md.
type Device struct {
	ID           string `json:"id"`
	FCMToken     string `json:"fcmToken"`
	RegisteredAt string `json:"registeredAt"`
}

// Session is the minimal session info a Notifier needs to describe which
// session finished. It's a small local type (not session.Session) so this
// package doesn't need to import runner/internal/session.
type Session struct {
	ID        string
	ProjectID string
}

// Registry tracks registered devices, keyed by FCM token so re-registering
// the same token updates it in place instead of duplicating. It's guarded by
// a mutex (same pattern as runner/internal/project's Registry) and persisted
// to a devices.json file so registrations survive a runner restart - the
// phone only re-registers its token in MainActivity.onCreate, which won't
// run again until the user happens to open the app.
type Registry struct {
	mu      sync.Mutex
	file    string // path to the devices.json file; empty = no persistence
	devices map[string]*Device // keyed by fcmToken
	nextID  uint64
}

// NewRegistry creates a Registry persisted to $RELAY_HOME/devices.json,
// falling back to ~/.relay/devices.json when RELAY_HOME is unset - matching
// internal/config.Load's home-dir resolution. Any existing devices file is
// loaded; a missing file just means no devices yet, and a corrupt file is
// logged and treated as empty rather than failing startup.
func NewRegistry() *Registry {
	path, err := devicesFilePath()
	if err != nil {
		log.Printf("notify: resolve devices file path: %v; registrations will not persist", err)
		return &Registry{devices: make(map[string]*Device)}
	}
	return NewRegistryAt(path)
}

// NewRegistryAt creates a Registry persisted to the given path, loading any
// existing devices from it. Intended for tests; production code should use
// NewRegistry, which resolves the path from $RELAY_HOME.
func NewRegistryAt(path string) *Registry {
	r := &Registry{file: path, devices: make(map[string]*Device)}
	r.loadLocked()
	return r
}

func devicesFilePath() (string, error) {
	home := os.Getenv("RELAY_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home dir: %w", err)
		}
		home = filepath.Join(userHome, ".relay")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("create relay home %q: %w", home, err)
	}
	return filepath.Join(home, devicesFileName), nil
}

// loadLocked populates r.devices from r.file. Called only during
// construction, before r is shared, so it doesn't need r.mu.
func (r *Registry) loadLocked() {
	if r.file == "" {
		return
	}
	data, err := os.ReadFile(r.file)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("notify: read devices file %q: %v; starting with no registered devices", r.file, err)
		}
		return
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return
	}

	var devices []*Device
	if err := json.Unmarshal(data, &devices); err != nil {
		log.Printf("notify: parse devices file %q: %v; starting with no registered devices", r.file, err)
		return
	}
	for _, d := range devices {
		if d == nil || d.FCMToken == "" {
			continue
		}
		r.devices[d.FCMToken] = d
		r.nextID++
	}
}

// Register adds a new device for fcmToken, or - if that token is already
// registered - updates its registeredAt timestamp in place rather than
// creating a duplicate entry. The registry is persisted to disk afterward;
// a persist failure is logged but does not fail the registration, since an
// in-memory registration is still better than none for the current process.
func (r *Registry) Register(fcmToken string) Device {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out Device
	if d, ok := r.devices[fcmToken]; ok {
		d.RegisteredAt = now()
		out = *d
	} else {
		r.nextID++
		d := &Device{
			ID:           fmt.Sprintf("dev%d-%d", time.Now().UnixNano(), r.nextID),
			FCMToken:     fcmToken,
			RegisteredAt: now(),
		}
		r.devices[fcmToken] = d
		out = *d
	}

	if err := r.persistLocked(); err != nil {
		log.Printf("notify: persist devices file %q: %v", r.file, err)
	}
	return out
}

// List returns all currently registered devices.
func (r *Registry) List() []Device {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Device, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, *d)
	}
	return out
}

// persistLocked writes r.devices to r.file atomically (temp file + rename)
// so a crash mid-write can't corrupt the devices file. Callers must hold
// r.mu. A no-op when r.file is empty (e.g. a Registry built without a path).
func (r *Registry) persistLocked() error {
	if r.file == "" {
		return nil
	}

	devices := make([]*Device, 0, len(r.devices))
	for _, d := range r.devices {
		devices = append(devices, d)
	}
	data, err := json.MarshalIndent(devices, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal devices: %w", err)
	}

	dir := filepath.Dir(r.file)
	tmp, err := os.CreateTemp(dir, ".devices-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp devices file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed away

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp devices file %q: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp devices file %q: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("chmod temp devices file %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, r.file); err != nil {
		return fmt.Errorf("rename %q to %q: %w", tmpPath, r.file, err)
	}
	return nil
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
