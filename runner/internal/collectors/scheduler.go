package collectors

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"relay/runner/internal/collectors/sandbox"
)

const (
	// Backfill: missed windows ending within this span are caught up.
	Backfill = 7 * 24 * time.Hour
	// TickEvery is how often the scheduler looks for due windows.
	TickEvery = time.Minute
	// sourceRetry: after a source error (ActivityWatch down) a collector
	// is left alone this long.
	sourceRetry = 5 * time.Minute
)

// Window is one [From, To) collection window.
type Window struct{ From, To time.Time }

// dueWindows lists the complete, due windows ending after now-Backfill,
// oldest first. hourly: local hours, due at the hour's end. daily HH:MM:
// the previous local day, due at HH:MM the next day.
func dueWindows(mf Manifest, now time.Time, loc *time.Location) []Window {
	now = now.In(loc)
	horizon := now.Add(-Backfill)
	var out []Window
	if mf.hourly {
		end := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, loc)
		for end.After(horizon) {
			out = append(out, Window{end.Add(-time.Hour), end})
			end = end.Add(-time.Hour)
		}
	} else {
		dayEnd := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		for dayEnd.After(horizon) {
			due := time.Date(dayEnd.Year(), dayEnd.Month(), dayEnd.Day(), mf.dailyH, mf.dailyM, 0, 0, loc)
			if !now.Before(due) {
				out = append(out, Window{dayEnd.AddDate(0, 0, -1), dayEnd})
			}
			dayEnd = dayEnd.AddDate(0, 0, -1)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Start runs the scheduler until ctx is done. It also pins
// RELAY_COLLECTOR_PYTHON to the discovered interpreter so the Windows
// sandbox's probe checks the same python the engine runs.
func (e *Engine) Start(ctx context.Context) {
	e.init()
	if os.Getenv(PythonEnv) == "" {
		if p, err := e.Python(); err == nil {
			os.Setenv(PythonEnv, p)
		}
	}
	go func() {
		t := time.NewTicker(TickEvery)
		defer t.Stop()
		for {
			e.Tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Tick runs every due, not-yet-recorded window of every active+approved
// collector, oldest first. Never runs anything if the sandbox is
// unavailable.
func (e *Engine) Tick(ctx context.Context) {
	e.init()
	e.tick.Lock()
	defer e.tick.Unlock()
	e.mu.Lock()
	ms, err := e.st.list()
	e.mu.Unlock()
	if err != nil {
		log.Printf("collectors: list: %v", err)
		return
	}
	now := e.Now()
	sandboxErr := e.Sandbox.Available()
	for _, m := range ms {
		if m.State != StateActive || !m.approved() {
			continue
		}
		e.mu.Lock()
		retryAt := e.retry[m.ID]
		e.mu.Unlock()
		if now.Before(retryAt) {
			continue
		}
		if sandboxErr != nil {
			e.setResult(m.ID, nil, "sandbox unavailable, not running: "+sandboxErr.Error(), "")
			continue
		}
		e.runDue(ctx, m.ID, now)
	}
}

func (e *Engine) runDue(ctx context.Context, id string, now time.Time) {
	m, mf, vdir, err := e.current(id)
	if err != nil {
		return
	}
	e.mu.Lock()
	have, err := e.st.readings(id, time.Time{})
	e.mu.Unlock()
	if err != nil {
		return
	}
	done := map[int64]bool{}
	for _, r := range have {
		done[r.From.Unix()] = true
	}
	for _, w := range dueWindows(mf, now, e.loc()) {
		if done[w.From.Unix()] || ctx.Err() != nil {
			continue
		}
		out, err := e.runWindow(ctx, mf, vdir, w.From, w.To)
		ran := e.Now()
		switch {
		case err == nil:
			e.mu.Lock()
			cur, lerr := e.st.load(id)
			if lerr == nil && cur.Version == m.Version {
				lerr = e.st.appendReading(id, Reading{From: w.From, To: w.To, Value: out.Value, Unit: out.Unit, RecordedAt: ran})
			}
			e.mu.Unlock()
			if lerr != nil {
				return // deleted or replaced mid-run
			}
			e.setResult(id, &ran, "", "")
		case errors.Is(err, ErrSource):
			e.mu.Lock()
			e.retry[id] = now.Add(sourceRetry)
			e.mu.Unlock()
			e.setResult(id, &ran, err.Error(), "")
			return
		case errors.Is(err, sandbox.ErrUnavailable):
			e.setResult(id, &ran, err.Error(), "")
			return
		default: // collector error (or bad file source): stop scheduling it
			e.setResult(id, &ran, err.Error(), StateFailed)
			return
		}
	}
}

func (e *Engine) setResult(id string, at *time.Time, lastErr, state string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.st.load(id)
	if err != nil {
		return
	}
	if at != nil {
		m.LastRunAt = at
	}
	m.LastError = lastErr
	if state != "" && m.State == StateActive {
		m.State = state
	}
	_ = e.st.save(m)
}
