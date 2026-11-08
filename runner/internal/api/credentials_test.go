package api

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"

	"relay/runner/internal/credentials"
)

func TestCredentialsEndpointsNeverLeak(t *testing.T) {
	const secret = "ghp_SUPERSECRET_value"
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	s := newTestServer(t)
	h := s.Routes()
	if rec := doRequest(t, h, http.MethodGet, "/v1/credentials", testKey, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled = %d", rec.Code)
	}
	s.Credentials = credentials.Open(t.TempDir())
	h = s.Routes()

	if rec := doRequest(t, h, http.MethodPut, "/v1/credentials/github", "wrong", map[string]any{"host": "api.github.com", "auth": "bearer", "secret": secret}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no key = %d", rec.Code)
	}
	var all strings.Builder
	rec := doRequest(t, h, http.MethodPut, "/v1/credentials/github", testKey, map[string]any{"host": "api.github.com", "auth": "bearer", "secret": secret})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	all.WriteString(rec.Body.String())
	for _, bad := range []map[string]any{
		{"host": "127.0.0.1", "auth": "bearer", "secret": secret},
		{"host": "api.github.com", "auth": "nope", "secret": secret},
		{"host": "api.github.com", "auth": "header", "headerName": "Host", "secret": secret},
		{"host": "api.github.com", "auth": "bearer", "secret": secret + "\n"},
	} {
		rec := doRequest(t, h, http.MethodPut, "/v1/credentials/x", testKey, bad)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%v = %d", bad["host"], rec.Code)
		}
		all.WriteString(rec.Body.String())
	}
	rec = doRequest(t, h, http.MethodPut, "/v1/credentials/Bad!", testKey, map[string]any{"host": "api.github.com", "auth": "bearer", "secret": secret})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad name = %d", rec.Code)
	}
	rec = doRequestRaw(t, h, http.MethodPut, "/v1/credentials/x", `{"secret":"`+secret+`", broken`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("broken json = %d", rec.Code)
	}
	all.WriteString(rec.Body.String())

	rec = doRequest(t, h, http.MethodGet, "/v1/credentials", testKey, nil)
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list) != 1 || list[0]["name"] != "github" || list[0]["host"] != "api.github.com" || list[0]["auth"] != "bearer" || list[0]["updatedAt"] == nil {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}
	if len(list[0]) != 4 {
		t.Errorf("GET has extra fields: %v", list[0])
	}
	all.WriteString(rec.Body.String())
	if rec := doRequest(t, h, http.MethodDelete, "/v1/credentials/github", testKey, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", rec.Code)
	}
	if rec := doRequest(t, h, http.MethodDelete, "/v1/credentials/github", testKey, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE again = %d", rec.Code)
	}
	if strings.Contains(all.String(), secret) {
		t.Errorf("a response leaks the secret: %s", all.String())
	}
	if strings.Contains(logs.String(), secret) {
		t.Errorf("logs leak the secret: %s", logs.String())
	}
}
