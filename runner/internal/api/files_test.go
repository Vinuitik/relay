package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestListFilesRoot(t *testing.T) {
	s := newTestServer(t)
	p, err := s.Projects.Create("Files Project")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(p.Path, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(p.Path, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	rec := doRequest(t, s.Routes(), http.MethodGet, "/v1/projects/"+p.ID+"/files", testKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var entries []fileEntry
	decodeBody(t, rec, &entries)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2", entries)
	}
}

func TestListFilesUnknownProject(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), http.MethodGet, "/v1/projects/nope/files", testKey, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestFileContentReturnsText(t *testing.T) {
	s := newTestServer(t)
	p, err := s.Projects.Create("Content Project")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(p.Path, "notes.txt"), []byte("hello world"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	rec := doRequest(t, s.Routes(), http.MethodGet, "/v1/projects/"+p.ID+"/files/content?path=notes.txt", testKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got fileContent
	decodeBody(t, rec, &got)
	if got.Content != "hello world" {
		t.Errorf("Content = %q, want %q", got.Content, "hello world")
	}
}

func TestFileContentRejectsPathEscape(t *testing.T) {
	s := newTestServer(t)
	p, err := s.Projects.Create("Escape Project")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doRequest(t, s.Routes(), http.MethodGet, "/v1/projects/"+p.ID+"/files/content?path=../../etc/passwd", testKey, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestFileContentRejectsBinary(t *testing.T) {
	s := newTestServer(t)
	p, err := s.Projects.Create("Binary Project")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(p.Path, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	rec := doRequest(t, s.Routes(), http.MethodGet, "/v1/projects/"+p.ID+"/files/content?path=blob.bin", testKey, nil)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415, body = %s", rec.Code, rec.Body.String())
	}
}

func TestFileContentRejectsDirectory(t *testing.T) {
	s := newTestServer(t)
	p, err := s.Projects.Create("Dir Content Project")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.Mkdir(filepath.Join(p.Path, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	rec := doRequest(t, s.Routes(), http.MethodGet, "/v1/projects/"+p.ID+"/files/content?path=sub", testKey, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func writeReq(t *testing.T, s *Server, pid string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return doRequest(t, s.Routes(), http.MethodPut, "/v1/projects/"+pid+"/files/content", testKey, body)
}

func TestWriteFileRoundTrip(t *testing.T) {
	s := newTestServer(t)
	p, _ := s.Projects.Create("Edit Project")
	f := filepath.Join(p.Path, "notes.txt")
	if err := os.WriteFile(f, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	rec := doRequest(t, s.Routes(), http.MethodGet, "/v1/projects/"+p.ID+"/files/content?path=notes.txt", testKey, nil)
	var got fileContent
	decodeBody(t, rec, &got)
	if got.Hash == "" {
		t.Fatal("GET returned no hash")
	}

	rec = writeReq(t, s, p.ID, map[string]string{"path": "notes.txt", "content": "new", "baseHash": got.Hash})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	data, _ := os.ReadFile(f)
	if string(data) != "new" {
		t.Errorf("file = %q, want new", data)
	}
	if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640 kept", fi.Mode().Perm())
	}

	// Stale hash (the old one) → 409, file untouched.
	rec = writeReq(t, s, p.ID, map[string]string{"path": "notes.txt", "content": "x", "baseHash": got.Hash})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale status = %d, want 409", rec.Code)
	}
	if data, _ := os.ReadFile(f); string(data) != "new" {
		t.Errorf("file changed on conflict: %q", data)
	}
}

func TestWriteFileRefusals(t *testing.T) {
	s := newTestServer(t)
	p, _ := s.Projects.Create("Refuse Project")
	_ = os.Mkdir(filepath.Join(p.Path, "dir"), 0o755)
	_ = os.WriteFile(filepath.Join(p.Path, "bin"), []byte{0, 1, 2}, 0o600)
	cases := []struct {
		body map[string]string
		want int
	}{
		{map[string]string{"path": "../escape.txt", "content": "x", "baseHash": "h"}, http.StatusBadRequest},
		{map[string]string{"path": "missing.txt", "content": "x", "baseHash": "h"}, http.StatusNotFound},
		{map[string]string{"path": "dir", "content": "x", "baseHash": "h"}, http.StatusBadRequest},
		{map[string]string{"path": "bin", "content": "x", "baseHash": contentHash([]byte{0, 1, 2})}, http.StatusUnsupportedMediaType},
		{map[string]string{"path": "notes.txt", "content": "x"}, http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := writeReq(t, s, p.ID, c.body); rec.Code != c.want {
			t.Errorf("%v: status = %d, want %d (%s)", c.body, rec.Code, c.want, rec.Body.String())
		}
	}
}
