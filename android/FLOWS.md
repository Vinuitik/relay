# Android app flows

Files: MainActivity.kt, RelayNavHost.kt, HomeScreen.kt, HomeViewModel.kt, ProjectScreen.kt,
ProjectViewModel.kt, RunnerListScreen.kt, QrScanScreen.kt, ChatScreen.kt, ChatViewModel.kt, MarkdownText.kt, Theme.kt,
SignInSheet.kt (SignInViewModel, SignInsSheet), UsageScreen.kt (UsageViewModel.kt),
FileBrowserScreen.kt (FileBrowserViewModel + FileBrowserContent), ContainersScreen.kt
(ContainersViewModel + ContainersContent), FolderPickerScreen.kt, ScreenStates.kt,
KnownRunnersRepository.kt, AppPrefsRepository.kt, RelayApiClient.kt, RelayApiService.kt,
RelayFirebaseMessagingService.kt, RegisterDeviceWorker.kt, ScheduleRepository.kt, ScheduleModels.kt,
ScheduleScreen.kt, ScheduleViewModel.kt, ScheduleLayout.kt, BookingEditorScreen.kt,
BookingEditorViewModel.kt, BookingEditorLogic.kt, TodaySchedule.kt, ReminderSettingsDialog.kt,
ReminderLogic.kt, ReminderPrefs.kt, ReminderReceiver.kt (+ ReminderRescheduleReceiver), ReminderScheduler.kt

## Scope

The app is deliberately down to one core loop: **pair a runner → Home (projects + Needs you) →
Project (Chats | Files | Git | Containers) → chat**, plus a read-only file browser, a Git tab and FCM push. A 2026-09-20 pass deleted everything built
ahead of that loop — home-screen widget, uptime dashboard, Room offline cache, Wake-on-LAN
config, generated friendly names, in-app update prompt, bulk container start/stop, and the
old auth-login relay (rebuilt 2026-10-04 as a sign-in sheet - see "Sign-in relay"). See
"Deleted 2026-09-20" at the bottom for what's gone and why, so nobody goes looking for it.

## Navigation

Design source: DESIGN.md "Navigation & flow". Routes in `RelayNavHost.Routes`:

| Route | Screen | Notes |
|---|---|---|
| `home` | HomeScreen | current runner = `AppPrefsRepository.currentRunner` (fallback: first runner) |
| `runners` | RunnerListScreen ("Manage runners") | start destination when no runners exist |
| `r/{host}/p/{projectId}?tab={chats\|files\|git\|containers}` | ProjectScreen | tab arg = initial tab |
| `r/{host}/p/{projectId}/s/{sessionId}` | ChatScreen | deep link `relay://r/{host}/p/{projectId}/s/{sessionId}` |
| `r/{host}/pick-folder` | FolderPickerScreen | |
| `r/{host}/usage` | UsageScreen | |
| `r/{host}/schedule` | ScheduleScreen | week grid; re-fetches when the editor sets `SCHEDULE_CHANGED` on its savedStateHandle |
| `r/{host}/schedule/edit?bookingId={id}&date={date}` | BookingEditorScreen | `date` only = new, both = one occurrence, `bookingId` only = whole booking (`Routes.scheduleEdit`) |

MainActivity → RelayNavHost → waits for first DataStore read (runners, lastRoute, currentRunner)
→ start = `home` (or `runners` if none) → cold launch w/o deep link: restore last route
(`project`, then `chat` pushed on top) → Home ⇄ Project → Chat.

- Home: top bar = wordmark "Relay" (Barlow SemiBold) + lamp (lit amber while any `needsYou`
  session is `waiting`, else dark) over the runner switcher (online lamp + label ▾ → runners with
  health lamps + "Manage runners"; press = scale 0.97, `pressScale`). One runner → label only,
  "Manage runners" in ⋮. No projects → one line + "Add project" button (opens the add sheet). Body: "Needs you" (`listAllSessions("waiting,busy")`,
  tap → Chat) then Projects. Row = name · path · containers lamp (lit = something up; only when a
  compose file exists) · ⋮ (New chat, Files, Containers, Containers up/down). Several compose
  files + nothing up → "Containers up…" opens the Containers tab to pick one.
  ExtendedFAB "Add project" → ModalBottomSheet: Register existing folder → `pick-folder`;
  New empty project → name dialog → `POST /v1/projects {name}`.
  To change: HomeScreen.kt (`HomeList`, `ProjectRow`, `RunnerSwitcher`, `Wordmark`).
- Project: title = project name, `PrimaryTabRow` Chats | Files | Git | Containers, Crossfade 150ms.
  Chats: rows = `Session.displayTitle` (title, else provider) · relative time + preview · StatusChip,
  sorted waiting first then newest `activityAt`. ExtendedFAB "New chat" → create with
  `lastProvider` → Chat; long-press → provider list (`AppPrefsRepository.KNOWN_PROVIDERS`).
  Empty → inline composer: first message = `createSession` + `sendMessage` → Chat.
  Below the chats, "On this computer": Claude chats for this folder not open in Relay (VS Code, CLI) -
  `ProjectViewModel.refresh` → `listAgentChats` (in parallel, never blocks the list; failures = no
  section) → rows minus ones with `sessionId`. Tap → `continueLaptopChat` →
  `createSession {provider:"claude", agentSessionId}` (runner replays the history, a few s) → Chat.
  Opening any chat re-reads it, so turns typed in VS Code since show up (runner-side, see
  runner/FLOWS.md "Shared chats").
  Files tab = `FileBrowserContent`, Git tab = `GitContent`, Containers tab = `ContainersContent` (no own top bar).
  To change: ProjectScreen.kt (`ChatsContent`, `LaptopChatRow`, `NewChatFab`, `FirstMessageComposer`),
  ProjectViewModel.kt (`laptopChats`, `continueLaptopChat`).
- Manage runners: tap row → becomes current runner → Home. Row ⋮ = Rename / Usage / Sign-ins / Sleep / Remove
  (Sleep + Remove confirmed). ExtendedFAB "Pair runner" → QR (Crossfade 200ms, same screen).
  No runners → `EmptyState` line + "Pair runner" button.
- Leaving a runner-scoped route for an unknown host → "Unknown runner" placeholder.
- Back from Project/Chat with nothing under it (deep link/restore) → `goHome()`.

Transitions (NavHost level, `RelayNavHost.kt` `forwardEnter/forwardExit/backEnter/backExit`):
forward = slide 10% + fade (200ms after 60ms), 280ms EaseOut, old fades out 100ms; back =
slide-out 10% 240ms + fade 200ms, revealed screen fades in 200ms. Tabs: Crossfade 150ms. QR: fade 200ms.

ContainersContent → status card (lamp + "Up · N of M running" / "Everything is down" /
"Starting… 0:42" = in-flight operation + elapsed time, "from <file>", progress bar in a fixed 4dp
slot, then request/`lastError`/`dockerError` in red) → one card per compose file ("Compose files"
header only when there are several), each with its own button:
- that file's stack is up → **Down** (`stopContainers`)
- another file's stack is up → **Switch here** (`startContainers(file)`; runner downs the old one first)
- nothing up → **Up** (`startContainers(file)`)
→ Services list (lamp, service, file, state; rows `key`ed) → 24dp gap → full-width outlined
error-colour **Take everything down** → confirm AlertDialog → LongPress haptic + `stopContainers`
(enabled whenever no operation is in flight - compose down is idempotent).
No compose file → one line naming the expected `docker-compose.yml`.
Motion: lamp/card/state colours `animateColorAsState` 250ms EaseOut; status title AnimatedContent
fade 150 in (+4dp slide) / 100 out; Up/Switch/Down button Crossfade 150ms; bar alpha 200ms.
Elapsed time = `ContainersViewModel.opStartedAt` (`SystemClock.elapsedRealtime()` when an operation
is first seen; cleared when `operation` goes empty) - client-side, so opening the tab mid-operation
counts from then, not from when the runner started.
"Which file is up" = the `composeFile` of the first running container, falling back to
`activeFile` - truer than `activeFile` alone if someone ran compose by hand on the runner.
Runner answers `202` immediately → tab polls every 2s while `operation` is non-empty, 10s
otherwise - only while the tab is composed (`ContainersViewModel.pollWhileVisible()`).
To change the labels/polling/button rules: ContainersScreen.kt (`ComposeFileCard`, `StatusCard`, `Elapsed`).

To add a screen: RelayNavHost.kt (`Routes` + a `composable(...)`)

## Data flow

RunnerListScreen ↔ KnownRunnersRepository (DataStore Preferences) — `hostname` + `port` + `key`
+ optional `displayName` per runner, one JSON blob under a single prefs key. `hostname` is the
de-facto id: `addRunner`/`updateRunner` upsert by it and `removeRunner` deletes by it. Added via
QR scan by default (see "Pairing"); manual entry is the fallback.

AppPrefsRepository (separate DataStore file `app_prefs`): `currentRunner` (Home's runner; set by
the switcher, Manage runners tap, and on entering any project/chat route - `KeepCurrentRunner`),
`lastRoute` (written by a NavController destination listener for home/project/chat only),
`lastProvider` (written on every New chat / first message).

Screen state lives in ViewModels scoped to the NavBackStackEntry (`viewModel()` inside each
`composable {}`), built via `viewModelFactory { initializer { … } }` with the runner + ids:

| ViewModel | Holds | Load |
|---|---|---|
| HomeViewModel | runner, projects, needsYou, containers map, online map, refreshing, error | `selectRunner()` each time Home composes; containers per row in parallel after the list |
| ProjectViewModel | project (name), sessions, refreshing, error, creating, missing | init + `refreshIfLoaded()` on return |
| FileBrowserViewModel | pathSegments, entries, viewingFile, fileText, errors | init + per directory |
| ContainersViewModel | status, error, opStartedAt | polled while the Containers tab is visible |
| GitViewModel | status, log, branches, loadError, actionError, busy, message, diff | polled while the Git tab is visible |
| ChatViewModel | session, loadError, projectName, input, sending, pendingText, events (Snackbar) | init → poll loop; `listProjects` once for the subtitle |

Rules (DESIGN.md "States"): `null` data = never loaded → `SkeletonRows`; error with no data →
`FullScreenError` (cause + "Is Tailscale on? Is the runner awake?" + Retry); refresh with data →
content stays + 2dp `LinearProgressIndicator` (`RefreshableBox`); pull-to-refresh on every list.
Home with stale data + failed refresh → "Runner offline" banner + rows at 50% alpha.
Action errors (new chat, create project, containers up/down, register folder) → Snackbar.
Empty lists → `EmptyState(text, actionLabel, onAction)`: one line saying what to do + the primary
action Button (Manage runners: Pair runner; Files: Refresh at root / Go up in a subfolder). Home and
the folder picker render the same shape inline in their LazyColumn. No "Error:" prefixes.

Every call: `RelayApiClient.forRunner(runner)` → `RelayApiService` (Retrofit+OkHttp+Moshi) →
`X-Relay-Key` header on every call → runner's `shared/API.md` v1 endpoints. **Network only — no
local cache of sessions/messages**; ViewModels only keep the last answer while their back-stack
entry lives.

ChatViewModel polls `GET /v1/sessions/{id}` every 1s while `busy`, 3s while `waiting`, and stops
when idle — no WebSocket/SSE; "streaming" is the runner merging text chunks into the transcript
between polls. Poll error with no data → `FullScreenError`; with data → polling stops + Snackbar Retry.
- Top bar: title `Session.displayTitle`, subtitle `project name · mode`; mode picker (`current ▾`,
  ACP only) → `setMode`; ⋮ Files → `Routes.project(…, tab=files)` pushed on top (Back returns to chat).
- Transcript (`Transcript`, LazyColumn keyed by index): `user` → right `primaryContainer` bubble,
  `agent` → `MarkdownText` on surface (no bubble), `tool` → `ToolRow` (codeSmall, check / error /
  spinner icon). 4dp between consecutive tool rows, 8 otherwise, 16 gutter.
- Agent message with `kind` `quota`/`auth` → `ProblemCard` (errorContainer + amber interlock bar,
  provider's own text carries the reset time). `auth` → "Sign in again" Button → see "Sign-in relay". FCM `session_finished` with `problem` → push.
- `pendingPermission` → `StickyPermission` above the composer (not a list item): warning chip
  colours + interlock bar, allow* = Button, reject* = OutlinedButton (error) → `answerPermission`.
  FCM `session_needs_input` → "Agent needs your approval" notification.
- Send → `ChatViewModel.send()`: input cleared + optimistic bubble at 60% alpha (`pendingText`) →
  `sendMessage` → poll; first `user` message past the send point = echo → bubble 100%. Failure →
  text restored to input + Snackbar Retry. Idle with no echo after 10 ticks → bubble dropped.
- Stop (same 48dp slot as Send while busy/waiting) → `cancelTurn` → turn stops, session stays.
- Motion (DESIGN.md): items appended after first load enter 8dp up + fade 180ms (`AppearOnce`);
  follow-bottom only if the user was at the bottom (stick = non-animated `scrollBy`, new item = one
  `animateScrollToItem`), else "↓ New" pill; 4dp busy-bar slot fades 200ms; Send↔Stop
  `AnimatedContent`; permission card fade+slide 1/6+expand 220 / fade 120+shrink 150.
  Haptics: Send TextHandleMove; Stop, Allow/Deny, permission arriving while watching LongPress.

To change poll intervals: `ChatViewModel.kt` (`POLL_BUSY_MS`, `POLL_WAITING_MS`, `ECHO_WAIT_TICKS`)
To change API base URL scheme (http vs https): `RelayApiClient.kt`
To change the default port: `model/Models.kt` (`KnownRunner.DEFAULT_PORT` — **7777**, matching
the runner's own default; this was wrong at 8080 until 2026-09-20)

## Display name (label a runner instead of showing its raw address)

Files: model/Models.kt (`KnownRunner.displayName`, `.label`), RunnerListScreen.kt
(`NameRunnerDialog`, `AddRunnerDialog`)

`KnownRunner.hostname` is the Tailscale address/IP used to actually connect (e.g.
"100.124.46.7") — a meaningless string of digits to read as a name. `displayName` is a separate,
purely cosmetic field set once at pairing time and never recomputed; `KnownRunner.label`
(`displayName` if set, else `hostname`) is what every screen shows.

Both name dialogs start **empty** with the hostname as placeholder text; leaving the field blank
falls back to the hostname. (There used to be a `FriendlyNameGenerator` producing an "Adjective
Noun" default — deleted; a random name nobody chose is worse than the address.)

`hostname` itself is untouched everywhere it's used as an actual identifier
(`RelayApiClient`'s base URL, `KnownRunnersRepository`'s upsert key) — only display surfaces use
`.label`.

## Pairing (QR scan)

Files: QrScanScreen.kt, RunnerListScreen.kt

RunnerListScreen's "Pair runner" ExtendedFAB → `QrScanScreen` (full-screen, replaces the whole
screen content while open — the `Crossfade(showQrScan)` at the top of `RunnerListScreen`, 200ms
fade) → requests CAMERA permission → CameraX preview + ML Kit on-device barcode
decode → `parseRelayQrContent(rawValue)` → on a valid `relay://host:port?key=...` match:

- **dedup check first** (`runners.find { it.hostname == scanned.hostname }`) — a re-scan of an
  already-known runner just refreshes its `key` in place via `updateRunner` and shows an
  "Already added as X — refreshed" toast. No naming dialog, no re-add flow.
- **new hostname** → `NameRunnerDialog` → confirmed name (or the hostname if left blank) →
  `KnownRunnersRepository.addRunner(...)`.

Unknown query parameters in the QR URI (the runner still emits `mac=` for the wake feature it
once fed) are ignored by `parseRelayQrContent`.

Manual entry is reachable two ways: `AddRunnerDialog` directly, or tapping "Enter key manually
instead" from inside the scan screen (denied camera permission, no camera, or preference).

## Connection error messages

Files: network/RelayApiClient.kt (`friendlyErrorMessage`)

Every screen's network catch block routes its exception through `friendlyErrorMessage(e,
runner)` instead of using `e.message` directly — a raw `ConnectException`/`SocketTimeoutException`
message like "Failed to connect to /100.124.46.7 (port 7777) after 1000ms: ECONNREFUSED" reads
as a slow timeout when it's usually actually a near-instant refusal, and gives no hint what to
check. The friendly version names the runner, its address, and walks through the three real
causes found so far: runner not running, this phone's Tailscale not connected, or — confirmed by
hand once already (2026-09-19, a Windows Firewall auto-generated inbound Block rule on the
runner's Public-profile connection silently dropped every connection from other Tailscale peers
while working fine from the runner's own machine) — a host firewall blocking inbound connections
from other devices specifically.

To change the wording or add another exception type: `network/RelayApiClient.kt`
(`friendlyErrorMessage`).

## Sign-in relay (phone-side CLI logins: Claude, GitHub, Google Cloud)

Files: ui/components/SignInSheet.kt (`SignInSheet` + `SignInViewModel`, `SignInsSheet` +
`SignInsViewModel`), ChatScreen.kt (`ProblemCard`, `signInOpen`), RunnerListScreen.kt
(`signingInRunner`), network/RelayApiService.kt (`authList`/`authStatus`/`authStart`/`authFinish`),
model/Models.kt (`AuthStatus`)

Entries:
- Chat `ProblemCard` kind `auth` → "Sign in again" → `SignInSheet(provider="claude")`, `doneHint`
  "send your message again"; on success the sheet closes + Snackbar.
- Runner row ⋮ → "Sign-ins" → `SignInsSheet` → `GET /v1/auth` → one row per provider: name +
  account / "Not signed in" / "Not installed on this runner" (disabled) + ✓ or "Sign in" → tap →
  `SignInSheet(provider)` on top → closing it re-fetches the list.

`SignInSheet` (ModalBottomSheet; fresh `SignInViewModel` per opening, keyed by a random id)
→ `init` → `POST /v1/auth/{provider}/start` (spinner "Starting sign-in…")
→ `awaiting_code` (claude, gcloud) → `CodeStep`: 1 "Open sign-in page" → `ACTION_VIEW(url)` (no
  browser → link shown as text) → 2 "Paste code" field (Paste icon reads clipboard) → "Finish
  sign-in" → `POST …/finish {code}` → `signed_in` → `GET /v1/auth/{provider}` for the account
→ `awaiting_approval` (github) → `ApprovalStep`: 1 the one-time code, big, + copy icon → 2 "Copy
  code & open page" (copies, then opens github.com/login/device) → "Waiting for approval - this
  finishes by itself." → `pollApproval` polls `GET /v1/auth/{provider}` every 2s (`POLL_MS`;
  transient errors ignored) until `signed_in` / `failed` / anything else (= stopped)
→ `SignedIn(account)` → haptic LongPress → `onSignedIn(account)`; `Failed(message)` + "Try again".

Errors (`SignInViewModel.explain`): 503 → "can't find the <name> CLI" + install/PATH hint;
409 → "sign-in expired before the code arrived"; 404 → runner doesn't know this provider (update
it); 400 on finish → inline field error (no restart); other → runner's `{error}` or
`friendlyErrorMessage`. `SignInsSheet` on an old runner (404 on `GET /v1/auth`) → "update it".

To change wording/error mapping: `SignInViewModel.explain()`; steps/layout: `CodeStep`,
`ApprovalStep`; provider rows: `ProviderRow`. Runner side (recipes, CLI process, URL/code
capture): runner/FLOWS.md "Sign-in relay".

## Runner can't update (banner)

Files: HomeViewModel.kt (`updateStuck`), HomeScreen.kt (`UpdateStuckBanner`), model/Models.kt (`UpdateStatus`)

`HomeViewModel.refresh` → `runnerInfo()` (failure ignored) → `update.stuck` (`failures >= 3`, ~30 min of
failed keeperd checks) → red banner at the top of Home: "Runner can't update · failing since <time>" +
the last error. Cleared on the next refresh after a working check. Server side: runner/FLOWS.md "keeperd".
To change the threshold: `UpdateStatus.stuck`.

## Manual suspend ("Sleep") / remove runner

Files: RunnerListScreen.kt (`suspendRunner`), network/RelayApiService.kt (`suspend()`)

RunnerListScreen row ⋮ → **Sleep** → confirm dialog (explains: refused if busy) →
`RelayApiClient.forRunner(runner).suspend()` called **directly from a coroutine on the screen**
→ every outcome Toasted, because 409/503 are the normal answers to this button, not crashes:

| Response | Toast |
|---|---|
| 2xx | "<label> is going to sleep" |
| 409 | "<label> is busy - a session is still running, nothing was stopped" |
| 503 | "<label> can't sleep on request - it wasn't started with RELAY_IDLE_SUSPEND_ENABLED=true" |
| other HTTP | "<label>: sleep failed (HTTP nnn)" |
| exception | `friendlyErrorMessage(e, runner)` |

This used to go through a `SuspendRunnerWorker` (WorkManager) whose 409/503 handling was a
`Log.w` line — the user tapped Sleep and saw nothing at all. The worker is deleted; the direct
call is both simpler and the only way the result is actually visible.

To change: `ui/screens/RunnerListScreen.kt` (`suspendRunner`).

"Rename" (row ⋮) → `NameRunnerDialog` prefilled with the current display name →
`updateRunner(copy(displayName))`.

"Remove" (row ⋮, error colour) → confirm dialog → `repository.removeRunner(hostname)` —
local-only, the runner itself is untouched; re-adding is just a rescan since the runner's key
never changes.

## Usage (would a wake device pay for itself?)

Files: UsageScreen.kt, UsageViewModel.kt (`savings()`), AppPrefsRepository.kt (`UsageCosts`),
MainActivity.kt (`pingActivityWhileForeground`), RelayApiService.kt (`usage()`, `activity()`)

```
RunnerListScreen row ⋮ → Usage  |  Home runner switcher ▾ → "Usage · <runner>" (or Home ⋮ with one runner)
  → Routes.USAGE "r/{host}/usage"
  → UsageViewModel.refresh() → GET /v1/usage?days=N  (7/14/30/90 chips, default 14)
  → UsageScreen: Measured · If it slept when idle (per limit) · When you use it (heatmap) ·
                 Agent turns · Assumptions
Assumptions field edit → AppPrefsRepository.setUsageCosts(host, …) → flow → £ recomputed locally
```

- £/yr per limit = `(1 − awake%) × 8760 h × (idle W − asleep W) × £/kWh` (`savings()`).
  Verdict (15-min row) also subtracts the wake device's own 24/7 draw → payback months.
- Remote wake-ups = wake-ups with no keyboard/mouse that minute — the only ones a device is for.
- The simulation itself runs on the runner: runner/internal/usage/FLOWS.md.

Foreground ping: `MainActivity.pingActivityWhileForeground` → `repeatOnLifecycle(STARTED)` →
every 30 s `POST /v1/activity` to **every** known runner, failures ignored. Stops on background.
Feeds idle-suspend and the recorder's `app` signal.

To change defaults (5 W idle, 0.5 W asleep, £0.25/kWh, £35.60 device, 1 W device):
`AppPrefsRepository.UsageCosts`. To change ping rate: `MainActivity.ACTIVITY_PING_MS`.

## Schedule (server sleep/wake bookings)

Server side: runner/internal/schedule/FLOWS.md (rules, plan file) → power/server/FLOWS.md (timers).
All dates/times are the **runner's** zone (`Schedule.timezone`, fallback phone zone: `zoneOf()`),
except reminders (phone-local).

### Schedule screen (week grid)

Files: ScheduleScreen.kt, ScheduleViewModel.kt, ScheduleLayout.kt

Entry: Home ⋮ "Schedule" (one runner) / switcher ▾ "Schedule · <runner>" / Today card tap /
Manage runners row ⋮ "Schedule" → `Routes.schedule(host)` = `r/{host}/schedule`.

```
ScheduleViewModel.init → repo.cached(runner).collect → cache → ensureWeek()
                       → refresh() → ScheduleRepository.refresh() → schedule (live) | refreshError
shown week = currentMonday(zone) + weekOffset   (‹ › Today)
  week fully inside cache window? → draw from cache (weekCovered)
  else → live GET /v1/schedule/occurrences Mon..Sun (LiveWeek); cached days show meanwhile
```
- Grid: 7 day columns × 24h, hour height = max(viewport/18, 28dp), opens scrolled to 06:00
  (`VISIBLE_HOURS`, `FIRST_VISIBLE_HOUR`). Block = awake with sleep bands (`blockSegments`);
  now line on today, updated each minute.
- Tap a block → editor for that occurrence; tap empty slot / day header → new booking that day;
  FAB "Book" → new booking today.
- Status line (bottom bar, `scheduleStatusLine`): refresh failed → "Offline · showing saved copy
  from N ago"; else "Applied on server ✓ HH:MM" / "Not applied yet" (only if the runner has a
  plan file) · "next: sleep Tue 09:00" (first `sleep` in `plan`).
- Nothing cached + refresh failed → `FullScreenError`. Week fetch failed → inline "Couldn't load
  this week". Empty week → "No bookings this week · tap a day to book".
- ⋮ "Reminders" → `ReminderSettingsDialog`.
To change grid sizing: `ScheduleScreen.kt` consts. Status text: `ScheduleLayout.scheduleStatusLine()`.

### Booking editor

Files: BookingEditorScreen.kt, BookingEditorViewModel.kt, BookingEditorLogic.kt

Mode (`EditorMode`, from the route): `NEW` (date only) / `OCCURRENCE` (bookingId + date) /
`SERIES` (bookingId only - nothing navigates there yet). An existing booking is loaded via
`repo.refresh(runner).bookings` (no GET-by-id endpoint); occurrence mode prefills from that day's
override if any (`formFromBooking`).
Form: Title · Date (read-only in occurrence mode) · Awake from/until · Sleeps (+ Add sleep =
`proposeSleep`: half the largest free stretch, 10 min..3 h, 5-min aligned) · day preview bar ·
Repeat (Never/Daily/Weekly/Monthly/Yearly, every N, weekday chips, ends never/on date/after N) · Delete.

Save / Delete (`asksScope` = occurrence mode on a series → "This day only" / "The whole series" dialog):

| Situation | Save | Delete |
|---|---|---|
| New | `POST bookings` | - |
| One-off (any mode) | `PUT bookings/{id}` | confirm → `DELETE bookings/{id}` |
| Series day → "This day only" | `PUT …/occurrences/{date}` (Day only) | `DELETE …/occurrences/{date}` (cancel) |
| Series day → "The whole series" | `PUT bookings/{id}` (day's times become the series') | `DELETE bookings/{id}` |
| Series day, title/repeat touched | "This day only" disabled (`seriesOnlyChanges`) | - |

Client validation (`validateForm`) mirrors server `ValidateDay`/`Validate`: end > start; each sleep
inside the block, ≥ 10 min, no overlap, ≥ 10 awake min after the previous; repeat: interval ≥ 1,
weekly needs a weekday, until ≥ date, count ≥ 1 (repeat skipped for "this day only"). Save is
disabled while invalid. Overlaps are server-only: 400/409 → runner's `error` inline above the
form (`serverError`); anything else → Snackbar. Success → `done` → sets `SCHEDULE_CHANGED` → pop.
To change rules: `BookingEditorLogic.validateForm()` and runner `ValidateDay()` together.

### Home "Today" card

Files: HomeScreen.kt (`TodayCard`), HomeViewModel.kt (`scheduleCache`, `refreshSchedule`), TodaySchedule.kt

`HomeViewModel.selectRunner` → `refreshSchedule(r)` (background, failures ignored) →
HomeScreen collects `vm.scheduleCache(runner)` → each minute `todaySummary(cache, now in runner zone)`
→ null (no occurrence today) = no card; else "Today · 08:00–22:00", 24h bar (awake / sleep bands /
now tick) and `nextTransition` ("Sleeps at 09:00 → wakes 12:00", "Awake until …", "Asleep
until …", "Asleep since …"). Tap → Schedule.
Reads the **cache**, so it shows offline / while the server sleeps.

### Reminders

Files: ReminderPrefs.kt, ReminderScheduler.kt, ReminderReceiver.kt, ReminderLogic.kt, ReminderSettingsDialog.kt

```
ReminderScheduler.reschedule() → per kind (MORNING, EVENING): cancel → set alarm at next
  phone-local HH:MM (ReminderLogic.nextTriggerMillis)
→ ReminderReceiver (ACTION_FIRE) → goAsync → reschedule() (arms tomorrow) → enabled?
→ EVENING only: refresh every runner's cache (parallel, 6 s cap)
→ per known runner with a cached occurrence dated today (phone-local date) → notify:
     MORNING "Server booked today" / "Booked today 08:00–22:00 · turn on <label>"
     EVENING "Booking over" / "<label>: done for today, power it down"
```
- Settings (`ReminderPrefs`, DataStore `reminder_prefs`, global, not per runner): enabled
  (default on), morning 06:30, evening 22:00. Every setter calls `reschedule()`. UI: Schedule ⋮ "Reminders".
- Reschedule triggers: app start (`MainActivity` → `rescheduleAsync`), each setter, each fire,
  `ReminderRescheduleReceiver` on BOOT_COMPLETED / TIME_SET / TIMEZONE_CHANGED /
  MY_PACKAGE_REPLACED / exact-alarm permission change.
- Exact vs inexact: `setExactAndAllowWhileIdle` below API 31 or when `canScheduleExactAlarms()`
  ("Alarms & reminders", off by default on 33+); otherwise `setAndAllowWhileIdle` (Doze may delay).
- Notification id = hash(kind + hostname) (re-fire replaces), channel `schedule_reminders`; no
  POST_NOTIFICATIONS → silently skipped.
To change defaults: `ReminderSettings`. Texts: `ReminderLogic.text()`. Refresh cap: `REFRESH_TIMEOUT_MS`.

### ScheduleRepository (cache)

Files: ScheduleRepository.kt, ScheduleModels.kt, RelayApiService.kt (schedule calls)

`refresh(runner)` → `GET /v1/schedule` → window = `cacheWindow(today in runner zone)` = this
week's Monday .. today+13 (14..20 days) → `GET /v1/schedule/occurrences` → write `ScheduleCache`
(timezone, fetchedAt, from, to, occurrences) as one Moshi JSON blob in DataStore `schedule_cache`,
key `schedule_<hostname>` → return the live `Schedule` (bookings/plan/applied are **not** cached).
Mutations (`createBooking`, `updateSeries`, `deleteSeries`, `updateOccurrence`,
`cancelOccurrence`) → API → `refreshQuietly` (a failed re-fetch leaves the cache stale, no error).
A failed `refresh` leaves the cache untouched; an unreadable blob = no cache.
To change the window: `ScheduleRepository.DAYS` / `cacheWindow()`.

## New project: scaffold vs. register existing folder

Files: HomeScreen.kt, FolderPickerScreen.kt

Home's "Add project" ExtendedFAB opens a `ModalBottomSheet`: "New empty project"
(`POST /v1/projects {name}` via `HomeViewModel.createProject`) or "Register existing folder" →
`FolderPickerScreen`.

`FolderPickerScreen` is an unscoped filesystem browser (`GET /v1/browse`, see runner/FLOWS.md) —
deliberately separate from `FileBrowserContent`, which is read-only and scoped to one
already-registered project. It opens at `~` (the runner resolves that to the user's Documents
folder) → the response's `path`/`parent` become `resolvedPath`/`parentPath` → a ".." row pushes
`parentPath`, so the user can walk up from Documents. Path history is a stack of paths (`~` first,
empty string = the drive-list view), same local-state back-navigation pattern as
`FileBrowserViewModel`; child paths are built from `resolvedPath`, never from the stack. Its
"Select this folder" ExtendedFAB (`canRegister`: hidden while loading, on error, or while
`parentPath` is null — registering `/` or the drive list is nonsensical; an empty folder also
shows it as a Button) calls `register()` → `POST /v1/projects {path}` **directly from this screen**
(failure → Snackbar, listing stays; load failure → `FullScreenError` + Retry, first load → `SkeletonRows`), then hands the
resulting `Project` to `onRegistered` — registration happens here, not back on Home,
so nothing depends on Home noticing it should re-fetch.

`RelayNavHost`'s `FOLDER_PICKER` route wires `onRegistered` to navigate straight into that
project (Chats tab, `popUpTo` Home), skipping an intermediate "now go tap the
project you just made" step.

To change: `ui/screens/FolderPickerScreen.kt`, `ui/screens/HomeScreen.kt` (add sheet).

## File browser (read-only)

Files: FileBrowserScreen.kt, RelayApiService.kt

Project › Files tab (or Home row ⋮ → Files) → `FileBrowserContent(FileBrowserViewModel)`.
Directory drill-down and file viewing are ViewModel state, not routes — `openDir` appends to
`pathSegments`, `openFile` sets `viewingFile` and fetches its content; a "↑ path" row and
`BackHandler` (enabled only while `canStepBack`) step out of a file, then up one directory; at the
root back falls through to normal navigation. `GET /v1/projects/{id}/files` lists a directory,
`GET /v1/projects/{id}/files/content` returns one file's text — see runner/FLOWS.md "File
viewing". Read-only — no edit/save affordance exists. Folder position survives tab switches and
chat round-trips (ViewModel on the Project entry).

To change: `ui/screens/FileBrowserScreen.kt`.

## Git tab

Files: ui/screens/GitScreen.kt (`GitViewModel`, `GitContent`), network/RelayApiService.kt
(`git*`), model/Models.kt (`GitStatus`, `GitFile`, `GitDiff`, `GitCommit`, `GitBranch`)

Project › Git → `GitContent(GitViewModel)` → `pollWhileVisible`: `GET …/git` every 10s, 1.5s while
a push/pull/fetch runs; log re-fetched only when branch/ahead/behind changed or an op ended.
Not a repo → `EmptyState`. Pull-to-refresh = status + `fetch` (so ↓ counts are real).

LazyColumn:
- `BranchCard`: branch ▾ → `BranchMenu` ("New branch…" → `NewBranchDialog` → switch -c; local
  branches ✓ current; remote-only ones dimmed → `switch --track`) · "origin/main · ↑1 ↓0" / "Not
  published yet" / "No remote" · Fetch | Pull n | Push n / Publish · 4dp progress slot ·
  `OpResult`: "Pushing…" / "Pushed." / git's error. Auth failure with a provider → "Sign in to
  GitHub and retry" → `SignInSheet(provider)` → on success `retryLast()` (re-POSTs the same op);
  without one (ssh, other host) → "no working credentials for this remote" + git's text.
- `CommitBox`: message (≤4 lines) + "Commit N staged" / "Stage all & commit" (stages first).
- "Staged (n)" + Unstage all, "Changes (n)" + Stage all: `FileRow` = checkbox (stage/unstage
  that path) · name + dir / "from <old>" / conflict · XY letter (`StatusLetter`). A partially staged
  file shows in both lists. Tap → `DiffView` (in place, Back closes): +/- lines on success/error
  chip colours, hunks in primary, header lines dropped.
- "Recent commits": subject · short hash · author · relative time; the first `ahead` are
  marked "not pushed".

Errors: local action refusals → `actionError` (git's `{error}` text) in the branch card until the
next action; load failure with no data → `FullScreenError`.

To change polling: `GitViewModel.pollWhileVisible`. Labels/buttons: `BranchCard`, `OpResult`,
`CommitBox`. Runner side: runner/FLOWS.md "Git".

## FCM device registration

`RelayFirebaseMessagingService.onNewToken(token)` → enqueues `RegisterDeviceWorker` (WorkManager)
→ loops every known runner from KnownRunnersRepository → POSTs `{"fcmToken": token, "runnerRef":
runner.hostname}` to `/v1/devices` on each, using that runner's own key. One unreachable runner logs + is skipped;
does not block the others.

`MainActivity.registerCurrentFcmTokenWithAllRunners()` does the same thing once at app startup
using `FirebaseMessaging.getInstance().token` — covers a runner added AFTER the token was already
issued, without waiting for a token refresh to fire `onNewToken`.

A real Firebase project (`relay-sizonenko`) exists — `google-services.json` is present
(gitignored, generated via `firebase apps:sdkconfig`) and `app/build.gradle.kts` applies the
`com.google.gms.google-services` plugin conditionally on that file existing, so a checkout
without it (any fresh clone) still builds, just without live FCM. The try/catch in
`MainActivity.registerCurrentFcmTokenWithAllRunners()` stays as a safety net for that case.

`onMessageReceived` posts a real Android notification → `contentIntentFor(data)`: push has
`runnerRef` + `projectId` + `sessionId` and `runnerRef` is a known runner's `hostname` → ACTION_VIEW
`relay://r/{runnerRef}/p/{projectId}/s/{sessionId}` (manifest intent-filter scheme `relay`, host
`r`) → NavController deep link on `Routes.CHAT` → Chat (back stack: Home, Chat). Otherwise
(empty/unknown `runnerRef`, `runner_suspending`) → plain launch (last route restored).
To change: `RelayFirebaseMessagingService.contentIntentFor()`, `Routes.CHAT_DEEP_LINK`.
Requires POST_NOTIFICATIONS granted at runtime on API 33+ (requested once at startup in
`MainActivity.requestNotificationPermissionIfNeeded()`); if denied, pushes still arrive and
register but no banner shows (Android silently drops it, not something this code can detect).

To change: `fcm/RegisterDeviceWorker.kt`, `MainActivity.registerCurrentFcmTokenWithAllRunners()`,
`fcm/RelayFirebaseMessagingService.onMessageReceived()` (notification content/channel).

## Building (no local JDK/Gradle — Docker only)

There is no JDK or Android SDK on the dev machine. Builds run in `mingc/android-build-box`
(bundles JDK 17 + Android SDK + Gradle), reusing a persistent `relay-gradle-cache` volume so the
Gradle distro/dependencies aren't re-downloaded every time:

```
docker run --rm -v "$PWD:/project" -v relay-gradle-cache:/root/.gradle \
  -w /project mingc/android-build-box bash -c "./gradlew assembleDebug"
```

(run from `android/`; the image is ~10 GB, first pull is slow).

## Distribution (Firebase App Distribution)

**Signing + versioning (2026-10-04).** CI restores one permanent key (secrets `RELAY_KEYSTORE_B64`,
`RELAY_KEYSTORE_PASSWORD`) → `RELAY_KEYSTORE_FILE` → `app/build.gradle.kts` `signingConfigs.relay` signs
the debug APK. `versionCode` = `RELAY_BUILD_NUMBER` = `github.run_number`, `versionName` = `0.1.<run>`.
Before this, CI signed with a fresh random debug key per run → Android refused the update → forced
uninstall → paired runners (DataStore) wiped. Same key + rising versionCode = installs as an update,
data kept. The key's only copies: `~/.relay-signing/relay-app.jks` + `signing.txt` on the dev PC and the
GitHub secret. **Lose both and the next build forces one uninstall again.** Missing secrets → the
build fails (empty keystore) instead of silently shipping a throwaway-key APK.
To change: `app/build.gradle.kts` (`signingConfigs`, `versionCode`), `.github/workflows/android-deploy.yml`.


Files: .firebaserc, .github/workflows/android-deploy.yml

Not on the Play Store by design (see ARCHITECTURE.md), so updates ship via Firebase App
Distribution — reuses the same `relay-sizonenko` Firebase project already wired for FCM.

**Shipping a build is automatic**: a push to `main` touching `android/**` triggers
`.github/workflows/android-deploy.yml`, which builds and distributes. Manual equivalent:
`npx firebase appdistribution:distribute <path-to-apk> --app
1:960396843466:android:fd2974c630731375591733 --testers sizonenkodima6@gmail.com` (run from
`android/`, project comes from `.firebaserc`).

Firebase emails testers on every release — no dev-side toggle exists to suppress it (a
firebase-tools feature request for one was filed and closed "not planned"); only the tester can
opt out from their own App Distribution web view. **There is no in-app update prompt anymore**
(the `firebase-appdistribution` SDK and `MainActivity.checkForUpdate` were deleted 2026-09-20) —
installing a new build means going through the emailed link / the App Distribution app.

**A force-push to `main` never triggers this workflow.** GitHub's `push` event is not emitted for
a non-fast-forward ref update — confirmed 2026-09-16 by diffing the repo's public events feed:
every ordinary push to `main` shows up as a `PushEvent`, a force-pushed commit showed zero
matching event and zero workflow run. Not a bug, not a quota limit — GitHub simply never tells
Actions a push happened. After ANY force-push to `main`, fire the build manually:
`gh workflow run android-deploy.yml --ref main`.

To change who gets releases: swap `--testers` for `--groups <name>` (create the group in the
Firebase console first). To change the app targeted: the `--app` id comes from
`android/app/google-services.json` (`mobilesdk_app_id`).

**`--release-notes` is passed via an `env:` var (`$RELEASE_NOTES`), never
`"${{ github.event.head_commit.message }}"` interpolated directly into a quoted shell string.**
A commit message containing a literal `"` closes that shell string early and the rest gets parsed
as stray arguments — `appdistribution:distribute` failed outright with "Too many arguments."
`$VAR` expansion doesn't re-parse its value as shell syntax. Same fix in runner-release.yml's
`--notes`.

## Technology notes

- **Usage costs** live in DataStore (`app_prefs`): idle watts per runner hostname (`usage_idle_w_<host>`),
  the rest global. Renaming a runner keeps them; re-pairing under a new hostname starts at defaults.
- **Activity ping** goes to every known runner, not just the one on screen - "app open" means
  "the user is around", which is what idle-suspend needs. A runner the phone can't reach just
  misses pings; nothing retries.
- **Claude sign-in sheet ViewModel** is keyed by a `remember`ed random id, so each opening gets a
  fresh one - but it lives in the host screen's ViewModelStore until that back-stack entry pops
  (a few tiny leftovers per screen, harmless). Rotation/process death drops the id → the sheet
  (if still shown) starts a new login, invalidating a code from the old page. The URL opens in the
  phone's browser, so the code must be copied back by hand (no redirect into the app).
- **ViewModel lifetime = back-stack entry.** Going Home → Project → back keeps Home's
  ViewModel (no spinner, background refresh). Popping an entry (back from it) destroys its
  ViewModels; process death loses all of them (nothing in `SavedStateHandle`) — the screen
  reloads from network with skeleton rows. A ViewModel captures the `KnownRunner` it was built
  with; re-pairing (new key) while a Project entry is open keeps using the old key until that
  entry is popped.
- **Last route is persisted in DataStore, restored only on a cold launch with no deep link**
  (`savedInstanceState == null && intent.data == null`). Validation: runner must still be known
  (else stay on Home); a deleted project is caught after load by `ProjectViewModel.missing` →
  `goHome()`; a deleted session just shows ChatScreen's error. If DataStore is slow, nothing
  renders until the first read completes (`produceState` gate in RelayNavHost).
- **Deep links** use the runner's `hostname` as `{host}` — the same string sent as `runnerRef` at
  registration. A runner re-paired under a different address won't match old pushes until the
  token is re-registered (startup does this). Navigation's handling of a NEW_TASK deep-link
  intent restarts the activity task with a synthetic stack (Home, Chat) — the project screen is
  not in between. `relay://` is not verified (custom scheme): any app can fire it, but it can only
  open a chat on a runner already paired here.
- **Runner switcher lamps** = `GET /v1/health` per runner, 3s timeout, only when the dropdown
  opens (plus the current runner on every Home refresh). Not polled.
- **Project row "last activity" is [NOT IMPLEMENTED]** — `Project` has no activity field and
  computing it would need every session of every project; rows show the path instead.

- **Cleartext HTTP was blocked on every real device until 2026-09-16.** `RelayApiClient` talks
  plain `http://` deliberately (ARCHITECTURE.md: Tailscale's tunnel is the transport security,
  not app-level TLS), but Android has blocked cleartext traffic by default since API 28 and the
  manifest never declared `android:usesCleartextTraffic="true"` to opt back in. Every API call
  failed with "not permitted by network security policy" the first time this app ever ran against
  a real runner — invisible to the Docker-only compile check, since that never executes the app.
  Fixed in `AndroidManifest.xml`.
- **No build/run verification via emulator** — none available. Compile verified via the
  Dockerized `./gradlew assembleDebug` above; no instrumented/UI tests have ever run, so every
  flow here is compile-checked only, never seen on a device.
- **`KnownRunner.DEFAULT_PORT` was 8080 until 2026-09-20 while the runner defaults to 7777.**
  Only ever hit by manual entry / a QR code with no explicit port, which is why it survived this
  long. If the runner's default ever changes, this constant must change with it — nothing
  enforces the pairing.
- **No offline cache for sessions, messages or projects.** They are fetched live on
  every screen entry; an asleep or unreachable runner shows `friendlyErrorMessage`, not a stale
  transcript. The **only** offline cache is the schedule window (`ScheduleRepository`, below). The Room cache that used to back this was deleted 2026-09-20 (it was a
  write-through mirror, so nothing unrecoverable was stored in it). If offline reading is wanted
  back, it's a fresh decision, not a revert.
- **Home fans out one `GET …/containers` per project** (each runs `docker ps` on the
  runner) every time the list loads (incl. each return to Home). Fine for a handful of projects; with dozens it's dozens of
  docker calls per screen entry. Docker down/slow on the runner → lamps just don't appear.
- **Always dark theme**: `RelayTheme(darkTheme = true)` ignores the phone's light/dark setting;
  dynamic (wallpaper) colors still apply on Android 12+.
- **Markdown is hand-rolled** (`MarkdownText.kt`, no library): headings, lists, quotes, rules,
  fenced code, inline bold/italic/code/links/strike. Tables and nested structures show as plain
  text; links are underlined but not tappable; `_` is never emphasis (keeps snake_case intact).
- **DataStore Preferences has no encryption** — the runner key is stored in plaintext prefs.
  Acceptable for now (single-user, own devices) but worth revisiting before wider use.
- **Chat polling is 1s while `busy`, 3s while `waiting`, off otherwise.** It stops on any exception
  (Snackbar Retry, or full-screen error before first load) rather than retrying — a runner that goes away mid-session needs the
  screen re-entered. There is no backoff and no WebSocket.
- **WorkManager is now used for exactly one thing** (`RegisterDeviceWorker`, FCM token
  registration) and has no dedup/backoff tuning — `OneTimeWorkRequestBuilder` defaults. Its loop
  over runners is sequential, so a slow/unreachable runner delays (but does not block, thanks to
  the try/catch) the rest.
- **Schedule cache** (DataStore `schedule_cache`): per runner hostname, one JSON blob, last
  successful fetch only. Survives app restarts; re-pairing under a new hostname starts empty.
  `ScheduleRepository.clear()` exists but nothing calls it - removing a runner leaves its blob.
- **Reminders are phone-local, the cache is runner-local.** A reminder's "today" is the phone's
  `LocalDate.now()` matched against runner-zone date strings; if the zones differ it can pick the
  wrong day near midnight.
- **Reminders depend on AlarmManager surviving.** Force-stop and OEM battery killers
  (Xiaomi/Huawei/Samsung…) drop the alarms until the app is next opened; reboot is covered by
  BOOT_COMPLETED. Inexact alarms can be minutes late in Doze. The morning reminder reads only the
  cache (the server is usually off), so a booking made elsewhere since the last evening refresh is missed.
- **Booking times are formatted client-side** with `String.format` in the default locale
  (`BookingEditorLogic.formatMinutes`) - a locale with non-ASCII digits would send times the runner
  rejects (400).
- **FCM is live end-to-end in code**, gated only on the runner side having
  `RELAY_FCM_CREDENTIALS` pointed at a real service-account key (see runner/FLOWS.md) — without
  that the runner still registers devices but silently skips sending.

## Deleted 2026-09-20

Removed as premature/broken, so the app is only the core loop. Listed so nobody hunts for them:

| Gone | Was | Why |
|---|---|---|
| `widget/` package, `data/WidgetConfigRepository.kt`, `res/layout/relay_widget.xml`, `res/xml/relay_widget_info.xml`, manifest `<receiver>` | home-screen widget + its Stop/Wake workers, the star "widget default project" toggle | never finished; widget actions were invisible-failure background workers |
| `ui/screens/DashboardScreen.kt`, `data/UptimeSyncWorker.kt`, `MainActivity.scheduleUptimeSync`, `GET /v1/uptime` client call, `UptimeInterval` | per-app uptime history | built before there was anything to measure |
| `data/db/` (Room: Entities/Daos/RelayDatabase), Room+KSP gradle deps, all cache reads/writes in ChatScreen + SessionListScreen | offline cache of sessions/messages | see Technology notes above |
| `data/WakeViaMatcher.kt`, `KnownRunner.wakeMac`/`wakeViaRunnerId`, "Wake settings" menu item + dialog, "Wake" button, `mac` on `ScannedRunner`, `wake()` API call | Wake-on-LAN config held on the phone | wake is moving to a separate always-on Pi daemon; the phone will not hold wake config |
| `data/FriendlyNameGenerator.kt` | generated "Adjective Noun" default runner name | replaced by an empty field defaulting to the hostname |
| "Authenticate agent" menu item, `startAuthLogin()`, `AUTH_LOGIN_CHAT` route, `onAuthLoginStarted` | headless OAuth relay through ChatScreen | premature then; **rebuilt 2026-10-04** as `SignInSheet` (login-expired card + runner ⋮ Sign-ins) - see "Sign-in relay" |
| `MainActivity.checkForUpdate` / `UpdateAvailableDialog` / `pendingUpdate`, `firebase-appdistribution` dep | in-app "update available" prompt | premature |
| "Start/Stop all containers" menu items, `ContainersAllWorker`, `startAllContainers()`/`stopAllContainers()`, `ContainerActionResult` | bulk container control per runner | premature; failures were never surfaced |

`SuspendRunnerWorker` is also gone, but the **Sleep button stayed** and now calls
`RelayApiService.suspend()` directly with visible results — see "Manual suspend" above.
Per-project `startContainers`/`stopContainers` stayed on `RelayApiService` and are now used by
ContainersScreen (2026-09-30), which also surfaces failures (`lastError`).

**Replaced 2026-10-03 (stage 3 navigation):** `ProjectListScreen.kt` → HomeScreen;
`SessionListScreen.kt` (+ its `ContainersRow`, `NewSessionDialog`) → ProjectScreen Chats tab;
`ContainersChip` button on project rows → lamp + row ⋮ (DESIGN.md "Dropped"); standalone
Files/Containers routes → Project tabs. The always-visible Sleep button → runner row ⋮.

## Change Index

| Thing | Where |
|---|---|
| APK signing key / version number | `app/build.gradle.kts` (`signingConfigs`, `RELAY_BUILD_NUMBER`), `.github/workflows/android-deploy.yml`, secrets `RELAY_KEYSTORE_B64`/`RELAY_KEYSTORE_PASSWORD` |
| Routes / deep link pattern / transitions | `ui/navigation/RelayNavHost.kt` (`Routes`, `forwardEnter`…`backExit`) |
| Last-route restore | `ui/navigation/RelayNavHost.kt` (`RESTORE_PATTERN`, restore `LaunchedEffect`), `MainActivity.kt` (`restoreLastRoute`) |
| Current runner / last route / last provider prefs | `data/AppPrefsRepository.kt` |
| "On this computer" chats (VS Code's) | `ui/screens/ProjectViewModel.kt` (`laptopChats`, `continueLaptopChat`), `ProjectScreen.kt` (`LaptopChatRow`), `RelayApiService.listAgentChats` |
| "Runner can't update" banner | `ui/screens/HomeScreen.kt` (`UpdateStuckBanner`), `HomeViewModel.updateStuck`, `UpdateStatus.stuck` |
| Usage screen layout / heatmap | `ui/screens/UsageScreen.kt` |
| Chat long-press: Copy / Edit & resend | `ui/screens/ChatScreen.kt` (`MessageActions`) |
| Model / effort / fast picker (chip above composer) | `ui/screens/ChatScreen.kt` (`AgentConfigChip`), `ChatViewModel.setConfig`, runner `session/config.go` |
| £/yr + payback math | `ui/screens/UsageViewModel.kt` (`savings()`) |
| Usage cost defaults / storage | `data/AppPrefsRepository.kt` (`UsageCosts`, `usageCosts()`) |
| Foreground activity ping | `MainActivity.kt` (`pingActivityWhileForeground`, `ACTIVITY_PING_MS`) |
| Provider list on New chat long-press | `data/AppPrefsRepository.kt` (`KNOWN_PROVIDERS`, `DEFAULT_PROVIDER`) |
| Home layout (Needs you, project rows, row ⋮, add sheet, switcher) | `ui/screens/HomeScreen.kt` |
| Home data loading / container lamps / up-down from row | `ui/screens/HomeViewModel.kt` |
| Project tabs, chat rows, New chat FAB, first-message composer | `ui/screens/ProjectScreen.kt` |
| Chat list sort / project-missing check | `ui/screens/ProjectViewModel.kt` (`refresh`) |
| Skeleton / full-screen error / pull-to-refresh / relative time | `ui/components/ScreenStates.kt` |
| Session state → chip colour | `ui/components/ScreenStates.kt` (`sessionStatus`) |
| Push tap target (deep link vs plain launch) | `fcm/RelayFirebaseMessagingService.kt` (`contentIntentFor`) |
| Deep-link intent-filter | `app/src/main/AndroidManifest.xml` |
| Known runners storage | `data/KnownRunnersRepository.kt`, `model/Models.kt` (`KnownRunner`) |
| Default runner port (7777) | `model/Models.kt` (`KnownRunner.DEFAULT_PORT`) |
| Chat transcript rendering (bubbles, tool rows, Stop, mode picker, ⋮ Files) | `ui/screens/ChatScreen.kt` (`Transcript`, `UserBubble`, `ToolRow`, `Composer`, `ModePicker`, `ChatOverflow`) |
| Chat state / polling / optimistic send / Snackbar events | `ui/screens/ChatViewModel.kt` (`startPolling`, `send`, `act`) |
| Permission card (sticky) + interlock bar | `ui/screens/ChatScreen.kt` (`StickyPermission`, `PermissionCard`, `InterlockCard`) |
| Chat follow-bottom / "↓ New" pill / item enter animation | `ui/screens/ChatScreen.kt` (`Transcript`, `STICK_DELTA`, `AppearOnce`) |
| Sign-in sheet (steps, wording, 503/409/404/400 mapping, device-flow polling) | `ui/components/SignInSheet.kt` (`SignInSheet`, `SignInViewModel.explain`, `POLL_MS`) |
| Git tab (branch card, commit box, file lists, diff, sign-in retry) | `ui/screens/GitScreen.kt` |
| Runner ⋮ Sign-ins list | `ui/components/SignInSheet.kt` (`SignInsSheet`, `ProviderRow`) |
| Re-login entry points | `ui/screens/ChatScreen.kt` (`ProblemCard` "Sign in again", `signInOpen` + Snackbar), `ui/screens/RunnerListScreen.kt` (row ⋮ "Sign-ins", `signingInRunner`) |
| Re-login API calls / types | `network/RelayApiService.kt` (`claudeAuthStatus`/`claudeAuthStart`/`claudeAuthFinish`, `ClaudeAuthFinishRequest`), `model/Models.kt` (`ClaudeAuthStatus`) |
| Quota / login-expired card + push text | `ui/screens/ChatScreen.kt` (`ProblemCard`), `fcm/RelayFirebaseMessagingService.kt` (`notificationContentFor`) |
| Markdown in agent replies | `ui/screens/MarkdownText.kt` (`parseBlocks`, `inline`) |
| Dark/light theme | `ui/theme/Theme.kt` (`RelayTheme` `darkTheme` default) |
| Notification text per push type | `fcm/RelayFirebaseMessagingService.kt` (`notificationContentFor`) |
| Containers tab (per-file Up/Down/Switch cards, take-everything-down + confirm, polling) | `ui/screens/ContainersScreen.kt` (`ContainersContent`, `ContainersViewModel`) |
| Containers status motion / elapsed-time line | `ui/screens/ContainersScreen.kt` (`StatusCard`, `Elapsed`, `ColorSpec`, `ContainersViewModel.opStartedAt`) |
| Home wordmark + "waiting" lamp | `ui/screens/HomeScreen.kt` (`Wordmark`) |
| Press-scale feedback (custom clickables) | `ui/screens/HomeScreen.kt` (`pressScale`) |
| Empty-state line + action button | `ui/components/ScreenStates.kt` (`EmptyState`) |
| Project-row containers lamp | `ui/screens/HomeScreen.kt` (`ProjectRow`) |
| Runner error text in error messages | `network/RelayApiClient.kt` (`friendlyErrorMessage`) |
| QR pairing content parsing | `ui/screens/QrScanScreen.kt` (`parseRelayQrContent`) |
| Runner display name / blank fallback | `model/Models.kt` (`.displayName`, `.label`), `ui/screens/RunnerListScreen.kt` (`NameRunnerDialog`, `AddRunnerDialog`) |
| API types (must match shared/API.md) | `model/Models.kt` |
| HTTP client / auth header | `network/RelayApiClient.kt`, `network/RelayApiService.kt` |
| Connection error wording | `network/RelayApiClient.kt` (`friendlyErrorMessage`) |
| Navigation graph / routes | `ui/navigation/RelayNavHost.kt` (`Routes`) |
| Schedule routes / grid refresh after edit | `ui/navigation/RelayNavHost.kt` (`Routes.SCHEDULE`, `SCHEDULE_EDIT`, `scheduleEdit()`, `SCHEDULE_CHANGED`) |
| Schedule entry points | `ui/screens/HomeScreen.kt` (⋮ / switcher "Schedule", `TodayCard`), `ui/screens/RunnerListScreen.kt` (row ⋮ "Schedule") |
| Week grid layout / sizing / taps | `ui/screens/ScheduleScreen.kt` (`WeekGrid`, `DayColumn`, `OccurrenceBlock`, `VISIBLE_HOURS`, `FIRST_VISIBLE_HOUR`) |
| Cache vs live week fetch | `ui/screens/ScheduleViewModel.kt` (`ensureWeek`, `weekOccurrences`), `ScheduleLayout.kt` (`weekCovered`) |
| Schedule status line | `ui/screens/ScheduleLayout.kt` (`scheduleStatusLine`, `nextSleep`) |
| Booking editor form / modes / scope dialog | `ui/screens/BookingEditorScreen.kt` (`EditorForm`, `ScopeDialog`), `BookingEditorViewModel.kt` (`mode`, `asksScope`, `seriesOnlyChanges`, `save`, `delete`) |
| Booking client validation / Add-sleep proposal | `ui/screens/BookingEditorLogic.kt` (`validateForm`, `proposeSleep`, `MIN_GAP_MINUTES`) |
| Home Today card | `ui/screens/TodaySchedule.kt` (`todaySummary`, `nextTransition`), `HomeScreen.kt` (`TodayCard`), `HomeViewModel.refreshSchedule` |
| Schedule cache window / storage | `data/ScheduleRepository.kt` (`DAYS`, `cacheWindow`, `ScheduleCacheCodec`) |
| Schedule API calls / types | `network/RelayApiService.kt`, `model/ScheduleModels.kt` |
| Reminder defaults / storage | `reminders/ReminderPrefs.kt` (`ReminderSettings`) |
| Reminder alarms (exact vs inexact) | `reminders/ReminderScheduler.kt` (`reschedule`, `set`) |
| Reminder text / which day / next trigger | `reminders/ReminderLogic.kt` (`text`, `todays`, `nextTriggerMillis`) |
| Reminder firing / evening refresh / notification | `reminders/ReminderReceiver.kt` (`fire`, `refreshAll`, `REFRESH_TIMEOUT_MS`, `CHANNEL_ID`) |
| Reminder reschedule broadcasts / permissions | `app/src/main/AndroidManifest.xml` (`ReminderRescheduleReceiver`, `SCHEDULE_EXACT_ALARM`, `RECEIVE_BOOT_COMPLETED`) |
| Reminder settings dialog | `ui/screens/ReminderSettingsDialog.kt` |
| Chat poll intervals | `ui/screens/ChatViewModel.kt` (`POLL_BUSY_MS`, `POLL_WAITING_MS`, `ECHO_WAIT_TICKS`) |
| Sleep (row ⋮) + its 409/503/error Toasts | `ui/screens/RunnerListScreen.kt` (`suspendRunner`) |
| Rename runner | `ui/screens/RunnerListScreen.kt` (`renamingRunner`, `NameRunnerDialog`) |
| Remove runner | `data/KnownRunnersRepository.kt` (`removeRunner`), `ui/screens/RunnerListScreen.kt` |
| Register existing folder / new-project sheet | `ui/screens/HomeScreen.kt`, `ui/screens/FolderPickerScreen.kt` |
| File browser tab | `ui/screens/FileBrowserScreen.kt` (`FileBrowserContent`, `FileBrowserViewModel`) |
| FCM token registration (on refresh) | `fcm/RelayFirebaseMessagingService.kt` |
| FCM token registration (on app startup) | `MainActivity.kt` |
| FCM registration background call (loops all runners, sends `runnerRef`) | `fcm/RegisterDeviceWorker.kt` |
| Notification content/channel | `fcm/RelayFirebaseMessagingService.onMessageReceived()` |
| Cleartext HTTP opt-in | `app/src/main/AndroidManifest.xml` (`android:usesCleartextTraffic`) |
| Gradle/Kotlin/Compose versions | `app/build.gradle.kts`, `build.gradle.kts`, `gradle/wrapper/gradle-wrapper.properties` |
| Dockerized build command | this file, "Building" |
| Distribution target (Firebase project/app) | `.firebaserc`, `app/google-services.json` |
| Auto-distribute on push to main | `.github/workflows/android-deploy.yml` |
