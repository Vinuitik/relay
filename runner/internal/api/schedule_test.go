package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"relay/runner/internal/schedule"
)

// newScheduleServer: a test server with a schedule service on a temp store, plan dir and a
// fixed clock (2026-10-09 08:00 Europe/London).
func newScheduleServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s := newTestServer(t)
	dir := t.TempDir()
	st, err := schedule.Open(filepath.Join(dir, "relay.db"))
	if err != nil {
		t.Fatalf("schedule.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	svc := schedule.NewService(st)
	svc.Now = func() time.Time { return time.Date(2026, 10, 9, 8, 0, 0, 0, loc) }
	svc.PlanPath = filepath.Join(dir, "schedule-plan")
	svc.StatusPath = filepath.Join(dir, "schedule-applied")
	svc.Zone = "Europe/London"
	s.Schedule = svc
	return s, s.Routes()
}

func bookingBody(date, start, end string, sleeps ...map[string]string) map[string]any {
	if sleeps == nil {
		sleeps = []map[string]string{}
	}
	return map[string]any{"title": "Commute", "date": date, "start": start, "end": end, "sleeps": sleeps, "repeat": nil}
}

func createBooking(t *testing.T, h http.Handler, body map[string]any) schedule.Booking {
	t.Helper()
	rec := doRequest(t, h, "POST", "/v1/schedule/bookings", testKey, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}
	var b schedule.Booking
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSchedule_CreateStatuses(t *testing.T) {
	_, h := newScheduleServer(t)
	b := createBooking(t, h, bookingBody("2026-10-10", "07:00", "23:00", map[string]string{"from": "12:00", "to": "13:00"}))
	if b.ID == "" || len(b.Sleeps) != 1 || b.Exceptions == nil {
		t.Fatalf("booking = %+v", b)
	}

	rec := doRequest(t, h, "POST", "/v1/schedule/bookings", testKey,
		bookingBody("2026-10-11", "07:00", "23:00", map[string]string{"from": "12:00", "to": "12:05"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad sleep status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, "POST", "/v1/schedule/bookings", testKey, bookingBody("2026-10-10", "22:00", "23:30"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("overlap status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}

	req := doRequestRaw(t, h, "POST", "/v1/schedule/bookings", "{not json")
	if req.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON status = %d, want 400", req.Code)
	}
}

func doRequestRaw(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Relay-Key", testKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSchedule_GetShape(t *testing.T) {
	s, h := newScheduleServer(t)
	createBooking(t, h, bookingBody("2026-10-10", "07:00", "23:00", map[string]string{"from": "12:00", "to": "13:00"}))
	rec := doRequest(t, h, "GET", "/v1/schedule", testKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"timezone", "bookings", "plan", "planWrittenAt", "appliedAt", "applied"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("missing %q in %s", k, rec.Body.String())
		}
	}
	var sch schedule.Schedule
	_ = json.Unmarshal(rec.Body.Bytes(), &sch)
	if sch.Timezone != "Europe/London" || len(sch.Bookings) != 1 || len(sch.Plan) != 3 || sch.PlanWrittenAt == nil || sch.Applied {
		t.Fatalf("schedule = %s", rec.Body.String())
	}
	if _, err := os.Stat(s.Schedule.PlanPath); err != nil {
		t.Fatalf("plan file not written: %v", err)
	}
}

func TestSchedule_Occurrences(t *testing.T) {
	_, h := newScheduleServer(t)
	body := bookingBody("2026-10-10", "07:00", "23:00")
	body["repeat"] = map[string]any{"freq": "daily", "interval": 1}
	createBooking(t, h, body)

	rec := doRequest(t, h, "GET", "/v1/schedule/occurrences?from=2026-10-10&to=2026-10-12", testKey, nil)
	var occs []schedule.Occurrence
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &occs) != nil || len(occs) != 3 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{
		"from=2026-10-01&to=2026-12-31", // span > 62
		"from=2026-10-10",               // missing to
		"from=2026-10-12&to=2026-10-10", // to before from
		"from=10/10/2026&to=2026-10-12", // bad date
	} {
		rec := doRequest(t, h, "GET", "/v1/schedule/occurrences?"+q, testKey, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}

func TestSchedule_UpdateSeries(t *testing.T) {
	_, h := newScheduleServer(t)
	b := createBooking(t, h, bookingBody("2026-10-10", "07:00", "23:00"))
	rec := doRequest(t, h, "PUT", "/v1/schedule/bookings/"+b.ID, testKey, bookingBody("2026-10-10", "08:00", "22:00"))
	var got schedule.Booking
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Start != "08:00" || got.ID != b.ID {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, h, "PUT", "/v1/schedule/bookings/nope", testKey, bookingBody("2026-10-10", "08:00", "22:00"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id status = %d, want 404", rec.Code)
	}
	rec = doRequest(t, h, "DELETE", "/v1/schedule/bookings/"+b.ID, testKey, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", rec.Code)
	}
	rec = doRequest(t, h, "DELETE", "/v1/schedule/bookings/"+b.ID, testKey, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", rec.Code)
	}
}

func TestSchedule_Occurrence(t *testing.T) {
	_, h := newScheduleServer(t)
	body := bookingBody("2026-10-10", "07:00", "23:00")
	body["repeat"] = map[string]any{"freq": "weekly", "interval": 1}
	series := createBooking(t, h, body)
	day := map[string]any{"start": "09:00", "end": "21:00", "sleeps": []any{}}

	rec := doRequest(t, h, "PUT", "/v1/schedule/bookings/"+series.ID+"/occurrences/2026-10-11", testKey, day)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-occurrence PUT status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, h, "PUT", "/v1/schedule/bookings/"+series.ID+"/occurrences/2026-10-17", testKey, day)
	if rec.Code != http.StatusOK {
		t.Fatalf("occurrence PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, h, "DELETE", "/v1/schedule/bookings/"+series.ID+"/occurrences/2026-10-24", testKey, nil)
	var got schedule.Booking
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got.Exceptions) != 2 {
		t.Fatalf("series cancel status = %d, body=%s", rec.Code, rec.Body.String())
	}

	one := createBooking(t, h, bookingBody("2026-10-12", "07:00", "23:00"))
	rec = doRequest(t, h, "DELETE", "/v1/schedule/bookings/"+one.ID+"/occurrences/2026-10-12", testKey, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("one-off cancel status = %d, want 204, body=%s", rec.Code, rec.Body.String())
	}
}

func TestSchedule_NotConfiguredIs503(t *testing.T) {
	s := newTestServer(t)
	rec := doRequest(t, s.Routes(), "GET", "/v1/schedule", testKey, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestSchedule_RequiresAuth(t *testing.T) {
	_, h := newScheduleServer(t)
	rec := doRequest(t, h, "GET", "/v1/schedule", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestSchedule_EmptyListsAreArrays(t *testing.T) {
	_, h := newScheduleServer(t)
	// Empty store: bookings, plan and an occurrence window all serialise as [].
	rec := doRequest(t, h, "GET", "/v1/schedule", testKey, nil)
	for _, k := range []string{`"bookings":[]`, `"plan":[]`} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Fatalf("GET /v1/schedule missing %s: %s", k, rec.Body.String())
		}
	}
	rec = doRequest(t, h, "GET", "/v1/schedule/occurrences?from=2026-10-10&to=2026-10-12", testKey, nil)
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("empty occurrences = %s, want []", got)
	}

	body := bookingBody("2026-10-10", "07:00", "23:00")
	delete(body, "sleeps") // omitted in the request → still [] in responses
	body["repeat"] = map[string]any{"freq": "daily", "interval": 1}
	rec = doRequest(t, h, "POST", "/v1/schedule/bookings", testKey, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var b schedule.Booking
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	for _, k := range []string{`"sleeps":[]`, `"exceptions":[]`} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Fatalf("created booking missing %s: %s", k, rec.Body.String())
		}
	}
	rec = doRequest(t, h, "PUT", "/v1/schedule/bookings/"+b.ID+"/occurrences/2026-10-11", testKey,
		map[string]any{"start": "08:00", "end": "22:00"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"override":{"start":"08:00","end":"22:00","sleeps":[]}`) {
		t.Fatalf("override status = %d, body=%s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/v1/schedule", "/v1/schedule/occurrences?from=2026-10-10&to=2026-10-12"} {
		rec = doRequest(t, h, "GET", path, testKey, nil)
		if strings.Contains(rec.Body.String(), `"sleeps":null`) || strings.Contains(rec.Body.String(), `"exceptions":null`) {
			t.Fatalf("%s has a null list: %s", path, rec.Body.String())
		}
	}
}
