# Usage recording & sleep simulation

Files: store.go, recorder.go, simulate.go, usage_test.go (wired in cmd/runnerd/main.go, served by api.go `handleUsage`)

Answers "would idle-suspend actually save anything, and would a wake device (Pi / old phone /
router WoL) pay for itself?" from real usage — while the machine stays on. Nothing here ever
suspends.

## Record flow (always on)

```
main() → usage.NewRecorder($RELAY_HOME/usage) → Prune(>90 days) → go Recorder.Run()
Recorder.Run() → sleep to next wall-clock minute → sample(minute just finished)
  → turn  : Recorder.turnSeen (a turn ended this minute) || Busy()   [session.Manager.IdleStatus]
  → app   : AppLast() ≥ minute start                                  [activity.Tracker, POST /v1/activity]
  → local : LocalIdle() ≤ time since minute start                     [activity.LocalIdleTime, Windows only]
  → appendLine({"k":"m","t":<unix>,"a":"tal"}) → $RELAY_HOME/usage/YYYY-MM-DD.jsonl (UTC date)
session.runTurn() end → Manager.OnTurn → Recorder.RecordTurn
  → appendLine({"k":"t","sid":…,"s":<ms>,"e":<ms>}) + turnSeen = true
```

A minute with `"a"` absent = runner up, nobody using it. A minute with no line at all = runner
not running (or the machine was asleep) — Report counts that as downtime, not as sleep.
Day change → Prune again.

To change retention: `usage.RetentionDays`. To change what counts as activity: `Recorder.sample()`.
To change where files go: env `RELAY_HOME` (config.Load).

## Report flow

```
GET /v1/usage?days=14&limits=5,15,30,60 → api.handleUsage → Recorder.Report(days, limits)
  → Load(dir, now-days, now)  (skips corrupt lines)
  → Simulate(minutes, turns, from, to, limits, time.Local) → UsageReport (shared/API.md)
```

Simulation model, per idle limit L (`simulateLimit`):
`awake(m) = m had activity || m − lastActive ≤ L`; activity while `m − lastActive > L` = **wake-up**;
a wake-up with no local input that minute = **remote wake-up** (the only kind that needs a wake
device — a keypress wakes the machine by itself). Assumed awake at the first observed minute.

- `uptimePct` = observed minutes / minutes since the first recorded one (or `from`).
- Heatmap = active / observed minutes per [weekday Mon=0][hour], runner's local zone.
- Reply latency = next turn start − previous turn end, same session.

To change defaults: `usage.DefaultLimits`, `handleUsage` (days default 14, max 90).

## Technology Notes

- **Storage**: plain JSONL, one file per UTC day, ~1440 lines/day (~40 KB) → ~4 MB at 90 days.
  Each sample opens/appends/closes the file — no buffering, so a crash loses at most the line
  being written; a torn line is skipped by Load.
- **Minute resolution**: activity is binary per minute. A 5-second ping and a full minute of typing
  look the same. Fine for 5+ minute limits; meaningless for sub-minute ones.
- **Wall clock**: minutes are aligned to wall time, so a clock jump (NTP, DST is fine — unix time)
  can duplicate or skip one minute. The `last` guard stops double-recording the same minute.
- **App signal depends on the app**: `app` is only real while the phone is pinging POST
  /v1/activity every 30s in the foreground (android `ActivityPinger`). Old app builds → no `app` flag.
- **Linux has no local-input signal** (`activity.ErrUnsupported`) → every wake-up on the Acer
  server counts as remote, which is correct for a headless box.
- **Time zone**: the heatmap uses the runner's `time.Local`; a runner on UTC (systemd service with
  no TZ) shifts the heatmap. Set `TZ` in the service env if it looks off.
- **Model simplification**: the sim assumes wake-up is instant and that activity would still have
  happened. Real wake costs ~20–60 s and may discourage some usage.

## Change Index

| What | Where |
|---|---|
| Activity sources sampled | `Recorder.sample()`, wiring in `cmd/runnerd/main.go` |
| Turn spans | `session.Manager.OnTurn` (set in `acp_session.go runTurn`) → `Recorder.RecordTurn` |
| File format / location | `store.go line`, `dayFile()`, env `RELAY_HOME` |
| Retention | `RetentionDays` |
| Sleep model | `simulateLimit()` |
| Default limits / days | `DefaultLimits`, `api.handleUsage` |
| Response shape | `Report` in simulate.go, shared/API.md `UsageReport` |
