# Relay HTTP API v1 — contract between runner and android

Transport: plain HTTPS/HTTP over the Tailscale tailnet only (runner binds to the tailnet
interface, never 0.0.0.0 — see ARCHITECTURE.md). No TLS termination is Relay's own concern for
v1; Tailscale's tunnel is the transport security.

Auth: every request carries header `X-Relay-Key: <key>`. The key is generated locally by the
runner on first run and copied into the phone app manually (see ARCHITECTURE.md
"Registration"). A request with a missing/wrong key gets `401`.

Content type: `application/json` for all request/response bodies.

## Types

```
Project {
  id: string          // stable slug, e.g. "relay" — also the directory name
  name: string
  path: string         // absolute path on the runner's machine
  createdAt: string    // RFC3339
}

Session {
  id: string
  projectId: string
  provider: string     // "claude" | "codex" | ... (matches a configured CLI command)
  state: string        // "busy" | "idle" | "waiting" (paused on a permission request) | "finished" | "error"
  createdAt: string
  finishedAt: string?  // null while not finished
  messages: Message[]
  title: string        // first user message, one line, <=80 runes ("…" if cut); "" if none
  preview: string      // last non-empty agent message, one line, <=120 runes ("…" if cut); "" if none
  // ACP providers only (claude), omitted otherwise:
  mode?: string                         // current permission mode id, e.g. "bypassPermissions"
  modes?: {id, name, description?}[]    // what the agent offers
  configOptions?: {                     // ACP session config options (ACP providers only)
    id: string,                         // "model" | "effort" | "fast" | "mode" | ...
    name: string, description?: string,
    category?: string,                  // "model" | "thought_level" | "model_config" | "mode"
    type: "select",
    currentValue: string,
    options: {value, name, description?}[]
  }[]
  lastActiveAt?: string                 // RFC3339, last change; bumped ~every 30s while busy
  pendingPermission?: {                 // set while state == "waiting"
    title: string, toolKind: string,
    options: {optionId, name, kind}[]   // kind: allow_once | allow_always | reject_once | reject_always
  }
}

Message {
  role: string         // "user" | "agent" | "tool" (one agent step, e.g. a file edit or command)
  text: string         // for "tool": one-line title, e.g. "Write hello.txt"
  at: string           // RFC3339
  toolKind?: string    // tool only: read | edit | delete | move | search | execute | think | fetch | other
  status?: string      // tool only: pending | in_progress | completed | failed
  kind?: string        // agent only: "quota" (subscription limit used up) | "auth" (login missing/expired)
}

RunnerInfo {
  hostname: string     // tailnet hostname
  busy: boolean         // true if any session across any project is busy
  version: string
  update?: {            // keeperd's last self-update check; absent if not managed by keeperd
    keeperVersion: string
    lastCheckAt: string    // RFC3339
    lastError?: string     // absent if the last check worked
    failingSince?: string  // RFC3339, start of the current run of failures
    failures?: number      // consecutive failed checks (one every ~10 min)
  }
}

Device {
  id: string           // stable id for this phone install, generated client-side
  fcmToken: string
  registeredAt: string // RFC3339
  runnerRef: string    // address the phone uses for this runner, as sent at registration; "" if never sent
}

FileEntry {
  name: string
  isDir: boolean
  size: number         // bytes; 0 for directories
}

FileContent {
  path: string         // echoes the requested project-relative path
  content: string      // raw file text (UTF-8/ASCII assumed)
}

BrowseResult {
  path: string         // the absolute path that was listed (empty when listing filesystem roots)
  parent?: string      // one level up ("" = the filesystem-roots view); absent at the top
  entries: {name: string}[]  // subdirectories only, hidden (dotfile) dirs excluded
}
```

### ContainersStatus

```
{
  composeFiles: string[],      // file names, sorted
  activeFile: string,          // "" = several files and none chosen yet
  hasDockerfile: boolean,
  containers: { service: string, state: string, composeFile: string }[],
  operation: "" | "starting" | "switching" | "stopping",
  lastError: string,           // from the last finished start/stop, "" if it succeeded
  dockerError: string          // docker itself unreachable (not running, not installed)
}
```

`Project` also gains `activeComposeFile?: string`.

### GitStatus (and Git types)

All paths are relative to the **repository root** (the project dir may be a subfolder of it).

```
GitStatus {
  isRepo: boolean,          // false = plain folder; every other field empty
  branch: string,           // "" when detached
  detached: boolean,
  upstream?: string,        // "origin/main"; absent = branch not published yet
  ahead: number, behind: number,   // vs upstream, as of the last fetch
  files: GitFile[],         // at most 500
  truncated?: boolean,      // more changed paths than that
  remoteUrl?: string,       // push remote URL, embedded credentials removed
  operation: "" | "pushing" | "pulling" | "fetching" | "local",
  lastOp?: "push" | "pull" | "fetch",   // last finished network op (in memory, lost on restart)
  lastError?: string,       // its failure, git's own message
  lastOutput?: string,      // its output on success
  authFailed?: boolean,     // lastError was missing/invalid credentials
  needsAuth?: string        // sign-in relay provider that fixes it ("github"), if any
}
GitFile { path, origPath?, index, worktree, untracked?, conflicted? }  // index/worktree = git XY letter, "." = unchanged
GitDiff { path, staged, diff, truncated? }   // unified diff, capped at 256 KiB
GitCommit { hash, short, author, time /* unix s */, subject }
GitBranch { name /* "origin/x" for a remote one */, remote, current, upstream? }
```

### AuthStatus / AuthResult (sign-in relay)

`{provider}` is one of `claude` (paste-code), `github` (device flow, `gh`), `gcloud` (paste-code).

```
AuthStatus {
  provider: string, name: string,   // "github", "GitHub"
  state: "idle" | "awaiting_code" | "awaiting_approval" | "signed_in" | "failed",  // last attempt since runner start
  url?: string,       // only while awaiting_* - open it on the phone to sign in
  userCode?: string,  // only while awaiting_approval (device flow) - enter it at url
  message?: string,   // failed: why, incl. the CLI's last output lines; signed_in: "Login successful."
  loggedIn: boolean,  // from the provider's status command, false if unknown
  account?: string,   // the logged-in email/username, if known
  email?: string,     // claude only: same as account (older apps read this)
  cliFound: boolean   // false = the provider's CLI isn't installed on this runner
}
AuthResult {
  state: "awaiting_code" | "awaiting_approval" | "signed_in" | "failed",
  url?: string, userCode?: string,  // start only
  message?: string
}
```

### UsageReport

```
UsageReport {
  from: string, to: string,          // RFC3339 window asked for (now - days .. now)
  spanMinutes: number,               // from the first recorded minute (or `from`, if later) to `to`
  observedMinutes: number,           // minutes the runner was up and recording
  uptimePct: number,                 // observedMinutes / spanMinutes
  activeMinutes: number,             // minutes with any activity source live
  bySource: {turn, app, local},      // minutes per source (a minute can count for several)
  limits: [{
    idleMinutes: number,             // simulated idle-suspend timeout
    awakePct: number,                // % of observed minutes it would have been awake
    wakeups: number,                 // activity arrived while it would have been asleep
    remoteWakeups: number,           // ...with no local input that minute (needs a wake device)
    wakeupsPerDay: number, remoteWakeupsPerDay: number
  }],
  heatmapActive: number[7][24],      // [weekday 0=Monday][hour], runner's local zone, active minutes
  heatmapObserved: number[7][24],    // same shape, observed minutes (the denominator)
  turns: Stats,                      // agent turn durations
  replyLatency: Stats                // agent turn end -> next user message, same session
}
Stats { count, medianSec, p90Sec, meanSec, totalSec }
```

### Schedule (bookings for the server's sleep/wake)

All dates `"YYYY-MM-DD"`, times `"HH:MM"` 24h, wall-clock in the runner's zone (`timezone`).

```
Booking {
  id: string, title: string,          // title may be ""
  date: string,                       // the only day (one-off) or first day (series)
  start: string, end: string,         // awake block, start < end, same day
  sleeps: Gap[],                      // sorted, inside [start, end], each >= 10 min, no overlap
  repeat: Repeat?,                    // null = one-off
  exceptions: Exception[],
  createdAt: string, updatedAt: string
}
Gap { from: string, to: string }      // suspend at from, wake at to
Repeat {
  freq: "daily" | "weekly" | "monthly" | "yearly",
  interval: number,                   // every N, >= 1
  weekdays?: number[],                // weekly only, 1=Mon..7=Sun; omitted = date's weekday
  until?: string,                     // last possible date, inclusive  } at most one
  count?: number                      // total occurrences              } of these two
}                                     // monthly: date's day-of-month, short month → last day
                                      // yearly: date's month/day, Feb 29 → Feb 28
Exception { date: string, cancelled: bool, override?: Day }   // date = the occurrence's original date
Day { start, end, sleeps }
BookingInput = Booking minus id/exceptions/createdAt/updatedAt

Occurrence {
  bookingId, title, date, start, end, sleeps,
  recurring: bool,                    // from a series
  edited: bool                        // this day has an override
}
Schedule {
  timezone: string,                   // e.g. "Europe/London"
  bookings: Booking[],
  plan: Event[],                      // what the runner wants applied, next 14 days
  planWrittenAt: string?,             // RFC3339, null if no plan file (non-Linux, not installed)
  appliedAt: string?,                 // when the root applier last applied it
  applied: bool                       // applier's copy matches the current plan
}
Event { kind: "warn" | "sleep" | "wake", date: string, at: string }
```

## Endpoints

| Method | Path | Body | Response | Notes |
|---|---|---|---|---|
| GET | `/v1/health` | - | `200 {"ok":true}` | no auth required, for basic reachability checks |
| GET | `/v1/runner/info` | - | `200 RunnerInfo` | |
| GET | `/v1/projects` | - | `200 Project[]` | |
| POST | `/v1/projects` | `{name: string, path?: string}` | `201 Project` | scaffolds a new project dir under the runner's configured projects root, unless `path` is set, in which case that already-existing absolute directory is registered as-is (name defaults to the directory's base name). `400` if `path` doesn't exist, isn't a directory, or is already registered. |
| GET | `/v1/browse?path=<absolute>` | - | `200 BrowseResult` | lists subdirectories of `path` — **unscoped**, unlike the project file-viewing endpoints, since its purpose is finding a directory to register via `POST /v1/projects {path}` before any project-level scoping exists. `path` omitted/empty lists filesystem roots; `path=~` lists the user's Documents folder (falls back to home) - where the picker opens. `400` if `path` isn't absolute or isn't a directory. |
| GET | `/v1/projects/{projectId}/sessions` | - | `200 Session[]` | includes finished sessions not yet purged by weekly cleanup |
| POST | `/v1/projects/{projectId}/sessions` | `{provider: string, agentSessionId?: string}` | `201 Session` | spawns the configured CLI command for `provider`, scoped to the project dir. With `agentSessionId` (one from `agent-chats`): opens that saved agent conversation instead, its history replayed into `messages` (a few seconds); `200` with the existing session if one already shows it. `400` if the provider isn't ACP. |
| GET | `/v1/projects/{projectId}/agent-chats?provider=claude` | - | `200 {chats: AgentChat[]}` | the agent's own saved conversations for the project folder, newest first - including ones started in VS Code / the CLI on the runner's machine. `AgentChat = {agentSessionId, title, updatedAt, sessionId?}`; `sessionId` = the live Relay session already showing it. Spawns the agent to list (~1-2s). `provider` default `claude`; `400` non-ACP provider. |
| GET | `/v1/sessions?state=<s1,s2>` | - | `200 Session[]` | sessions across **all** projects (the app's "Needs you" strip). `state` optional, comma list (e.g. `waiting,busy`); omitted = every state. Sorted: `waiting` first, then `busy`, then the rest; within each, `lastActiveAt` (else `createdAt`) newest first. |
| GET | `/v1/sessions/{sessionId}` | - | `200 Session` | full transcript so far. If the chat was continued elsewhere (VS Code) since, the transcript is first reloaded from the agent's own copy - that one call then takes a few seconds. |
| POST | `/v1/sessions/{sessionId}/message` | `{text: string}` | `202 {}` | appends a user message and starts a turn; the reply streams into `messages` (poll `GET`). `409` while the previous turn is still running. |
| POST | `/v1/sessions/{sessionId}/stop` | - | `200 Session` | kills the subprocess, marks session `finished` |
| POST | `/v1/sessions/{sessionId}/cancel` | - | `200 Session` | stops the current turn but keeps the session (the Stop button); the session goes `idle` shortly after. No-op if nothing is running. ACP only, `400` otherwise. |
| POST | `/v1/sessions/{sessionId}/mode` | `{modeId: string}` | `200 Session` | switches permission mode, one of `modes`. `400` for an unknown id. ACP only. |
| POST | `/v1/sessions/{sessionId}/config` | `{configId: string, value: string}` | `200 Session` | sets one `configOptions` entry (model, effort, fast). The session's choices are re-applied whenever its agent resumes, and become this runner's default for new sessions (`$RELAY_HOME/session-defaults.json`). `400` unknown option/value or `mode` (use `/mode`). ACP only. |
| POST | `/v1/sessions/{sessionId}/permission` | `{optionId: string}` | `200 Session` | answers `pendingPermission` with one of its options. `400` if nothing is pending or the option is unknown. |
| GET | `/v1/projects/{projectId}/containers` | - | `200 ContainersStatus` | compose files found at the project's top level (`docker-compose.yml`, `docker-compose.dev.yml`, `compose-prod.yaml`, ...), the active one, and every container compose started from this directory. Only calls docker if a compose file exists; docker failures go in `dockerError`, not an HTTP error. |
| POST | `/v1/projects/{projectId}/containers/start` | `{file?: string}` (body optional) | `202 ContainersStatus` | turns on `file` (default: the active file) with `docker compose -f <file> up -d`. If a different file was active it's brought down first - a **switch**, so only one compose file runs per project. The choice is persisted. Runs in the background: poll GET until `operation` is `""`, then check `lastError`. `400` if no compose file, `file` isn't one of `composeFiles`, or several exist and none is chosen yet; `409` if an operation is already running. |
| POST | `/v1/projects/{projectId}/containers/stop` | - | `202 ContainersStatus` | `docker compose -f <active> down --remove-orphans` in the background - removes containers from any of the project's compose files. Same `400`/`409` as start. |
| POST | `/v1/devices` | `{fcmToken: string, runnerRef?: string}` | `200 Device` | registers/updates this phone's FCM push token with this runner (`runnerRef`: the address the phone uses for this runner, e.g. its Tailscale hostname - stored and echoed in every push; re-registering overwrites it), so the runner can notify it on session-finish (or on suspend, see below). Call on every known runner, and again whenever the token refreshes. Runner-side sending is a no-op until a Firebase credential is configured — see Technology Notes in runner/FLOWS.md. |
| POST | `/v1/suspend` | - | `202 {}` | manually suspends this machine to sleep (S3 on Linux, Modern Standby on Windows) right now, instead of waiting for the idle timeout. `409` if any session is currently busy (never kills active work); `503` if the runner wasn't started with `RELAY_IDLE_SUSPEND_ENABLED=true` — this endpoint deliberately reuses that same opt-in gate, see runner/FLOWS.md "Idle-suspend". |
| POST | `/v1/activity` | - | `202 {}` | tells the runner "the phone app is in the foreground right now" — one of the signals idle-suspend uses to decide whether to suspend the machine, alongside session busy/idle state and local keyboard/mouse input. Call every 30s while, and only while, the app is in the foreground; stop calling when it's backgrounded or closed. Always accepted, even if idle-suspend is disabled on this runner — see runner/FLOWS.md "Idle-suspend". |
| GET | `/v1/usage?days=<1-90>&limits=<m1,m2,...>` | - | `200 UsageReport` | recorded activity + idle-suspend simulation (nothing is ever suspended). `days` default 14, `limits` default `5,15,30,60` (minutes, 1-1440). Recording is always on, independent of `RELAY_IDLE_SUSPEND_ENABLED`; data is kept 90 days. `400` bad params, `503` recorder failed to start. See runner/internal/usage/FLOWS.md. |
| GET | `/v1/schedule` | - | `200 Schedule` | bookings, the current plan and whether the server has applied it. |
| GET | `/v1/schedule/occurrences?from=&to=` | - | `200 Occurrence[]` | expanded days in `[from, to]` inclusive, sorted. `400` bad dates or span > 62 days. |
| POST | `/v1/schedule/bookings` | `BookingInput` | `201 Booking` | `400 {error}` invalid (message says which field); `409 {error}` overlaps another booking's day within 366 days. Plan is rewritten. |
| PUT | `/v1/schedule/bookings/{id}` | `BookingInput` | `200 Booking` | edits the **whole series**. Exceptions are kept, except those whose date is no longer an occurrence. `404` / `400` / `409` as above. |
| DELETE | `/v1/schedule/bookings/{id}` | - | `204` | deletes the whole series. `404` unknown. |
| PUT | `/v1/schedule/bookings/{id}/occurrences/{date}` | `Day` | `200 Booking` | **this day only**: sets an override. `404` unknown booking or `date` isn't an occurrence; `400` / `409` as above. |
| DELETE | `/v1/schedule/bookings/{id}/occurrences/{date}` | - | `200 Booking` | **this day only**: cancels it (for a one-off, same as deleting the booking → `204`). `404` as above. |
| GET | `/v1/projects/{projectId}/files?path=<relative>` | - | `200 FileEntry[]` | lists a directory within the project. `path` omitted/empty = project root. `400` if `path` escapes the project directory (`../`) or isn't a directory. Read-only — see ARCHITECTURE.md "Runner responsibilities". |
| GET | `/v1/projects/{projectId}/git` | - | `200 GitStatus` | working tree + last network op. Not a repo → `200 {isRepo:false}`. `503` git not installed. |
| GET | `/v1/projects/{projectId}/git/diff?path=&staged=` | - | `200 GitDiff` | `staged=true`: index vs HEAD; else worktree vs index (whole file for an untracked one). `400` absolute/escaping path. |
| GET | `/v1/projects/{projectId}/git/log?limit=` | - | `200 GitCommit[]` | HEAD's last `limit` commits (default 30, max 100); `[]` before the first commit. |
| GET | `/v1/projects/{projectId}/git/branches` | - | `200 GitBranch[]` | local branches, then remote ones no local branch tracks. |
| POST | `/v1/projects/{projectId}/git/stage` | `{paths?: string[], all?: bool}` | `200 GitStatus` | `git add --all -- paths` (or everything). |
| POST | `/v1/projects/{projectId}/git/unstage` | `{paths?: string[], all?: bool}` | `200 GitStatus` | `git reset -q -- paths` (or everything); keeps worktree changes. |
| POST | `/v1/projects/{projectId}/git/commit` | `{message}` | `200 GitStatus` | commits what's staged. `400` empty message / nothing staged. |
| POST | `/v1/projects/{projectId}/git/switch` | `{branch, create?, remote?}` | `200 GitStatus` | `switch` / `switch -c` / `switch --track <remote>/<b>`. `400` bad name; `409 {error}` = git refused (e.g. local changes would be overwritten). |
| POST | `/v1/projects/{projectId}/git/push` | - | `202 GitStatus` | background; no upstream → `push -u <remote> HEAD`. Poll GET until `operation` is "". |
| POST | `/v1/projects/{projectId}/git/pull` | - | `202 GitStatus` | background `pull --ff-only` (never merges; diverged → `lastError`). |
| POST | `/v1/projects/{projectId}/git/fetch` | - | `202 GitStatus` | background `fetch --prune`. |
| GET | `/v1/projects/{projectId}/files/content?path=<relative>` | - | `200 FileContent` | returns one file's text content. `400` if `path` is missing/escapes the project dir/is a directory, `404` if it doesn't exist, `413` if over 1MiB, `415` if it looks binary (a null byte in the first 512 bytes). |
| GET | `/v1/auth` | - | `200 AuthStatus[]` | every provider's login state, in a fixed order (statuses run in parallel, ≤10s each). Missing CLI → `cliFound:false`, not an error. |
| GET | `/v1/auth/{provider}` | - | `200 AuthStatus` | one provider's login state. `404` unknown provider, `503` if its CLI isn't found (`RELAY_<CLI>_CLI` / PATH). |
| POST | `/v1/auth/{provider}/start` | - | `200 AuthResult` | starts the CLI login (can take up to ~20s): paste-code providers → `{state:"awaiting_code", url}`; device flow → `{state:"awaiting_approval", url, userCode}`, then poll `GET /v1/auth/{provider}` until `signed_in`/`failed` (github: runs `gh auth setup-git` after login). A login already waiting (< 10 min old) is reused. If the CLI never prints a URL: `200 {state:"failed", message}`. The pending login is killed after 10 min. `404` / `503` as above. |
| POST | `/v1/auth/{provider}/finish` | `{code: string}` | `200 AuthResult` | paste-code providers only: sends the code from the callback page to the waiting login (can take up to ~60s): `{state:"signed_in", message}` or `{state:"failed", message}`. claude: on success, idle Claude sessions restart their agent silently and resume on the next message (busy ones are left alone). `400` empty code or device-flow provider, `409` no login in progress, `404` / `503` as above. |

Errors: `4xx/5xx` bodies are `{"error": string}`.

Every git endpoint: `404` unknown project, `409` not a repo (except GET `/git`) or another git
operation running for the project, `503` git not installed.

## FCM message `data.type` values

Every push the runner sends is data-only (no `notification` block — the app builds its own, see
`RelayFirebaseMessagingService.notificationContentFor`). Every payload below also carries
`runnerRef` — the value this device registered via `POST /v1/devices` (`""` if it never sent one),
so the app knows which runner sent the push. `data.type` is:

- `session_finished` — `data: {type, sessionId, projectId, problem, runnerRef}` (once per completed agent turn;
  `problem` is `"quota"` / `"auth"` when the turn ended on that provider problem, `""` otherwise)
- `session_needs_input` — `data: {type, sessionId, projectId, runnerRef}`, the agent is paused on a
  permission request (session `waiting`)
- `runner_suspending` — `data: {type, hostname, runnerRef}`, sent best-effort right before the runner
  suspends to sleep (never on a manual `/v1/sessions/{id}/stop`) — see runner/FLOWS.md "Idle-suspend".

An older app build (or a message missing `type` entirely) falls back to the `session_finished`
text, so this list can grow without breaking already-installed clients.

## Schedule plan file (runner → root applier)

Not HTTP: the runner writes, a root-owned systemd path unit applies (power/server/FLOWS.md).

- Path: `/var/lib/relay/schedule-plan` (env `RELAY_SCHEDULE_PLAN`). Runner skips writing if the
  directory doesn't exist (Windows, applier not installed).
- Written atomically (temp file + rename) on every booking change, at startup, and daily at 00:05.
- Format: UTF-8, `\n` lines, sorted by time. `#` lines are comments. Every other line is exactly
  `^(warn|sleep|wake) [0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}$`. Only future events,
  next 14 days. The applier rejects the **whole file** if any line fails the pattern.
- Per sleep gap: `warn` (from − 5 min), `sleep` (from), `wake` (to). A warn already in the past
  is dropped; its sleep is kept.
- Empty plan = no sleeps = the server stays awake. That is the fail-safe direction.
- Applier status: `/var/lib/relay/schedule-applied`, written by root: line 1 = sha256 of the
  applied plan, line 2 = RFC3339 time. `applied` in `GET /v1/schedule` = hash matches.

## Not covered by this contract (see ARCHITECTURE.md)

- Job-done push notifications: `/v1/devices` above covers token registration. Actually sending
  a push still needs a real Firebase project + service-account credential from the user
  (`RELAY_FCM_CREDENTIALS` env var pointing at the downloaded JSON key) — without it the runner
  logs and skips instead of sending. `[NOT IMPLEMENTED]` until that credential exists.
