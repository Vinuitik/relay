package api

import (
	"net/http"
	"reflect"
	"testing"

	"relay/runner/internal/compose"
)

// composeFake records compose calls in order, e.g. "down docker-compose.dev.yml".
type composeFake struct {
	files []string
	calls []string
}

func newContainersTestServer(t *testing.T, files ...string) (*Server, *composeFake) {
	t.Helper()
	s := newTestServer(t)
	s.runSync = true
	f := &composeFake{files: files}
	s.Compose = ComposeFuncs{
		Detect: func(string) (compose.Detected, error) {
			return compose.Detected{ComposeFiles: f.files}, nil
		},
		Status: func(string) ([]compose.Container, error) { return []compose.Container{}, nil },
		Up:     func(_, file string) error { f.calls = append(f.calls, "up "+file); return nil },
		Down:   func(_, file string) error { f.calls = append(f.calls, "down "+file); return nil },
	}
	return s, f
}

func TestContainersStartDefaultFileWithoutBody(t *testing.T) {
	s, f := newContainersTestServer(t, "docker-compose.yml")
	h := s.Routes()
	p := createProject(t, h, "One File")

	rec := doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/containers/start", testKey, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", rec.Code, rec.Body.String())
	}
	if want := []string{"up docker-compose.yml"}; !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
}

func TestContainersSwitchStopsPreviousFirst(t *testing.T) {
	s, f := newContainersTestServer(t, "docker-compose.dev.yml", "docker-compose.prod.yml")
	h := s.Routes()
	p := createProject(t, h, "Dev Prod")

	// Two files, none chosen: must pick one.
	rec := doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/containers/start", testKey, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no-choice status = %d, want 400", rec.Code)
	}

	rec = doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/containers/start", testKey, map[string]string{"file": "docker-compose.dev.yml"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start dev status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/containers/start", testKey, map[string]string{"file": "docker-compose.prod.yml"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("switch status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var st ContainersStatus
	decodeBody(t, rec, &st)
	if st.ActiveFile != "docker-compose.prod.yml" {
		t.Errorf("ActiveFile = %q, want prod", st.ActiveFile)
	}
	want := []string{"up docker-compose.dev.yml", "down docker-compose.dev.yml", "up docker-compose.prod.yml"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}

	// The choice survives: GET reports it, stop uses it.
	rec = doRequest(t, h, "GET", "/v1/projects/"+p.ID+"/containers", testKey, nil)
	decodeBody(t, rec, &st)
	if st.ActiveFile != "docker-compose.prod.yml" {
		t.Errorf("GET ActiveFile = %q, want prod", st.ActiveFile)
	}
	rec = doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/containers/stop", testKey, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("stop status = %d", rec.Code)
	}
	if last := f.calls[len(f.calls)-1]; last != "down docker-compose.prod.yml" {
		t.Errorf("last call = %q, want down prod", last)
	}
}

func TestContainersStartUnknownFileIs400(t *testing.T) {
	s, _ := newContainersTestServer(t, "docker-compose.yml")
	h := s.Routes()
	p := createProject(t, h, "Unknown File")
	rec := doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/containers/start", testKey, map[string]string{"file": "../evil.yml"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestContainersNoComposeFileIs400(t *testing.T) {
	s, _ := newContainersTestServer(t)
	h := s.Routes()
	p := createProject(t, h, "No Compose")
	rec := doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/containers/stop", testKey, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestContainersConcurrentOpIs409(t *testing.T) {
	s, _ := newContainersTestServer(t, "docker-compose.yml")
	h := s.Routes()
	p := createProject(t, h, "Busy")
	s.beginContainerOp(p.ID, "starting")
	rec := doRequest(t, h, "POST", "/v1/projects/"+p.ID+"/containers/stop", testKey, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}
