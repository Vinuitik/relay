package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestRunPairByQR(t *testing.T) {
	pollEvery = 10 * time.Millisecond
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/relay/pair/start":
			w.Write([]byte(`{"claimId":"CLAIMID","pollSecret":"sec","expiresInSeconds":60}`))
		case "/relay/pair/poll":
			var in struct{ ClaimID, PollSecret string }
			json.NewDecoder(r.Body).Decode(&in)
			if in.ClaimID != "CLAIMID" || in.PollSecret != "sec" {
				http.NotFound(w, r)
				return
			}
			if polls++; polls < 3 {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			w.Write([]byte(`{"deviceId":"d2","token":"tok2"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	var out bytes.Buffer
	if err := runPair([]string{"--hub", srv.URL, "--name", "desk"}, dir, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), srv.URL+"/relay/claim/CLAIMID") || !strings.Contains(out.String(), "paired as desk") {
		t.Errorf("output %q", out.String())
	}
	cfg, err := uplink.Load(dir)
	if err != nil || cfg == nil || cfg.DeviceID != "d2" || cfg.Token != "tok2" {
		t.Fatalf("hub.json = %+v, %v", cfg, err)
	}
}
