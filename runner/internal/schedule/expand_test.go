package schedule

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func strp(s string) *string { return &s }
func intp(n int) *int       { return &n }

func day(start, end string, sleeps ...Gap) Day {
	return Day{Start: start, End: end, Sleeps: sleeps}
}

func booking(id, date string, rep *Repeat, ex ...Exception) Booking {
	return Booking{
		ID: id, Title: "t-" + id, Date: date,
		Day:    day("08:00", "18:00", Gap{"12:00", "13:00"}),
		Repeat: rep, Exceptions: ex,
	}
}

func dates(occs []Occurrence) []string {
	out := []string{}
	for _, o := range occs {
		out = append(out, o.Date)
	}
	return out
}

func TestExpandDates(t *testing.T) {
	cases := []struct {
		name     string
		b        Booking
		from, to string
		want     []string
	}{
		{"weekly tue/thu/fri, first never before date",
			booking("a", "2026-10-07", &Repeat{Freq: Weekly, Interval: 1, Weekdays: []int{5, 2, 4}}),
			"2026-10-05", "2026-10-18",
			[]string{"2026-10-08", "2026-10-09", "2026-10-13", "2026-10-15", "2026-10-16"}},
		{"weekly interval 2 on monday",
			booking("a", "2026-10-07", &Repeat{Freq: Weekly, Interval: 2, Weekdays: []int{1}}),
			"2026-10-01", "2026-11-08",
			[]string{"2026-10-19", "2026-11-02"}},
		{"weekly interval 2 default weekday",
			booking("a", "2026-10-06", &Repeat{Freq: Weekly, Interval: 2}),
			"2026-10-01", "2026-11-08",
			[]string{"2026-10-06", "2026-10-20", "2026-11-03"}},
		{"daily until",
			booking("a", "2026-10-10", &Repeat{Freq: Daily, Interval: 1, Until: strp("2026-10-13")}),
			"2026-10-01", "2026-10-31",
			[]string{"2026-10-10", "2026-10-11", "2026-10-12", "2026-10-13"}},
		{"daily count 3 interval 2, cancelled one still counted",
			booking("a", "2026-10-10", &Repeat{Freq: Daily, Interval: 2, Count: intp(3)},
				Exception{Date: "2026-10-12", Cancelled: true}),
			"2026-10-01", "2026-10-31",
			[]string{"2026-10-10", "2026-10-14"}},
		{"monthly on the 31st",
			booking("a", "2026-01-31", &Repeat{Freq: Monthly, Interval: 1}),
			"2026-01-01", "2026-05-31",
			[]string{"2026-01-31", "2026-02-28", "2026-03-31", "2026-04-30", "2026-05-31"}},
		{"yearly feb 29",
			booking("a", "2024-02-29", &Repeat{Freq: Yearly, Interval: 1}),
			"2027-01-01", "2028-12-31",
			[]string{"2027-02-28", "2028-02-29"}},
		{"one-off inside window",
			booking("a", "2026-10-10", nil),
			"2026-10-10", "2026-10-10",
			[]string{"2026-10-10"}},
		{"one-off outside window",
			booking("a", "2026-10-10", nil),
			"2026-10-11", "2026-10-31",
			[]string{}},
		{"cancel one of an endless series",
			booking("a", "2026-10-10", &Repeat{Freq: Daily, Interval: 1},
				Exception{Date: "2026-10-11", Cancelled: true}),
			"2026-10-10", "2026-10-12",
			[]string{"2026-10-10", "2026-10-12"}},
		{"exception on a non-occurrence date is ignored",
			booking("a", "2026-10-06", &Repeat{Freq: Weekly, Interval: 1},
				Exception{Date: "2026-10-07", Cancelled: true}),
			"2026-10-01", "2026-10-14",
			[]string{"2026-10-06", "2026-10-13"}},
		{"far future weekly",
			booking("a", "2026-10-05", &Repeat{Freq: Weekly, Interval: 1, Weekdays: []int{1}}),
			"2030-01-07", "2030-01-20",
			[]string{"2030-01-07", "2030-01-14"}},
		{"far future weekly interval 3",
			booking("a", "2026-10-05", &Repeat{Freq: Weekly, Interval: 3, Weekdays: []int{1}}),
			"2030-01-01", "2030-01-31",
			// 2026-10-05 + 168 weeks = 2029-12-24, +3w = 2030-01-14, +3w = 2030-02-04
			[]string{"2030-01-14"}},
		{"far future daily interval 3",
			booking("a", "2026-10-05", &Repeat{Freq: Daily, Interval: 3}),
			"2030-01-01", "2030-01-07",
			// 2026-10-05 + 1182 days (394*3) = 2029-12-30
			[]string{"2030-01-02", "2030-01-05"}},
		{"far future monthly 31st",
			booking("a", "2026-01-31", &Repeat{Freq: Monthly, Interval: 1}),
			"2030-02-01", "2030-03-31",
			[]string{"2030-02-28", "2030-03-31"}},
		{"far future count exhausted",
			booking("a", "2026-10-05", &Repeat{Freq: Daily, Interval: 1, Count: intp(5)}),
			"2030-01-01", "2030-12-31",
			[]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Expand([]Booking{c.b}, c.from, c.to)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(dates(got), c.want) {
				t.Fatalf("got %v, want %v", dates(got), c.want)
			}
			for _, o := range got {
				if o.Recurring != (c.b.Repeat != nil) || o.BookingID != "a" || o.Title != "t-a" {
					t.Fatalf("bad occurrence %+v", o)
				}
			}
		})
	}
}

func TestExpandOverride(t *testing.T) {
	ov := day("06:00", "20:00", Gap{"07:00", "09:00"})
	b := booking("a", "2026-10-10", &Repeat{Freq: Daily, Interval: 1, Count: intp(3)},
		Exception{Date: "2026-10-11", Override: &ov})
	got, err := Expand([]Booking{b}, "2026-10-01", "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %v", dates(got))
	}
	if got[0].Edited || got[2].Edited || got[0].Start != "08:00" {
		t.Fatalf("unedited days changed: %+v", got)
	}
	if !got[1].Edited || !reflect.DeepEqual(got[1].Day, ov) {
		t.Fatalf("override not applied: %+v", got[1])
	}
}

func TestExpandSortsAcrossBookings(t *testing.T) {
	early := booking("early", "2026-10-10", nil)
	early.Day = day("06:00", "07:00")
	late := booking("late", "2026-10-09", &Repeat{Freq: Daily, Interval: 1})
	got, err := Expand([]Booking{late, early}, "2026-10-09", "2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, o := range got {
		ids = append(ids, o.Date+"/"+o.BookingID)
	}
	want := []string{"2026-10-09/late", "2026-10-10/early", "2026-10-10/late"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
}

func TestExpandFarFutureIsFast(t *testing.T) {
	bs := []Booking{
		booking("a", "2026-10-05", &Repeat{Freq: Daily, Interval: 1}),
		booking("b", "2026-10-05", &Repeat{Freq: Weekly, Interval: 1, Weekdays: []int{1, 3}}),
	}
	start := time.Now()
	for i := 0; i < 100; i++ {
		got, err := Expand(bs, "2030-06-01", "2030-06-07")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 9 {
			t.Fatalf("got %d occurrences", len(got))
		}
	}
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Fatalf("100 far-future expansions took %v", el)
	}
}

func TestIsOccurrence(t *testing.T) {
	b := booking("a", "2026-10-07", &Repeat{Freq: Weekly, Interval: 2, Weekdays: []int{3, 5}},
		Exception{Date: "2026-10-09", Cancelled: true})
	cases := map[string]bool{
		"2026-10-07": true, "2026-10-09": true, // cancelled still an original occurrence
		"2026-10-14": false, "2026-10-21": true, "2026-10-06": false, "2030-01-02": false,
		"2026-10-05": false, "bad": false,
	}
	for d, want := range cases {
		if got := IsOccurrence(b, d); got != want {
			t.Errorf("%s: got %v, want %v", d, got, want)
		}
	}
	if !IsOccurrence(booking("x", "2026-10-10", nil), "2026-10-10") {
		t.Error("one-off date")
	}
}

func TestValidate(t *testing.T) {
	ok := booking("a", "2026-10-10", &Repeat{Freq: Weekly, Interval: 1, Weekdays: []int{1, 3}})
	if err := Validate(ok); err != nil {
		t.Fatalf("valid booking rejected: %v", err)
	}
	if err := Validate(booking("a", "2026-10-10", nil)); err != nil {
		t.Fatalf("valid one-off rejected: %v", err)
	}
	bad := day("08:00", "07:00")
	cases := []struct {
		name string
		mod  func(b *Booking)
		want string
	}{
		{"bad date", func(b *Booking) { b.Date = "2026-02-30" }, "date"},
		{"bad clock hour", func(b *Booking) { b.Start = "24:00" }, "start"},
		{"bad clock format", func(b *Booking) { b.End = "9:00" }, "end"},
		{"start after end", func(b *Booking) { b.Start, b.End = "18:00", "08:00" }, "end"},
		{"start equals end", func(b *Booking) { b.End = "08:00" }, "end"},
		{"sleep from >= to", func(b *Booking) { b.Sleeps = []Gap{{"13:00", "12:00"}} }, "sleeps[0]"},
		{"sleep before block", func(b *Booking) { b.Sleeps = []Gap{{"07:00", "09:00"}} }, "sleeps[0]: starts before"},
		{"sleep after block", func(b *Booking) {
			b.Sleeps = []Gap{{"09:00", "10:00"}, {"17:00", "19:00"}}
		}, "sleeps[1]: ends after the awake block"},
		{"sleep too short", func(b *Booking) { b.Sleeps = []Gap{{"12:00", "12:09"}} }, "sleeps[0]: shorter"},
		{"sleeps overlap", func(b *Booking) {
			b.Sleeps = []Gap{{"09:00", "10:00"}, {"09:30", "11:00"}}
		}, "sleeps[1]"},
		{"sleeps unsorted", func(b *Booking) {
			b.Sleeps = []Gap{{"12:00", "13:00"}, {"09:00", "10:00"}}
		}, "sleeps[1]"},
		{"bad sleep clock", func(b *Booking) { b.Sleeps = []Gap{{"12:60", "13:00"}} }, "sleeps[0].from"},
		{"unknown freq", func(b *Booking) { b.Repeat.Freq = "hourly" }, "repeat.freq"},
		{"interval 0", func(b *Booking) { b.Repeat.Interval = 0 }, "repeat.interval"},
		{"weekdays on daily", func(b *Booking) { b.Repeat.Freq = Daily }, "repeat.weekdays"},
		{"weekday 8", func(b *Booking) { b.Repeat.Weekdays = []int{1, 8} }, "repeat.weekdays[1]"},
		{"weekday 0", func(b *Booking) { b.Repeat.Weekdays = []int{0} }, "repeat.weekdays[0]"},
		{"duplicate weekday", func(b *Booking) { b.Repeat.Weekdays = []int{2, 2} }, "repeat.weekdays[1]"},
		{"until before date", func(b *Booking) { b.Repeat.Until = strp("2026-10-09") }, "repeat.until"},
		{"bad until", func(b *Booking) { b.Repeat.Until = strp("tomorrow") }, "repeat.until"},
		{"count 0", func(b *Booking) { b.Repeat.Count = intp(0) }, "repeat.count"},
		{"until and count", func(b *Booking) {
			b.Repeat.Until, b.Repeat.Count = strp("2026-12-01"), intp(3)
		}, "mutually exclusive"},
		{"bad exception date", func(b *Booking) {
			b.Exceptions = []Exception{{Date: "2026/10/12", Cancelled: true}}
		}, "exceptions[0].date"},
		{"bad override", func(b *Booking) {
			b.Exceptions = []Exception{{Date: "2026-10-12"}, {Date: "2026-10-14", Override: &bad}}
		}, "exceptions[1].override.end"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := booking("a", "2026-10-10", &Repeat{Freq: Weekly, Interval: 1, Weekdays: []int{1, 3}})
			c.mod(&b)
			err := Validate(b)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

func TestOverlaps(t *testing.T) {
	// 2026-10-10 is a Saturday.
	a := booking("a", "2026-10-10", nil)
	a.Title = "Commute"
	a.Day = day("09:00", "12:00")
	touching := booking("b", "2026-10-03", &Repeat{Freq: Weekly, Interval: 1})
	touching.Day = day("12:00", "15:00")
	clash := booking("c", "2026-10-01", &Repeat{Freq: Daily, Interval: 1})
	clash.Title = "Lunch"
	clash.Day = day("11:00", "13:00")
	sameID := clash
	sameID.ID = "a"
	beyond := booking("d", "2027-10-12", nil) // outside a's 366-day window, a has no later days anyway

	if err := Overlaps(a, []Booking{touching, sameID, beyond}, "2026-10-01"); err != nil {
		t.Fatalf("unexpected overlap: %v", err)
	}
	err := Overlaps(a, []Booking{touching, clash}, "2026-10-01")
	if err == nil {
		t.Fatal("overlap not detected")
	}
	for _, s := range []string{"Lunch", "c", "2026-10-10"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("error %q missing %q", err, s)
		}
	}
	// A series clashing only on a day after the window start: weekly Saturday vs a one-off.
	series := touching
	series.Day = day("10:00", "16:00")
	late := booking("late", "2026-11-14", nil)
	late.Day = day("15:00", "17:00")
	if err := Overlaps(series, []Booking{late}, "2026-10-01"); err == nil || !strings.Contains(err.Error(), "2026-11-14") {
		t.Fatalf("got %v", err)
	}
	// Checking from after the clash date finds nothing.
	if err := Overlaps(series, []Booking{late}, "2026-11-15"); err != nil {
		t.Fatalf("clash before from reported: %v", err)
	}
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no tzdata for %s: %v", name, err)
	}
	return loc
}

func lines(evs []Event) []string {
	out := []string{}
	for _, e := range evs {
		out = append(out, string(e.Kind)+" "+e.Date+" "+e.At)
	}
	return out
}

func occ(date string, d Day) Occurrence {
	return Occurrence{BookingID: "a", Date: date, Day: d}
}

func TestPlanEventsDSTLondon(t *testing.T) {
	loc := mustLoc(t, "Europe/London")
	b := booking("a", "2026-10-24", &Repeat{Freq: Daily, Interval: 1})
	b.Day = day("08:00", "18:00", Gap{"09:00", "10:00"})
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, loc) // BST; clocks go back 2026-10-25 02:00
	occs, err := Expand([]Booking{b}, "2026-10-23", "2026-11-08")
	if err != nil {
		t.Fatal(err)
	}
	evs := PlanEvents(occs, now)
	// Horizon = now + 336h = 2026-11-06 23:00 GMT, so 14 days (10-24 .. 11-06) x 3 events.
	if len(evs) != 42 {
		t.Fatalf("got %d events: %v", len(evs), lines(evs))
	}
	got := lines(evs)[:9]
	want := []string{
		"warn 2026-10-24 08:55", "sleep 2026-10-24 09:00", "wake 2026-10-24 10:00",
		"warn 2026-10-25 08:55", "sleep 2026-10-25 09:00", "wake 2026-10-25 10:00",
		"warn 2026-10-26 08:55", "sleep 2026-10-26 09:00", "wake 2026-10-26 10:00",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if last := evs[41]; last.Date != "2026-11-06" || last.At != "10:00" {
		t.Fatalf("last event %+v", last)
	}
}

func TestPlanEvents(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		name string
		occs []Occurrence
		now  time.Time
		want []string
	}{
		{"past warn dropped, sleep kept",
			[]Occurrence{occ("2026-10-09", day("08:00", "18:00", Gap{"09:00", "10:00"}))},
			time.Date(2026, 10, 9, 8, 57, 0, 0, utc),
			[]string{"sleep 2026-10-09 09:00", "wake 2026-10-09 10:00"}},
		{"event exactly at now dropped",
			[]Occurrence{occ("2026-10-09", day("08:00", "18:00", Gap{"09:00", "10:00"}))},
			time.Date(2026, 10, 9, 9, 0, 0, 0, utc),
			[]string{"wake 2026-10-09 10:00"}},
		{"warn crosses midnight",
			[]Occurrence{occ("2026-10-10", day("00:00", "06:00", Gap{"00:03", "02:00"}))},
			time.Date(2026, 10, 9, 12, 0, 0, 0, utc),
			[]string{"warn 2026-10-09 23:58", "sleep 2026-10-10 00:03", "wake 2026-10-10 02:00"}},
		{"horizon cut-off",
			[]Occurrence{
				occ("2026-10-22", day("08:00", "18:00", Gap{"09:00", "10:00"})),
				occ("2026-10-23", day("08:00", "18:00", Gap{"09:00", "10:00"})),
				occ("2026-10-24", day("08:00", "18:00", Gap{"09:00", "10:00"})),
			},
			time.Date(2026, 10, 9, 9, 30, 0, 0, utc), // horizon 2026-10-23 09:30
			[]string{"warn 2026-10-22 08:55", "sleep 2026-10-22 09:00", "wake 2026-10-22 10:00",
				"warn 2026-10-23 08:55", "sleep 2026-10-23 09:00"}},
		{"ties ordered warn, sleep, wake; sorted across occurrences",
			[]Occurrence{
				occ("2026-10-10", day("12:00", "13:00", Gap{"12:00", "12:30"})),
				occ("2026-10-10", day("08:00", "12:00", Gap{"11:00", "11:55"})),
				occ("2026-10-11", day("08:00", "18:00", Gap{"09:00", "10:00"}, Gap{"10:00", "11:00"})),
			},
			time.Date(2026, 10, 9, 0, 0, 0, 0, utc),
			[]string{
				"warn 2026-10-10 10:55", "sleep 2026-10-10 11:00",
				"warn 2026-10-10 11:55", "wake 2026-10-10 11:55", "sleep 2026-10-10 12:00", "wake 2026-10-10 12:30",
				"warn 2026-10-11 08:55", "sleep 2026-10-11 09:00", "warn 2026-10-11 09:55",
				"sleep 2026-10-11 10:00", "wake 2026-10-11 10:00", "wake 2026-10-11 11:00"}},
		{"no sleeps, no events",
			[]Occurrence{occ("2026-10-10", day("08:00", "18:00"))},
			time.Date(2026, 10, 9, 0, 0, 0, 0, utc),
			[]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lines(PlanEvents(c.occs, c.now)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got  %v\nwant %v", got, c.want)
			}
		})
	}
}

func TestFormatPlan(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 5, 0, 0, time.FixedZone("BST", 3600))
	evs := []Event{
		{Kind: Warn, Date: "2026-10-09", At: "08:55"},
		{Kind: Sleep, Date: "2026-10-09", At: "09:00"},
		{Kind: Wake, Date: "2026-10-09", At: "10:00"},
	}
	want := "# relay schedule plan, written 2026-10-09T00:05:00+01:00\n" +
		"warn 2026-10-09 08:55\n" +
		"sleep 2026-10-09 09:00\n" +
		"wake 2026-10-09 10:00\n"
	if got := FormatPlan(evs, now); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if got := FormatPlan(nil, now); got != "# relay schedule plan, written 2026-10-09T00:05:00+01:00\n" {
		t.Fatalf("empty plan: %q", got)
	}
}
