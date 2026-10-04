// Package usage records how this machine is actually used and simulates
// how much it would sleep under idle-suspend - without ever suspending.
// It exists to answer one question with data: is a wake-on-LAN device (a
// Pi, an old phone, a router) worth buying, or would the machine be awake
// nearly all the time anyway?
//
// Recording is always on, independent of RELAY_IDLE_SUSPEND_ENABLED:
//
//   - one "minute" line per wall-clock minute the runner was up, flagging
//     which activity sources were live in it (agent turn / phone app in
//     the foreground / local keyboard+mouse);
//   - one "turn" line per completed agent turn (start, end, session id).
//
// Lines are JSON, appended to $RELAY_HOME/usage/YYYY-MM-DD.jsonl (UTC
// date), pruned after RetentionDays. Report reads them back and runs the
// sleep simulation (simulate.go) for GET /v1/usage.
package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RetentionDays is how long day files are kept before Prune deletes them.
const RetentionDays = 90

// Activity source flags, as letters in Minute.Active.
const (
	FlagTurn  = 't' // an agent turn was running at some point in the minute
	FlagApp   = 'a' // the phone app pinged POST /v1/activity in the minute
	FlagLocal = 'l' // local keyboard/mouse input in the minute (Windows only)
)

// Minute is one wall-clock minute the runner was up. T is the minute's
// start (unix seconds, a multiple of 60). Active holds zero or more of the
// Flag* letters; empty means "up but nobody using it".
type Minute struct {
	T      int64  `json:"t,omitempty"`
	Active string `json:"a,omitempty"`
}

// Turn is one completed agent turn: Start is when the user's message
// started it, End is when the agent finished (unix milliseconds).
type Turn struct {
	SessionID string `json:"sid,omitempty"`
	Start     int64  `json:"s,omitempty"`
	End       int64  `json:"e,omitempty"`
}

// line is the on-disk record: K is "m" (Minute) or "t" (Turn).
type line struct {
	K string `json:"k"`
	Minute
	Turn
}

func dayFile(dir string, t time.Time) string {
	return filepath.Join(dir, t.UTC().Format("2006-01-02")+".jsonl")
}

func appendLine(dir string, t time.Time, l line) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dayFile(dir, t), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// Load reads every minute and turn recorded in [from, to) from dir. A
// missing dir or day file is just "no data"; a corrupt line (e.g. a write
// torn by a power cut) is skipped rather than failing the whole read.
func Load(dir string, from, to time.Time) ([]Minute, []Turn, error) {
	var mins []Minute
	var turns []Turn
	fromS, toS := from.Unix(), to.Unix()
	for d := from.UTC().Truncate(24 * time.Hour); d.Before(to); d = d.Add(24 * time.Hour) {
		f, err := os.Open(dayFile(dir, d))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var l line
			if json.Unmarshal(sc.Bytes(), &l) != nil {
				continue
			}
			switch l.K {
			case "m":
				if l.Minute.T >= fromS && l.Minute.T < toS {
					mins = append(mins, l.Minute)
				}
			case "t":
				if e := l.Turn.End / 1000; e >= fromS && e < toS {
					turns = append(turns, l.Turn)
				}
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, nil, fmt.Errorf("usage: read %s: %w", d.Format("2006-01-02"), err)
		}
	}
	sort.Slice(mins, func(i, j int) bool { return mins[i].T < mins[j].T })
	sort.Slice(turns, func(i, j int) bool { return turns[i].Start < turns[j].Start })
	return mins, turns, nil
}

// Prune deletes day files older than RetentionDays before now.
func Prune(dir string, now time.Time) error {
	cutoff := now.UTC().AddDate(0, 0, -RetentionDays).Format("2006-01-02")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		// YYYY-MM-DD sorts lexically, so a string compare is a date compare.
		if strings.TrimSuffix(name, ".jsonl") < cutoff {
			os.Remove(filepath.Join(dir, name))
		}
	}
	return nil
}
