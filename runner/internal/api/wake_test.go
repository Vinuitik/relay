package api

import (
	"bytes"
	"net/http"
	"testing"

	"relay/runner/internal/wol"
)

func TestWake_Returns202AndInvokesSender(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()

	rec := doRequest(t, h, "POST", "/v1/wake", testKey, map[string]string{"mac": "AA:BB:CC:DD:EE:FF"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", rec.Code, rec.Body.String())
	}

	fake, ok := s.Sender.(*fakeSender)
	if !ok {
		t.Fatalf("s.Sender = %T, want *fakeSender", s.Sender)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("sender invoked %d times, want 1", len(fake.sent))
	}
	want, err := wol.BuildMagicPacket("AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatalf("BuildMagicPacket: %v", err)
	}
	if !bytes.Equal(fake.sent[0], want) {
		t.Fatalf("sent payload mismatch")
	}
}

func TestWake_MalformedMACIs400(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()

	rec := doRequest(t, h, "POST", "/v1/wake", testKey, map[string]string{"mac": "not-a-mac"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}

	fake, ok := s.Sender.(*fakeSender)
	if !ok {
		t.Fatalf("s.Sender = %T, want *fakeSender", s.Sender)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("sender should not have been invoked for malformed MAC, got %d calls", len(fake.sent))
	}
}

func TestWake_RequiresAuth(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), "POST", "/v1/wake", "", map[string]string{"mac": "AA:BB:CC:DD:EE:FF"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
