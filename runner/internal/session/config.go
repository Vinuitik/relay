package session

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"relay/runner/internal/acp"
)

// ConfigValue is one chosen config option value, e.g. {model, opus}.
type ConfigValue struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// isModeOption: the permission mode is also listed as a config option, but
// it's handled by Mode/SetMode, so config code leaves it alone.
func isModeOption(o acp.ConfigOption) bool { return o.Category == "mode" || o.ID == "mode" }

// chosenConfig is what a session currently has selected (mode excluded), in
// the agent's order - model comes before effort, which matters because the
// effort levels on offer depend on the model.
func chosenConfig(opts []acp.ConfigOption) []ConfigValue {
	var out []ConfigValue
	for _, o := range opts {
		if !isModeOption(o) && o.Current() != "" {
			out = append(out, ConfigValue{ID: o.ID, Value: o.Current()})
		}
	}
	return out
}

// applyConfig sets each wanted value the agent offers and doesn't already
// have, returning the resulting option list. Failures are logged, not fatal:
// a model that disappeared just leaves the agent's default.
func applyConfig(sessionID string, client *acp.Client, sid string, opts []acp.ConfigOption, want []ConfigValue) []acp.ConfigOption {
	for _, w := range want {
		var cur *acp.ConfigOption
		for i := range opts {
			if opts[i].ID == w.ID {
				cur = &opts[i]
			}
		}
		if cur == nil || isModeOption(*cur) || cur.Current() == w.Value || !cur.Has(w.Value) {
			continue
		}
		next, err := client.SetConfigOption(sid, w.ID, w.Value)
		if err != nil {
			log.Printf("session %s: set %s=%q: %v", sessionID, w.ID, w.Value, err)
			continue
		}
		if next != nil {
			opts = next
		}
	}
	return opts
}

// SetConfig changes one config option (model, effort, fast mode) of a
// session. A dormant session just records it; attach applies it when the
// agent resumes. The choice also becomes this runner's default for new
// sessions (configDefaults).
func (m *Manager) SetConfig(sessionID, configID, value string) (Session, error) {
	rec, err := m.lookup(sessionID)
	if err != nil {
		return Session{}, err
	}
	if !rec.isACP() {
		return Session{}, ErrNotSupported
	}
	rec.mu.Lock()
	terminal := isTerminal(rec.data.State)
	client, sid := rec.acp, rec.acpSession
	var opt *acp.ConfigOption
	for i := range rec.data.ConfigOptions {
		if rec.data.ConfigOptions[i].ID == configID {
			o := rec.data.ConfigOptions[i]
			opt = &o
		}
	}
	rec.mu.Unlock()
	if terminal {
		return Session{}, ErrSessionFinished
	}
	if opt == nil || isModeOption(*opt) || !opt.Has(value) {
		return Session{}, fmt.Errorf("%w: %s=%q", ErrInvalidChoice, configID, value)
	}

	var next []acp.ConfigOption
	if client != nil {
		if next, err = client.SetConfigOption(sid, configID, value); err != nil {
			return Session{}, err
		}
	}
	rec.mu.Lock()
	if next != nil {
		rec.data.ConfigOptions = next
	} else {
		for i := range rec.data.ConfigOptions {
			if rec.data.ConfigOptions[i].ID == configID {
				rec.data.ConfigOptions[i].CurrentValue = value
			}
		}
	}
	chosen := chosenConfig(rec.data.ConfigOptions)
	snapshot := cloneSession(rec.data)
	rec.mu.Unlock()
	m.setConfigDefaults(chosen)
	return snapshot, nil
}

// configDefaults is what new sessions start with: the last choices made on
// this runner, persisted to $RELAY_HOME/session-defaults.json.
func (m *Manager) configDefaults() []ConfigValue {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ConfigValue(nil), m.defaults...)
}

func (m *Manager) setConfigDefaults(v []ConfigValue) {
	m.mu.Lock()
	m.defaults = v
	file := m.defaultsFile
	m.mu.Unlock()
	if file == "" {
		return
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(file, b, 0o600); err != nil {
		log.Printf("save session defaults: %v", err)
	}
}

// loadConfigDefaults reads session-defaults.json next to the sessions dir.
func (m *Manager) loadConfigDefaults(storeDir string) {
	file := filepath.Join(filepath.Dir(storeDir), "session-defaults.json")
	var v []ConfigValue
	if b, err := os.ReadFile(file); err == nil {
		if err := json.Unmarshal(b, &v); err != nil {
			log.Printf("read %s: %v", file, err)
		}
	}
	m.mu.Lock()
	m.defaultsFile, m.defaults = file, v
	m.mu.Unlock()
}
