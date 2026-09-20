package waker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeSender records payloads instead of touching the network.
type fakeSender struct {
	mu      sync.Mutex
	packets [][]byte
	err     error
}

func (f *fakeSender) SendBroadcast(payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.packets = append(f.packets, append([]byte(nil), payload...))
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.packets)
}

// fakeProber answers "down" for the first downFor probes, then "up".
// upAfter < 0 means never up.
type fakeProber struct {
	mu      sync.Mutex
	calls   int
	upAfter int
}

func (p *fakeProber) Probe(ctx context.Context, m Machine) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.upAfter < 0 {
		return false
	}
	return p.calls > p.upAfter
}

const testKey = "test-key"

func testServer(sender *fakeSender, prober Prober) *Server {
	return &Server{
		Key: testKey,
		Machines: []Machine{
			{ID: "dell", Name: "Dell laptop", MAC: "34:CF:F6:81:08:76", ProbeHost: "192.168.1.20"},
			{ID: "broken", Name: "Typo'd MAC", MAC: "not-a-mac"},
		},
		Sender:       sender,
		Prober:       prober,
		WakeTimeout:  40 * time.Millisecond,
		PollInterval: time.Millisecond,
		Logf:         func(string, ...any) {},
	}
}

func do(t *testing.T, s *Server, method, path string, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if key != "" {
		req.Header.Set("X-Relay-Key", key)
	}
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

func TestHealthNeedsNoAuth(t *testing.T) {
	s := testServer(&fakeSender{}, &fakeProber{upAfter: -1})
	rec := do(t, s, "GET", "/v1/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestAuthRequired(t *testing.T) {
	s := testServer(&fakeSender{}, &fakeProber{upAfter: -1})
	for _, path := range []string{"/v1/machines", "/v1/machines/dell/status"} {
		if rec := do(t, s, "GET", path, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with bad key: status = %d, want 401", path, rec.Code)
		}
		if rec := do(t, s, "GET", path, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with no key: status = %d, want 401", path, rec.Code)
		}
	}
	if rec := do(t, s, "POST", "/v1/machines/dell/wake", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wake with bad key: status = %d, want 401", rec.Code)
	}
}

func TestListMachines(t *testing.T) {
	// upAfter 0 => every probe reports up.
	s := testServer(&fakeSender{}, &fakeProber{upAfter: 0})
	rec := do(t, s, "GET", "/v1/machines", testKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got []machineView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "dell" || got[0].Name != "Dell laptop" || got[0].MAC != "34:CF:F6:81:08:76" {
		t.Errorf("entry[0] = %+v", got[0])
	}
	if !got[0].Up {
		t.Error("entry[0].Up = false, want true")
	}
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		upAfter  int
		wantCode int
		wantUp   bool
	}{
		{"up", "dell", 0, http.StatusOK, true},
		{"down", "dell", -1, http.StatusOK, false},
		{"unknown machine", "nope", 0, http.StatusNotFound, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(&fakeSender{}, &fakeProber{upAfter: tt.upAfter})
			rec := do(t, s, "GET", "/v1/machines/"+tt.id+"/status", testKey)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantCode != http.StatusOK {
				return
			}
			var got statusResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.ID != tt.id || got.Up != tt.wantUp {
				t.Errorf("got %+v, want id=%s up=%v", got, tt.id, tt.wantUp)
			}
		})
	}
}

func TestWake(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		upAfter     int
		sendErr     error
		wantCode    int
		wantWoken   bool
		wantPackets int
	}{
		// First probe is the "already up?" short-circuit; probe #2 onward
		// are the poll. upAfter 2 => comes up on the second poll.
		{"happy path", "dell", 2, nil, http.StatusOK, true, 1},
		{"already up", "dell", 0, nil, http.StatusOK, true, 0},
		{"never wakes", "dell", -1, nil, http.StatusOK, false, 1},
		{"unknown machine", "nope", -1, nil, http.StatusNotFound, false, 0},
		{"bad mac", "broken", -1, nil, http.StatusBadRequest, false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &fakeSender{err: tt.sendErr}
			s := testServer(sender, &fakeProber{upAfter: tt.upAfter})

			rec := do(t, s, "POST", "/v1/machines/"+tt.id+"/wake", testKey)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantCode, rec.Body)
			}
			if sender.count() != tt.wantPackets {
				t.Errorf("packets sent = %d, want %d", sender.count(), tt.wantPackets)
			}
			if tt.wantCode != http.StatusOK {
				return
			}

			var got wakeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.ID != tt.id {
				t.Errorf("id = %q, want %q", got.ID, tt.id)
			}
			if got.Woken != tt.wantWoken {
				t.Errorf("woken = %v, want %v", got.Woken, tt.wantWoken)
			}
			if got.WaitedSeconds < 0 {
				t.Errorf("waitedSeconds = %d, want >= 0", got.WaitedSeconds)
			}
			if tt.wantPackets > 0 && len(sender.packets[0]) != 102 {
				t.Errorf("packet length = %d, want 102", len(sender.packets[0]))
			}
		})
	}
}
