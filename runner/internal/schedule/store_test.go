package schedule

import (
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"
)

// fakeClock returns t, then advances by one minute per call.
func fakeClock(t time.Time) func() time.Time {
	return func() time.Time {
		cur := t
		t = t.Add(time.Minute)
		return cur
	}
}

func openTest(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.now = fakeClock(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC))
	t.Cleanup(func() { s.Close() })
	return s
}

func newStore(t *testing.T) *Store {
	return openTest(t, filepath.Join(t.TempDir(), "sub", "dir", "schedule.db"))
}

func ptr[T any](v T) *T { return &v }

func userVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCreateGetRoundTrip(t *testing.T) {
	s := newStore(t)
	cases := []Booking{
		{
			Title: "commute weekly until", Date: "2026-10-05",
			Day:    Day{Start: "07:00", End: "22:00", Sleeps: []Gap{{From: "09:00", To: "17:00"}}},
			Repeat: &Repeat{Freq: Weekly, Interval: 1, Weekdays: []int{1, 3, 5}, Until: ptr("2026-12-31")},
			Exceptions: []Exception{
				{Date: "2026-10-12", Override: &Day{Start: "08:00", End: "20:00", Sleeps: []Gap{}}},
				{Date: "2026-10-07", Cancelled: true},
			},
		},
		{
			Title: "daily count", Date: "2026-10-02",
			Day:    Day{Start: "06:00", End: "23:00", Sleeps: []Gap{{From: "01:00", To: "02:00"}, {From: "12:00", To: "13:00"}}},
			Repeat: &Repeat{Freq: Daily, Interval: 2, Count: ptr(5)},
		},
		{
			Title: "one-off empty sleeps", Date: "2026-10-03",
			Day: Day{Start: "10:00", End: "11:00"}, // nil sleeps
		},
	}
	for _, in := range cases {
		in.ID, in.CreatedAt, in.UpdatedAt = "ignored", "ignored", "ignored"
		got, err := s.Create(in)
		if err != nil {
			t.Fatalf("Create %q: %v", in.Title, err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(got.ID) {
			t.Errorf("id %q is not 16 hex chars", got.ID)
		}
		if _, err := time.Parse(time.RFC3339, got.CreatedAt); err != nil || got.CreatedAt != got.UpdatedAt {
			t.Errorf("timestamps %q/%q", got.CreatedAt, got.UpdatedAt)
		}

		want := in
		want.ID, want.CreatedAt, want.UpdatedAt = got.ID, got.CreatedAt, got.UpdatedAt
		if want.Sleeps == nil {
			want.Sleeps = []Gap{}
		}
		if want.Exceptions == nil {
			want.Exceptions = []Exception{}
		}
		// exceptions come back sorted by date
		if len(want.Exceptions) == 2 {
			want.Exceptions = []Exception{want.Exceptions[1], want.Exceptions[0]}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Create returned\n %+v\nwant\n %+v", got, want)
		}
		fetched, err := s.Get(got.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !reflect.DeepEqual(fetched, want) {
			t.Errorf("Get returned\n %+v\nwant\n %+v", fetched, want)
		}
	}
}

func TestListOrdering(t *testing.T) {
	s := newStore(t)
	add := func(title, date, start string) string {
		b, err := s.Create(Booking{Title: title, Date: date, Day: Day{Start: start, End: "23:00"}})
		if err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	add("c", "2026-10-03", "08:00")
	add("b", "2026-10-02", "09:00")
	add("a", "2026-10-02", "07:00")
	add("d", "2026-10-03", "06:00")

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, b := range list {
		titles = append(titles, b.Title)
		if b.Exceptions == nil || b.Sleeps == nil {
			t.Errorf("%s: nil Exceptions or Sleeps", b.Title)
		}
	}
	if want := []string{"a", "b", "d", "c"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("order %v, want %v", titles, want)
	}
}

func TestListEmpty(t *testing.T) {
	s := newStore(t)
	list, err := s.List()
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("List on empty store = %v, %v", list, err)
	}
}

func TestUpdateReplacesExceptionsKeepsCreatedAt(t *testing.T) {
	s := newStore(t)
	b, err := s.Create(Booking{
		Title: "series", Date: "2026-10-05", Day: Day{Start: "07:00", End: "22:00"},
		Repeat:     &Repeat{Freq: Daily, Interval: 1},
		Exceptions: []Exception{{Date: "2026-10-06", Cancelled: true}, {Date: "2026-10-07", Cancelled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	upd := Booking{
		Title: "renamed", Date: "2026-10-06", Day: Day{Start: "08:00", End: "21:00", Sleeps: []Gap{{From: "10:00", To: "11:00"}}},
		Repeat:     nil,
		Exceptions: []Exception{{Date: "2026-10-09", Override: &Day{Start: "09:00", End: "10:00", Sleeps: []Gap{}}}},
		CreatedAt:  "ignored",
	}
	got, err := s.Update(b.ID, upd)
	if err != nil {
		t.Fatal(err)
	}
	if got.CreatedAt != b.CreatedAt {
		t.Errorf("CreatedAt changed: %q -> %q", b.CreatedAt, got.CreatedAt)
	}
	if got.UpdatedAt <= b.UpdatedAt {
		t.Errorf("UpdatedAt not bumped: %q -> %q", b.UpdatedAt, got.UpdatedAt)
	}
	if got.Title != "renamed" || got.Date != "2026-10-06" || got.Repeat != nil || !reflect.DeepEqual(got.Day, upd.Day) {
		t.Errorf("fields not replaced: %+v", got)
	}
	if !reflect.DeepEqual(got.Exceptions, upd.Exceptions) {
		t.Errorf("exceptions %+v, want %+v", got.Exceptions, upd.Exceptions)
	}
	fetched, _ := s.Get(b.ID)
	if !reflect.DeepEqual(fetched, got) {
		t.Errorf("Get after Update differs:\n %+v\n %+v", fetched, got)
	}
}

func TestDeleteCascadesExceptions(t *testing.T) {
	s := newStore(t)
	b, err := s.Create(Booking{
		Title: "x", Date: "2026-10-05", Day: Day{Start: "07:00", End: "22:00"},
		Repeat:     &Repeat{Freq: Daily, Interval: 1},
		Exceptions: []Exception{{Date: "2026-10-06", Cancelled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM exceptions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d exceptions left after delete", n)
	}
}

func TestSetExceptionInsertThenOverwrite(t *testing.T) {
	s := newStore(t)
	b, err := s.Create(Booking{
		Title: "x", Date: "2026-10-05", Day: Day{Start: "07:00", End: "22:00"},
		Repeat: &Repeat{Freq: Daily, Interval: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetException(b.ID, Exception{Date: "2026-10-08", Cancelled: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []Exception{{Date: "2026-10-08", Cancelled: true}}; !reflect.DeepEqual(got.Exceptions, want) {
		t.Errorf("after insert %+v", got.Exceptions)
	}
	if got.UpdatedAt <= b.UpdatedAt || got.CreatedAt != b.CreatedAt {
		t.Errorf("timestamps: created %q->%q updated %q->%q", b.CreatedAt, got.CreatedAt, b.UpdatedAt, got.UpdatedAt)
	}
	prev := got.UpdatedAt

	ov := &Day{Start: "09:00", End: "18:00", Sleeps: []Gap{{From: "12:00", To: "13:00"}}}
	got, err = s.SetException(b.ID, Exception{Date: "2026-10-08", Override: ov})
	if err != nil {
		t.Fatal(err)
	}
	if want := []Exception{{Date: "2026-10-08", Override: ov}}; !reflect.DeepEqual(got.Exceptions, want) {
		t.Errorf("after overwrite %+v", got.Exceptions)
	}
	if got.UpdatedAt <= prev {
		t.Errorf("UpdatedAt not bumped on overwrite")
	}
}

func TestNotFound(t *testing.T) {
	s := newStore(t)
	if _, err := s.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get: %v", err)
	}
	if _, err := s.Update("nope", Booking{Title: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update: %v", err)
	}
	if err := s.Delete("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete: %v", err)
	}
	if _, err := s.SetException("nope", Exception{Date: "2026-10-01", Cancelled: true}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetException: %v", err)
	}
}

func TestReopenKeepsDataAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, s); v != 1 {
		t.Fatalf("user_version = %d, want 1", v)
	}
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v", mode, err)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d, %v", fk, err)
	}
	b, err := s.Create(Booking{
		Title: "keep", Date: "2026-10-05", Day: Day{Start: "07:00", End: "22:00"},
		Exceptions: []Exception{{Date: "2026-10-05", Cancelled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := openTest(t, path) // a re-run of migration v1 would fail on CREATE TABLE
	if v := userVersion(t, s2); v != 1 {
		t.Errorf("user_version after reopen = %d, want 1", v)
	}
	got, err := s2.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, b) {
		t.Errorf("after reopen\n %+v\nwant\n %+v", got, b)
	}
}
