package uplink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakePairHub(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/relay/pair" {
			http.NotFound(w, r)
			return
		}
		var in struct{ Code, Name string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if in.Code != "ABCD2345" {
			http.Error(w, "unknown code", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"deviceId": "dev-" + in.Name, "token": "tok"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPairSuccess(t *testing.T) {
	srv := fakePairHub(t)
	cfg, err := Pair(context.Background(), nil, srv.URL+"/", "ABCD2345", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hub != srv.URL || cfg.DeviceID != "dev-laptop" || cfg.Token != "tok" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestPairBadCode(t *testing.T) {
	srv := fakePairHub(t)
	_, err := Pair(context.Background(), nil, srv.URL, "WRONG", "laptop")
	if err == nil || !strings.Contains(err.Error(), "rejected the code") {
		t.Errorf("err = %v", err)
	}
}

func TestCheckHubURL(t *testing.T) {
	ok := []string{"https://hub.example.com", "http://localhost:8080", "http://127.0.0.1:9", "http://[::1]:9"}
	bad := []string{"http://hub.example.com", "ws://localhost", "hub.example.com", "ftp://x"}
	for _, u := range ok {
		if err := CheckHubURL(u); err != nil {
			t.Errorf("CheckHubURL(%q) = %v, want ok", u, err)
		}
	}
	for _, u := range bad {
		if err := CheckHubURL(u); err == nil {
			t.Errorf("CheckHubURL(%q) = nil, want error", u)
		}
	}
	if _, err := Pair(context.Background(), nil, "http://hub.example.com", "C", "n"); err == nil {
		t.Error("Pair accepted a plain-http remote hub")
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	if c, err := Load(dir); c != nil || err != nil {
		t.Fatalf("Load(absent) = %v, %v; want nil, nil", c, err)
	}
	want := Config{Hub: "https://h.example", DeviceID: "d", Token: "t"}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || *got != want {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, FileName))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", st.Mode().Perm())
		}
	}
}
