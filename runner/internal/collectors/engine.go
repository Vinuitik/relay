package collectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"relay/runner/internal/collectors/sandbox"
)

// ErrCollector means the collector itself misbehaved (non-zero exit, bad
// or non-finite output, timeout) → HTTP 422; a scheduled run marks the
// collector failed.
var ErrCollector = errors.New("collector failed")

const (
	RunTimeout  = 60 * time.Second
	RunMemMB    = 128
	maxEvidence = 20
	maxLabel    = 200
	// maxConcurrent sandboxed runs at once (API + scheduler together).
	maxConcurrent = 2
	// PythonEnv overrides interpreter discovery; shared with the Windows
	// sandbox's own availability probe (sandbox_windows.go).
	PythonEnv = "RELAY_COLLECTOR_PYTHON"
)

// Sandbox is the OS sandbox; OSSandbox in production, fakes in tests.
type Sandbox interface {
	Kind() string
	Available() error
	Run(ctx context.Context, s sandbox.Spec) (sandbox.Result, error)
}

type osSandbox struct{}

func (osSandbox) Kind() string     { return sandbox.Kind() }
func (osSandbox) Available() error { return sandbox.Available() }
func (osSandbox) Run(ctx context.Context, s sandbox.Spec) (sandbox.Result, error) {
	return sandbox.Run(ctx, s)
}

// OSSandbox is the real sandbox for this OS.
var OSSandbox Sandbox = osSandbox{}

// Engine owns the collector store, runs collectors sandboxed and schedules
// active ones (scheduler.go).
type Engine struct {
	DataDir string // runner data dir ($RELAY_HOME); collectors live in DataDir/collectors
	Sandbox Sandbox
	Python  func() (string, error)
	AWBase  string
	HTTP    *http.Client
	Now     func() time.Time
	Loc     *time.Location // scheduling time zone; nil = time.Local

	mu    sync.Mutex // store
	st    store
	sem   chan struct{}
	tick  sync.Mutex
	retry map[string]time.Time // scheduler backoff after a source error

	busyMu  sync.Mutex
	running int
	lastEnd time.Time
}

// NewEngine builds an Engine with production defaults.
func NewEngine(dataDir string) *Engine {
	e := &Engine{DataDir: dataDir, Sandbox: OSSandbox, AWBase: DefaultAWBase, Now: time.Now}
	e.Python = func() (string, error) { return FindPython(dataDir) }
	return e
}

func (e *Engine) init() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.st.dir == "" {
		e.st = store{dir: filepath.Join(e.DataDir, "collectors")}
		e.sem = make(chan struct{}, maxConcurrent)
		e.retry = map[string]time.Time{}
		if e.Now == nil {
			e.Now = time.Now
		}
	}
}

func (e *Engine) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (e *Engine) loc() *time.Location {
	if e.Loc != nil {
		return e.Loc
	}
	return time.Local
}

// ---- store-facing API ----

func (e *Engine) List() ([]Info, error) {
	e.init()
	e.mu.Lock()
	defer e.mu.Unlock()
	ms, err := e.st.list()
	out := make([]Info, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.info())
	}
	return out, err
}

func (e *Engine) Get(id string) (Info, error) {
	e.init()
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.st.load(id)
	return m.info(), err
}

func (e *Engine) Install(req InstallRequest) (Info, error) {
	e.init()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := os.MkdirAll(e.st.dir, 0o700); err != nil {
		return Info{}, err
	}
	m, err := e.st.install(req)
	return m.info(), err
}

func (e *Engine) Approve(id, hash string) error {
	e.init()
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.st.load(id)
	if err != nil {
		return err
	}
	if hash == "" || hash != m.ContractHash {
		return ErrHashMismatch
	}
	m.ApprovedHash = hash
	return e.st.save(m)
}

// Activate starts scheduling an approved collector (also re-activates a
// failed one).
func (e *Engine) Activate(id string) error {
	e.init()
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.st.load(id)
	if err != nil {
		return err
	}
	if !m.approved() {
		return ErrNotApproved
	}
	m.State = StateActive
	m.LastError = ""
	delete(e.retry, id)
	return e.st.save(m)
}

func (e *Engine) Delete(id string) error {
	e.init()
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.retry, id)
	return e.st.remove(id)
}

func (e *Engine) Readings(id string, since time.Time) ([]Reading, error) {
	e.init()
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.st.load(id); err != nil {
		return nil, err
	}
	return e.st.readings(id, since)
}

// ---- busy tracking (idle-suspend / runner info) ----

func (e *Engine) begin() {
	e.busyMu.Lock()
	e.running++
	e.busyMu.Unlock()
}

func (e *Engine) end() {
	e.busyMu.Lock()
	e.running--
	e.lastEnd = time.Now()
	e.busyMu.Unlock()
}

// Active is the number of sandboxed runs executing right now.
func (e *Engine) Active() int {
	e.busyMu.Lock()
	defer e.busyMu.Unlock()
	return e.running
}

// IdleStatus has idle.SessionStatus's shape (see cmd/runnerd busyStatus).
func (e *Engine) IdleStatus() (bool, time.Time) {
	e.busyMu.Lock()
	defer e.busyMu.Unlock()
	return e.running > 0, e.lastEnd
}

// ---- running ----

// Evidence is one row explaining the number.
type Evidence struct {
	Label string `json:"label"`
	Value any    `json:"value"`
}

// Output is a collector's parsed stdout plus which sandbox ran it.
type Output struct {
	Value    float64    `json:"value"`
	Unit     string     `json:"unit"`
	Evidence []Evidence `json:"evidence"`
	Sandbox  string     `json:"sandbox"`
}

// sandboxed runs collector.py from scriptDir over inputsDir. InputsDir and
// ScratchDir are always fresh per run (the Windows sandbox revokes its
// grants afterwards).
func (e *Engine) sandboxed(ctx context.Context, scriptDir, inputsDir string, from, to time.Time) (Output, error) {
	if err := e.Sandbox.Available(); err != nil {
		return Output{}, fmt.Errorf("%w: %v", sandbox.ErrUnavailable, err)
	}
	py, err := e.Python()
	if err != nil {
		return Output{}, fmt.Errorf("%w: %v", sandbox.ErrUnavailable, err)
	}
	scratch, err := os.MkdirTemp("", "relay-collector-tmp-")
	if err != nil {
		return Output{}, err
	}
	defer os.RemoveAll(scratch)

	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		return Output{}, ctx.Err()
	}
	e.begin()
	res, err := e.Sandbox.Run(ctx, sandbox.Spec{
		Python: py, ScriptDir: scriptDir, InputsDir: inputsDir, ScratchDir: scratch,
		Args: []string{"--inputs", inputsArg(inputsDir),
			"--from", from.Format(time.RFC3339), "--to", to.Format(time.RFC3339)},
		Timeout: RunTimeout, MemoryMB: RunMemMB,
	})
	e.end()
	<-e.sem
	if errors.Is(err, sandbox.ErrUnavailable) {
		return Output{}, err
	}
	if err != nil {
		return Output{}, fmt.Errorf("%w: %v", ErrCollector, err)
	}
	if res.ExitCode != 0 {
		return Output{}, fmt.Errorf("%w: exit %d: %s", ErrCollector, res.ExitCode, tail(res.Stderr, 2000))
	}
	out, err := parseOutput(res.Stdout)
	if err != nil {
		return Output{}, fmt.Errorf("%w: %v (stderr: %s)", ErrCollector, err, tail(res.Stderr, 500))
	}
	out.Sandbox = e.Sandbox.Kind()
	return out, nil
}

// inputsArg is the --inputs path as the script sees it: bwrap remaps the
// inputs dir to /in; the Windows AppContainer uses host paths.
func inputsArg(hostDir string) string {
	if runtime.GOOS == "linux" {
		return "/in"
	}
	return hostDir
}

func tail(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		s = "..." + s[len(s)-n:]
	}
	return s
}

// parseOutput takes the whole stdout as JSON, else its last non-empty
// line (tolerates debug prints). value must be a finite number.
func parseOutput(stdout []byte) (Output, error) {
	type raw struct {
		Value    json.RawMessage `json:"value"`
		Unit     string          `json:"unit"`
		Evidence []Evidence      `json:"evidence"`
	}
	var r raw
	b := bytes.TrimSpace(stdout)
	if err := json.Unmarshal(b, &r); err != nil {
		lines := bytes.Split(b, []byte("\n"))
		last := bytes.TrimSpace(lines[len(lines)-1])
		r = raw{}
		if err2 := json.Unmarshal(last, &r); err2 != nil {
			return Output{}, fmt.Errorf("stdout is not {value, unit, evidence} JSON: %v (got %q)", err, tail(b, 300))
		}
	}
	if len(r.Value) == 0 || string(r.Value) == "null" {
		return Output{}, errors.New("output has no value")
	}
	var v float64
	if err := json.Unmarshal(r.Value, &v); err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return Output{}, fmt.Errorf("value %s is not a finite number", r.Value)
	}
	if len(r.Evidence) > maxEvidence {
		r.Evidence = r.Evidence[:maxEvidence]
	}
	for i := range r.Evidence {
		if len(r.Evidence[i].Label) > maxLabel {
			r.Evidence[i].Label = r.Evidence[i].Label[:maxLabel]
		}
		if s, ok := r.Evidence[i].Value.(string); ok && len(s) > maxLabel {
			r.Evidence[i].Value = s[:maxLabel]
		}
	}
	if r.Evidence == nil {
		r.Evidence = []Evidence{}
	}
	return Output{Value: v, Unit: r.Unit, Evidence: r.Evidence}, nil
}

// current loads the collector's current version.
func (e *Engine) current(id string) (meta, Manifest, string, error) {
	e.init()
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.st.load(id)
	if err != nil {
		return m, Manifest{}, "", err
	}
	mf, err := e.st.manifest(m)
	return m, mf, e.st.versionDir(m.ID, m.Version), err
}

// Run snapshots the declared sources for [from, to) and runs the current
// version. Requires an approved contract (data is read only after
// approval). Not stored - scheduled runs store readings.
func (e *Engine) Run(ctx context.Context, id string, from, to time.Time) (Output, error) {
	m, mf, vdir, err := e.current(id)
	if err != nil {
		return Output{}, err
	}
	if !m.approved() {
		return Output{}, ErrNotApproved
	}
	if !to.After(from) || to.Sub(from) > 31*24*time.Hour {
		return Output{}, badf("need from < to, at most 31 days apart")
	}
	return e.runWindow(ctx, mf, vdir, from, to)
}

func (e *Engine) runWindow(ctx context.Context, mf Manifest, vdir string, from, to time.Time) (Output, error) {
	// Refuse before touching any data if we can't sandbox.
	if err := e.Sandbox.Available(); err != nil {
		return Output{}, fmt.Errorf("%w: %v", sandbox.ErrUnavailable, err)
	}
	in, err := os.MkdirTemp("", "relay-collector-in-")
	if err != nil {
		return Output{}, err
	}
	defer os.RemoveAll(in)
	if err := e.snapshot(ctx, mf, from, to, in); err != nil {
		return Output{}, err
	}
	out, err := e.sandboxed(ctx, vdir, in, from, to)
	if err == nil && out.Unit == "" {
		out.Unit = mf.Unit
	}
	return out, err
}

// CaseResult is one fixture case of POST /v1/collectors/{id}/test.
type CaseResult struct {
	Name     string   `json:"name"`
	Passed   bool     `json:"passed"`
	Expected float64  `json:"expected"`
	Got      *float64 `json:"got"`
	Error    string   `json:"error,omitempty"`
}

// TestResult is the response of POST /v1/collectors/{id}/test.
type TestResult struct {
	Passed  bool         `json:"passed"`
	Sandbox string       `json:"sandbox"`
	Cases   []CaseResult `json:"cases"`
}

// expected.json: {value, tolerance?, from?, to?}. Without from/to the
// window is 2000-01-01..2100-01-01 (everything in the fixture counts).
type expectation struct {
	Value     *float64   `json:"value"`
	Tolerance float64    `json:"tolerance"`
	From      *time.Time `json:"from"`
	To        *time.Time `json:"to"`
}

var (
	fixtureFrom = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	fixtureTo   = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Test runs every fixtures/<case> of the current version in the sandbox.
// No approval needed: fixtures are the hub's own data. Each case's inputs
// are copied to a fresh dir.
func (e *Engine) Test(ctx context.Context, id string) (TestResult, error) {
	_, _, vdir, err := e.current(id)
	if err != nil {
		return TestResult{}, err
	}
	if err := e.Sandbox.Available(); err != nil {
		return TestResult{}, fmt.Errorf("%w: %v", sandbox.ErrUnavailable, err)
	}
	res := TestResult{Sandbox: e.Sandbox.Kind(), Cases: []CaseResult{}}
	ents, _ := os.ReadDir(filepath.Join(vdir, "fixtures"))
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		c, err := e.testCase(ctx, vdir, ent.Name())
		if errors.Is(err, sandbox.ErrUnavailable) {
			return TestResult{}, err
		}
		res.Cases = append(res.Cases, c)
	}
	res.Passed = len(res.Cases) > 0
	for _, c := range res.Cases {
		res.Passed = res.Passed && c.Passed
	}
	return res, nil
}

func (e *Engine) testCase(ctx context.Context, vdir, name string) (CaseResult, error) {
	c := CaseResult{Name: name}
	dir := filepath.Join(vdir, "fixtures", name)
	var exp expectation
	b, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err == nil {
		err = json.Unmarshal(b, &exp)
	}
	if err != nil || exp.Value == nil {
		c.Error = fmt.Sprintf("expected.json missing or has no value: %v", err)
		return c, nil
	}
	c.Expected = *exp.Value
	from, to := fixtureFrom, fixtureTo
	if exp.From != nil && exp.To != nil {
		from, to = *exp.From, *exp.To
	}
	in, err := os.MkdirTemp("", "relay-collector-in-")
	if err != nil {
		return c, err
	}
	defer os.RemoveAll(in)
	if err := copyTree(filepath.Join(dir, "inputs"), in); err != nil {
		c.Error = err.Error()
		return c, nil
	}
	out, err := e.sandboxed(ctx, vdir, in, from, to)
	if err != nil {
		if errors.Is(err, sandbox.ErrUnavailable) {
			return c, err
		}
		c.Error = err.Error()
		return c, nil
	}
	got := out.Value
	c.Got = &got
	tol := exp.Tolerance
	if tol <= 0 {
		tol = 1e-6 * math.Max(1, math.Abs(c.Expected))
	}
	c.Passed = math.Abs(got-c.Expected) <= tol
	if !c.Passed {
		c.Error = fmt.Sprintf("got %v, expected %v", got, c.Expected)
	}
	return c, nil
}

func copyTree(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		_, err = copyFile(p, t, maxBundle)
		return err
	})
}

// FindPython picks the collector interpreter: $RELAY_COLLECTOR_PYTHON,
// then (Windows) an embedded python under <dataDir>/python or next to the
// runner binary, then python3/python on PATH. Microsoft Store aliases
// (WindowsApps) are skipped: AppContainer can't run them.
func FindPython(dataDir string) (string, error) {
	if p := os.Getenv(PythonEnv); p != "" {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		// TODO(collectors): ship the embeddable python zip with the
		// installer; these are where it will land.
		cands := []string{filepath.Join(dataDir, "python", "python.exe")}
		if exe, err := os.Executable(); err == nil {
			cands = append(cands, filepath.Join(filepath.Dir(exe), "python", "python.exe"))
		}
		for _, c := range cands {
			if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() {
				return c, nil
			}
		}
	}
	for _, name := range []string{"python3", "python"} {
		p, err := exec.LookPath(name)
		if err != nil || strings.Contains(strings.ToLower(p), "windowsapps") {
			continue
		}
		return p, nil
	}
	return "", errors.New("no python found (install python3, or set " + PythonEnv + ")")
}
