package waker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCreatesConfigOnFirstRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WAKER_HOME", home)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.FirstRun {
		t.Error("FirstRun = false, want true")
	}
	if len(cfg.Key) != 64 {
		t.Errorf("key length = %d, want 64 hex chars (32 bytes)", len(cfg.Key))
	}
	if len(cfg.Machines) != 0 {
		t.Errorf("machines = %v, want empty", cfg.Machines)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("listen addr = %q, want %q", cfg.ListenAddr, DefaultListenAddr)
	}

	// The file must exist and round-trip.
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse written config: %v", err)
	}
	if f.Key != cfg.Key {
		t.Errorf("written key = %q, want %q", f.Key, cfg.Key)
	}

	// Second load must reuse, not regenerate.
	cfg2, err := Load()
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if cfg2.FirstRun {
		t.Error("second Load FirstRun = true, want false")
	}
	if cfg2.Key != cfg.Key {
		t.Errorf("key changed across loads: %q -> %q", cfg.Key, cfg2.Key)
	}
}

func TestLoadExisting(t *testing.T) {
	tests := []struct {
		name         string
		contents     string
		listenEnv    string
		wantErr      bool
		wantKey      string
		wantMachines int
		wantListen   string
	}{
		{
			name:         "one machine",
			contents:     `{"key":"abc","machines":[{"id":"dell","name":"Dell laptop","mac":"34:CF:F6:81:08:76"}]}`,
			wantKey:      "abc",
			wantMachines: 1,
			wantListen:   DefaultListenAddr,
		},
		{
			name:       "listen addr override",
			contents:   `{"key":"abc","machines":[]}`,
			listenEnv:  "0.0.0.0:9999",
			wantKey:    "abc",
			wantListen: "0.0.0.0:9999",
		},
		{
			name:     "malformed json",
			contents: `{not json`,
			wantErr:  true,
		},
		{
			name:     "missing key",
			contents: `{"machines":[]}`,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("WAKER_HOME", home)
			t.Setenv("WAKER_LISTEN_ADDR", tt.listenEnv)
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(tt.contents), 0o600); err != nil {
				t.Fatalf("seed config: %v", err)
			}

			cfg, err := Load()
			if tt.wantErr {
				if err == nil {
					t.Fatal("Load succeeded, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Key != tt.wantKey {
				t.Errorf("key = %q, want %q", cfg.Key, tt.wantKey)
			}
			if len(cfg.Machines) != tt.wantMachines {
				t.Errorf("machines = %d, want %d", len(cfg.Machines), tt.wantMachines)
			}
			if cfg.ListenAddr != tt.wantListen {
				t.Errorf("listen = %q, want %q", cfg.ListenAddr, tt.wantListen)
			}
		})
	}
}

func TestMachineHostPortDefaults(t *testing.T) {
	tests := []struct {
		name     string
		machine  Machine
		wantHost string
		wantPort int
	}{
		{"falls back to id and default port", Machine{ID: "dell"}, "dell", DefaultProbePort},
		{"explicit host", Machine{ID: "dell", ProbeHost: "192.168.1.20"}, "192.168.1.20", DefaultProbePort},
		{"explicit port", Machine{ID: "dell", ProbePort: 22}, "dell", 22},
		{"both", Machine{ID: "dell", ProbeHost: "pc.local", ProbePort: 8080}, "pc.local", 8080},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.machine.Host(); got != tt.wantHost {
				t.Errorf("Host() = %q, want %q", got, tt.wantHost)
			}
			if got := tt.machine.Port(); got != tt.wantPort {
				t.Errorf("Port() = %d, want %d", got, tt.wantPort)
			}
		})
	}
}
