// Package notify registers phones' FCM push tokens and notifies them when a
// session finishes. See ARCHITECTURE.md "Notifications: Firebase Cloud
// Messaging (FCM)" and shared/API.md's /v1/devices entry.
package notify

import (
	"fmt"
	"sync"
	"time"
)

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

// Registry tracks registered devices in memory, keyed by FCM token so
// re-registering the same token updates it in place instead of duplicating.
//
// Limitation (by design, for this milestone): registrations live in memory
// only, guarded by a mutex - same limitation as runner/internal/session's
// Manager. A runner restart loses all registered devices; the phone app is
// expected to re-register on its next call to /v1/devices (e.g. whenever
// its FCM token refreshes, per shared/API.md).
type Registry struct {
	mu      sync.Mutex
	devices map[string]*Device // keyed by fcmToken
	nextID  uint64
}

// NewRegistry creates an empty device Registry.
func NewRegistry() *Registry {
	return &Registry{devices: make(map[string]*Device)}
}

// Register adds a new device for fcmToken, or - if that token is already
// registered - updates its registeredAt timestamp in place rather than
// creating a duplicate entry.
func (r *Registry) Register(fcmToken string) Device {
	r.mu.Lock()
	defer r.mu.Unlock()

	if d, ok := r.devices[fcmToken]; ok {
		d.RegisteredAt = now()
		return *d
	}

	r.nextID++
	d := &Device{
		ID:           fmt.Sprintf("dev%d-%d", time.Now().UnixNano(), r.nextID),
		FCMToken:     fcmToken,
		RegisteredAt: now(),
	}
	r.devices[fcmToken] = d
	return *d
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

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
