// Package updatestatus is how keeperd tells the runner whether its update
// checks are working, so the phone isn't blind to a stuck updater: keeperd
// writes a small JSON file after every check (Record), the runner reads it
// into GET /v1/runner/info (Read). A file, not an API call, so it survives
// runner restarts. See runner/FLOWS.md "keeperd".
package updatestatus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// EnvVar carries the file's path from keeperd to the runner it starts.
const EnvVar = "RELAY_UPDATE_STATUS"

// FileName is the status file's name in keeperd's install dir.
const FileName = "update-status.json"

// Status is keeperd's record of its update checks.
type Status struct {
	// KeeperVersion is the keeperd build that wrote this ("runner-<sha>").
	KeeperVersion string `json:"keeperVersion"`
	// LastCheckAt is when the last check finished (RFC3339).
	LastCheckAt string `json:"lastCheckAt"`
	// LastError is the last check's error, "" if it worked.
	LastError string `json:"lastError,omitempty"`
	// FailingSince is when the current run of failed checks began, ""
	// while checks work.
	FailingSince string `json:"failingSince,omitempty"`
	// Failures counts consecutive failed checks.
	Failures int `json:"failures,omitempty"`
}

// Path is the status file inside keeperd's install dir.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Record updates the file at path with one check's outcome (err == nil:
// it worked), keeping FailingSince across consecutive failures. Best
// effort: a write failure is ignored (it only costs visibility).
func Record(path, keeperVersion string, err error, now time.Time) {
	prev, _ := Read(path)
	st := Status{KeeperVersion: keeperVersion, LastCheckAt: now.UTC().Format(time.RFC3339)}
	if err != nil {
		st.LastError = err.Error()
		st.Failures = prev.Failures + 1
		st.FailingSince = prev.FailingSince
		if st.FailingSince == "" {
			st.FailingSince = st.LastCheckAt
		}
	}
	b, mErr := json.MarshalIndent(st, "", " ")
	if mErr != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// Read loads the file at path; ok is false if it's missing or unreadable
// (no keeperd, or one too old to write it).
func Read(path string) (st Status, ok bool) {
	if path == "" {
		return Status{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Status{}, false
	}
	if json.Unmarshal(b, &st) != nil {
		return Status{}, false
	}
	return st, true
}
