package schedule

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const dateLayout = "2006-01-02"

// parseDate turns "YYYY-MM-DD" into a day number (days since 1970-01-01). Dates carry no
// zone, so the arithmetic is done in UTC where every day is exactly 24h.
func parseDate(s Date) (int, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil || t.Format(dateLayout) != s {
		return 0, fmt.Errorf("%q is not a YYYY-MM-DD date", s)
	}
	return int(t.Unix() / 86400), nil
}

func dayTime(n int) time.Time { return time.Unix(int64(n)*86400, 0).UTC() }

func formatDay(n int) Date { return dayTime(n).Format(dateLayout) }

// isoWeekday: 1=Mon..7=Sun.
func isoWeekday(n int) int { return (int(dayTime(n).Weekday())+6)%7 + 1 }

// parseClock turns "HH:MM" into minutes since midnight.
func parseClock(s Clock) (int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, fmt.Errorf("%q is not an HH:MM time", s)
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("%q is not an HH:MM time", s)
		}
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if h > 23 || m > 59 {
		return 0, fmt.Errorf("%q is not a time between 00:00 and 23:59", s)
	}
	return h*60 + m, nil
}

// ValidateDay checks an awake block and its sleeps.
func ValidateDay(d Day) error {
	start, err := parseClock(d.Start)
	if err != nil {
		return fmt.Errorf("start: %v", err)
	}
	end, err := parseClock(d.End)
	if err != nil {
		return fmt.Errorf("end: %v", err)
	}
	if start >= end {
		return fmt.Errorf("end: must be after start")
	}
	prevTo := -1
	for i, g := range d.Sleeps {
		from, err := parseClock(g.From)
		if err != nil {
			return fmt.Errorf("sleeps[%d].from: %v", i, err)
		}
		to, err := parseClock(g.To)
		if err != nil {
			return fmt.Errorf("sleeps[%d].to: %v", i, err)
		}
		switch {
		case from >= to:
			return fmt.Errorf("sleeps[%d]: from must be before to", i)
		case from < start:
			return fmt.Errorf("sleeps[%d]: starts before the awake block", i)
		case to > end:
			return fmt.Errorf("sleeps[%d]: ends after the awake block", i)
		case to-from < MinGapMinutes:
			return fmt.Errorf("sleeps[%d]: shorter than %d minutes", i, MinGapMinutes)
		case from < prevTo:
			return fmt.Errorf("sleeps[%d]: overlaps or comes before sleeps[%d]", i, i-1)
		case prevTo >= 0 && from-prevTo < MinGapMinutes:
			// Its warn would fire while still asleep from the previous gap.
			return fmt.Errorf("sleeps[%d]: needs %d awake minutes after sleeps[%d]", i, MinGapMinutes, i-1)
		}
		prevTo = to
	}
	return nil
}

// Validate checks a whole booking: date, day, repeat rule and exceptions.
func Validate(b Booking) error {
	start, err := parseDate(b.Date)
	if err != nil {
		return fmt.Errorf("date: %v", err)
	}
	if err := ValidateDay(b.Day); err != nil {
		return err
	}
	if r := b.Repeat; r != nil {
		switch r.Freq {
		case Daily, Weekly, Monthly, Yearly:
		default:
			return fmt.Errorf("repeat.freq: unknown %q", r.Freq)
		}
		if r.Interval < 1 {
			return fmt.Errorf("repeat.interval: must be >= 1")
		}
		if len(r.Weekdays) > 0 && r.Freq != Weekly {
			return fmt.Errorf("repeat.weekdays: only allowed for weekly")
		}
		seen := map[int]bool{}
		for i, wd := range r.Weekdays {
			if wd < 1 || wd > 7 {
				return fmt.Errorf("repeat.weekdays[%d]: must be 1..7", i)
			}
			if seen[wd] {
				return fmt.Errorf("repeat.weekdays[%d]: duplicate", i)
			}
			seen[wd] = true
		}
		if r.Until != nil && r.Count != nil {
			return fmt.Errorf("repeat: until and count are mutually exclusive")
		}
		if r.Until != nil {
			u, err := parseDate(*r.Until)
			if err != nil {
				return fmt.Errorf("repeat.until: %v", err)
			}
			if u < start {
				return fmt.Errorf("repeat.until: before date")
			}
		}
		if r.Count != nil && *r.Count < 1 {
			return fmt.Errorf("repeat.count: must be >= 1")
		}
	}
	for i, ex := range b.Exceptions {
		if _, err := parseDate(ex.Date); err != nil {
			return fmt.Errorf("exceptions[%d].date: %v", i, err)
		}
		if ex.Override != nil {
			if err := ValidateDay(*ex.Override); err != nil {
				return fmt.Errorf("exceptions[%d].override.%v", i, err)
			}
		}
	}
	return nil
}

// seriesDates calls fn with every original occurrence date (day number) of a valid booking, in
// order, up to limit inclusive, until fn returns false. hint is the earliest date the caller
// cares about: when the series has no Count, whole periods before it are skipped. With a
// Count every occurrence from Date on must be counted, so nothing is skipped.
func seriesDates(b Booking, limit, hint int, fn func(d int) bool) {
	start, _ := parseDate(b.Date)
	r := b.Repeat
	if r == nil {
		if start <= limit {
			fn(start)
		}
		return
	}
	if r.Until != nil {
		if u, _ := parseDate(*r.Until); u < limit {
			limit = u
		}
	}
	left := -1 // occurrences left; -1 = unlimited
	if r.Count != nil {
		left = *r.Count
	}
	skip := left < 0 && hint > start
	emit := func(d int) bool {
		if left == 0 {
			return false
		}
		if left > 0 {
			left--
		}
		return fn(d) && left != 0
	}
	iv := r.Interval

	switch r.Freq {
	case Daily:
		d := start
		if skip {
			d += (hint - start) / iv * iv
		}
		for ; d <= limit; d += iv {
			if !emit(d) {
				return
			}
		}
	case Weekly:
		wds := append([]int(nil), r.Weekdays...)
		if len(wds) == 0 {
			wds = []int{isoWeekday(start)}
		}
		sort.Ints(wds)
		monday := start - (isoWeekday(start) - 1)
		w := 0
		if skip {
			weeks := (hint - monday) / 7
			w = weeks / iv * iv
		}
		for ; monday+7*w <= limit; w += iv {
			for _, wd := range wds {
				d := monday + 7*w + wd - 1
				if d < start {
					continue
				}
				if d > limit || !emit(d) {
					return
				}
			}
		}
	case Monthly, Yearly:
		t := dayTime(start)
		y, m, dom := t.Date()
		for k := 0; ; k += iv {
			var first time.Time
			if r.Freq == Monthly {
				first = time.Date(y, m+time.Month(k), 1, 0, 0, 0, 0, time.UTC)
			} else {
				first = time.Date(y+k, m, 1, 0, 0, 0, 0, time.UTC)
			}
			dim := first.AddDate(0, 1, -1).Day()
			day := dom
			if day > dim {
				day = dim
			}
			d := int(first.Unix()/86400) + day - 1
			if d > limit || !emit(d) {
				return
			}
		}
	}
}

// Expand returns every occurrence of the bookings dated within [from, to], with exceptions
// applied, sorted by date then start.
func Expand(bs []Booking, from, to Date) ([]Occurrence, error) {
	f, err := parseDate(from)
	if err != nil {
		return nil, fmt.Errorf("from: %v", err)
	}
	t, err := parseDate(to)
	if err != nil {
		return nil, fmt.Errorf("to: %v", err)
	}
	var out []Occurrence
	for _, b := range bs {
		if err := Validate(b); err != nil {
			return nil, fmt.Errorf("booking %s: %v", b.ID, err)
		}
		exc := map[Date]Exception{}
		for _, ex := range b.Exceptions {
			if _, dup := exc[ex.Date]; !dup {
				exc[ex.Date] = ex
			}
		}
		seriesDates(b, t, f, func(d int) bool {
			if d < f {
				return true
			}
			o := Occurrence{
				BookingID: b.ID,
				Title:     b.Title,
				Date:      formatDay(d),
				Day:       copyDay(b.Day),
				Recurring: b.Repeat != nil,
			}
			if ex, ok := exc[o.Date]; ok {
				if ex.Cancelled {
					return true
				}
				if ex.Override != nil {
					o.Day = copyDay(*ex.Override)
					o.Edited = true
				}
			}
			out = append(out, o)
			return true
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Start < out[j].Start
	})
	return out, nil
}

func copyDay(d Day) Day {
	// Never nil: JSON clients expect "sleeps": [] (shared/API.md).
	sleeps := make([]Gap, len(d.Sleeps))
	copy(sleeps, d.Sleeps)
	d.Sleeps = sleeps
	return d
}

// IsOccurrence reports whether d is one of the booking's original occurrence dates. It
// ignores exceptions: a cancelled or overridden date still counts.
func IsOccurrence(b Booking, d Date) bool {
	n, err := parseDate(d)
	if err != nil || Validate(b) != nil {
		return false
	}
	found := false
	seriesDates(b, n, n, func(x int) bool {
		if x == n {
			found = true
			return false
		}
		return x < n
	})
	return found
}

// Overlaps returns an error if any occurrence of b in [from, from+OverlapCheckDays] shares a
// date with another booking's occurrence and their awake blocks intersect. Touching ends
// (one ends at 12:00, the other starts at 12:00) are fine. Others with b's ID are skipped.
func Overlaps(b Booking, others []Booking, from Date) error {
	f, err := parseDate(from)
	if err != nil {
		return fmt.Errorf("from: %v", err)
	}
	to := formatDay(f + OverlapCheckDays)
	mine, err := Expand([]Booking{b}, from, to)
	if err != nil {
		return err
	}
	if len(mine) == 0 {
		return nil
	}
	var rest []Booking
	for _, o := range others {
		if o.ID != b.ID {
			rest = append(rest, o)
		}
	}
	theirs, err := Expand(rest, from, to)
	if err != nil {
		return err
	}
	byDate := map[Date][]Occurrence{}
	for _, o := range theirs {
		byDate[o.Date] = append(byDate[o.Date], o)
	}
	for _, m := range mine {
		ms, _ := parseClock(m.Start)
		me, _ := parseClock(m.End)
		for _, o := range byDate[m.Date] {
			os, _ := parseClock(o.Start)
			oe, _ := parseClock(o.End)
			if ms < oe && os < me {
				name := o.BookingID
				if o.Title != "" {
					name = fmt.Sprintf("%q (%s)", o.Title, o.BookingID)
				}
				return fmt.Errorf("overlaps %s on %s (%s-%s)", name, m.Date, o.Start, o.End)
			}
		}
	}
	return nil
}

var kindOrder = map[EventKind]int{Warn: 0, Sleep: 1, Wake: 2}

// PlanEvents turns occurrences into warn/sleep/wake events strictly after now and before
// now+PlanHorizonDays, reading dates and clocks as wall-clock time in now.Location().
func PlanEvents(occs []Occurrence, now time.Time) []Event {
	loc := now.Location()
	horizon := now.Add(PlanHorizonDays * 24 * time.Hour)
	type timed struct {
		at   time.Time
		kind EventKind
	}
	var ts []timed
	add := func(at time.Time, k EventKind) {
		if at.After(now) && at.Before(horizon) {
			ts = append(ts, timed{at, k})
		}
	}
	for _, o := range occs {
		d, err := parseDate(o.Date)
		if err != nil {
			continue
		}
		y, mo, day := dayTime(d).Date()
		at := func(mins int) time.Time { return time.Date(y, mo, day, 0, mins, 0, 0, loc) }
		for _, g := range o.Sleeps {
			from, err1 := parseClock(g.From)
			to, err2 := parseClock(g.To)
			if err1 != nil || err2 != nil {
				continue
			}
			add(at(from-WarnMinutes), Warn)
			add(at(from), Sleep)
			add(at(to), Wake)
		}
	}
	sort.SliceStable(ts, func(i, j int) bool {
		if !ts[i].at.Equal(ts[j].at) {
			return ts[i].at.Before(ts[j].at)
		}
		return kindOrder[ts[i].kind] < kindOrder[ts[j].kind]
	})
	out := make([]Event, 0, len(ts))
	for _, e := range ts {
		l := e.at.In(loc)
		out = append(out, Event{Kind: e.kind, Date: l.Format(dateLayout), At: l.Format("15:04")})
	}
	return out
}

// FormatPlan renders the plan file: a comment line, then "<kind> YYYY-MM-DD HH:MM" per event.
func FormatPlan(events []Event, now time.Time) string {
	var sb strings.Builder
	sb.WriteString("# relay schedule plan, written " + now.Format(time.RFC3339) + "\n")
	for _, e := range events {
		fmt.Fprintf(&sb, "%s %s %s\n", e.Kind, e.Date, e.At)
	}
	return sb.String()
}
