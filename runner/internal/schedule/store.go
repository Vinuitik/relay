package schedule

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver "sqlite"; keeps the runner CGO_ENABLED=0
)

// ErrNotFound is returned when a booking id does not exist.
var ErrNotFound = errors.New("schedule: booking not found")

// Store persists bookings and their exceptions in one SQLite file. It does no validation.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// migrations[i] upgrades the schema from user_version i to i+1. Append only; never edit a
// step that has shipped.
var migrations = []string{
	// v1
	`CREATE TABLE bookings (
		id         TEXT PRIMARY KEY,
		title      TEXT NOT NULL,
		date       TEXT NOT NULL,
		"start"    TEXT NOT NULL,
		"end"      TEXT NOT NULL,
		sleeps     TEXT NOT NULL, -- JSON []Gap
		repeat     TEXT,          -- JSON Repeat, NULL = one-off
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE TABLE exceptions (
		booking_id TEXT NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
		date       TEXT NOT NULL,
		cancelled  INTEGER NOT NULL,
		override   TEXT, -- JSON Day, NULL when cancelled
		PRIMARY KEY (booking_id, date)
	);`,
}

// Open opens (creating if needed) the database at path and brings its schema up to date.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("schedule: create dir: %w", err)
		}
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("schedule: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return fmt.Errorf("schedule: read user_version: %w", err)
	}
	if v > len(migrations) {
		return fmt.Errorf("schedule: db schema v%d is newer than this runner (v%d)", v, len(migrations))
	}
	for ; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("schedule: migration v%d: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("schedule: set user_version %d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("schedule: commit migration v%d: %w", v+1, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) stamp() string { return s.now().UTC().Format(time.RFC3339) }

// querier is the subset of *sql.DB / *sql.Tx the read helpers need.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

const bookingCols = `id, title, date, "start", "end", sleeps, repeat, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanBooking(r scanner) (Booking, error) {
	var (
		b      Booking
		sleeps string
		repeat sql.NullString
	)
	if err := r.Scan(&b.ID, &b.Title, &b.Date, &b.Start, &b.End, &sleeps, &repeat, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return Booking{}, err
	}
	if err := json.Unmarshal([]byte(sleeps), &b.Sleeps); err != nil {
		return Booking{}, fmt.Errorf("schedule: booking %s sleeps: %w", b.ID, err)
	}
	if b.Sleeps == nil {
		b.Sleeps = []Gap{}
	}
	if repeat.Valid {
		b.Repeat = new(Repeat)
		if err := json.Unmarshal([]byte(repeat.String), b.Repeat); err != nil {
			return Booking{}, fmt.Errorf("schedule: booking %s repeat: %w", b.ID, err)
		}
	}
	b.Exceptions = []Exception{}
	return b, nil
}

// List returns every booking ordered by date, start, id, with exceptions loaded.
func (s *Store) List() ([]Booking, error) {
	rows, err := s.db.Query(`SELECT ` + bookingCols + ` FROM bookings ORDER BY date, "start", id`)
	if err != nil {
		return nil, err
	}
	out := []Booking{}
	idx := map[string]int{}
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		idx[b.ID] = len(out)
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	erows, err := s.db.Query(`SELECT booking_id, date, cancelled, override FROM exceptions ORDER BY booking_id, date`)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var id string
		e, err := scanException(erows, &id)
		if err != nil {
			return nil, err
		}
		if i, ok := idx[id]; ok {
			out[i].Exceptions = append(out[i].Exceptions, e)
		}
	}
	return out, erows.Err()
}

func scanException(r scanner, bookingID *string) (Exception, error) {
	var (
		e        Exception
		override sql.NullString
	)
	if err := r.Scan(bookingID, &e.Date, &e.Cancelled, &override); err != nil {
		return Exception{}, err
	}
	if override.Valid {
		e.Override = new(Day)
		if err := json.Unmarshal([]byte(override.String), e.Override); err != nil {
			return Exception{}, fmt.Errorf("schedule: exception %s/%s override: %w", *bookingID, e.Date, err)
		}
		if e.Override.Sleeps == nil {
			e.Override.Sleeps = []Gap{}
		}
	}
	return e, nil
}

// Get returns one booking with its exceptions, or ErrNotFound.
func (s *Store) Get(id string) (Booking, error) { return get(s.db, id) }

func get(q querier, id string) (Booking, error) {
	b, err := scanBooking(q.QueryRow(`SELECT `+bookingCols+` FROM bookings WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Booking{}, ErrNotFound
	}
	if err != nil {
		return Booking{}, err
	}
	rows, err := q.Query(`SELECT booking_id, date, cancelled, override FROM exceptions WHERE booking_id = ? ORDER BY date`, id)
	if err != nil {
		return Booking{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var bid string
		e, err := scanException(rows, &bid)
		if err != nil {
			return Booking{}, err
		}
		b.Exceptions = append(b.Exceptions, e)
	}
	return b, rows.Err()
}

func newID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func encodeBooking(b Booking) (sleeps string, repeat any, err error) {
	gaps := b.Sleeps
	if gaps == nil {
		gaps = []Gap{}
	}
	sj, err := json.Marshal(gaps)
	if err != nil {
		return "", nil, err
	}
	if b.Repeat != nil {
		rj, err := json.Marshal(b.Repeat)
		if err != nil {
			return "", nil, err
		}
		repeat = string(rj)
	}
	return string(sj), repeat, nil
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func putException(x execer, id string, e Exception) error {
	var override any
	if e.Override != nil {
		oj, err := json.Marshal(e.Override)
		if err != nil {
			return err
		}
		override = string(oj)
	}
	_, err := x.Exec(`INSERT INTO exceptions (booking_id, date, cancelled, override) VALUES (?, ?, ?, ?)
		ON CONFLICT (booking_id, date) DO UPDATE SET cancelled = excluded.cancelled, override = excluded.override`,
		id, e.Date, e.Cancelled, override)
	return err
}

// Create inserts b with a fresh ID and timestamps (incoming ID/CreatedAt/UpdatedAt ignored).
func (s *Store) Create(b Booking) (Booking, error) {
	id, err := newID()
	if err != nil {
		return Booking{}, fmt.Errorf("schedule: new id: %w", err)
	}
	sleeps, repeat, err := encodeBooking(b)
	if err != nil {
		return Booking{}, err
	}
	now := s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return Booking{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO bookings (`+bookingCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, b.Title, b.Date, b.Start, b.End, sleeps, repeat, now, now); err != nil {
		return Booking{}, err
	}
	for _, e := range b.Exceptions {
		if err := putException(tx, id, e); err != nil {
			return Booking{}, err
		}
	}
	out, err := get(tx, id)
	if err != nil {
		return Booking{}, err
	}
	return out, tx.Commit()
}

// Update replaces title/date/day/repeat and the whole exception set with b's; keeps CreatedAt.
func (s *Store) Update(id string, b Booking) (Booking, error) {
	sleeps, repeat, err := encodeBooking(b)
	if err != nil {
		return Booking{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Booking{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE bookings SET title = ?, date = ?, "start" = ?, "end" = ?, sleeps = ?, repeat = ?, updated_at = ? WHERE id = ?`,
		b.Title, b.Date, b.Start, b.End, sleeps, repeat, s.stamp(), id)
	if err != nil {
		return Booking{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Booking{}, err
	} else if n == 0 {
		return Booking{}, ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM exceptions WHERE booking_id = ?`, id); err != nil {
		return Booking{}, err
	}
	for _, e := range b.Exceptions {
		if err := putException(tx, id, e); err != nil {
			return Booking{}, err
		}
	}
	out, err := get(tx, id)
	if err != nil {
		return Booking{}, err
	}
	return out, tx.Commit()
}

// Delete removes a booking; its exceptions cascade.
func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM bookings WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetException upserts one exception keyed by (id, e.Date) and bumps the booking's UpdatedAt.
func (s *Store) SetException(id string, e Exception) (Booking, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Booking{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE bookings SET updated_at = ? WHERE id = ?`, s.stamp(), id)
	if err != nil {
		return Booking{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Booking{}, err
	} else if n == 0 {
		return Booking{}, ErrNotFound
	}
	if err := putException(tx, id, e); err != nil {
		return Booking{}, err
	}
	out, err := get(tx, id)
	if err != nil {
		return Booking{}, err
	}
	return out, tx.Commit()
}
