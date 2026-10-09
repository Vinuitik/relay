package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PlanEnvVar overrides where the plan file is written (shared/API.md "Schedule plan file").
const PlanEnvVar = "RELAY_SCHEDULE_PLAN"

// DefaultPlanPath is the plan file the root applier watches.
const DefaultPlanPath = "/var/lib/relay/schedule-plan"

// Error kinds returned by Service, so the HTTP layer can map them: ErrNotFound → 404,
// ErrInvalid → 400, ErrOverlap → 409. Test with errors.Is; Error() is the bare message.
var (
	ErrInvalid = errors.New("schedule: invalid")
	ErrOverlap = errors.New("schedule: overlap")
)

type kindError struct {
	kind error
	err  error
}

func (e *kindError) Error() string        { return e.err.Error() }
func (e *kindError) Unwrap() error        { return e.err }
func (e *kindError) Is(target error) bool { return target == e.kind }

func invalid(err error) error { return &kindError{ErrInvalid, err} }
func overlap(err error) error { return &kindError{ErrOverlap, err} }

// BookingInput is the request body for creating or editing a whole series: a Booking minus
// id/exceptions/createdAt/updatedAt.
type BookingInput struct {
	Title  string  `json:"title"`
	Date   Date    `json:"date"`
	Day            // start/end/sleeps, inlined in JSON
	Repeat *Repeat `json:"repeat"`
}

func (in BookingInput) booking() Booking {
	return Booking{Title: in.Title, Date: in.Date, Day: in.Day, Repeat: in.Repeat}
}

// Schedule is GET /v1/schedule.
type Schedule struct {
	Timezone      string    `json:"timezone"`
	Bookings      []Booking `json:"bookings"`
	Plan          []Event   `json:"plan"`
	PlanWrittenAt *string   `json:"planWrittenAt"`
	AppliedAt     *string   `json:"appliedAt"`
	Applied       bool      `json:"applied"`
}

// Service is the booking API on top of Store. Every mutation and its plan-file rewrite run
// under one mutex, so the plan always reflects a consistent set of bookings.
type Service struct {
	store *Store
	mu    sync.Mutex

	// Now is the clock; its Location is the zone dates/clocks are read in. Default time.Now.
	Now func() time.Time
	// PlanPath is the plan file. If its directory doesn't exist the plan is not written.
	PlanPath string
	// StatusPath is the root applier's status file (sha256 line, RFC3339 line).
	StatusPath string
	// Zone is the IANA name reported as Schedule.Timezone; "" = detect (see localZoneName).
	Zone string
}

// NewService wraps st. PlanPath comes from RELAY_SCHEDULE_PLAN (default DefaultPlanPath);
// StatusPath is "schedule-applied" next to it.
func NewService(st *Store) *Service {
	plan := os.Getenv(PlanEnvVar)
	if plan == "" {
		plan = DefaultPlanPath
	}
	return &Service{
		store:      st,
		Now:        time.Now,
		PlanPath:   plan,
		StatusPath: filepath.Join(filepath.Dir(plan), "schedule-applied"),
	}
}

func (s *Service) today() Date { return s.Now().Format(dateLayout) }

// Start writes the plan once, then again every day at 00:05 local until ctx is done (the
// plan only covers the next PlanHorizonDays, so it has to roll forward).
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	s.writePlanLogged()
	s.mu.Unlock()
	go func() {
		for {
			now := s.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 0, 5, 0, 0, now.Location())
			if !next.After(now) {
				next = next.AddDate(0, 0, 1)
			}
			t := time.NewTimer(next.Sub(now))
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
				s.mu.Lock()
				s.writePlanLogged()
				s.mu.Unlock()
			}
		}
	}()
}

// Schedule returns the bookings, the plan the runner wants applied and the applier's status.
func (s *Service) Schedule() (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.store.List()
	if err != nil {
		return Schedule{}, err
	}
	events, err := s.events(all)
	if err != nil {
		return Schedule{}, err
	}
	if all == nil {
		all = []Booking{}
	}
	for i := range all {
		all[i] = norm(all[i])
	}
	if events == nil {
		events = []Event{}
	}
	out := Schedule{Timezone: s.Zone, Bookings: all, Plan: events}
	if out.Timezone == "" {
		out.Timezone = localZoneName()
	}
	if fi, err := os.Stat(s.PlanPath); err == nil {
		at := fi.ModTime().Format(time.RFC3339)
		out.PlanWrittenAt = &at
		plan, perr := os.ReadFile(s.PlanPath)
		status, serr := os.ReadFile(s.StatusPath)
		if perr == nil && serr == nil {
			lines := strings.Split(strings.ReplaceAll(string(status), "\r\n", "\n"), "\n")
			sum := sha256.Sum256(plan)
			out.Applied = strings.TrimSpace(lines[0]) == hex.EncodeToString(sum[:])
			if len(lines) > 1 {
				if a := strings.TrimSpace(lines[1]); a != "" {
					out.AppliedAt = &a
				}
			}
		}
	}
	return out, nil
}

// Occurrences expands every booking into concrete days within [from, to] inclusive.
// ErrInvalid for bad dates, to before from, or a span over 62 days.
func (s *Service) Occurrences(from, to Date) ([]Occurrence, error) {
	f, err := time.Parse(dateLayout, from)
	if err != nil {
		return nil, invalid(fmt.Errorf("from: must be YYYY-MM-DD"))
	}
	t, err := time.Parse(dateLayout, to)
	if err != nil {
		return nil, invalid(fmt.Errorf("to: must be YYYY-MM-DD"))
	}
	if t.Before(f) {
		return nil, invalid(fmt.Errorf("to: must not be before from"))
	}
	if t.Sub(f) > 62*24*time.Hour {
		return nil, invalid(fmt.Errorf("to: span must be at most 62 days"))
	}
	all, err := s.store.List()
	if err != nil {
		return nil, err
	}
	occs, err := Expand(all, from, to)
	if err != nil {
		return nil, err
	}
	if occs == nil {
		occs = []Occurrence{}
	}
	for i := range occs {
		if occs[i].Sleeps == nil {
			occs[i].Sleeps = []Gap{}
		}
	}
	return occs, nil
}

// Create validates and stores a new booking, refusing one that overlaps another's day.
func (s *Service) Create(in BookingInput) (Booking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := in.booking()
	if err := s.check(b); err != nil {
		return Booking{}, err
	}
	out, err := s.store.Create(b)
	if err != nil {
		return Booking{}, err
	}
	s.writePlanLogged()
	return norm(out), nil
}

// UpdateSeries replaces the whole series. Exceptions whose date is no longer an occurrence of
// the new rule are dropped; the rest are kept.
func (s *Service) UpdateSeries(id string, in BookingInput) (Booking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.store.Get(id)
	if err != nil {
		return Booking{}, err
	}
	b := in.booking()
	b.ID = id
	if err := Validate(b); err != nil {
		return Booking{}, invalid(err)
	}
	b.Exceptions = []Exception{}
	for _, ex := range old.Exceptions {
		if IsOccurrence(b, ex.Date) {
			b.Exceptions = append(b.Exceptions, ex)
		}
	}
	if err := s.check(b); err != nil {
		return Booking{}, err
	}
	out, err := s.store.Update(id, b)
	if err != nil {
		return Booking{}, err
	}
	s.writePlanLogged()
	return norm(out), nil
}

// DeleteSeries removes a booking and all its exceptions.
func (s *Service) DeleteSeries(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.Delete(id); err != nil {
		return err
	}
	s.writePlanLogged()
	return nil
}

// UpdateOccurrence overrides one day of a booking. ErrNotFound if date isn't an occurrence.
func (s *Service) UpdateOccurrence(id string, date Date, d Day) (Booking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.store.Get(id)
	if err != nil {
		return Booking{}, err
	}
	if !IsOccurrence(b, date) {
		return Booking{}, ErrNotFound
	}
	if err := ValidateDay(d); err != nil {
		return Booking{}, invalid(err)
	}
	if d.Sleeps == nil {
		d.Sleeps = []Gap{}
	}
	ex := Exception{Date: date, Override: &d}
	b.Exceptions = withException(b.Exceptions, ex)
	if err := s.check(b); err != nil {
		return Booking{}, err
	}
	out, err := s.store.SetException(id, ex)
	if err != nil {
		return Booking{}, err
	}
	s.writePlanLogged()
	return norm(out), nil
}

// CancelOccurrence cancels one day. For a one-off that means deleting the booking: deleted is
// true and the returned Booking is zero. ErrNotFound if date isn't an occurrence.
func (s *Service) CancelOccurrence(id string, date Date) (b Booking, deleted bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err = s.store.Get(id)
	if err != nil {
		return Booking{}, false, err
	}
	if !IsOccurrence(b, date) {
		return Booking{}, false, ErrNotFound
	}
	if b.Repeat == nil {
		if err := s.store.Delete(id); err != nil {
			return Booking{}, false, err
		}
		s.writePlanLogged()
		return Booking{}, true, nil
	}
	out, err := s.store.SetException(id, Exception{Date: date, Cancelled: true})
	if err != nil {
		return Booking{}, false, err
	}
	s.writePlanLogged()
	return norm(out), false, nil
}

// check validates b and refuses it if it overlaps another booking from today on.
func (s *Service) check(b Booking) error {
	if err := Validate(b); err != nil {
		return invalid(err)
	}
	all, err := s.store.List()
	if err != nil {
		return err
	}
	if err := Overlaps(b, all, s.today()); err != nil {
		return overlap(err)
	}
	return nil
}

// norm makes every JSON list in b an empty array rather than null.
func norm(b Booking) Booking {
	if b.Sleeps == nil {
		b.Sleeps = []Gap{}
	}
	if b.Exceptions == nil {
		b.Exceptions = []Exception{}
	}
	for i, ex := range b.Exceptions {
		if ex.Override != nil && ex.Override.Sleeps == nil {
			d := *ex.Override
			d.Sleeps = []Gap{}
			b.Exceptions[i].Override = &d
		}
	}
	return b
}

func withException(exs []Exception, ex Exception) []Exception {
	out := make([]Exception, 0, len(exs)+1)
	for _, e := range exs {
		if e.Date != ex.Date {
			out = append(out, e)
		}
	}
	return append(out, ex)
}

// events is the plan the runner wants applied right now.
func (s *Service) events(all []Booking) ([]Event, error) {
	now := s.Now()
	end := now.AddDate(0, 0, PlanHorizonDays+1).Format(dateLayout)
	occs, err := Expand(all, now.Format(dateLayout), end)
	if err != nil {
		return nil, err
	}
	return PlanEvents(occs, now), nil
}

func (s *Service) writePlanLogged() {
	if err := s.writePlan(); err != nil {
		log.Printf("schedule: write plan %s: %v", s.PlanPath, err)
	}
}

// writePlan rewrites the plan file atomically. Caller holds s.mu. No-op if the directory is
// missing (applier not installed) or the event lines are unchanged (so the applier isn't
// re-run just because the comment's timestamp moved).
func (s *Service) writePlan() error {
	dir := filepath.Dir(s.PlanPath)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil
	}
	all, err := s.store.List()
	if err != nil {
		return err
	}
	events, err := s.events(all)
	if err != nil {
		return err
	}
	content := FormatPlan(events, s.Now())
	if cur, err := os.ReadFile(s.PlanPath); err == nil && eventLines(string(cur)) == eventLines(content) {
		return nil
	}
	tmp, err := os.CreateTemp(dir, ".schedule-plan-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.PlanPath)
}

func eventLines(plan string) string {
	var sb strings.Builder
	for _, l := range strings.Split(plan, "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// localZoneName is the runner's IANA zone: $TZ, else the /etc/localtime symlink target after
// "zoneinfo/", else time.Local's name (often just "Local").
func localZoneName() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		return tz
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.LastIndex(target, "zoneinfo/"); i >= 0 {
			return target[i+len("zoneinfo/"):]
		}
	}
	return time.Local.String()
}
