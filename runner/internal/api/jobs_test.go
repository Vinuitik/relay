package api

import (
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"relay/runner/internal/auth"
	"relay/runner/internal/jobs"
)

func TestJobsDisabled(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), http.MethodGet, "/v1/jobs/x", testKey, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestJobsValidationAndLifecycle(t *testing.T) {
	s := newTestServer(t)
	p, err := s.Projects.Create("Jobs Project")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The test binary with no tests to run stands in for claude: it exits 0
	// without a stream-json result, so the job ends up failed.
	s.Jobs = jobs.New(s.Projects.Dir, func() (string, error) { return os.Args[0], nil })
	s.Jobs.ExtraArgs = []string{"-test.run=^$"}
	h := s.Routes()

	for _, c := range []struct {
		body any
		want int
	}{
		{map[string]any{"kind": "map", "prompt": "x"}, http.StatusBadRequest},
		{map[string]any{"kind": "map", "projectId": "nope", "prompt": "x"}, http.StatusNotFound},
		{map[string]any{"kind": "bash", "prompt": "x"}, http.StatusBadRequest},
		{map[string]any{"kind": "kpi"}, http.StatusBadRequest},
	} {
		if rec := doRequest(t, h, http.MethodPost, "/v1/jobs", testKey, c.body); rec.Code != c.want {
			t.Errorf("POST %v = %d, want %d (%s)", c.body, rec.Code, c.want, rec.Body.String())
		}
	}
	if rec := doRequest(t, h, http.MethodGet, "/v1/jobs/job_missing", testKey, nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET missing = %d", rec.Code)
	}
	if rec := doRequest(t, h, http.MethodDelete, "/v1/jobs/job_missing", testKey, nil); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE missing = %d", rec.Code)
	}

	rec := doRequest(t, h, http.MethodPost, "/v1/jobs", testKey, map[string]any{"kind": "map", "projectId": p.ID, "prompt": "x"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
	}
	var created struct{ JobID string }
	decodeBody(t, rec, &created)
	deadline := time.Now().Add(10 * time.Second)
	var j jobs.Job
	for {
		rec = doRequest(t, h, http.MethodGet, "/v1/jobs/"+created.JobID+"?after=0", testKey, nil)
		decodeBody(t, rec, &j)
		if j.State == jobs.StateFailed || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if j.State != jobs.StateFailed || j.JobID != created.JobID || j.Kind != "map" {
		t.Fatalf("job = %+v", j)
	}
	if rec := doRequest(t, h, http.MethodGet, "/v1/jobs/"+created.JobID+"?after=-1", testKey, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("after=-1 = %d", rec.Code)
	}
}

func TestJobsNoCLI(t *testing.T) {
	s := newTestServer(t)
	s.Jobs = jobs.New(s.Projects.Dir, func() (string, error) { return "", errors.Join(auth.ErrCLINotFound) })
	rec := doRequest(t, s.Routes(), http.MethodPost, "/v1/jobs", testKey, map[string]any{"kind": "kpi", "prompt": "x"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
