package collectors

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// On-disk layout, <dataDir>/collectors/<id>/:
//   collector.json        meta (version, contract hash, approved hash, state, last run)
//   versions/<n>/         manifest.json + collector.py + fixtures/... (last keepVersions kept)
//   readings.jsonl        one Reading per line, append-only

var (
	ErrNotFound     = errors.New("collector not found")
	ErrHashMismatch = errors.New("contract hash does not match the current version")
	ErrNotApproved  = errors.New("collector contract is not approved")
)

const (
	StatePending = "pending"
	StateActive  = "active"
	StateFailed  = "failed"

	keepVersions = 5
	maxFiles     = 500
	maxBundle    = 20 << 20
)

var (
	idRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	caseRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

type meta struct {
	ID           string     `json:"id"`
	KPIName      string     `json:"kpiName"`
	Version      int        `json:"version"`
	ContractHash string     `json:"contractHash"`
	ApprovedHash string     `json:"approvedHash,omitempty"`
	State        string     `json:"state"`
	LastRunAt    *time.Time `json:"lastRunAt,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
}

// Info is the API view of a collector (GET /v1/collectors).
type Info struct {
	ID           string     `json:"id"`
	KPIName      string     `json:"kpiName"`
	Version      int        `json:"version"`
	ContractHash string     `json:"contractHash"`
	Approved     bool       `json:"approved"`
	State        string     `json:"state"`
	LastRunAt    *time.Time `json:"lastRunAt"`
	LastError    string     `json:"lastError"`
}

func (m meta) info() Info {
	return Info{ID: m.ID, KPIName: m.KPIName, Version: m.Version, ContractHash: m.ContractHash,
		Approved: m.approved(), State: m.State, LastRunAt: m.LastRunAt, LastError: m.LastError}
}

func (m meta) approved() bool { return m.ApprovedHash != "" && m.ApprovedHash == m.ContractHash }

// Reading is one stored window value.
type Reading struct {
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Value      float64   `json:"value"`
	Unit       string    `json:"unit"`
	RecordedAt time.Time `json:"recordedAt"`
}

// InstallRequest is POST /v1/collectors.
type InstallRequest struct {
	ID       string            `json:"id"`
	KPIName  string            `json:"kpiName"`
	Manifest json.RawMessage   `json:"manifest"`
	Files    map[string]string `json:"files"`
}

// store is not safe for concurrent use; Engine.mu guards it.
type store struct{ dir string }

func (s store) cdir(id string) string { return filepath.Join(s.dir, id) }

func (s store) versionDir(id string, v int) string {
	return filepath.Join(s.cdir(id), "versions", strconv.Itoa(v))
}

func (s store) load(id string) (meta, error) {
	var m meta
	if !idRe.MatchString(id) {
		return m, ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(s.cdir(id), "collector.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

func (s store) save(m meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(s.cdir(m.ID), "collector.json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s store) list() ([]meta, error) {
	ents, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []meta
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if m, err := s.load(e.Name()); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s store) manifest(m meta) (Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(s.versionDir(m.ID, m.Version), "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	mf, _, err := ParseManifest(raw)
	return mf, err
}

// checkFiles validates bundle paths: "collector.py" or
// "fixtures/<case>/expected.json" / "fixtures/<case>/inputs/<...>".
func checkFiles(files map[string]string) error {
	if _, ok := files["collector.py"]; !ok {
		return badf(`files must include "collector.py"`)
	}
	if len(files) > maxFiles {
		return badf("at most %d files", maxFiles)
	}
	total := 0
	for p, c := range files {
		total += len(c)
		if p == "collector.py" {
			continue
		}
		if strings.Contains(p, `\`) || path.Clean(p) != p || path.IsAbs(p) || strings.Contains(p, "..") {
			return badf("bad file path %q", p)
		}
		parts := strings.Split(p, "/")
		if len(parts) < 3 || parts[0] != "fixtures" || !caseRe.MatchString(parts[1]) ||
			!(len(parts) == 3 && parts[2] == "expected.json" || len(parts) >= 4 && parts[2] == "inputs") {
			return badf(`file %q: only "collector.py", "fixtures/<case>/expected.json" and "fixtures/<case>/inputs/..." are allowed`, p)
		}
	}
	if total > maxBundle {
		return badf("bundle exceeds %d MB", maxBundle>>20)
	}
	return nil
}

// install writes a new version. Same id → version+1; the approval carries
// over only if the contract hash is unchanged, and so does "active".
func (s store) install(req InstallRequest) (meta, error) {
	if !idRe.MatchString(req.ID) {
		return meta{}, badf("id must match %s", idRe)
	}
	if strings.TrimSpace(req.KPIName) == "" {
		return meta{}, badf("kpiName is required")
	}
	_, hash, err := ParseManifest(req.Manifest)
	if err != nil {
		return meta{}, err
	}
	if err := checkFiles(req.Files); err != nil {
		return meta{}, err
	}
	m, err := s.load(req.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		m = meta{ID: req.ID, State: StatePending}
	case err != nil:
		return meta{}, err
	}
	m.KPIName = req.KPIName
	m.Version++
	if hash != m.ContractHash || m.State != StateActive {
		m.State = StatePending
	}
	m.ContractHash = hash
	m.LastError = ""

	vdir := s.versionDir(m.ID, m.Version)
	_ = os.RemoveAll(vdir)
	if err := os.MkdirAll(vdir, 0o700); err != nil {
		return meta{}, err
	}
	if err := os.WriteFile(filepath.Join(vdir, "manifest.json"), req.Manifest, 0o600); err != nil {
		return meta{}, err
	}
	for p, c := range req.Files {
		dst := filepath.Join(vdir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return meta{}, err
		}
		if err := os.WriteFile(dst, []byte(c), 0o600); err != nil {
			return meta{}, err
		}
	}
	if err := s.save(m); err != nil {
		return meta{}, err
	}
	for v := m.Version - keepVersions; v > 0; v-- {
		d := s.versionDir(m.ID, v)
		if _, err := os.Stat(d); err != nil {
			break
		}
		_ = os.RemoveAll(d)
	}
	return m, nil
}

func (s store) appendReading(id string, r Reading) error {
	f, err := os.OpenFile(filepath.Join(s.cdir(id), "readings.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// readings returns readings recorded after since (zero = all), oldest first.
// Filtering on recordedAt (not window end) means backfilled old windows
// still reach a hub that pulls with since = its last pull time.
func (s store) readings(id string, since time.Time) ([]Reading, error) {
	f, err := os.Open(filepath.Join(s.cdir(id), "readings.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return []Reading{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []Reading{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r Reading
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue // torn last line after a crash
		}
		if since.IsZero() || r.RecordedAt.After(since) {
			out = append(out, r)
		}
	}
	return out, sc.Err()
}

func (s store) remove(id string) error {
	if _, err := s.load(id); err != nil {
		return err
	}
	if err := os.RemoveAll(s.cdir(id)); err != nil {
		return fmt.Errorf("delete collector %s: %w", id, err)
	}
	return nil
}
