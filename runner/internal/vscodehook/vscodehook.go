// Package vscodehook tracks Claude in VS Code (same machine) through Claude
// Code hooks - see HANDOFF-chat-sync.md stage 2.
//
//	hook:    `runnerd vscode-hook <event>` → Handle → $RELAY_HOME/vscode-state/<id>.json
//	guard:   UserPromptSubmit on a chat that moved away → exit 2 (prompt blocked)
//	install: Install merges the hooks into <claude dir>/settings.json
//	read:    Busy / LastEvent (ChatBusy and GET /v1/vscode-tracking)
//
// The hook side FAILS OPEN: any problem → exit 0, never break the user's Claude.
package vscodehook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// StaleAfter: a busy/waiting state older than this counts as idle, so a
// crashed VS Code can't pin a chat forever.
const StaleAfter = 30 * time.Minute

// Subcommand is the runnerd argument that runs a hook; also what identifies
// our entries in settings.json.
const Subcommand = "vscode-hook"

// Events we install, in settings.json order.
var Events = []string{"UserPromptSubmit", "Stop", "Notification", "PermissionRequest"}

// PermissionHold is how long a PermissionRequest hook may wait for the phone
// (set from RELAY_PERMISSION_HOLD_SEC by the runner before Install).
var PermissionHold = 120 * time.Second

// Timeout is the settings.json hook timeout (seconds) for an event:
// PermissionRequest = hold + 30s, everything else 5s.
func Timeout(event string) int {
	if event == "PermissionRequest" {
		return int(PermissionHold/time.Second) + 30
	}
	return 5
}

// MovedMessage is shown to the user when a stale tab is blocked.
const MovedMessage = "This chat moved to another machine via Relay. Reopen it from history after it comes back, or continue on the phone."

var idPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// State is the content of a vscode-state file.
type State struct {
	State string `json:"state"` // busy | idle | waiting
	Event string `json:"event"`
	At    string `json:"at"` // RFC3339
}

// Input is the part of the hook's stdin JSON we use.
type Input struct {
	SessionID        string `json:"session_id"`
	TranscriptPath   string `json:"transcript_path"`
	HookEventName    string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
}

func stateDir(home string) string { return filepath.Join(home, "vscode-state") }

// StateFor maps an event to the state it records ("" = record nothing).
// idle_prompt notifications ("Claude is waiting for your input" after a
// finished turn) are ignored so they don't turn an idle chat back to waiting.
func StateFor(event, notificationType string) string {
	switch event {
	case "UserPromptSubmit":
		return "busy"
	case "Stop":
		return "idle"
	case "Notification":
		if notificationType == "idle_prompt" {
			return ""
		}
		return "waiting"
	}
	return ""
}

// ShouldBlock decides the stale-tab guard: the transcript is gone from this
// machine and a handed-off copy proves it moved away on purpose.
func ShouldBlock(transcriptExists, handedOffExists bool) bool {
	return !transcriptExists && handedOffExists
}

func exists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// Handle processes one hook call. Returns the process exit code and the
// stderr text (only meaningful for exit 2). Never fails loudly.
func Handle(event string, stdin []byte, home string, now time.Time) (int, string) {
	var in Input
	if json.Unmarshal(stdin, &in) != nil || !idPattern.MatchString(in.SessionID) {
		return 0, ""
	}
	if in.HookEventName != "" {
		event = in.HookEventName
	}
	if event == "UserPromptSubmit" {
		handed := filepath.Join(home, "handed-off", in.SessionID+".jsonl")
		if ShouldBlock(exists(in.TranscriptPath), exists(handed)) {
			return 2, MovedMessage
		}
	}
	st := StateFor(event, in.NotificationType)
	if st == "" {
		return 0, ""
	}
	_ = writeState(home, in.SessionID, State{State: st, Event: event, At: now.UTC().Format(time.RFC3339)})
	return 0, ""
}

func writeState(home, id string, s State) error {
	dir := stateDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(s)
	dst := filepath.Join(dir, id+".json")
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// ParseBusy reports whether a state file's content means "VS Code is
// mid-turn" at time now (busy/waiting and younger than StaleAfter).
func ParseBusy(data []byte, now time.Time) bool {
	var s State
	if json.Unmarshal(data, &s) != nil {
		return false
	}
	if s.State != "busy" && s.State != "waiting" {
		return false
	}
	at, err := time.Parse(time.RFC3339, s.At)
	if err != nil {
		return false
	}
	return now.Sub(at) < StaleAfter
}

// Busy reads the chat's state file.
func Busy(home, id string, now time.Time) bool {
	if !idPattern.MatchString(id) {
		return false
	}
	data, err := os.ReadFile(filepath.Join(stateDir(home), id+".json"))
	if err != nil {
		return false
	}
	return ParseBusy(data, now)
}

// LastEvent is the newest hook event time across all state files.
func LastEvent(home string) (time.Time, bool) {
	files, _ := filepath.Glob(filepath.Join(stateDir(home), "*.json"))
	var best time.Time
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s State
		if json.Unmarshal(data, &s) != nil {
			continue
		}
		if at, err := time.Parse(time.RFC3339, s.At); err == nil && at.After(best) {
			best = at
		}
	}
	return best, !best.IsZero()
}

// ---- installer ----

// IsOurs reports whether a settings.json hook command is one of ours.
func IsOurs(cmd string) bool { return strings.Contains(cmd, " "+Subcommand+" ") }

// Command builds the hook command line for a binary path and event.
func Command(exe, event string) string {
	return fmt.Sprintf("\"%s\" %s %s", filepath.ToSlash(exe), Subcommand, event)
}

// Merge adds/updates our hooks in a parsed settings object, preserving
// everything else. Returns whether anything changed.
func Merge(settings map[string]any, exe string) bool {
	changed := false
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		settings["hooks"] = hooks
		changed = true
	}
	for _, ev := range Events {
		want := Command(exe, ev)
		groups, _ := hooks[ev].([]any)
		found := false
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			hs, _ := gm["hooks"].([]any)
			for _, h := range hs {
				hm, _ := h.(map[string]any)
				cmd, _ := hm["command"].(string)
				if !IsOurs(cmd) {
					continue
				}
				found = true
				if cmd != want {
					hm["command"] = want
					changed = true
				}
				if t, _ := hm["timeout"].(json.Number); t.String() != fmt.Sprint(Timeout(ev)) {
					hm["timeout"] = Timeout(ev)
					changed = true
				}
			}
		}
		if !found {
			groups = append(groups, map[string]any{"hooks": []any{
				map[string]any{"type": "command", "command": want, "timeout": Timeout(ev)},
			}})
			hooks[ev] = groups
			changed = true
		}
	}
	return changed
}

// HasAll reports whether settings contains our hook for every event.
func HasAll(settings map[string]any) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	for _, ev := range Events {
		ok := false
		groups, _ := hooks[ev].([]any)
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			hs, _ := gm["hooks"].([]any)
			for _, h := range hs {
				hm, _ := h.(map[string]any)
				if cmd, _ := hm["command"].(string); IsOurs(cmd) {
					ok = true
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func readSettings(path string) (map[string]any, []byte, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	m := map[string]any{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return m, raw, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, raw, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, raw, nil
}

// Install idempotently merges our hooks into <claudeDir>/settings.json.
// An unparsable settings file is left untouched (returns the error). The
// original is backed up once to settings.json.relay-bak; the write is atomic.
func Install(claudeDir, exe string) error {
	path := filepath.Join(claudeDir, "settings.json")
	settings, raw, err := readSettings(path)
	if err != nil {
		return err
	}
	if !Merge(settings, exe) {
		return nil
	}
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return err
	}
	if raw != nil {
		bak := path + ".relay-bak"
		if _, err := os.Stat(bak); os.IsNotExist(err) {
			if err := os.WriteFile(bak, raw, 0o600); err != nil {
				return err
			}
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return err
	}
	tmp := path + ".relay-tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Installed reports whether settings.json currently has all our hooks.
func Installed(claudeDir string) bool {
	m, _, err := readSettings(filepath.Join(claudeDir, "settings.json"))
	return err == nil && HasAll(m)
}

// DefaultHome resolves RELAY_HOME (else ~/.relay) without touching config, so
// the hook never creates keys or state as a side effect.
func DefaultHome() string {
	if h := os.Getenv("RELAY_HOME"); h != "" {
		return h
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(u, ".relay")
}
