# Schedule (server sleep/wake bookings)

Files: types.go, expand.go, store.go, service.go (HTTP: ../api/schedule.go; wiring: ../../cmd/runnerd/main.go)

Bookings made in the phone app say when the server should be awake and when it sleeps inside that
block. This package stores them, expands them into dated `warn|sleep|wake` events and writes the
plan file. The root applier that turns the plan into systemd timers is `power/server/FLOWS.md`.
API contract: shared/API.md "Schedule" + "Schedule plan file".

## Model (types.go)

`Booking` = `Date` + `Day` (awake block `Start`..`End`, `Sleeps []Gap{From,To}`) + optional
`Repeat` + `Exceptions`.
- `Repeat == nil` → one-off on `Date`. Else a series starting `Date`: `Freq`
  (`daily|weekly|monthly|yearly`), `Interval` ≥ 1, `Weekdays` (weekly only, ISO 1=Mon..7=Sun,
  empty = `Date`'s weekday), `Until` (inclusive) **or** `Count`, neither = forever.
- `Exception{Date, Cancelled, Override *Day}` changes one occurrence, keyed by its **original** date.
- `Occurrence` = one concrete day after expansion (`Recurring`, `Edited` flags).
- `Date`/`Clock` are plain strings (`YYYY-MM-DD`, `HH:MM`), no zone - wall clock in `time.Local`.

Constants: `PlanHorizonDays=14`, `WarnMinutes=5`, `MinGapMinutes=10`, `OverlapCheckDays=366`.

## Validation (expand.go)

`ValidateDay(d)`: valid clocks → `Start < End` → per sleep in order: `From < To`, inside the
block, ≥ 10 min long, not before/overlapping the previous sleep, ≥ 10 awake min after it (else
its warn would fire while still asleep).
`Validate(b)`: real date → `ValidateDay` → repeat: known freq, interval ≥ 1, weekdays only for
weekly / 1..7 / no dupes, `until` XOR `count`, `until ≥ date`, `count ≥ 1` → each exception's date
parses + its override passes `ValidateDay`.
To change rules: `ValidateDay()` / `Validate()`; keep `BookingEditorLogic.validateForm()` (Android) in step.

## Expansion (expand.go)

`Expand(bookings, from, to)` → per booking: `Validate` (any bad booking fails the whole call) →
`seriesDates()` yields original dates → drop `< from` → apply exception (cancelled = skip,
override = swap `Day`, `Edited=true`) → sort by date, start.
- Date arithmetic = day numbers in UTC (`parseDate`, `dayTime`), so DST never shifts a date.
- Daily/weekly skip whole periods before `from` (`hint`) when there's no `Count`.
- **Weekly**: weeks counted from `Date`'s Monday every `Interval` weeks; days before `Date` in
  the first week are skipped (not counted).
- **Monthly**: same day-of-month; a shorter month uses its last day (31st → 30 Apr, 28/29 Feb).
- **Yearly**: same month/day; Feb 29 → Feb 28 in non-leap years.
- **Count includes cancelled occurrences** (counted on original dates, exceptions applied after).
`IsOccurrence(b, d)` = is `d` an original date (ignores exceptions).

`Overlaps(b, others, today)` → expand `b` and all others (minus same ID) over
today..today+366 → same date and awake blocks intersect → error. Touching ends are fine.
Past days aren't checked; an overlap first occurring > 366 days out is missed.

## Plan events (expand.go)

`Service.events()` → `Expand(today .. today+15)` → `PlanEvents(occs, now)` → per sleep:
`warn` at From−5, `sleep` at From, `wake` at To → keep only `now < t < now+14d` (so a warn
already in the past is dropped while its sleep still fires) → sort by time, ties warn→sleep→wake
→ `FormatPlan` = `# relay schedule plan, written <RFC3339>` + `<kind> YYYY-MM-DD HH:MM` lines.
Dates/clocks are read in `now.Location()` (= `time.Local`).
To change lead time / horizon: `WarnMinutes` / `PlanHorizonDays` (types.go).

## Plan writing (service.go)

`NewService(store)` → `PlanPath` = env `RELAY_SCHEDULE_PLAN` or `/var/lib/relay/schedule-plan`;
`StatusPath` = `schedule-applied` next to it.
`writePlan()` (caller holds `mu`) → plan dir missing? → **no-op** (applier not installed) →
compute events → event lines identical to the current file (comment ignored)? → **no-op** (no
needless timer restart) → temp file in same dir → chmod 0644 → `os.Rename` (atomic).
Errors are only logged (`writePlanLogged`); the HTTP call still succeeds.

Triggers: every mutation (Create / UpdateSeries / DeleteSeries / UpdateOccurrence /
CancelOccurrence) → `Start()`: once at startup, then daily 00:05 local (the 14-day window rolls).

## Applied status (service.go)

`Schedule()` → bookings + `events()` + `Timezone` (`localZoneName()`: `$TZ` → `/etc/localtime`
symlink → `time.Local`) → plan file exists? → `planWrittenAt` = its mtime → status file line 1 ==
sha256(plan)? → `applied=true`, line 2 → `appliedAt`. No plan file → both null, `applied=false`.

## Service operations → HTTP (service.go, ../api/schedule.go)

| Call | Service | Notes |
|---|---|---|
| `GET /v1/schedule` | `Schedule()` | |
| `GET /v1/schedule/occurrences?from&to` | `Occurrences()` | span ≤ 62 days, `to ≥ from` |
| `POST /v1/schedule/bookings` | `Create()` | 201; `check()` = validate + overlap |
| `PUT /v1/schedule/bookings/{id}` | `UpdateSeries()` | keeps exceptions still on an occurrence date of the new rule, drops the rest |
| `DELETE /v1/schedule/bookings/{id}` | `DeleteSeries()` | 204, exceptions cascade |
| `PUT …/{id}/occurrences/{date}` | `UpdateOccurrence()` | upserts an override, re-checks overlap |
| `DELETE …/{id}/occurrences/{date}` | `CancelOccurrence()` | series → cancelled exception (200 Booking); one-off → booking deleted (204) |

Error kinds (`kindError`, test with `errors.Is`) → `writeScheduleError`: `ErrNotFound` → 404
(also: date isn't an occurrence), `ErrInvalid` → 400, `ErrOverlap` → 409, anything else → 500.
Bad JSON body → 400. `Server.Schedule == nil` → 503 (`scheduleOn`).
All JSON lists are `[]`, never `null` (`norm`, `copyDay`).

## Store (store.go)

`Open($RELAY_HOME/relay.db)` → `MkdirAll` → `modernc.org/sqlite` with `journal_mode(WAL)`,
`foreign_keys(1)`, `busy_timeout(5000)`, `SetMaxOpenConns(1)` → `migrate()`.
Tables: `bookings` (sleeps/repeat as JSON text, `"start"`/`"end"` quoted - `END` is an SQL
keyword) and `exceptions` (PK booking_id+date, `ON DELETE CASCADE`).
`Create` (new 16-hex random ID, server timestamps) / `Update` (replaces row + **whole** exception
set) / `Delete` / `SetException` (upsert, bumps `updated_at`) each run in one transaction.
Migrations: `migrations[i]` takes `PRAGMA user_version` i → i+1, each in a transaction.
To change schema: **append** to `migrations`, never edit a shipped step.

## Technology Notes

- **One mutex for everything** (`Service.mu`): every mutation + its plan rewrite is serialized,
  so the plan always matches a consistent booking set. `Occurrences()` reads without it (fine:
  each Store call is its own read). Throughput is irrelevant at one user.
- **No app-wide context.** main.go passes `context.Background()` to `Start()`; the 00:05 goroutine
  only dies with the process. Restarting the runner rewrites the plan anyway.
- **Daily timer is a Go timer on the monotonic clock**, which on Linux doesn't advance during
  suspend. After a day with sleeps the 00:05 rewrite fires late by the time spent asleep. Harmless
  (14-day horizon), but don't rely on it being on time.
- **Store failure only disables the feature.** Open/migrate error → logged
  `schedule disabled: …` → `Server.Schedule` stays nil → `/v1/schedule*` = 503; the rest of the
  runner runs. A DB written by a **newer** runner (`user_version` > known) also fails Open.
- **One bad stored booking breaks everything.** `Expand` validates each booking and fails the whole
  call → `GET /v1/schedule` 500 and plan writes fail (old plan stays). Only possible via direct DB
  edits or a validation rule tightened later.
- **SQLite: `modernc.org/sqlite` v1.36.1, pinned** - pure Go (runner stays `CGO_ENABLED=0`); newer
  releases need a newer Go than the module's `go 1.22`. WAL leaves `relay.db-wal`/`-shm` next to
  the DB; copy all three when backing up while running. Single connection → no concurrent writers.
- **Timezone = runner's `time.Local`.** Changing the server's zone shifts every booking. A clock
  inside a DST spring-forward gap is normalized by `time.Date` (moves by an hour; direction not
  guaranteed). `Timezone` may be the useless `"Local"` if neither `$TZ` nor `/etc/localtime` resolve.
- **Hardcoded `/var/lib/relay/schedule-plan`** must match `PLAN` in
  `power/server/relay-schedule-apply.sh` and the `.path` unit; override only with `RELAY_SCHEDULE_PLAN`.
- **Missing plan dir = silent no-op.** Windows runners (and Linux without `install.sh`) store and
  show bookings, but nothing ever applies them; the app just shows no "Applied" line.
- **Overlap check is forward-only** (today..+366): edits never complain about past days, and a
  yearly clash beyond a year isn't caught.
- **`applied` trusts the status file**, which the runner user can also write (see power/server).

## Change Index

| Thing | Where |
|---|---|
| Booking / occurrence / event JSON shape | `types.go` (keep shared/API.md + Android `model/ScheduleModels.kt` in step) |
| Warn lead, plan horizon, min sleep/awake gap, overlap window | `types.go` consts |
| Validation rules | `expand.go` `ValidateDay`, `Validate` |
| Repeat expansion (month-end, Feb 29, count) | `expand.go` `seriesDates` |
| Exceptions applied | `expand.go` `Expand` |
| Overlap rule | `expand.go` `Overlaps` |
| Plan events / file format | `expand.go` `PlanEvents`, `FormatPlan` |
| Plan path | env `RELAY_SCHEDULE_PLAN`, `service.go` `DefaultPlanPath` |
| Plan rewrite / skip rules | `service.go` `writePlan`, `eventLines` |
| Daily rollover time (00:05) | `service.go` `Start` |
| Applied detection | `service.go` `Schedule` |
| Reported timezone | `service.go` `localZoneName` |
| Keep/drop exceptions on series edit | `service.go` `UpdateSeries` |
| One-off cancel = delete | `service.go` `CancelOccurrence` |
| Occurrences span limit (62 d) | `service.go` `Occurrences` |
| Error kind → HTTP status | `../api/schedule.go` `writeScheduleError`, `scheduleOn` |
| Routes | `../api/api.go` `Routes()` |
| DB path | `../../cmd/runnerd/main.go` (`$RELAY_HOME/relay.db`) |
| Schema / migrations | `store.go` `migrations`, `migrate` |
| SQLite pragmas / driver version | `store.go` `Open`, `runner/go.mod` |
