package api

import (
	"net/http"
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
