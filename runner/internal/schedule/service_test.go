package schedule

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

// newTestService: fixed clock 2026-10-09 08:00 Europe/London, plan dir in a temp dir.
func newTestService(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "relay.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 8, 0, 0, 0, loc)
	svc := NewService(st)
	svc.Now = func() time.Time { return now }
	planDir := filepath.Join(dir, "lib")
	if err := os.Mkdir(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	svc.PlanPath = filepath.Join(planDir, "schedule-plan")
	svc.StatusPath = filepath.Join(planDir, "schedule-applied")
	svc.Zone = "Europe/London"
	return svc, &now
}

func oneOff(date, start, end string, sleeps ...Gap) BookingInput {
	return BookingInput{Title: "t", Date: date, Day: Day{Start: start, End: end, Sleeps: sleeps}}
}

func readPlan(t *testing.T, svc *Service) string {
	t.Helper()
	b, err := os.ReadFile(svc.PlanPath)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	return string(b)
}

func TestService_CreateWritesPlan(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Create(oneOff("2026-10-10", "07:00", "23:00", Gap{"12:00", "13:00"})); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := eventLines(readPlan(t, svc))
	want := "warn 2026-10-10 11:55\nsleep 2026-10-10 12:00\nwake 2026-10-10 13:00\n"
	if got != want {
		t.Fatalf("plan events:\n%s\nwant:\n%s", got, want)
	}
	if !strings.HasPrefix(readPlan(t, svc), "#") {
		t.Fatalf("plan should start with a comment line")
	}
	sch, err := svc.Schedule()
	if err != nil {
		t.Fatal(err)
	}
	if sch.PlanWrittenAt == nil || len(sch.Plan) != 3 || len(sch.Bookings) != 1 || sch.Timezone != "Europe/London" {
		t.Fatalf("schedule = %+v", sch)
	}
}

func TestService_IdenticalRewriteSkipped(t *testing.T) {
	svc, now := newTestService(t)
	if _, err := svc.Create(oneOff("2026-10-10", "07:00", "23:00", Gap{"12:00", "13:00"})); err != nil {
		t.Fatal(err)
	}
	before := readPlan(t, svc)
	*now = now.Add(time.Minute) // comment line would differ
	// Far beyond the horizon: bookings change, events don't.
	if _, err := svc.Create(oneOff("2027-03-01", "07:00", "23:00", Gap{"12:00", "13:00"})); err != nil {
		t.Fatal(err)
	}
	if after := readPlan(t, svc); after != before {
		t.Fatalf("plan rewritten although events are identical:\n%s\nvs\n%s", before, after)
	}
}

func TestService_PlanDirMissing(t *testing.T) {
	svc, _ := newTestService(t)
	svc.PlanPath = filepath.Join(t.TempDir(), "nope", "schedule-plan")
	svc.StatusPath = filepath.Join(filepath.Dir(svc.PlanPath), "schedule-applied")
	if _, err := svc.Create(oneOff("2026-10-10", "07:00", "23:00", Gap{"12:00", "13:00"})); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(svc.PlanPath); !os.IsNotExist(err) {
		t.Fatalf("plan file should not exist, stat err = %v", err)
	}
	sch, err := svc.Schedule()
	if err != nil {
		t.Fatal(err)
	}
	if sch.PlanWrittenAt != nil || sch.Applied || sch.AppliedAt != nil {
		t.Fatalf("schedule = %+v", sch)
	}
}

func TestService_AppliedFromStatusFile(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Create(oneOff("2026-10-10", "07:00", "23:00", Gap{"12:00", "13:00"})); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(readPlan(t, svc)))
	write := func(hash string) {
		if err := os.WriteFile(svc.StatusPath, []byte(hash+"\n2026-10-09T08:00:05+01:00\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(hex.EncodeToString(sum[:]))
	sch, err := svc.Schedule()
	if err != nil {
		t.Fatal(err)
	}
	if !sch.Applied || sch.AppliedAt == nil || *sch.AppliedAt != "2026-10-09T08:00:05+01:00" {
		t.Fatalf("want applied, got %+v", sch)
	}
	write(strings.Repeat("0", 64))
	if sch, _ = svc.Schedule(); sch.Applied {
		t.Fatalf("want not applied for a stale hash")
	}
}

func TestService_SeriesEditDropsStaleExceptions(t *testing.T) {
	svc, _ := newTestService(t)
	in := oneOff("2026-10-10", "07:00", "23:00", Gap{"12:00", "13:00"}) // Saturday
	in.Repeat = &Repeat{Freq: Daily, Interval: 1}
	b, err := svc.Create(in)
	if err != nil {
		t.Fatal(err)
	}
	override := Day{Start: "08:00", End: "22:00", Sleeps: []Gap{}}
	for _, d := range []string{"2026-10-11", "2026-10-17"} { // Sunday, next Saturday
		if _, err := svc.UpdateOccurrence(b.ID, d, override); err != nil {
			t.Fatalf("UpdateOccurrence %s: %v", d, err)
		}
	}
	in.Repeat = &Repeat{Freq: Weekly, Interval: 1}
	b, err = svc.UpdateSeries(b.ID, in)
	if err != nil {
		t.Fatalf("UpdateSeries: %v", err)
	}
	if len(b.Exceptions) != 1 || b.Exceptions[0].Date != "2026-10-17" {
		t.Fatalf("exceptions = %+v, want only 2026-10-17", b.Exceptions)
	}
}

func TestService_CancelOneOffDeletes(t *testing.T) {
	svc, _ := newTestService(t)
	b, err := svc.Create(oneOff("2026-10-10", "07:00", "23:00"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CancelOccurrence(b.ID, "2026-10-11"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-occurrence: err = %v, want ErrNotFound", err)
	}
	_, deleted, err := svc.CancelOccurrence(b.ID, "2026-10-10")
	if err != nil || !deleted {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
	if _, err := svc.store.Get(b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("booking still exists: %v", err)
	}
}

func TestService_ErrorKinds(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Create(oneOff("2026-10-10", "07:00", "12:00")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(oneOff("2026-10-10", "11:00", "15:00")); !errors.Is(err, ErrOverlap) {
		t.Fatalf("overlap: err = %v", err)
	}
	if _, err := svc.Create(oneOff("2026-10-10", "12:00", "15:00")); err != nil {
		t.Fatalf("touching ends should be fine: %v", err)
	}
	if _, err := svc.Create(oneOff("2026-10-11", "07:00", "12:00", Gap{"08:00", "08:05"})); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid: err = %v", err)
	}
	if _, err := svc.UpdateSeries("missing", oneOff("2026-10-11", "07:00", "12:00")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not found: err = %v", err)
	}
}
