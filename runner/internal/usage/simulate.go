package usage

import (
	"math"
	"sort"
	"strings"
	"time"
)

// DefaultLimits are the idle-suspend timeouts (minutes) simulated when the
// caller doesn't pick its own.
var DefaultLimits = []int{5, 15, 30, 60}

// Report is GET /v1/usage's response body - see shared/API.md.
type Report struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// SpanMinutes runs from the first recorded minute (or From, if later)
	// to To, so a recorder started yesterday doesn't report 7% uptime for a
	// 14-day window. ObservedMinutes are the minutes the runner was up.
	SpanMinutes     int     `json:"spanMinutes"`
	ObservedMinutes int     `json:"observedMinutes"`
	UptimePct       float64 `json:"uptimePct"`
	// ActiveMinutes had at least one source live; BySource counts each
	// source separately (a minute can count towards several).
	ActiveMinutes int            `json:"activeMinutes"`
	BySource      map[string]int `json:"bySource"`
	Limits        []LimitResult  `json:"limits"`
	// Heatmaps are [weekday 0=Monday][hour 0-23] in the runner's local
	// zone: active minutes, and observed minutes (the denominator).
	HeatmapActive   [7][24]int `json:"heatmapActive"`
	HeatmapObserved [7][24]int `json:"heatmapObserved"`
	Turns           Stats      `json:"turns"`
	// ReplyLatency is agent turn end → the next user message in the same
	// session.
	ReplyLatency Stats `json:"replyLatency"`
}

// LimitResult is the simulated outcome of suspending after IdleMinutes of
// no activity.
type LimitResult struct {
	IdleMinutes int     `json:"idleMinutes"`
	AwakePct    float64 `json:"awakePct"`
	// Wakeups: activity arrived while the simulated machine was asleep -
	// each one is ~20-60 s of waiting in real life. RemoteWakeups are the
	// ones with no local input in that minute, i.e. the ones that need a
	// wake device (local input wakes the machine by itself).
	Wakeups             int     `json:"wakeups"`
	RemoteWakeups       int     `json:"remoteWakeups"`
	WakeupsPerDay       float64 `json:"wakeupsPerDay"`
	RemoteWakeupsPerDay float64 `json:"remoteWakeupsPerDay"`
}

// Stats summarises a set of durations, in seconds.
type Stats struct {
	Count     int     `json:"count"`
	MedianSec float64 `json:"medianSec"`
	P90Sec    float64 `json:"p90Sec"`
	MeanSec   float64 `json:"meanSec"`
	TotalSec  float64 `json:"totalSec"`
}

// Simulate runs the sleep model over recorded minutes (sorted by T).
//
// Model, per idle limit L: the machine is awake in an observed minute if
// some activity happened in it or within the L minutes before it;
// otherwise it's asleep. Activity in a minute where the machine would be
// asleep is a wake-up. The machine is assumed awake at the first observed
// minute. Unobserved minutes (runner down) are skipped, not counted as
// asleep - they're outside what was measured.
func Simulate(mins []Minute, turns []Turn, from, to time.Time, limits []int, loc *time.Location) Report {
	rep := Report{From: from, To: to, BySource: map[string]int{"turn": 0, "app": 0, "local": 0}}

	spanStart := from
	if len(mins) > 0 {
		if first := time.Unix(mins[0].T, 0); first.After(spanStart) {
			spanStart = first
		}
	}
	rep.SpanMinutes = int(to.Sub(spanStart) / time.Minute)
	rep.ObservedMinutes = len(mins)
	rep.UptimePct = pct(rep.ObservedMinutes, rep.SpanMinutes)

	for _, m := range mins {
		lt := time.Unix(m.T, 0).In(loc)
		wd := (int(lt.Weekday()) + 6) % 7
		rep.HeatmapObserved[wd][lt.Hour()]++
		if m.Active == "" {
			continue
		}
		rep.ActiveMinutes++
		rep.HeatmapActive[wd][lt.Hour()]++
		if strings.IndexByte(m.Active, FlagTurn) >= 0 {
			rep.BySource["turn"]++
		}
		if strings.IndexByte(m.Active, FlagApp) >= 0 {
			rep.BySource["app"]++
		}
		if strings.IndexByte(m.Active, FlagLocal) >= 0 {
			rep.BySource["local"]++
		}
	}

	days := float64(rep.ObservedMinutes) / (24 * 60)
	for _, l := range limits {
		rep.Limits = append(rep.Limits, simulateLimit(mins, l, days))
	}

	var durs []float64
	lastEnd := map[string]int64{}
	var lats []float64
	for _, t := range turns {
		durs = append(durs, float64(t.End-t.Start)/1000)
		if e, ok := lastEnd[t.SessionID]; ok && t.Start > e {
			lats = append(lats, float64(t.Start-e)/1000)
		}
		lastEnd[t.SessionID] = t.End
	}
	rep.Turns = stats(durs)
	rep.ReplyLatency = stats(lats)
	return rep
}

func simulateLimit(mins []Minute, limit int, days float64) LimitResult {
	res := LimitResult{IdleMinutes: limit}
	if len(mins) == 0 {
		return res
	}
	lastActive := mins[0].T / 60 // awake at the first observed minute
	awake := 0
	for _, m := range mins {
		idx := m.T / 60
		asleep := idx-lastActive > int64(limit)
		if m.Active != "" {
			if asleep {
				res.Wakeups++
				if strings.IndexByte(m.Active, FlagLocal) < 0 {
					res.RemoteWakeups++
				}
			}
			lastActive = idx
			awake++
		} else if !asleep {
			awake++
		}
	}
	res.AwakePct = pct(awake, len(mins))
	if days > 0 {
		res.WakeupsPerDay = round1(float64(res.Wakeups) / days)
		res.RemoteWakeupsPerDay = round1(float64(res.RemoteWakeups) / days)
	}
	return res
}

func stats(v []float64) Stats {
	if len(v) == 0 {
		return Stats{}
	}
	sort.Float64s(v)
	total := 0.0
	for _, x := range v {
		total += x
	}
	return Stats{
		Count:     len(v),
		MedianSec: round1(quantile(v, 0.5)),
		P90Sec:    round1(quantile(v, 0.9)),
		MeanSec:   round1(total / float64(len(v))),
		TotalSec:  round1(total),
	}
}

// quantile uses nearest-rank on sorted v.
func quantile(v []float64, q float64) float64 {
	i := int(math.Ceil(q*float64(len(v)))) - 1
	if i < 0 {
		i = 0
	}
	return v[i]
}

func pct(n, d int) float64 {
	if d <= 0 {
		return 0
	}
	p := 100 * float64(n) / float64(d)
	if p > 100 {
		p = 100
	}
	return round1(p)
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
