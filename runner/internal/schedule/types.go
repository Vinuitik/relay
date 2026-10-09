// Package schedule holds the server's sleep/wake bookings: a booking is one contiguous awake
// block in a day with sleep gaps inside it, optionally repeating like an Outlook series. The
// runner expands bookings into dated sleep/warn/wake events and writes them to a plan file; a
// root-owned applier (power/server) turns that file into systemd timers. Contract:
// shared/API.md "Schedule".
//
// All dates and times are wall-clock in the runner's local zone (time.Local).
package schedule

// Date is "YYYY-MM-DD"; Clock is "HH:MM" (24h). Kept as strings so JSON, SQLite and the
// API all carry exactly what the user typed, with no zone attached.
type (
	Date  = string
	Clock = string
)

// Gap is one sleep inside a booking's awake block: suspend at From, wake at To.
type Gap struct {
	From Clock `json:"from"`
	To   Clock `json:"to"`
}

// Day is what one occurrence looks like: the awake block and its sleeps.
type Day struct {
	Start  Clock `json:"start"`
	End    Clock `json:"end"`
	Sleeps []Gap `json:"sleeps"`
}

type Freq string

const (
	Daily   Freq = "daily"
	Weekly  Freq = "weekly"
	Monthly Freq = "monthly" // on Date's day-of-month; a shorter month uses its last day
	Yearly  Freq = "yearly"  // on Date's month/day; Feb 29 falls back to Feb 28
)

// Repeat makes a booking a series. Until and Count are mutually exclusive; neither = forever.
type Repeat struct {
	Freq     Freq  `json:"freq"`
	Interval int   `json:"interval"`           // every N days/weeks/months/years, >= 1
	Weekdays []int `json:"weekdays,omitempty"` // weekly only, ISO 1=Mon..7=Sun; empty = Date's weekday
	Until    *Date `json:"until,omitempty"`    // last possible occurrence date, inclusive
	Count    *int  `json:"count,omitempty"`    // total occurrences, >= 1 (cancelled ones still count)
}

// Exception changes one occurrence of a series, keyed by that occurrence's original date.
type Exception struct {
	Date      Date `json:"date"`
	Cancelled bool `json:"cancelled"`
	Override  *Day `json:"override,omitempty"` // set when not cancelled
}

type Booking struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Date       Date        `json:"date"` // the only day (one-off) or the first day (series)
	Day                    // start/end/sleeps, inlined in JSON
	Repeat     *Repeat     `json:"repeat"` // null = one-off
	Exceptions []Exception `json:"exceptions"`
	CreatedAt  string      `json:"createdAt"` // RFC3339
	UpdatedAt  string      `json:"updatedAt"`
}

// Occurrence is one concrete booked day after expanding series and applying exceptions.
type Occurrence struct {
	BookingID string `json:"bookingId"`
	Title     string `json:"title"`
	Date      Date   `json:"date"`
	Day
	Recurring bool `json:"recurring"` // comes from a series
	Edited    bool `json:"edited"`    // this day has an override
}

type EventKind string

const (
	Warn  EventKind = "warn" // 5 min before each sleep
	Sleep EventKind = "sleep"
	Wake  EventKind = "wake"
)

// Event is one line of the plan file: "<kind> YYYY-MM-DD HH:MM".
type Event struct {
	Kind EventKind `json:"kind"`
	Date Date      `json:"date"`
	At   Clock     `json:"at"`
}

const (
	PlanHorizonDays  = 14 // the plan file covers now .. now+14 days
	WarnMinutes      = 5
	MinGapMinutes    = 10
	OverlapCheckDays = 366 // create/update is refused if it overlaps another booking within this window
)
