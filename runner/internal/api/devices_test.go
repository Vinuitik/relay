package api

import (
	"net/http"
	"testing"

	"relay/runner/internal/notify"
)

func TestRegisterDevice_ReturnsDevice(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()

	rec := doRequest(t, h, "POST", "/v1/devices", testKey, map[string]string{"fcmToken": "tok-a"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var d notify.Device
	decodeBody(t, rec, &d)
	if d.ID == "" || d.FCMToken != "tok-a" || d.RegisteredAt == "" {
		t.Fatalf("unexpected device: %+v", d)
	}
}

func TestRegisterDevice_ReRegisterUpdatesNotDuplicates(t *testing.T) {
	s := newTestServer(t)
	h := s.Routes()

	rec := doRequest(t, h, "POST", "/v1/devices", testKey, map[string]string{"fcmToken": "tok-a"})
	var first notify.Device
	decodeBody(t, rec, &first)

	rec = doRequest(t, h, "POST", "/v1/devices", testKey, map[string]string{"fcmToken": "tok-a"})
	var second notify.Device
	decodeBody(t, rec, &second)

	if first.ID != second.ID {
		t.Fatalf("re-registering the same token changed ID: %q -> %q", first.ID, second.ID)
	}
	if len(s.Devices.List()) != 1 {
		t.Fatalf("registry has %d devices, want 1 (no duplicate)", len(s.Devices.List()))
	}
}

func TestRegisterDevice_MissingTokenIs400(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), "POST", "/v1/devices", testKey, map[string]string{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRegisterDevice_RequiresAuth(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), "POST", "/v1/devices", "", map[string]string{"fcmToken": "tok-a"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
