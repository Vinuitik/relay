package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"relay/runner/internal/uplink"
)

func TestRunPair(t *testing.T) {
	var gotName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/relay/pair" {
			http.NotFound(w, r)
			return
		}
		var in struct{ Code, Name string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Code != "ABCD2345" {
			http.NotFound(w, r)
			return
		}
		gotName = in.Name
		w.Write([]byte(`{"deviceId":"d1","token":"tok"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	var out bytes.Buffer
	if err := runPair([]string{"--hub", srv.URL, "--code", "ABCD2345", "--name", "laptop"}, dir, &out); err != nil {
		t.Fatal(err)
	}
	if gotName != "laptop" || !strings.Contains(out.String(), "paired as laptop") {
		t.Errorf("name sent %q, output %q", gotName, out.String())
	}
	cfg, err := uplink.Load(dir)
	if err != nil || cfg == nil || cfg.DeviceID != "d1" || cfg.Token != "tok" || cfg.Hub != srv.URL {
		t.Fatalf("hub.json = %+v, %v", cfg, err)
	}

	// Default name = hostname (non-empty).
	if err := runPair([]string{"--hub", srv.URL, "--code", "ABCD2345"}, t.TempDir(), &out); err != nil || gotName == "" {
		t.Errorf("default name: err %v, name %q", err, gotName)
	}
	if err := runPair([]string{"--hub", srv.URL, "--code", "NOPE"}, t.TempDir(), &out); err == nil {
		t.Error("bad code accepted")
	}
	if err := runPair([]string{"--hub", "http://hub.example.com", "--code", "ABCD2345"}, t.TempDir(), &out); err == nil {
		t.Error("plain-http remote hub accepted")
	}
}
