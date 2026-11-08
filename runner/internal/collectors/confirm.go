package collectors

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// Local confirm queue: installs a collector's approve needs wait here until
// the person at THIS computer allows or denies them (api/localconfirm.go
// serves the page; it refuses anything that came through the uplink).
// In memory only: a runner restart drops pending entries (approve again).

// Pending is one install awaiting a local decision.
type Pending struct {
	Token       string    `json:"token"`
	CollectorID string    `json:"collectorId"`
	Requires    []Require `json:"requires"`
	At          time.Time `json:"at"`
}

type pendingEntry struct {
	Pending
	resume func(allow bool)
}

// Confirms is the queue.
type Confirms struct {
	// OnPending fires when an entry is added (desktop notification; best effort).
	OnPending func(Pending)

	mu      sync.Mutex
	entries []*pendingEntry
}

func NewConfirms() *Confirms { return &Confirms{} }

// Request queues the install (one entry per collector; a repeat approve
// replaces the old entry's requires but keeps its token).
func (c *Confirms) Request(collectorID string, reqs []Require, resume func(allow bool)) {
	c.mu.Lock()
	for _, e := range c.entries {
		if e.CollectorID == collectorID {
			e.Requires, e.resume = reqs, resume
			c.mu.Unlock()
			return
		}
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	e := &pendingEntry{Pending: Pending{Token: hex.EncodeToString(b), CollectorID: collectorID, Requires: reqs, At: time.Now().UTC()}, resume: resume}
	c.entries = append(c.entries, e)
	hook := c.OnPending
	p := e.Pending
	c.mu.Unlock()
	if hook != nil {
		hook(p)
	}
}

// List: pending installs, oldest first.
func (c *Confirms) List() []Pending {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Pending, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e.Pending)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Decide resolves token (ErrNotFound if unknown or already decided) and runs
// the install (allow) or the decline (deny).
func (c *Confirms) Decide(token string, allow bool) error {
	c.mu.Lock()
	var hit *pendingEntry
	for i, e := range c.entries {
		if e.Token == token {
			hit = e
			c.entries = append(c.entries[:i], c.entries[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	if hit == nil {
		return ErrNotFound
	}
	hit.resume(allow)
	return nil
}
