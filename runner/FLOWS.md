# Runner flows

Files: main.go, config.go, project.go, session.go, compose.go, api.go,
registry.go, notifier.go, fcm.go, idle.go, shutdown_unix.go, shutdown_windows.go

This file covers **`runnerd`** only — the per-machine daemon that runs projects/sessions and
**puts its own machine to sleep**. It has no wake ability and never can: while a machine is
asleep nothing on it runs. Waking is `wakerd`'s job — a separate daemon on a separate,
always-on LAN device — documented in `cmd/wakerd/FLOWS.md`. The two never talk to each other;
the phone talks to each separately. See ARCHITECTURE.md "Two daemons" and "Sleep/wake states".

Sleep is covered in depth in `internal/idle/FLOWS.md` — including the
**per-machine wake matrix** (which machine can actually be woken, and why),
suspend-state detection, and the polkit/masked-target/hibernation traps.

## Startup

main() → config.Load() → generates ~/.relay/key.txt + projects.json if absent, prints key once
→ **`-qr` flag check**: if present, `printPairingQR` and exit, skipping everything below (see
"Install bootstrap") → api.NewServer(project.Registry, session.Manager) →
http.ListenAndServe(config.ListenAddr) → goroutine: `idle.NewMonitor(...).Run()` if
`RELAY_IDLE_SUSPEND_ENABLED=true` (see "Idle-suspend")

To change key/projects root: config.Load() (env `RELAY_HOME`)
To change listen address: config.Load() (env `RELAY_LISTEN_ADDR`, default 127.0.0.1:7777 —
binds to Tailscale interface only in production, not enforced by code)

## Request path

phone app → HTTP + `X-Relay-Key` header → api.go middleware checks key (401 if wrong, `/v1/health`
exempt) → routed via Go 1.22 ServeMux method+pattern → project.Registry / session.Manager /
compose.Runner

## Containers (docker compose, dev/prod switch)

GET /v1/projects/{id}/containers → api.handleContainersStatus → compose.Detect (top-level files
matching `composeFileRe`, e.g. docker-compose.yml / docker-compose.dev.yml / compose-prod.yaml,
plus Dockerfile) → active file = `Project.ActiveComposeFile` if still present, else
compose.DefaultFile (the only file, or the plain unsuffixed name) → compose.Status (`docker ps -a
--filter label=com.docker.compose.project.working_dir=<dir>`, so containers from every file show)

POST …/containers/start `{file?}` → validate file ∈ detected → beginContainerOp (409 if one is
running) → registry.SetActiveComposeFile → **background goroutine**: if a different file was
active, compose.Down(prev) first (the switch) → compose.Up(file) → endContainerOp records
`lastError` → `202` with current status; the app polls GET until `operation` is "".

POST …/containers/stop → compose.Down(active) in the background. `down --remove-orphans` removes
containers of any file in the project, since all files in one dir share one compose project name.

To change file-name matching: compose.composeFileRe. To change the no-choice default:
compose.defaultNames. To change what "switch" does: api.handleContainersStart.

## Session lifecycle

Files: session.go, acp_session.go, internal/acp/acp.go, internal/acp/types.go

POST /v1/projects/{id}/sessions → session.Manager.Start(projectID, provider) →
`resolveProvider`, in order: (1) env `RELAY_ACP_<NAME>` → ACP agent at that path, (2) env
`RELAY_PROVIDER_<NAME>` → raw `sh -c <value>`, (3) built-in "echo-agent" (`sh -c cat`, tests),
(4) **auto-detect** (`autoDetectProviders`): `"claude"` → `claude-agent-acp` on PATH (ACP),
`"codex"` → bare `codex` (raw, untested) → ACP or raw path below.
To add a provider: `autoDetectProviders` (ACP: set `ACP: true`), or env `RELAY_ACP_<NAME>`.

Every Session snapshot (`cloneSession`) derives `title` (first user message) + `preview` (last
non-empty agent message) via `summary.go summarize` → whitespace collapsed to one line, cut to
80 / 120 runes with "…". `messages` is still returned in full alongside.
To change caps: `titleMaxRunes` / `previewMaxRunes` (summary.go).

GET /v1/sessions[?state=waiting,busy] → api.handleListAllSessions → session.Manager.ListAll(states)
→ every in-memory session, all projects, filtered by state (empty = all) → `SortForAttention`:
waiting > busy > rest, then `lastActiveAt` (else `createdAt`) desc → 200 Session[]
(the app's "Needs you" strip). To change order: `SortForAttention` (summary.go).

### Sessions (ACP) - claude

Agent Client Protocol: JSON-RPC over the agent's stdin/stdout, one provider-neutral protocol
(Claude via `claude-agent-acp`, Codex via `codex-acp` [NOT IMPLEMENTED], Gemini via
`gemini --acp` [NOT IMPLEMENTED]).

Start: `startACP` → `acp.Start` (spawn) → `initialize` → `session/new {cwd: project dir}` (returns
the agent's modes) → `session/set_mode(Manager.DefaultMode)` → state `idle`.
To change the default mode: env `RELAY_DEFAULT_MODE` (default `bypassPermissions` = no prompts;
`default` = ask before every change). Mode ids come from the agent - Claude offers `default`,
`acceptEdits`, `plan`, `auto`, `bypassPermissions`.

Turn: POST …/message → `sendACP` (409 `ErrTurnInProgress` if a turn is running) → state `busy`
→ goroutine `runTurn` → `session/prompt` (blocks for the whole turn) → meanwhile the agent streams
`session/update` → `onUpdate`:
- `agent_message_chunk` → appended to the current agent message (merged by `messageId`)
- `tool_call` / `tool_call_update` → one `role:"tool"` message per call, title/kind/status
  rewritten in place (e.g. "Preparing file…" → "Write hello.txt", pending → completed)
- `current_mode_update` → `Session.mode`
→ prompt returns `stopReason` → `runTurn`: state `idle`, still-running tool rows → `failed`,
`OnFinished` fires (job-done push). `cancelled` adds "(stopped)", other non-`end_turn` reasons a note.

Provider problems: `runTurn` tags the turn's failure with `Message.kind` so the phone shows a card
instead of an ordinary reply - prompt error → `problemKind(err)` on the "Error: …" message; clean
turn → `markProblemReply` checks this turn's last agent reply (≤300 chars only - Claude Code says
"You've hit your limit · resets 5pm" as a normal reply). `kind` = `quota` | `auth` (ACP code
-32000, or text like "OAuth token has expired" / "Please run /login"). `Session.LastProblem()` →
FCM `session_finished` `problem` field → "Claude quota exhausted" / "Claude login expired" push.
To change the wording matched: `session/problem.go` (`quotaMarkers`, `authMarkers`).
Re-login from the phone: see "Claude re-login (from the phone)" below.

Permission: agent sends `session/request_permission` → `onRequest`: state `waiting`,
`Session.pendingPermission {title, toolKind, options}`, `OnNeedsInput` → FCM
`session_needs_input` → POST …/permission `{optionId}` → `RespondPermission` → agent continues
(state `busy`). Only happens in modes that ask (not in `bypassPermissions`).

Stop button: POST …/cancel → `Manager.Cancel` → answers any open permission with `cancelled` →
`session/cancel` notification → prompt returns `cancelled` → `idle`. Session stays usable.
Mode switch: POST …/mode `{modeId}` → `session/set_mode`.

End session: POST …/stop → `Manager.Stop` → kill agent process → `finished`.
Process exits on its own → `awaitExit` (waits for `acp.Client.Done()` so stdout is drained, then
`Wait`) → `finished`/`error`; on error the agent's last stderr lines are added to the transcript.

Agent process dies (crash, or the runner is killed) → `awaitACPExit` → session **dormant**
(idle, no process). Next message → `runTurn` → `ensureAgent` → spawn agent → `session/resume
{sessionId, cwd}` (same conversation, no history replay) → re-apply the saved mode (resume resets
it) → prompt.

### Session persistence

Files: store.go

`main` → `Manager.SetStoreDir($RELAY_HOME/sessions)` → `LoadPersisted` (one JSON file per session:
transcript, state, mode, `acpSessionId`, `lastActiveAt`) → `RunSaver` goroutine: every 2s writes
sessions whose content changed (tmp file + rename), stamping `lastActiveAt`; while `busy` also bumps
`lastActiveAt` every 30s (heartbeat). On load: finished stays finished, ACP sessions come back
dormant (in-flight turn → "(interrupted - the runner restarted)", running tool rows → failed,
pending permission dropped), raw sessions come back finished.
Shutdown (SIGINT/SIGTERM): `Manager.Shutdown` kills agent processes → sessions go dormant →
final flush.
To change intervals: `store.go` (`saveInterval`, `heartbeatInterval`).

### Sessions (raw) - echo-agent, RELAY_PROVIDER_<NAME>

Spawn → `pump` appends each stdout/stderr line as an agent message → `busy` from spawn until the
process exits (no turn signal) → Stop/exit as above.

### Busy/idle for idle-suspend

`Manager.IdleStatus()` (used by `internal/idle` and `api.anyBusy`): busy only while some session
is `busy`. `idle` (between turns) and `waiting` (paused on your permission decision) do not keep
the machine awake - a pending permission survives sleep (RAM is kept) and is still answerable
after wake; only a reboot loses it. Each record's `idleSince` (last turn end / permission request
/ exit / Stop) feeds the runner-wide idle-since.

## Idle-suspend (sleep) - the other half of the sleep path

Files: internal/idle/idle.go, internal/idle/shutdown_unix.go, internal/idle/shutdown_windows.go,
internal/activity/activity.go, internal/activity/local_windows.go, internal/activity/local_unix.go,
internal/api/api.go (`handleActivity`), cmd/runnerd/main.go

**This is the missing half of ARCHITECTURE.md's "Sleep path" line** - `internal/idle` is what
puts the machine to sleep (S3 suspend-to-RAM on Linux, Modern Standby on Windows - **not** S5
full poweroff; the project switched from S5 because the power delta vs. S3 is only ~1W, while
S5 costs a ~60s boot back and is far more fragile to wake reliably); there is no self-wake.
Waking it back up is a Wake-on-LAN magic packet sent from some other LAN-local device - **no
longer the runner's job** (runners no longer wake each other; that moves to a separate Pi
daemon). `internal/wol` still holds the magic-packet/broadcast primitives, but nothing in the
runner's API calls them.

**The suspend rule: never suspend while (a) any session is busy, OR (b) the phone app is open
and in the foreground, OR (c) someone is physically using this machine.** Suspend only fires
once all three have been quiet for `cfg.Timeout`. This replaced an earlier version that decided
purely from (a) - wrong on a dual-use laptop, where the owner can be sat typing with no phone
session open and get suspended mid-work out from under them. (As a stopgap before this fix,
that laptop ran with `RELAY_IDLE_TIMEOUT` pinned to 12h, effectively disabling the feature -
that pin should come back down to minutes now that (b)/(c) exist.)

main() → `idle.LoadConfig()` (env `RELAY_IDLE_SUSPEND_ENABLED`, `RELAY_IDLE_TIMEOUT`,
`RELAY_IDLE_CHECK_INTERVAL`) → `activity.NewTracker()` created unconditionally and wired to
`srv.Activity` (so `POST /v1/activity` always works) → only if `Enabled` → goroutine:
`idle.NewMonitor(sessions, idle.DefaultShutdowner, cfg)`, with `mon.Activity` = that same
tracker and `mon.LocalInput` = `activity.LocalIdleTime`, then `.Run()` (ticker at
`cfg.CheckInterval`, conditionally started)

Each tick computes an effective idle-since as the **latest** of:
- (a) `session.Manager.IdleStatus()` (busy bool, idleSince time.Time - computed from existing
  session state, not separately tracked; see its doc comment in session.go - each session
  record tracks its own `idleSince`, since an ACP session goes busy→idle→busy per
  turn rather than only busy→terminal, so the runner-wide idle-since is the max of all records'
  idleSince, not simply the latest `FinishedAt`). If busy, skip immediately - the newer inputs
  below never override a busy session.
- (b) `Monitor.Activity.LastActive()` (source: `POST /v1/activity`, see below) - only counted if
  the most recent ping is within `Monitor.ActivityWindow` (or `activity.DefaultWindow` = 90s if
  unset); a fresh ping pushes idle-since forward to "now" (i.e. blocks suspend), a stale one
  (older than the window) contributes nothing.
- (c) `Monitor.LocalInput()` (source: `activity.LocalIdleTime`, Windows-only Win32
  `GetLastInputInfo`) - contributes `now - (time since last input)`. `activity.ErrUnsupported`
  (always returned on Linux/unix, see `local_unix.go`) is treated as "no signal", not an error,
  and logged only if it's some other failure.
- `resumeFloor` (existing resume-re-arm logic, unchanged - see below)

→ if `time.Since(idleSince) < cfg.Timeout`, skip → else `idle.Shutdowner.Shutdown()`

**Source (b): phone-app foreground ping.** `POST /v1/activity` (`api.handleActivity`, auth'd
like every other endpoint) → `activity.Tracker.Mark()` → `202 {}`. The Android app calls this
every 30s while, and only while, it is in the foreground - it must stop calling the instant it's
backgrounded, since a stale-but-still-arriving ping would defeat the whole point of source (b).
`activity.Tracker` is in-memory only, mutex-guarded, shared between the API handler goroutine
and the Monitor's ticker goroutine - liveness only, not worth persisting across a runner
restart (no recent ping after a restart is exactly "nobody's around right now", the correct
default).

**Source (c): local keyboard/mouse input.** `activity.LocalIdleTime()` is per-OS:
`local_windows.go` calls Win32 `GetLastInputInfo` (via `golang.org/x/sys/windows`'s
`NewLazySystemDLL`, no cgo) compared against `GetTickCount`; `local_unix.go` always returns
`activity.ErrUnsupported` - there is no portable, dependency-free way to read local input on
Linux for a headless/sessionless systemd service (no X11/Wayland session to read from, and
shelling out to something like `xprintidle` was deliberately rejected as fragile/an extra
runtime dependency). This is a documented gap, not a bug: the real Linux deployment target is a
system service with no console user in the first place, which is exactly the case where source
(c) doesn't matter - it falls back to (a) and (b) alone.

Shutdowner is build-tagged like `wol.PacketSender`: `shutdown_unix.go` (`systemctl suspend`) /
`shutdown_windows.go` (`rundll32.exe powrprof.dll,SetSuspendState 0,1,0` - hibernates instead of
suspending if hibernation is enabled on that machine). Real impl is `idle.DefaultShutdowner`;
tests inject a fake that just counts calls - **a test must never invoke a real Shutdowner**.

**Linux `systemctl suspend` needs polkit permission the runner doesn't have by default.**
Confirmed on the real target: the runner runs as a systemd *system* service with no login
session/logind seat, and its service user has no passwordless sudo - polkit refuses the suspend
outright. `shutdown_unix.go`'s `Shutdown` surfaces this as a specific error naming exactly what's
missing (a polkit rule granting `org.freedesktop.login1.suspend`, or a sudoers entry) rather than
a bare exit status, and it never falls back to `systemctl poweroff` - that would silently
escalate to the more disruptive S5 action this feature deliberately moved away from. Until that
polkit rule/sudoers entry is provisioned on a deployment, every real suspend attempt there will
fail loudly (logged by `Monitor.tick`) rather than actually suspending.

Once `Shutdown()` succeeds, the Monitor sets an internal `triggered` flag and never calls it
again for that process's lifetime. If `Shutdown()` itself errors (command failed to run, e.g.
the polkit gap above), `triggered` stays false and the next tick retries - see `Monitor.tick`'s
doc comment in idle.go.

Right before calling `Shutdown()`, `tick` invokes `Monitor.BeforeShutdown` if set - wired in
main.go to a best-effort `notifier.NotifyRunnerSuspending` push to every registered device (data
type `runner_suspending`, see shared/API.md "FCM message data.type values"). Fire-and-forget:
a failure is logged only, never blocks or cancels the actual shutdown - "if lost, then
lost" is the deliberate choice here (no queue, no retry, no delivery guarantee), since guaranteed
delivery would mean building real message durability for a notification whose entire value is
"heads up, right now."

**Manual suspend** (POST /v1/suspend, `api.handleSuspend`): the "I'm done, don't make me wait out
the timeout" counterpart to the automatic path above. Shares the same `beforeShutdown` closure
(best-effort push) and the same `idle.DefaultShutdowner`, but is invoked directly
from the API instead of through `Monitor`'s ticker. Two independent refusals, checked before
anything happens: `anyBusy()` (never suspend out from under a running session, `409` if busy -
this check applies regardless of the flag below) and `srv.Suspend == nil` (`503`) - `main.go` only
sets `srv.Suspend` when `idleCfg.Enabled` is true, deliberately reusing idle-suspend's own opt-in
gate rather than making a manual trigger always available. Reasoning: it's still "suspend this
host" either way, the same real action the idle package's doc comment already warns never to
enable outside an intentional deployment - a manual button doesn't change that risk, so it
shouldn't get its own, looser gate.

`api.anyBusy()` (shared by `handleRunnerInfo`'s `busy` field and `handleSuspend`'s refusal) only
counts `session.StateBusy` - an ACP session sitting `StateIdle` between turns
(process still alive, waiting for the next message) never blocks a suspend.

To change the idle threshold: env `RELAY_IDLE_TIMEOUT` (Go duration string, e.g. "45m";
default `idle.DefaultTimeout` = 3m — a starting heuristic, tune once real usage data exists)
To change the poll interval: env `RELAY_IDLE_CHECK_INTERVAL` (default `idle.DefaultCheckInterval` = 1m)
To enable this feature at all: env `RELAY_IDLE_SUSPEND_ENABLED=true` - **disabled by default,
see Technology notes below before ever setting this locally.**
To change the phone-activity freshness window: `internal/activity/activity.go`
(`activity.DefaultWindow` = 90s), or per-Monitor via `idle.Monitor.ActivityWindow`.
To change how local input is read (Windows): `internal/activity/local_windows.go`
(`LocalIdleTime`).

## Usage recording (decide whether wake/sleep is worth it)

Always-on per-minute activity log + turn spans → `GET /v1/usage` sleep simulation. Full flow:
`internal/usage/FLOWS.md`.

## Register an existing project folder + unscoped browse

Files: internal/project/project.go (`RegisterExisting`, `BrowseDir`), internal/project/roots_unix.go,
internal/project/roots_windows.go, internal/api/api.go (`handleCreateProject`, `handleBrowse`)

`POST /v1/projects {name, path}` → if `path` set, `Registry.RegisterExisting(path, name)` instead
of `Create` → validates `path` is absolute and an existing directory, defaults `name` to
`filepath.Base(path)` if empty, refuses a `path` already registered as another project → same
persisted `Project` shape either way, no marker distinguishing "scaffolded" vs "registered
existing" after the fact.

`GET /v1/browse?path=<abs>` → `project.BrowseDir` → deliberately **unscoped** (unlike
`ResolvePath`/file-viewing below) - its whole purpose is finding a directory to register before
any project-level scoping exists. Lists subdirectories only (never files), skips dotfiles,
`path` empty/omitted lists filesystem roots (`listRoots()`, build-tagged: `/` on Unix, existing
drive letters `A:\`-`Z:\` on Windows via `os.Stat` probing - stdlib only, no cgo).
`path=~` (`project.StartPath`) → `documentsDir()` → the user's Documents folder, which is where the
phone picker opens: Windows asks the shell (`KnownFolderPath(FOLDERID_Documents)` - follows
OneDrive redirection), Linux reads `XDG_DOCUMENTS_DIR` from `~/.config/user-dirs.dirs`; then
`~/Documents`, then `~`. Every listing carries `parent` (one level up; `""` = the drive list on
Windows; omitted at `/`) so the phone can walk up from Documents.

To change what's excluded from a browse listing (e.g. dotfiles): `project.go`'s `BrowseDir`.
To change root listing: `roots_unix.go` / `roots_windows.go`.
To change the start folder: `documentsDir()` in `project.go`, `knownDocumentsDir()` in `roots_*.go`.

## File viewing (read-only, scoped to a project dir)

Files: internal/project/project.go (`ResolvePath`), internal/api/api.go (`handleListFiles`,
`handleFileContent`)

GET /v1/projects/{id}/files?path=<rel> → `Registry.ResolvePath(id, path)` (rejects absolute
paths and any `../` escape via `filepath.Rel` + prefix check) → `os.ReadDir` → `FileEntry[]`
(name, isDir, size). `path` omitted/empty = project root.

GET /v1/projects/{id}/files/content?path=<rel> → same `ResolvePath` scoping → refuses a
directory, anything over `maxViewableFileSize` (1MiB), and anything that looks binary (a null
byte in the first 512 bytes, `isBinary`) → `FileContent{path, content}`.

Read-only by design, per ARCHITECTURE.md "Runner responsibilities" - no write/delete/rename
endpoint exists or is planned for v1.

To change the size cap: `internal/api/api.go` (`maxViewableFileSize`).
To change path-escape rules: `internal/project/project.go` (`Registry.ResolvePath`).

## Device registration + notify-on-finish

Files: registry.go, notifier.go, fcm.go

POST /v1/devices {fcmToken, runnerRef?} → api.handleRegisterDevice → notify.Registry.Register(fcmToken, runnerRef)
→ new device (server-generated id) or, if that token is already registered, updates its
  registeredAt + runnerRef in place (no duplicate) → persists the full device list to
  `$RELAY_HOME/devices.json` (atomic temp-file + rename, mode 0o600) → 200 Device

notify.NewRegistry() (called from `main.go`, unchanged signature) resolves the devices file
path itself — `$RELAY_HOME/devices.json`, falling back to `~/.relay/devices.json` when
`RELAY_HOME` is unset, matching `internal/config.relayHome()`'s resolution exactly — and loads
any existing devices from it at construction. Missing file → starts empty (not an error).
Corrupt/unparseable file → logs and starts empty rather than crashing the runner. Tests use
`notify.NewRegistryAt(path)` (path-injecting constructor, `NewRegistry()` delegates to it) with
`t.TempDir()` so they never touch a real `~/.relay`.

**Why this matters:** the phone registers its FCM token once, in `MainActivity.onCreate`. Before
this, a runner restart lost every registration and the runner couldn't push again until the user
happened to reopen the app — exactly when they don't need the notification. Now registrations
survive a runner restart.

session.Manager.awaitExit / session.Manager.Stop → on transition to StateFinished →
session.Manager.notifyFinished (async, via `Manager.OnFinished` hook set in main.go)
→ for each notify.Registry.List() device → notify.Notifier.NotifySessionFinished(device, session)
  (failures are logged, never block or fail the session transition)

Push data: every payload (`sessionFinishedData` / `sessionNeedsInputData` / `runnerSuspendingData`
in fcm.go) carries `runnerRef` = the device's registered value ("" if the phone never sent one)
→ the app maps a push back to the runner that sent it. devices.json files written before
runnerRef existed load fine (field just empty until the phone re-registers).

To change the notifier: env `RELAY_FCM_CREDENTIALS` → path to a Google service-account JSON key.
Unset/missing/unreadable/malformed → `notify.NewNotifier` returns a no-op that logs "FCM not
configured, skipping notification" and returns nil — this is the only notify path exercised by
tests; real FCM delivery is `[NOT IMPLEMENTED IN TESTS]` (no way to test it without live
credentials in CI).

A real Firebase project (`relay-sizonenko`) and service-account key now exist
(`runner/install/fcm-service-account.json`, gitignored, generated via `gcloud iam
service-accounts keys create` with role `roles/firebasecloudmessaging.admin`) — copy it to
wherever the runner actually runs and set `RELAY_FCM_CREDENTIALS` to its path to turn on real
sending. This has not yet been exercised against a real deployed runner + real session finish +
real phone.

## Install bootstrap (install.sh)

Files: install.sh

`install.sh` → checks `tailscale` → if missing, installs via official
`curl https://tailscale.com/install.sh | sh` (always latest, not pinned - see
Technology notes) → if not logged in, runs `tailscale up` itself and blocks,
printing the login URL to the same terminal you're running install.sh from
→ checks `claude`/`codex` CLIs (bootstrapping Node/npm via apt first if
needed) → npm-installs any missing (`@anthropic-ai/claude-code`,
`@openai/codex`) → installs the `relay-runner` binary + systemd unit,
auto-filling `RELAY_LISTEN_ADDR` in `/etc/relay/runner.env` from the
Tailscale IP → prints a boxed summary with the real runner key + address
read straight from `key.txt`, then (if both a key and a Tailscale IP exist)
runs `relay-runner -qr` to print a terminal QR code encoding
`relay://host:port?key=...` — scan it in the app instead of typing 64 hex
chars by hand.

**Login is NOT automated, by necessity, not oversight.** `tailscale up`
still needs a human to open a URL in a browser somewhere - no amount of
sudo can click a login link. install.sh gets you as close as possible: it
runs the command and surfaces the URL inline rather than making you run a
separate step. `claude auth login` / `codex login` are NOT run by
install.sh. Claude's login (first or expired) is done from the phone instead -
see "Claude re-login" below. Codex login from the phone: `[NOT IMPLEMENTED]`.

To change what gets auto-installed: `runner/install/install.sh` steps 1-2.

## Claude re-login (from the phone)

Files: internal/auth/auth.go, internal/api/auth.go, internal/session/acp_session.go (`RestartIdleAgents`), cmd/runnerd/main.go

For when Claude Code's login expires and nobody is at the PC (the "Claude login expired" push).

Start: POST /v1/auth/claude/start → `api.handleClaudeAuthStart` → `auth.Manager.Start` → already
`awaiting_code` and < 10 min old? return the same URL → else `FindCLI` (env `RELAY_CLAUDE_CLI`, else
PATH `claude` / `claude.cmd` / `claude.exe`) → `Runner.Start("claude auth login")` (stdin = pipe, no
TTY) → `login.watch` streams stdout+stderr, `ParseLoginURL` = first `https://…oauth/authorize…`
(ANSI stripped) → `{state:"awaiting_code", url}`. No URL within 20s → kill → `failed`.
To change timeouts: `Manager.URLTimeout` / `CodeTimeout` / `FinishTimeout` in `auth.NewManager`.

Phone opens the URL → signs in on claude.com → the platform.claude.com callback page shows a code
→ user pastes it in the app.

Finish: POST /v1/auth/claude/finish `{code}` → `Manager.Finish` → trim → write `code+"\n"` to the
CLI's stdin → wait ≤ 60s for exit → exit 0 or output contains "Login successful" → `signed_in` →
`OnSignedIn` → `session.Manager.RestartIdleAgents("claude")`; else `failed`, message = last 5
output lines (full output in the runner log). The process is always reaped (`login.done`).
Nobody finishes within 10 min → `Manager.expire` kills it → `failed` ("login timed out").

Restart agents: `RestartIdleAgents` → for each live ACP `claude` session NOT mid-turn (busy or
waiting on permission are skipped) → detach under `rec.mu` (`acp=nil`, `dormant=true`) → kill the
agent → `awaitACPExit` sees `rec.acp != client` → no "(agent process exited …)" note → next
message → `ensureAgent` respawns + `session/resume` with the new credentials. Busy sessions keep
their old agent (which may still fail on the expired token; the user retries after the turn).

Status: GET /v1/auth/claude → `Manager.Status` → `{state, url (only while awaiting_code),
message, loggedIn, email}`; loggedIn/email from `claude auth status --json` (10s timeout,
`ParseAuthStatus`: `loggedIn` must be literally `true`, first `email` found anywhere).
Errors: 400 empty code, 409 finish with no login in progress, 503 claude CLI not found.

Verified 2026-10 on Windows (Claude Code 2.1.226): piped stdin prints the URL + "Paste code here
if prompted >" and "Login successful." on success. The paste-from-another-device step itself is
not yet verified end-to-end - hence the CLI's last lines in every failure message.

## keeperd - keeps the runner running and up to date

Files: cmd/keeperd/main.go, internal/keeper/{config,supervisor,updater,proc_unix,proc_windows}.go,
install/install-keeperd.{sh,ps1}, .github/workflows/runner-release.yml

The process started at boot; the process-level counterpart of wakerd (wakerd wakes machines,
keeperd (re)starts runnerd).

Boot → keeperd → `LoadEnvFile(<dir>/relay.env)` → `Updater.Bootstrap` (downloads the runner if
missing, installs the ACP adapter into `<dir>/acp` via npm) → `Supervisor.Start` runs
`<dir>/bin/relay-runner` with keeperd's env + `RELAY_ACP_CLAUDE=<dir>/acp/.../claude-agent-acp`
→ runner dies → restarted (1s, or 10s if it died within 30s of starting).

Update loop (first check 2 min after boot, then every `RELAY_UPDATE_INTERVAL`, default 10m - a push reaches every machine within ~10 min; adapter `npm install` is skipped while a turn runs):
`Updater.Check` → GitHub `releases/latest` → runner build (`GET /v1/runner/info` `build`) or
keeperd `version` differs from the release tag? → wait until the runner has been idle
`RELAY_UPDATE_QUIET` (default 0 = only wait out a running turn; retried every 3 min - each retry is a GitHub API call, 60/h per public IP; a runner that doesn't answer
isn't waited for) → `npm install` adapter@latest →
- keeperd outdated → download + SHA256SUMS check → rename running binary to `.old`, new one in
  → stop runner → hand over (Linux: exit 3, systemd restarts; Windows: start the new keeperd
  detached, breaking away from the task's job object, then exit) → the new keeperd updates the
  runner on its next pass.
- runner outdated → download + verify → stop runner → `relay-runner` → `.old`, new one in →
  start → must answer `/v1/health` and stay up 10s within 60s, else **rollback** to `.old`.
- Windows locked `.old` (a process still running it can be renamed, not deleted): `clearOld`
  renames it to `.old-<unix>` and deletes such asides once unlocked. The input watcher polls
  `keeperd.exe`'s mtime/size and restarts itself from the new binary (`RelaunchWatcher`), so it
  never pins an old one. Before this fix, every update failed with "Access is denied" (2026-10-04).

Release: push to main touching `runner/**` (not `*.md`) → `runner-release.yml` → `go test` →
builds relay-runner/keeperd/wakerd for linux amd64/arm64/arm + windows amd64, tag
`runner-<sha7>` stamped into the binaries (`api.Build`, `main.version`) → `SHA256SUMS` →
GitHub Release marked latest.

**Linux (server):** `relay-runner@<user>` systemd unit, `ExecStart=/opt/relay/bin/keeperd`,
`Restart=always`, settings in `/etc/relay/runner.env`. One-time switch from the old direct-runner
unit: `sudo install/install-keeperd.sh <user>`. `/opt/relay` must be owned by the user so keeperd
can replace binaries without root. Logs: `journalctl -u relay-runner@<user> -f`.

**Windows (laptop):** install dir `%LOCALAPPDATA%\Relay` (bin/, acp/, relay.env, keeper.log).
One-time: `install/install-keeperd.ps1` in an elevated PowerShell → registers
- "Relay keeper": **at startup, no logon needed** (S4U - runs as the user without a stored
  password; Claude's login is a plain file so it works; no desktop access), no time limit,
  restart on failure;
- "Relay input watcher": at logon, `keeperd -watch-input` in the desktop session - posts
  `/v1/activity` every 30s while keyboard/mouse were used in the last minute, because the
  boot-time runner can't see desktop input (`GetLastInputInfo` is per-session). Without it
  idle-suspend would treat an in-use laptop as idle.
and removes the old Startup-folder shortcut (`start-relay-runner.ps1`, now legacy).

To change update timing: env `RELAY_UPDATE_INTERVAL`, `RELAY_UPDATE_QUIET`. To stop updates:
`RELAY_UPDATE_DISABLED=true` (supervision continues). Private repo: `RELAY_GITHUB_TOKEN`
(see LATER.md).

## OS sleep settings - TEMPORARY "never sleep" until the Pi waker exists

**Temporary workaround (2026-09-30), not the design.** The design is sleep-when-idle plus wake on
demand: the runner suspends its machine after `RELAY_IDLE_TIMEOUT` without work, and the phone
wakes it through `wakerd` on an always-on Raspberry Pi on the same LAN (see
`cmd/wakerd/FLOWS.md`, SHOPPING.md). No Pi exists yet, so a sleeping runner is unreachable
until someone physically wakes it. Until then **no runner machine ever sleeps**.

Current overrides:
- **Laptop (Windows, Modern Standby / S0 only - no S3):** `powercfg` standby + hibernate timeouts
  = 0 on AC and battery (were: standby 5 min AC / 3 min battery). This machine exposes **no
  lid-close setting** in `powercfg`, so what closing the lid does is untested. Relay idle-suspend
  off: `RELAY_IDLE_SUSPEND_ENABLED=false` in `%LOCALAPPDATA%\Relay\relay.env`. This also disables
  the phone's Sleep button (returns 503; same flag).
- **Server (Ubuntu, GNOME):** `gsettings org.gnome.settings-daemon.plugins.power
  sleep-inactive-{ac,battery}-type = 'nothing'` (were: AC `suspend` with timeout 0 = never,
  battery `suspend` after 15 min). Lid close was already `ignore` in logind, unchanged. Relay
  idle-suspend was never enabled there.

**Revert when** the Pi is running `wakerd` **and** waking each runner machine from the phone
has worked end to end (per machine: the laptop's wake path is still unproven, see SHOPPING.md;
a machine without a working wake path stays on never-sleep):
1. Laptop: `powercfg /change standby-timeout-ac 5`, `powercfg /change standby-timeout-dc 3`;
   in `relay.env` set `RELAY_IDLE_SUSPEND_ENABLED=true` and a short `RELAY_IDLE_TIMEOUT`
   (e.g. `30m`), then restart the "Relay keeper" task.
2. Server: `gsettings reset org.gnome.settings-daemon.plugins.power sleep-inactive-ac-type`
   and `... sleep-inactive-battery-type`; add `RELAY_IDLE_SUSPEND_ENABLED=true` +
   `RELAY_IDLE_TIMEOUT` to `/etc/relay/runner.env`; `sudo systemctl restart relay-runner@victor`.
3. Delete this section's "Current overrides" and update the Change Index.

## Windows Firewall silently blocks inbound connections from other devices

**Found the hard way (2026-09-19):** the phone app could not reach the laptop runner at all
(fast "connection refused"-style failure, ~1s), while every test from the laptop itself (`curl`
to its own Tailscale IP) worked fine. Root cause: `Get-NetFirewallRule -DisplayName "*relay*"`
showed **two enabled inbound Block rules** for `relay-runner-windows-amd64.exe`, scoped to the
`Public` profile - auto-generated by Windows the first time the exe listened on a port, because
nobody was present to click "Allow" on the interactive firewall prompt (the runner was started
headless/backgrounded). Tailscale's virtual adapter is classified `Public` by Windows, so this
specifically blocked every Tailscale peer (the phone) while same-host traffic (this laptop
connecting to its own Tailscale IP) bypassed the inbound path entirely and looked fine.

**Fix (needs an elevated/Administrator PowerShell - this is not something the runner or install
script can do for you without admin rights):**
```powershell
Remove-NetFirewallRule -DisplayName "relay-runner-windows-amd64.exe"
New-NetFirewallRule -DisplayName "Relay runner (7777)" -Direction Inbound -Protocol TCP -LocalPort 7777 -Action Allow -Profile Any
```
`[NOT IMPLEMENTED]`: running this automatically from `install.sh`'s Windows-equivalent setup
step (there isn't one yet - `install.sh` targets Linux/systemd only, see "Install bootstrap"
above; the laptop path today is entirely manual: build, run, Startup shortcut, and now this
firewall rule, each a separate one-off step documented here rather than scripted).

## Dev note: Docker + Git Bash on Windows

Any `docker run -v src:/dst ...` command from Git Bash (not PowerShell) needs
`MSYS_NO_PATHCONV=1` prefixed, e.g. `MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w
/app golang:1.22 go test ./...`. Without it, Git Bash's automatic POSIX-to-Windows path
conversion mangles the `src:/dst` bind-mount syntax into one broken semicolon-joined path
(`C:\...\runner;C:\Program Files\Git\app`), the command fails, and as a side effect leaves a
literal junk directory with that exact garbled name behind on disk (found and deleted twice
now - 2026-09-13 and 2026-09-16, both empty, harmless, not related to any code path).

## Technology notes

- **Claude re-login depends on the Claude CLI's output wording**: the URL is found by matching
  `https://…oauth/authorize…`, success by exit 0 or "Login successful". A CLI that changes the
  URL path, stops printing it without a TTY, or reads the code differently breaks Start/Finish -
  the failure message carries the CLI's last 5 lines (full output in the runner log).
- **The new credentials are written by the CLI itself** to the runner user's home
  (`~/.claude/.credentials.json`; on macOS the keychain). A runner running as a different user
  than the one whose Claude login the agents use would log the wrong account in. Agents only
  read credentials at start, hence `RestartIdleAgents`; a session busy at login time keeps the
  stale token until its agent is restarted.
- **One login at a time, in memory**: `auth.Manager` holds a single `claude auth login`
  process; a second Start returns the same URL. A runner restart kills the pending login (the
  phone must Start again). The process is killed after 10 min without a code.
- **The login code travels over plain HTTP** inside the Tailscale tunnel (WireGuard-encrypted),
  gated by the runner key like every other endpoint. Anyone holding the runner key could also
  start a login and attach their own Claude account to this runner.
- **`RELAY_CLAUDE_CLI`** overrides the binary path; a Task Scheduler/systemd runner may not have
  npm's/the installer's bin dir on PATH (503 "claude CLI not found" otherwise).

- **Quota/auth detection is substring matching on the provider's wording** (`session/problem.go`).
  If Claude Code rewords its limit/expiry message, the card silently degrades to a plain
  "Error: …" bubble (nothing breaks, it just stops being labelled). Only the ACP auth error code
  -32000 is a stable signal. Agent replies over 300 chars are never classified, so a real answer
  that mentions "usage limit" isn't mislabelled; a short one could be.

- **Auto-update trusts GitHub**: whoever can push to `main` (or publish a release) runs code on
  every runner machine within ~10 min. SHA256SUMS only guards against corrupted downloads - it's
  published by the same workflow. Accepted by the user 2026-09-30; token/private repo in LATER.md.
- **An update restarts the runner** as soon as no agent is mid-turn (`RELAY_UPDATE_QUIET` adds a grace period; phone foreground is not considered) - a session
  `waiting` on a permission counts as idle and loses the pending request (the session itself
  resumes). keeperd itself has no state.
- **keeperd "dev" builds never self-update** (only release-stamped ones do); a runner reporting
  any build other than the latest tag is replaced, including "dev".
- **The runner dies with keeperd**: Linux - systemd stops the whole cgroup. Windows - keeperd puts
  the runner in a kill-on-close job object (`bindToKeeper`), so a stopped, crashed or killed keeperd
  takes the runner and its agents down too. Before this (up to `runner-e7a50d4`) a stopped keeperd
  left an orphan runner holding the port, and the next keeperd's runner crash-looped on "bind".
  Agents spawned in the few ms before the runner joins the job would escape it (none do in practice).
- **Windows S4U task**: no stored password, but also no network credentials (SMB shares) and no
  DPAPI user secrets - fine for Relay (Claude login is `~/.claude/.credentials.json`), would break
  anything the runner needs from Credential Manager. Hard stop = kill (no SIGTERM on Windows);
  the runner tolerates it (2s save interval).
- **ACP agents need Node >= 22 on the runner machine** plus
  `npm i -g @agentclientprotocol/claude-agent-acp` (0.84.0 tested 2026-09-30). The runner finds
  `claude-agent-acp` on PATH - a Task Scheduler/systemd runner may have a different PATH than your
  shell (npm's global bin must be on it), else set `RELAY_ACP_CLAUDE` to the full path (on
  Windows the `.cmd` shim works). The adapter uses the machine's existing `claude` login (Claude
  Pro/Max subscription) - no `ANTHROPIC_API_KEY` needed; setting one would switch to API billing.
- **Every Claude session loads the user's global `~/.claude` config** (CLAUDE.md, skills, MCP
  servers) - ~50k tokens of context before the first message, the same as a local `claude`.
- **`bypassPermissions` is the default** (user's choice, 2026-09-30): Claude runs any command
  and edits any file without asking. The brake is the phone's Stop button (`session/cancel`).
  Main real risk: prompt injection from content Claude reads (web pages, repos). Set
  `RELAY_DEFAULT_MODE=default` to be asked first.
- **One turn at a time per session**: a message sent while a turn runs gets 409, not queued
  (the adapter supports queueing; not used).
- **Streaming is polling**: the app polls `GET /v1/sessions/{id}` every 1s while busy. No SSE.
- **Sessions persist to `$RELAY_HOME/sessions/*.json`** on the runner's own machine and resume
  after a restart (verified 2026-09-30: hard-killed runner, restarted, the resumed Claude still
  knew the earlier conversation). The runner's file is the transcript; the agent keeps its own
  conversation state separately (Claude: `~/.claude/projects/`). Deleting either breaks resume.
  No cleanup yet - files accumulate [NOT IMPLEMENTED: purge].
- **A hard kill loses at most ~2s of transcript** (the save interval). Agent processes exit
  on their own when the runner's pipe closes (observed: 0 orphans after `Stop-Process -Force`).
- **Graceful shutdown on Windows**: `Stop-Process` is always a hard kill; SIGINT only arrives
  from a console Ctrl+C. Fine given the above.
- **Project registry persists to disk** (`projects.json`), as do sessions (see above).
- **compose.Runner is an interface** specifically so `docker compose` calls are fakeable in
  tests — no real compose file has been exercised by the tests yet.
- **Container operation state is in-memory** (`Server.containerOps`): a runner restart mid-`up`
  forgets it was running (the docker command dies with the runner anyway). The *chosen file*
  is persisted in `projects.json` (`activeComposeFile`) and survives restarts.
- **One compose project per directory**: compose names the project after the directory, so dev
  and prod files in one folder share containers/networks/volumes names. That's why switch = down
  then up, and why running dev and prod side by side from one folder is not supported. Named
  volumes are shared between dev and prod unless the files name them differently.
- **Status matches on the `working_dir` label string**: a stack started by hand from a different
  path spelling (symlink, different drive-letter case on Windows) won't show up.
- **Only the top level is scanned**: compose files in subfolders (e.g. `deploy/`) aren't found.
- **`up` failures surface only as `lastError`** (last 5 lines of docker output), since the
  request returned `202` long before.
- **Auth is a single static key**, no rotation, no per-session scoping. Compromise of the key
  compromises the whole runner. Relies entirely on Tailscale for transport-level access control.
- **HTTP listener defaults to 127.0.0.1** for local dev/testing — deploying it bound to a
  Tailscale interface (per ARCHITECTURE.md) is a config/ops step (`RELAY_ADDR`), not enforced
  by the code itself.
- **Wake-on-LAN broadcast requires SO_BROADCAST**, which Go's `net` package doesn't set by
  default — sending a UDP packet to `255.255.255.255` without it fails with `EACCES` on Linux.
  Handled via two build-tagged files (`broadcast_unix.go`, `broadcast_windows.go`) that reach
  into the raw socket fd with `syscall.SetsockoptInt`, stdlib only (no cgo, no external dep) —
  matches the "single static binary, cross-compiles Linux/Windows" decision in ARCHITECTURE.md.
  A WoL broadcast never crosses a router — it only reaches the LAN segment the sender is
  physically on, which is why waking belongs to a device that is always on that segment (see
  ARCHITECTURE.md "Relay device"). **Nothing in the runner calls these primitives today** — the
  runner-to-runner `/v1/wake` endpoint was removed; they're kept for the separate Pi daemon.
- **Device registrations persist to disk** (`notify.Registry`, mutex-guarded map keyed by FCM
  token, written atomically to `$RELAY_HOME/devices.json`, mode 0600 — see `NewRegistryAt`).
  They survive a runner restart, like `session.Manager`'s transcripts. A missing file means
  "no devices yet" and a corrupt one logs and starts empty, so a bad file can never stop the
  runner coming up. The file holds push tokens, hence 0600.
- **FCM auth is a hand-rolled JWT-bearer OAuth2 exchange** (RFC 7523), not
  `golang.org/x/oauth2` — deliberately, to keep the runner's dependency graph at stdlib-only
  (`crypto/rsa`, `encoding/json`, `net/http`) rather than depend on module-fetch succeeding at
  build time for a path most installs never exercise. The access token is cached in-memory on
  the `fcmNotifier` and refreshed ~1 minute before its 1h expiry; that cache is also lost on
  restart (cheap to rebuild, one HTTP round-trip).
- **No-op is the default, not an error path.** Any problem with `RELAY_FCM_CREDENTIALS` (unset,
  file missing, malformed JSON, bad RSA key) silently degrades to logging
  `"FCM not configured, skipping notification"` — by design (ARCHITECTURE.md says an
  unconfigured Firebase project must never crash the runner or fail a session's finish
  transition). This also means a *misconfigured* (as opposed to *missing*) credentials file
  fails the same silent way — check the runner's logs, not an error response, if pushes aren't
  arriving.
- **Real FCM sending is untested** — there is no way to test an actual Google service-account
  token exchange or FCM delivery without a real Firebase project's credentials. Only the no-op
  path (env unset / file missing / malformed) is covered by `internal/notify`'s tests.
- **Idle-suspend is disabled by default and MUST stay that way unless a deployment explicitly
  wants it.** Setting `RELAY_IDLE_SUSPEND_ENABLED=true` on a dev machine, in CI, or in any
  Docker container running `runnerd` will genuinely try to run `systemctl suspend` /
  `rundll32.exe powrprof.dll,SetSuspendState 0,1,0` against whatever host/container can reach
  that command — there is no sandboxing inside `internal/idle` itself, the env-var gate in
  `cmd/runnerd/main.go` is the only thing standing between "idle" and the machine actually
  suspending. Never enable it outside a real, intentional bare-metal-or-VM runner deployment.
- **Suspend means S3/Modern Standby (sleep), not S5 poweroff.** The project switched away from
  S5 — the power delta between S3 and S5 is only ~1W, while S5 costs a ~30-60s boot to come back
  and is far more fragile to wake reliably. `internal/idle` has no way to bring the machine
  back itself once asleep — that needs an external WoL magic packet from a LAN-local device (a
  separate Pi daemon, not another runner). On Linux, the runner's systemd service user needs a
  polkit rule or sudoers entry granting `systemctl suspend` (no passwordless sudo means suspend
  fails loudly, not silently — see "Idle-suspend" above); on Windows, if hibernation is enabled
  the machine hibernates (S4) instead of sleeping (S3) — see `shutdown_windows.go`.
- **Phone-app activity (source b) and local input (source c) are both liveness signals, not
  persisted state.** `activity.Tracker` is a mutex-guarded in-memory timestamp; a runner restart
  forgets the last ping, which is correct (no recent ping is exactly "nobody's confirmed
  present"). Source (b) depends entirely on the Android app actually pinging every 30s while
  foregrounded and going silent when backgrounded — a client bug that keeps pinging from the
  background would make this signal lie and block suspend indefinitely. `activity.DefaultWindow`
  (90s) is a generous multiple of that 30s interval specifically so one dropped ping on a flaky
  mobile connection doesn't look like the app closed.
- **Local input detection (source c) is Windows-only by design, not an oversight.** There is no
  portable, dependency-free way to read keyboard/mouse activity on headless Linux — X11/Wayland
  both assume a logged-in graphical session, which the real systemd-service deployment target
  doesn't have, and shelling out to `xprintidle` was rejected as a fragile extra runtime
  dependency. `local_unix.go` always returns `activity.ErrUnsupported`; `idle.Monitor` treats
  that as "no signal available" and falls back to sources (a) and (b) alone — this is
  intentionally a no-op on Linux, not a broken feature.
- **The idle check interval and threshold are both plain durations, not adaptive.** No
  backoff, no jitter, no "only check when otherwise idle anyway" optimization — a 1-minute
  ticker forever once enabled. Fine given how cheap `session.Manager.IdleStatus()` is (just
  iterating in-memory session state), but worth knowing if `internal/session` ever grows
  expensive per-call state.
- **Tailscale is installed via their own official script, deliberately not pinned to a
  version.** Tailscale controls both the client and the coordination server, and
  guarantees backward compat for older clients — pinning would only make our install.sh
  stale, not safer. If Tailscale ever ships a breaking client change this assumption needs
  revisiting, but as of 2026-09-14 "always latest" is the right default for a single-user
  deployment like this one.
- **Rerunning install.sh always ends in `systemctl restart`, never just `enable --now`.**
  Caught during testing: on an already-active service, `enable --now` is a no-op that does
  NOT pick up a binary step 3 just overwrote - the process keeps executing its old,
  already-open inode (even if that file was deleted from disk, e.g. the old
  `/usr/local/bin/relay-runner`). Data over speculation: `journalctl` + `ps -o cmd -C
  relay-runner` (its cmdline points at a path `ls` says no longer exists) confirmed it
  before this was "fixed" without evidence.
- **The binary lives at `/opt/relay/bin/relay-runner`, owned by the run user, not
  `/usr/local/bin`.** The service runs as a non-root user (`User=%i`), so keeping the
  binary under a user-owned directory means an upgrade never needs root. install.sh
  removes any stale binary left at the old `/usr/local/bin` location.
- **The runner has one external dependency: `github.com/mdp/qrterminal/v3`**, added
  specifically for `-qr` (terminal QR code rendering for pairing - see "Install
  bootstrap"). Everything else stayed stdlib-only by design (see below); this was a
  deliberate, small exception because hand-rolling QR encoding (Reed-Solomon error
  correction etc.) isn't something to redo reliably. Pure Go, no cgo - doesn't affect
  the single-static-binary cross-compile property.
- **`claude`/`codex` CLI auto-install also bootstraps Node.js/npm itself** via `apt-get
  install nodejs npm` if missing — true zero-prerequisite single-command deploy on
  apt-based systems (Debian/Ubuntu, which is what's actually targeted). On a non-apt
  system the CLI-install step is skipped with a message instead of guessing a package
  manager; everything else in install.sh still runs.

## Change Index

| Thing | Where |
|---|---|
| Key generation / storage | `internal/config/config.go` |
| Projects root / registry file location | `internal/config/config.go` (env `RELAY_HOME`) |
| Listen address | `internal/config/config.go` (env `RELAY_LISTEN_ADDR`) |
| Quota/auth problem detection (`Message.kind`, push `problem`) | `internal/session/problem.go` (`problemKind`, `markProblemReply`, `LastProblem`) |
| Provider → command mapping | `internal/session/session.go` (`resolveProvider`, `autoDetectProviders`, env `RELAY_PROVIDER_<NAME>` override) |
| ACP wire client (JSON-RPC over stdio) | `internal/acp/acp.go` (`Client.Call`, `readLoop`) |
| ACP method/field shapes | `internal/acp/types.go` |
| Transcript from streamed updates (text merge, tool rows) | `internal/session/acp_session.go` (`onUpdate`) |
| Permission request → waiting → answer | `acp_session.go` (`onRequest`, `RespondPermission`) |
| Stop button (cancel turn) | `acp_session.go` (`Cancel`), `POST /v1/sessions/{id}/cancel` |
| Default permission mode | env `RELAY_DEFAULT_MODE` (default `bypassPermissions`), `session.NewManager` |
| Which command runs for a provider | `session.autoDetectProviders`, env `RELAY_ACP_<NAME>` / `RELAY_PROVIDER_<NAME>` |
| TEMPORARY never-sleep overrides (until the Pi waker) | "OS sleep settings" section above: `powercfg`, GNOME `gsettings`, `RELAY_IDLE_SUSPEND_ENABLED` |
| Session files, save/heartbeat intervals | `internal/session/store.go` |
| Resume a dormant session | `acp_session.go` (`ensureAgent`), `acp.Client.ResumeSession` |
| Runner supervision / restart backoff | `internal/keeper/supervisor.go` |
| Runner dies with keeperd (Windows job object) | `internal/keeper/proc_windows.go` (`bindToKeeper`) |
| Update check, idle gate, swap + rollback, adapter npm install | `internal/keeper/updater.go` (`Check`, `replaceRunner`, `replaceKeeper`) |
| keeperd settings (dir, repo, token, intervals) | `internal/keeper/config.go`, env `RELAY_KEEPER_DIR` / `RELAY_UPDATE_*` / `RELAY_GITHUB_TOKEN`, file `<dir>/relay.env` |
| Release build targets / version stamping | `.github/workflows/runner-release.yml` |
| Windows boot task + input watcher | `install/install-keeperd.ps1`, `keeperd -watch-input` (`cmd/keeperd/main.go`) |
| Linux unit | `install/relay-runner.service`, `install/install-keeperd.sh` |
| Needs-input push | `cmd/runnerd/main.go` (`sessions.OnNeedsInput`), `notify.NotifySessionNeedsInput` |
| Auth header check | `internal/api/api.go` middleware |
| Docker compose invocation (up/down/ps args) | `internal/compose/compose.go` (`UpWith`, `DownWith`, `StatusWith`) |
| Which file names count as compose files | `compose.composeFileRe` |
| Default file when several and none chosen | `compose.defaultNames` / `compose.DefaultFile` |
| Switch/start/stop semantics, 409 on concurrent ops | `internal/api/containers.go` |
| Persisted chosen compose file | `project.Project.ActiveComposeFile`, `Registry.SetActiveComposeFile` |
| HTTP endpoint routing | `internal/api/api.go` (must match `shared/API.md`) |
| WoL primitives (unused by the runner API today) | `internal/wol/wol.go` (`BuildMagicPacket`) |
| WoL broadcast send / SO_BROADCAST | `internal/wol/broadcast_unix.go`, `broadcast_windows.go` |
| WoL target port / broadcast address | `internal/wol/wol.go` constants |
| Device registration (dedupe by token) | `internal/notify/registry.go` (`Registry.Register`) |
| Device `runnerRef` (stored per device, echoed in pushes) | `internal/notify/registry.go` (`Device.RunnerRef`), `internal/api/api.go` (`registerDeviceRequest`) |
| FCM data payload fields per push type | `internal/notify/fcm.go` (`sessionFinishedData`, `sessionNeedsInputData`, `runnerSuspendingData`) |
| Runner-wide session list (filter/sort) | `internal/session/summary.go` (`Manager.ListAll`, `SortForAttention`), `internal/api/api.go` (`handleListAllSessions`) |
| Session `title` / `preview` derivation + rune caps | `internal/session/summary.go` (`summarize`, `oneLine`, `titleMaxRunes`, `previewMaxRunes`), called from `cloneSession` |
| Device registry persistence path | env `RELAY_HOME` → `internal/notify/registry.go` (`devicesFilePath`), falls back to `~/.relay/devices.json` |
| Device registry path-injecting constructor (tests) | `internal/notify/registry.go` (`NewRegistryAt`) |
| FCM credential path | env `RELAY_FCM_CREDENTIALS` → `internal/notify/notifier.go` (`NewNotifier`) |
| FCM JWT/OAuth2 exchange | `internal/notify/fcm.go` (`exchangeJWT`, `accessTokenFor`) |
| Session-finish → notify wiring | `cmd/runnerd/main.go` (`sessions.OnFinished`), `internal/session/session.go` (`notifyFinished`) |
| Idle-suspend enable flag | env `RELAY_IDLE_SUSPEND_ENABLED` → `internal/idle/idle.go` (`LoadConfig`) — disabled unless exactly `"true"` |
| Idle timeout / check interval | env `RELAY_IDLE_TIMEOUT`, `RELAY_IDLE_CHECK_INTERVAL` → `internal/idle/idle.go` (`LoadConfig`, `DefaultTimeout`, `DefaultCheckInterval`) |
| Pairing QR content/rendering | `cmd/runnerd/main.go` (`printPairingQR`, flag `-qr`) |
| Tailscale / Node / CLI bootstrap | `runner/install/install.sh` steps 1-2 |
| Best-effort "runner suspending" push | `cmd/runnerd/main.go` (`Monitor.BeforeShutdown` wiring), `internal/notify/fcm.go` (`NotifyRunnerSuspending`) |
| Manual suspend endpoint / busy refusal / enable gate | `internal/api/api.go` (`handleSuspend`, `anyBusy`, `Server.Suspend`), `cmd/runnerd/main.go` (`srv.Suspend` wiring) |
| Idle/busy decision source | `internal/session/session.go` (`Manager.IdleStatus`) — read-only, derived from existing session state |
| Shutdown/suspend command (sleep, S3/Modern Standby) | `internal/idle/shutdown_unix.go` (`systemctl suspend`), `internal/idle/shutdown_windows.go` (`rundll32.exe powrprof.dll,SetSuspendState 0,1,0`) |
| Per-turn busy/idle tracking, turn-end detection | `internal/session/session.go` (`Manager.finishTurn`, `decodeClaudeStreamJSONEventType`, `record.idleSince`) |
| Idle-suspend ticker wiring | `cmd/runnerd/main.go` (`idle.NewMonitor(...).Run()`, gated on `idleCfg.Enabled`) |
| Phone-app activity endpoint / tracker | `internal/api/api.go` (`handleActivity`), `internal/activity/activity.go` (`Tracker.Mark`, `Tracker.LastActive`) |
| Phone-app activity freshness window | `internal/activity/activity.go` (`DefaultWindow` = 90s), `internal/idle/idle.go` (`Monitor.ActivityWindow`) |
| Local keyboard/mouse input detection (Windows) | `internal/activity/local_windows.go` (`LocalIdleTime`, Win32 `GetLastInputInfo`) |
| Local input detection unsupported on Linux | `internal/activity/local_unix.go` (`ErrUnsupported`) |
| Idle decision combining sources (a)/(b)/(c) + resumeFloor | `internal/idle/idle.go` (`Monitor.tick`) |
| File listing / content endpoints | `internal/api/api.go` (`handleListFiles`, `handleFileContent`) |
| Project-path escape guard | `internal/project/project.go` (`Registry.ResolvePath`) |
| Viewable file size cap | `internal/api/api.go` (`maxViewableFileSize`) |
| Register existing folder as a project | `internal/project/project.go` (`RegisterExisting`), `internal/api/api.go` (`handleCreateProject`) |
| Unscoped directory browse (project picking) | `internal/project/project.go` (`BrowseDir`), `internal/api/api.go` (`handleBrowse`) |
| Filesystem roots listing | `internal/project/roots_unix.go`, `roots_windows.go` (`listRoots`) |
| Folder picker start folder (Documents) | `internal/project/project.go` (`documentsDir`, `StartPath`), `roots_*.go` (`knownDocumentsDir`) |
| Claude re-login endpoints / HTTP status mapping | `internal/api/auth.go` (`handleClaudeAuth*`, `writeAuthError`) |
| Claude login process, state machine, timeouts | `internal/auth/auth.go` (`Manager.Start`/`Finish`/`expire`, `NewManager`) |
| Login URL detection | `internal/auth/auth.go` (`ParseLoginURL`, `loginRe`, `ansiRe`) |
| `claude auth status --json` parsing | `internal/auth/auth.go` (`ParseAuthStatus`) |
| claude binary location | env `RELAY_CLAUDE_CLI` → `internal/auth/auth.go` (`FindCLI`) |
| Restart idle agents after login | `internal/session/acp_session.go` (`RestartIdleAgents`), wired in `cmd/runnerd/main.go` (`claudeAuth.OnSignedIn`) |
| Fake CLI in tests | `auth.Runner` / `auth.Proc` interfaces (`internal/auth/auth_test.go`) |
| Laptop auto-start at boot | `install/install-keeperd.ps1` ("Relay keeper" task, see "keeperd"); `start-relay-runner.ps1` is legacy |
