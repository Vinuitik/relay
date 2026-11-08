// Package collectors runs KPI collectors on this machine: small stdlib-only
// Python scripts that turn read-only snapshots of local data (ActivityWatch
// events, copied files) into one number per time window, always inside
// the OS sandbox (internal/collectors/sandbox) - no network, no host
// filesystem. The hub (HabitTracker) installs them, the user approves the
// data contract, the runner schedules them and the hub pulls readings.
// See runner/FLOWS.md "Collectors" and shared/API.md.
package collectors

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ErrBadRequest marks invalid input (manifest, files, window) → HTTP 400.
var ErrBadRequest = errors.New("bad request")

func badf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadRequest, fmt.Sprintf(format, a...))
}

// Source is one declared input. type "activitywatch": Buckets are bucket
// types ("window", "web", "afk"). type "file": Glob (absolute or "~/...",
// filepath.Match syntax, no "**") and Why (shown to the user). type
// "http": Credential (name in the runner's credential store), URL (https,
// host == the credential's host, optional {from}/{to} placeholders, see
// httpsource.go), Method ("" or "GET") and Why.
type Source struct {
	Type       string   `json:"type"`
	Buckets    []string `json:"buckets,omitempty"`
	Glob       string   `json:"glob,omitempty"`
	Credential string   `json:"credential,omitempty"`
	URL        string   `json:"url,omitempty"`
	Method     string   `json:"method,omitempty"`
	Why        string   `json:"why,omitempty"`
}

// Manifest is a collector's manifest.json. Only Schedule, Window and
// Sources form the contract (ContractHash); Unit/Aggregation are metadata.
type Manifest struct {
	Schedule    string   `json:"schedule"` // "hourly" | "daily HH:MM"
	Window      string   `json:"window"`   // "1h" | "1d"
	Sources     []Source `json:"sources"`
	Unit        string   `json:"unit"`
	Aggregation string   `json:"aggregation"` // "sum" | "last" | "max"

	// parsed from Schedule
	hourly         bool
	dailyH, dailyM int
}

const maxSources = 10

var (
	bucketRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	dailyRe  = regexp.MustCompile(`^daily ([01][0-9]|2[0-3]):([0-5][0-9])$`)
)

// ParseManifest decodes and validates raw, returning the manifest and its
// contract hash.
func ParseManifest(raw json.RawMessage) (Manifest, string, error) {
	var m Manifest
	if len(bytes.TrimSpace(raw)) == 0 {
		return m, "", badf("manifest is required")
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, "", badf("manifest: %v", err)
	}
	if err := m.validate(); err != nil {
		return m, "", err
	}
	h, err := ContractHash(raw)
	if err != nil {
		return m, "", err
	}
	return m, h, nil
}

func (m *Manifest) validate() error {
	switch {
	case m.Schedule == "hourly":
		m.hourly = true
		if m.Window != "1h" {
			return badf(`manifest: schedule "hourly" needs window "1h"`)
		}
	case dailyRe.MatchString(m.Schedule):
		g := dailyRe.FindStringSubmatch(m.Schedule)
		m.dailyH, _ = strconv.Atoi(g[1])
		m.dailyM, _ = strconv.Atoi(g[2])
		if m.Window != "1d" {
			return badf(`manifest: schedule "daily HH:MM" needs window "1d"`)
		}
	default:
		return badf(`manifest: schedule must be "hourly" or "daily HH:MM", got %q`, m.Schedule)
	}
	if strings.TrimSpace(m.Unit) == "" {
		return badf("manifest: unit is required")
	}
	switch m.Aggregation {
	case "sum", "last", "max":
	default:
		return badf(`manifest: aggregation must be "sum", "last" or "max"`)
	}
	if len(m.Sources) == 0 || len(m.Sources) > maxSources {
		return badf("manifest: 1-%d sources required", maxSources)
	}
	for i, s := range m.Sources {
		switch s.Type {
		case "activitywatch":
			if len(s.Buckets) == 0 {
				return badf("source %d: activitywatch needs buckets", i)
			}
			seen := map[string]bool{}
			for _, b := range s.Buckets {
				if !bucketRe.MatchString(b) || seen[b] {
					return badf("source %d: bad or duplicate bucket type %q", i, b)
				}
				seen[b] = true
			}
		case "file":
			if err := checkGlob(s.Glob); err != nil {
				return badf("source %d: %v", i, err)
			}
			if strings.TrimSpace(s.Why) == "" {
				return badf("source %d: file source needs why", i)
			}
		case "http":
			if err := checkHTTPSource(s); err != nil {
				return badf("source %d: %v", i, err)
			}
		default:
			return badf("source %d: unknown type %q", i, s.Type)
		}
	}
	return nil
}

func checkGlob(g string) error {
	if g == "" {
		return errors.New("file source needs glob")
	}
	if strings.Contains(g, "**") {
		return errors.New(`glob: "**" is not supported`)
	}
	if !strings.HasPrefix(g, "~/") && !filepath.IsAbs(g) {
		return fmt.Errorf("glob %q must be absolute or start with ~/", g)
	}
	if _, err := filepath.Match(g, ""); err != nil {
		return fmt.Errorf("glob %q: %v", g, err)
	}
	if why := denied(g); why != "" {
		return fmt.Errorf("glob %q reads a secret path (%s)", g, why)
	}
	return nil
}

// ContractHash is lowercase hex sha256 of the canonical JSON of the
// manifest's {sources, schedule, window}, exactly as sent: object keys
// sorted at every level, no whitespace, no HTML escaping (<>& literal),
// numbers verbatim. The hub must compute the same bytes.
func ContractHash(raw json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var all map[string]any
	if err := dec.Decode(&all); err != nil {
		return "", badf("manifest: %v", err)
	}
	contract := map[string]any{}
	for _, k := range []string{"sources", "schedule", "window"} {
		if v, ok := all[k]; ok {
			contract[k] = v
		}
	}
	b, err := canonicalJSON(contract)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalJSON: encoding/json already sorts map keys; disable HTML
// escaping and drop the encoder's trailing newline.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
