package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC) // a Monday

// minutes builds n observed minutes from t0, with active set at the given
// offsets (minute index -> flags).
func minutes(n int, active map[int]string) []Minute {
	out := make([]Minute, n)
	for i := range out {
		out[i] = Minute{T: t0.Add(time.Duration(i) * time.Minute).Unix(), Active: active[i]}
	}
	return out
}

func TestSimulateAlwaysActiveIsAlwaysAwake(t *testing.T) {
	act := map[int]string{}
	for i := 0; i < 120; i++ {
		act[i] = "l"
	}
	rep := Simulate(minutes(120, act), nil, t0, t0.Add(2*time.Hour), []int{5}, time.UTC)
	l := rep.Limits[0]
	if l.AwakePct != 100 || l.Wakeups != 0 {
		t.Fatalf("got %+v, want 100%% awake, 0 wakeups", l)
	}
}

func TestSimulateIdleSleepsAndWakes(t *testing.T) {
	// 100 minutes: activity at 0, and app activity at 50.
	mins := minutes(100, map[int]string{0: "a", 50: "a"})
	rep := Simulate(mins, nil, t0, t0.Add(100*time.Minute), []int{5, 60}, time.UTC)

	five := rep.Limits[0]
	// Awake: minute 0 + 5 idle after, minute 50 + 5 idle after = 12.
	if five.AwakePct != 12 {
		t.Errorf("5-min awake = %v%%, want 12%%", five.AwakePct)
	}
	if five.Wakeups != 1 || five.RemoteWakeups != 1 {
		t.Errorf("5-min wakeups = %d/%d remote, want 1/1", five.Wakeups, five.RemoteWakeups)
	}

	sixty := rep.Limits[1]
	// Never asleep across the 50-minute gap: awake 0..110 clipped to 100.
	if sixty.AwakePct != 100 || sixty.Wakeups != 0 {
		t.Errorf("60-min = %+v, want 100%% awake, 0 wakeups", sixty)
	}
}

func TestSimulateLocalWakeupIsNotRemote(t *testing.T) {
	mins := minutes(30, map[int]string{0: "l", 20: "l"})
	l := Simulate(mins, nil, t0, t0.Add(30*time.Minute), []int{5}, time.UTC).Limits[0]
	if l.Wakeups != 1 || l.RemoteWakeups != 0 {
		t.Fatalf("got %d wakeups / %d remote, want 1 / 0", l.Wakeups, l.RemoteWakeups)
	}
}

func TestSimulateBoundaryExactlyAtLimitStaysAwake(t *testing.T) {
	// Activity at 0 and 5 with a 5-minute limit: minute 5 is still awake.
	mins := minutes(6, map[int]string{0: "a", 5: "a"})
	l := Simulate(mins, nil, t0, t0.Add(6*time.Minute), []int{5}, time.UTC).Limits[0]
	if l.Wakeups != 0 || l.AwakePct != 100 {
		t.Fatalf("got %+v, want no wakeup", l)
	}
}

func TestSimulateUptimeAndGaps(t *testing.T) {
	// 60 observed minutes in a 120-minute span; the 60-minute gap (runner
	// down) is neither awake nor asleep.
	mins := minutes(60, map[int]string{0: "t"})
	from := t0.Add(-24 * time.Hour) // window starts before the recorder did
	rep := Simulate(mins, nil, from, t0.Add(120*time.Minute), []int{5}, time.UTC)
	if rep.SpanMinutes != 120 || rep.UptimePct != 50 {
		t.Fatalf("span=%d uptime=%v, want 120, 50", rep.SpanMinutes, rep.UptimePct)
	}
	if rep.BySource["turn"] != 1 || rep.ActiveMinutes != 1 {
		t.Fatalf("bySource=%v active=%d", rep.BySource, rep.ActiveMinutes)
	}
}

func TestSimulateHeatmapUsesLocalZone(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*3600)
	mins := minutes(1, map[int]string{0: "a"}) // Mon 09:00 UTC = Mon 11:00 local
	rep := Simulate(mins, nil, t0, t0.Add(time.Minute), nil, loc)
	if rep.HeatmapActive[0][11] != 1 || rep.HeatmapObserved[0][11] != 1 {
		t.Fatalf("heatmap cell Mon 11:00 not set")
	}
}

func TestSimulateTurnsAndReplyLatency(t *testing.T) {
	ms := func(d time.Duration) int64 { return t0.Add(d).UnixMilli() }
	turns := []Turn{
		{SessionID: "a", Start: ms(0), End: ms(60 * time.Second)},
		{SessionID: "b", Start: ms(10 * time.Second), End: ms(20 * time.Second)},
		{SessionID: "a", Start: ms(90 * time.Second), End: ms(120 * time.Second)},
	}
	rep := Simulate(nil, turns, t0, t0.Add(time.Hour), nil, time.UTC)
	if rep.Turns.Count != 3 || rep.Turns.TotalSec != 100 || rep.Turns.MedianSec != 30 {
		t.Errorf("turns = %+v", rep.Turns)
	}
	// Only session a has a follow-up: 90s start - 60s previous end = 30s.
	if rep.ReplyLatency.Count != 1 || rep.ReplyLatency.MedianSec != 30 {
		t.Errorf("latency = %+v", rep.ReplyLatency)
	}
}

func TestRecorderRoundTripAndPrune(t *testing.T) {
	dir := t.TempDir()
	now := t0.Add(90 * time.Second)
	r := &Recorder{
		Dir:       dir,
		now:       func() time.Time { return now },
		Busy:      func() bool { return false },
		AppLast:   func() time.Time { return t0.Add(10 * time.Second) },
		LocalIdle: func() (time.Duration, error) { return time.Hour, nil },
	}
	r.RecordTurn("s1", t0, t0.Add(20*time.Second))
	r.sample(t0, t0.Add(time.Minute))

	mins, turns, err := Load(dir, t0.Add(-time.Hour), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(mins) != 1 || mins[0].Active != "ta" {
		t.Fatalf("minutes = %+v, want one with flags ta", mins)
	}
	if len(turns) != 1 || turns[0].SessionID != "s1" {
		t.Fatalf("turns = %+v", turns)
	}

	old := filepath.Join(dir, t0.AddDate(0, 0, -RetentionDays-1).Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Prune(dir, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old day file not pruned")
	}
	if _, err := os.Stat(dayFile(dir, t0)); err != nil {
		t.Fatalf("current day file pruned: %v", err)
	}
}
