// Package jobs runs one-shot headless Claude Code jobs (`claude -p`) for
// the hub: map/plan read a project folder (read-only, confined to it),
// kpi is pure text generation with no tools. See runner/FLOWS.md "Jobs".
//
// Jobs live in memory only: a runner restart loses queued, running and
// finished jobs alike.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Kinds.
const (
	KindMap  = "map"
	KindPlan = "plan"
	KindKPI  = "kpi"
)

// States.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Event kinds.
const (
	EventStep  = "step"
	EventText  = "text"
	EventError = "error"
)

// Limits. MaxResult + MaxEvents*MaxEventText stays under the hub's 4 MiB
// body limit (shared/HUB.md) with room for JSON escaping of normal text.
const (
	MaxConcurrent = 2
	Retention     = time.Hour
	MaxEvents     = 2000
	MaxEventText  = 500
	MaxResult     = 1 << 20
	MaxPrompt     = 1 << 20
)

var (
	ErrBadRequest      = errors.New("bad request")
	ErrProjectRequired = errors.New("projectId is required for map/plan jobs")
	ErrProjectNotFound = errors.New("project not found")
	ErrNotFound        = errors.New("job not found")
)

// Request is POST /v1/jobs's body.
type Request struct {
	Kind      string `json:"kind"`
	ProjectID string `json:"projectId,omitempty"`
	Prompt    string `json:"prompt"`
	Model     string `json:"model,omitempty"`
	MaxTurns  int    `json:"maxTurns,omitempty"`
}

// Event is one progress line.
type Event struct {
	Seq  int       `json:"seq"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
}

// Usage is the final token count. InputTokens includes cache reads/writes.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Job is the API view (shared/API.md "Job").
type Job struct {
	JobID   string  `json:"jobId"`
	Kind    string  `json:"kind"`
	State   string  `json:"state"`
	Events  []Event `json:"events"`
	NextSeq int     `json:"nextSeq"`
	Result  string  `json:"result,omitempty"`
	Error   string  `json:"error,omitempty"`
	Usage   *Usage  `json:"usage,omitempty"`
}

type job struct {
	id, kind, state string
	dir             string // project dir; "" for kpi (temp dir made at start)
	req             Request
	events          []Event
	seq             int
	capped          bool
	result, errMsg  string
	usage           *Usage
	finishedAt      time.Time
	cancel          func() // kills the process; set when the job starts
}

// Manager queues and runs jobs, at most MaxConcurrent at once.
type Manager struct {
	// Dir resolves a projectId to its folder (project.Registry.Dir).
	Dir func(projectID string) (string, bool)
	// FindCLI locates the claude binary (auth.Claude().FindCLI:
	// $RELAY_CLAUDE_CLI, else PATH).
	FindCLI func() (string, error)
	// ExtraArgs is prepended to the claude args (tests: "-test.run=^$").
	ExtraArgs []string

	mu      sync.Mutex
	jobs    map[string]*job
	queue   []*job
	running int
	lastEnd time.Time // latest finish, for IdleStatus
	now     func() time.Time
}

// New builds a Manager.
func New(dir func(string) (string, bool), findCLI func() (string, error)) *Manager {
	return &Manager{Dir: dir, FindCLI: findCLI, jobs: map[string]*job{}, now: time.Now}
}

// Submit validates req, queues the job and starts it if a slot is free.
func (m *Manager) Submit(req Request) (string, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return "", errorf("prompt is required")
	}
	if len(req.Prompt) > MaxPrompt {
		return "", errorf("prompt exceeds 1 MiB")
	}
	if req.MaxTurns < 0 {
		return "", errorf("maxTurns must be >= 0")
	}
	if strings.HasPrefix(req.Model, "-") {
		return "", errorf("invalid model")
	}
	j := &job{id: newID(), kind: req.Kind, state: StateQueued, req: req}
	switch req.Kind {
	case KindMap, KindPlan:
		if req.ProjectID == "" {
			return "", ErrProjectRequired
		}
		dir, ok := m.Dir(req.ProjectID)
		if !ok {
			return "", ErrProjectNotFound
		}
		j.dir = dir
	case KindKPI:
	default:
		return "", errorf("kind must be map, plan or kpi")
	}
	bin, err := m.FindCLI()
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	m.jobs[j.id] = j
	m.queue = append(m.queue, j)
	m.startLocked(bin)
	return j.id, nil
}

// Get returns the job with only the events after seq `after`.
func (m *Manager) Get(id string, after int) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	return j.view(after), nil
}

// Cancel kills a running job (or drops a queued one) and returns it.
// Finished jobs are returned unchanged.
func (m *Manager) Cancel(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	switch j.state {
	case StateQueued:
		for i, q := range m.queue {
			if q == j {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
		m.finishLocked(j, StateCancelled, "cancelled")
	case StateRunning:
		// Marked now; run() sees the state and won't overwrite it.
		m.finishLocked(j, StateCancelled, "cancelled")
		if j.cancel != nil {
			j.cancel()
		}
	}
	return j.view(0), nil
}

// Active reports how many jobs are queued or running (idle-suspend must not
// sleep the machine under them).
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running + len(m.queue)
}

// IdleStatus has idle.SessionStatus's shape so runnerd can fold jobs into
// the idle-suspend decision: busy while any job is queued/running, else
// idle since the last one finished (zero if none ever ran).
func (m *Manager) IdleStatus() (bool, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running+len(m.queue) > 0, m.lastEnd
}

// startLocked starts queued jobs while slots are free.
func (m *Manager) startLocked(bin string) {
	for m.running < MaxConcurrent && len(m.queue) > 0 {
		j := m.queue[0]
		m.queue = m.queue[1:]
		j.state = StateRunning
		m.running++
		ctx, cancel := context.WithCancel(context.Background())
		j.cancel = cancel // set before the goroutine so Cancel always kills
		go m.run(ctx, j, bin)
	}
}

// done is called by run() when the process has exited.
func (m *Manager) done(j *job, state, errMsg, result string, usage *Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running--
	j.cancel()                   // release the context
	if j.state == StateRunning { // not already cancelled
		j.result = truncateResult(result)
		j.usage = usage
		m.finishLocked(j, state, errMsg)
	}
	if len(m.queue) > 0 {
		if bin, err := m.FindCLI(); err == nil {
			m.startLocked(bin)
		} else {
			for _, q := range m.queue {
				m.finishLocked(q, StateFailed, err.Error())
			}
			m.queue = nil
		}
	}
}

func (m *Manager) finishLocked(j *job, state, errMsg string) {
	j.state = state
	j.errMsg = errMsg
	j.finishedAt = m.now()
	m.lastEnd = j.finishedAt
	if errMsg != "" && state != StateDone {
		m.addEventLocked(j, EventError, errMsg)
	}
}

// addEvent appends one event (thread-safe).
func (m *Manager) addEvent(j *job, kind, text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j.state != StateRunning {
		return
	}
	m.addEventLocked(j, kind, text)
}

func (m *Manager) addEventLocked(j *job, kind, text string) {
	if j.capped {
		return
	}
	if len(j.events) == MaxEvents-1 {
		j.capped = true
		kind, text = EventError, "event limit reached; later progress not shown"
	}
	j.seq++
	j.events = append(j.events, Event{Seq: j.seq, At: m.now().UTC(), Kind: kind, Text: clip(text, MaxEventText)})
}

// sweepLocked drops jobs finished more than Retention ago.
func (m *Manager) sweepLocked() {
	cut := m.now().Add(-Retention)
	for id, j := range m.jobs {
		if !j.finishedAt.IsZero() && j.finishedAt.Before(cut) {
			delete(m.jobs, id)
		}
	}
}

func (j *job) view(after int) Job {
	v := Job{JobID: j.id, Kind: j.kind, State: j.state, Events: []Event{}, NextSeq: j.seq,
		Result: j.result, Error: j.errMsg, Usage: j.usage}
	for _, e := range j.events {
		if e.Seq > after {
			v.Events = append(v.Events, e)
		}
	}
	return v
}

const truncMarker = "\n\n[truncated by runner: result exceeded 1 MiB]"

func truncateResult(s string) string {
	if len(s) <= MaxResult {
		return s
	}
	return cutUTF8(s, MaxResult-len(truncMarker)) + truncMarker
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return cutUTF8(s, n-3) + "..."
}

// cutUTF8 cuts s to at most n bytes without splitting a rune.
func cutUTF8(s string, n int) string {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "job_" + hex.EncodeToString(b)
}

type badRequest string

func (e badRequest) Error() string   { return string(e) }
func (e badRequest) Is(t error) bool { return t == ErrBadRequest }

func errorf(msg string) error { return badRequest(msg) }
