package api

import (
	"net/http"
	"testing"

	"relay/runner/internal/usage"
)

func TestUsageWithoutRecorderIs503(t *testing.T) {
	s := newTestServer(t)
	if rec := doRequest(t, s.Routes(), "GET", "/v1/usage", testKey, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestUsageReportsRequestedLimits(t *testing.T) {
	s := newTestServer(t)
	r, err := usage.NewRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.Usage = r

	rec := doRequest(t, s.Routes(), "GET", "/v1/usage?days=7&limits=10,20", testKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var rep usage.Report
	decodeBody(t, rec, &rep)
	if len(rep.Limits) != 2 || rep.Limits[0].IdleMinutes != 10 || rep.Limits[1].IdleMinutes != 20 {
		t.Fatalf("limits = %+v", rep.Limits)
	}

	for _, q := range []string{"days=0", "days=91", "limits=5,x", "limits=0"} {
		if rec := doRequest(t, s.Routes(), "GET", "/v1/usage?"+q, testKey, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}
