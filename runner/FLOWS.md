# Runner flows

Files: main.go, config.go, project.go, session.go, history.go, compose.go, api.go, wol.go,
localmac.go, registry.go, notifier.go, fcm.go, idle.go, shutdown_unix.go, shutdown_windows.go,
selfupdate.go, uptime.go

## Startup

main() → config.Load() → generates ~/.relay/key.txt + projects.json if absent, prints key once
→ **`-qr` flag check**: if present, `printPairingQR` and exit, skipping everything below (see
"Install bootstrap") → **self-update check** (`selfupdate.CheckOnce`, unless
`RELAY_AUTO_UPDATE_ENABLED=false` - see "Self-update"); an update found here exits before the
server ever starts → api.NewServer(project.Registry, session.Manager) →
http.ListenAndServe(config.ListenAddr) → goroutine: time.Ticker(1h) →
session.Manager.PurgeFinishedBefore(7d) → goroutine: `idle.NewMonitor(...).Run()` if
`RELAY_IDLE_SUSPEND_ENABLED=true` (see "Idle-suspend") → goroutine:
`selfupdate.RunPeriodically` (see "Self-update")

To change key/projects root: config.Load() (env `RELAY_HOME`)
To change listen address: config.Load() (env `RELAY_LISTEN_ADDR`, default 127.0.0.1:7777 —
binds to Tailscale interface only in production, not enforced by code)
To change purge cutoff: history.PurgeOlderThan() call site in main.go

## Request path

phone app → HTTP + `X-Relay-Key` header → api.go middleware checks key (401 if wrong, `/v1/health`
exempt) → routed via Go 1.22 ServeMux method+pattern → project.Registry / session.Manager /
compose.Runner

## Session lifecycle

POST /v1/projects/{id}/sessions → session.Manager.Start(projectID, provider)
→ provider resolved (`resolveProvider`), in order: (1) env `RELAY_PROVIDER_<NAME>` explicit
  override → `sh -c <value>`, (2) built-in "echo-agent" test provider (`sh -c cat`), (3)
  **auto-detect**: for `"claude"`/`"codex"` specifically, `exec.LookPath` on PATH right now - no
  env var needed at all if the CLI is installed where the runner's user can see it (see
  `autoDetectProviders`) → os/exec.Command spawned, scoped to project dir
→ stdout/stderr scanner goroutine appends to in-memory transcript (role "agent")
→ state: busy → idle/finished/error

POST /v1/sessions/{id}/message → session.Manager.SendMessage → io.WriteString to subprocess stdin
POST /v1/sessions/{id}/stop → session.Manager.Stop → kill process, state → finished

**No manual setup needed for claude/codex** as long as the CLI is on PATH - confirmed this is
exactly how `os/exec.Command` already resolves a bare name with no path separators, the same
mechanism `StartAuthLogin`'s `defaultAuthLoginCommand` already relied on. `RELAY_PROVIDER_<NAME>`
still exists to override the bare command (custom install path, extra flags) or add an
unlisted provider - checked first, so an explicit override always wins over auto-detection.
If the CLI genuinely isn't installed, starting a session fails with a clear "not found on PATH"
error rather than silently falling back to anything.

**"claude" needs a wire-format codec, not just a bare command.** A bare `claude` with piped
stdin and no flags silently runs in one-shot `--print` mode and errors if no input arrives
within a few seconds - discovered exactly this way the first time auto-detect was tried end to
end. Real multi-turn chat over one persistent process needs
`--print --input-format=stream-json --output-format=stream-json` (each line in and out is a
JSON event, not plain text) - confirmed by hand (2026-09-19) piping one probe message through it
directly, then again through the full runner→session→API path (two real messages in the same
session, second one correctly referencing the first - see the test transcript in this session's
history if you need the exact request/response). `autoDetectProviders["claude"]` carries both
the flags and `ProviderCommand.Codec = codecClaudeStreamJSON`; `SendMessage` encodes outgoing
text via `encodeClaudeStreamJSONUserMessage`, `pump` decodes incoming lines via
`decodeClaudeStreamJSONLine` (only `"assistant"`-type events' text content survives into the
transcript - `"system"`/`"rate_limit_event"`/`"result"` events and anything that fails to parse
as JSON are dropped, never shown as raw JSON on the phone). `[NOT IMPLEMENTED]`: surfacing
tool-use/tool-result content blocks in the transcript - only assistant text is shown for v1.
"codex" has no confirmed equivalent wire format yet and stays raw text/no-args - **untested
end-to-end**, likely to hit the same one-shot-vs-persistent problem claude did until someone
actually runs it and checks.

## Wake-on-LAN

POST /v1/wake → api.handleWake → wol.BuildMagicPacket(mac) (6×0xFF + MAC×16, 102 bytes)
→ wol.PacketSender.SendBroadcast → UDP broadcast to 255.255.255.255:9 (SO_BROADCAST enabled via
  build-tagged broadcast_unix.go/broadcast_windows.go, stdlib syscall only)
→ 202 on success, 400 on malformed MAC (before any send is attempted), 500 if the send itself fails

To change the sender: `api.Server.Sender` (`wol.PacketSender` interface) — real impl is
`wol.DefaultSender`, tests inject a fake, same pattern as `compose.Runner`.
To change target port/broadcast address: `wol.go` constants `discardPort`/`broadcastAddr`.

**Own-MAC detection for pairing** (`wol.LocalMAC`, in `localmac.go`): picks this machine's
own real NIC's MAC to embed in the pairing QR (`printPairingQR` in `cmd/runnerd/main.go`) -
answers "what MAC does another runner need to wake *this* machine" without the phone user
looking it up and typing it in by hand. Heuristic: first up, non-loopback, non-virtual
(`virtualIfacePrefixes` - skips `tailscale*`, `docker*`, `veth*`, etc.) interface that has an
assigned IP address. Wrong on a genuinely multi-NIC machine, or a WiFi-only machine whose
chipset doesn't support WoL at all regardless of MAC - the app's manual "Edit" affordance on
RunnerListScreen is the fallback either way. To change what counts as virtual: `localmac.go`'s
`virtualIfacePrefixes`.

**Same-LAN detection for wake-via auto-match** (`wol.LocalSubnet`, in `localmac.go`): shares
`LocalMAC`'s interface-picking heuristic (factored into `pickLANInterface`) but reports the
interface's IPv4 network in CIDR form instead of its hardware address - e.g. `192.168.1.0/24`.
Surfaced via `GET /v1/runner/info`'s `localSubnet` field. When a new runner is paired, the app
fetches this field from every already-known, reachable runner and compares it against the new
runner's own; exactly one match auto-sets `wakeViaRunnerId` on both runners, replacing the manual
"Edit" step (see android/FLOWS.md "Wake-on-LAN"). Same fallback as the MAC case: detection
failure, or zero/multiple matching runners, just skips the auto-fill and leaves manual Edit as
the fallback - never guesses wrong silently.

**Bug found and fixed (2026-09-19): `pickLANInterface` returned `addrs[0]` without checking IP
version.** On Windows, an interface's `Addrs()` commonly lists its IPv6 link-local address
(`fe80::...`) before its IPv4 one - confirmed by hand on a real laptop (`WiFi` interface, real
addr `192.168.1.40/24`, but `addrs[0]` was `fe80::.../64`). `LocalSubnet`'s `ipNet.IP.To4() ==
nil` check then always failed, so `GET /v1/runner/info` silently omitted `localSubnet` on every
Windows runner, which meant `WakeViaMatcher` could never auto-match a Windows runner against
anything, even a genuine same-LAN pair (this is exactly what happened during testing: laptop and
server were on the same `192.168.1.0/24` network the whole time, but auto-match silently never
fired because the laptop's half of the comparison was always missing - the phone app then showed
"wake not configured" on every runner with no way to tell why). `LocalMAC` happened to keep
working by accident - it only needed the right *interface*, not the right *address*, and
`pickLANInterface` picked the interface correctly the whole time. Fixed via `firstIPv4Addr`,
which searches an interface's address list for one that's actually IPv4 instead of trusting list
order - see `TestFirstIPv4AddrSkipsLeadingIPv6` in `localmac_test.go`.
**Runners paired before this fix keep whatever `wakeMac`/`wakeViaRunnerId` they got (likely
none, on Windows) - re-pair (or use "Wake settings") to pick up a corrected auto-match.**

## Idle-suspend (S5) - the other half of the sleep path

**This is the missing half of ARCHITECTURE.md's "Sleep path" line** - WoL (`internal/wol`)
wakes a machine back up; `internal/idle` is what puts it to sleep (S5, full poweroff) in the
first place. The two only make sense together: once a runner powers itself off via this
package, the *only* way back is a WoL magic packet sent from another LAN-local peer's
`/v1/wake` (see the Wake-on-LAN section above and ARCHITECTURE.md "Relay device") - there is
no self-wake. Cross-reference both sections if you're touching either.

main() → `idle.LoadConfig()` (env `RELAY_IDLE_SUSPEND_ENABLED`, `RELAY_IDLE_TIMEOUT`,
`RELAY_IDLE_CHECK_INTERVAL`) → only if `Enabled` → goroutine: `idle.NewMonitor(sessions,
idle.DefaultShutdowner, cfg).Run()` (ticker at `cfg.CheckInterval`, same wiring style as the
history-purge ticker above, just conditionally started)

Each tick → `session.Manager.IdleStatus()` (busy bool, idleSince time.Time - computed from
existing session state, not separately tracked; see its doc comment in session.go for why the
max `FinishedAt` across sessions is equivalent to "when did busy-count last hit zero") → if
busy, skip → if `time.Since(idleSince) < cfg.Timeout`, skip → else `idle.Shutdowner.Shutdown()`

Shutdowner is build-tagged like `wol.PacketSender`: `shutdown_unix.go` (`systemctl poweroff`,
falling back to `shutdown -h now` if systemctl isn't on PATH) / `shutdown_windows.go`
(`shutdown /s /t 0`). Real impl is `idle.DefaultShutdowner`; tests inject a fake that just
counts calls - **a test must never invoke a real Shutdowner**.

Once `Shutdown()` succeeds, the Monitor sets an internal `triggered` flag and never calls it
again for that process's lifetime (a poweroff should end the process anyway). If `Shutdown()`
itself errors (command failed to run), `triggered` stays false and the next tick retries -
see `Monitor.tick`'s doc comment in idle.go.

Right before calling `Shutdown()`, `tick` invokes `Monitor.BeforeShutdown` if set - wired in
main.go to (1) `uptimeStore.Close()` (see "Uptime tracking" below), and (2) a best-effort
`notifier.NotifyRunnerSuspending` push to every registered device (data type
`runner_suspending`, see shared/API.md "FCM message data.type values"). Both are fire-and-forget:
a failure in either is logged only, never blocks or cancels the actual shutdown - "if lost, then
lost" is the deliberate choice here (no queue, no retry, no delivery guarantee), since guaranteed
delivery would mean building real message durability for a notification whose entire value is
"heads up, right now."

**Manual suspend** (POST /v1/suspend, `api.handleSuspend`): the "I'm done, don't make me wait out
the timeout" counterpart to the automatic path above. Shares the same `beforeShutdown` closure
(uptime close + best-effort push) and the same `idle.DefaultShutdowner`, but is invoked directly
from the API instead of through `Monitor`'s ticker. Two independent refusals, checked before
anything happens: `anyBusy()` (never suspend out from under a running session, `409` if busy -
this check applies regardless of the flag below) and `srv.Suspend == nil` (`503`) - `main.go` only
sets `srv.Suspend` when `idleCfg.Enabled` is true, deliberately reusing idle-suspend's own opt-in
gate rather than making a manual trigger always available. Reasoning: it's still "run `systemctl
poweroff` on this host" either way, the same real action the idle package's doc comment already
warns never to enable outside an intentional deployment - a manual button doesn't change that
risk, so it shouldn't get its own, looser gate.

## Uptime tracking

Files: internal/uptime/uptime.go

`main()` → `uptime.Open(~/.relay/uptime.json)` at startup → appends a new open
`{start: now}` interval; if the last run's interval was left open (crash, power loss - it never
got to call `Close`), that orphaned interval is closed at load time using the current time as its
`end` (undercounts that one interval slightly, accepted per package doc - **this is deliberately
a short-term buffer, not the dashboard's source of truth**, see next paragraph) →
`idle.Monitor.BeforeShutdown` calls `uptimeStore.Close()` on a clean idle-suspend, sealing that
interval accurately instead of relying on the crash-recovery fallback.

GET /v1/uptime → `api.handleUptime` → `s.Uptime.List()` → `200 UptimeInterval[]`.

**Why the runner only keeps ~14 days (`uptime.MaxAge`), not forever:** a sleeping runner is
exactly the runner you can't ask for history, so the *phone* app is the one that persists a
durable weekly view - it pulls this endpoint whenever a runner happens to be reachable and keeps
its own merged copy locally (see android/FLOWS.md "Dashboard"). The runner's file is purely a
catch-up buffer for whatever the phone hasn't synced yet; purged the same way session history is.

To change the buffer window: `internal/uptime/uptime.go` (`MaxAge`).

To change the idle threshold: env `RELAY_IDLE_TIMEOUT` (Go duration string, e.g. "45m";
default `idle.DefaultTimeout` = 3m — a starting heuristic, tune once real usage data exists)
To change the poll interval: env `RELAY_IDLE_CHECK_INTERVAL` (default `idle.DefaultCheckInterval` = 1m)
To enable this feature at all: env `RELAY_IDLE_SUSPEND_ENABLED=true` - **disabled by default,
see Technology notes below before ever setting this locally.**

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

To change what's excluded from a browse listing (e.g. dotfiles): `project.go`'s `BrowseDir`.
To change root listing: `roots_unix.go` / `roots_windows.go`.

## Auth-login (headless OAuth for claude/codex CLI)

Files: internal/session/session.go (`StartAuthLogin`, `authLoginProvider`,
`defaultAuthLoginCommand`), internal/api/api.go (`handleAuthLogin`)

**The problem this solves:** a deployed runner has no local browser, but `claude auth login`
still wants one. Confirmed by hand (2026-09-19, isolated Docker container, `node:20-slim` +
`npm install -g @anthropic-ai/claude-code`) that headless `claude auth login` prints an OAuth
URL to stdout (`Opening browser to sign in… If the browser didn't open, visit: https://...`)
then blocks reading a pasted authorization code from stdin, retrying with a fresh URL on an
invalid code rather than exiting - i.e. exactly the shape `session.Manager`'s existing
stdout-pump / stdin-write session machinery already handles. No new subprocess mechanism was
needed, just a different command run through it.

`POST /v1/auth/login` → `Sessions.StartAuthLogin(s.Home)` → registers a synthetic
`auth-login` pseudo-provider (not a real project's CLI agent) mapped to `defaultAuthLoginCommand`
(`claude auth login`), then calls the same internal `Manager.start(projectID="", provider,
dir=s.Home)` every project session uses → returns a `Session` with `projectId: ""`.

That session is polled/messaged through the **same generic endpoints** as any project session -
`GET /v1/sessions/{id}` (the OAuth URL shows up as an "agent" `Message`) and
`POST /v1/sessions/{id}/message` (writes the pasted code to the CLI's stdin) - neither is
scoped by project, so no new session-handling code was needed beyond the start path.

To change the command: env `RELAY_PROVIDER_AUTH_LOGIN` (same `RELAY_PROVIDER_<NAME>` convention
every other provider uses, resolved via `resolveProvider`), e.g. to point at `codex login`
instead, or to test with a fake command.

**Not yet done:** where `claude auth login` actually persists credentials on a headless Linux
box (file vs. OS keychain call that might behave differently with no desktop session) hasn't
been checked - do that before relying on this against the real server.

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

## Bulk container start/stop (per-runner, all projects)

POST /v1/containers/start-all / POST /v1/containers/stop-all → api.handleContainersStartAll /
handleContainersStopAll → `runComposeAll` loops `s.Projects.List()`, calling `compose.Start`/
`compose.Stop` per project dir → returns `[{projectId, ok, error?}]`, one entry per project.
Best-effort by design: one project without a compose file (or docker not reachable) must not
block the others - mirrors `runCompose`'s per-project error shape but as a list instead of a
single error, since there's no single "the request failed" outcome across N independent
projects.

To change: `internal/api/api.go` (`runComposeAll`).

## Device registration + notify-on-finish

POST /v1/devices → api.handleRegisterDevice → notify.Registry.Register(fcmToken)
→ new device (server-generated id) or, if that token is already registered, updates its
  registeredAt in place (no duplicate) → 200 Device

session.Manager.awaitExit / session.Manager.Stop → on transition to StateFinished →
session.Manager.notifyFinished (async, via `Manager.OnFinished` hook set in main.go)
→ for each notify.Registry.List() device → notify.Notifier.NotifySessionFinished(device, session)
  (failures are logged, never block or fail the session transition)

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
`relay://host:port?key=...&mac=...` (mac from `wol.LocalMAC`, omitted if
detection fails) — scan it in the app instead of typing 64 hex chars and a
MAC address by hand.

**Login is NOT automated, by necessity, not oversight.** `tailscale up`
still needs a human to open a URL in a browser somewhere - no amount of
sudo can click a login link. install.sh gets you as close as possible: it
runs the command and surfaces the URL inline rather than making you run a
separate step. `claude auth login` / `codex login` are NOT run by
install.sh at all yet. `[NOT IMPLEMENTED]`: relaying that login URL to the
phone app so auth can be completed without physical/SSH access to the
server. Design depends on what `claude auth login` actually prints/does
when run headless (no local browser) - untested as of 2026-09-14, verify on
the real server before building the relay.

To change what gets auto-installed: `runner/install/install.sh` steps 1-2.

## Self-update

Files: internal/selfupdate/selfupdate.go, cmd/runnerd/main.go,
install/relay-runner.service, .github/workflows/runner-release.yml

`main()` → `selfupdate.CheckOnce(version)` at startup → if a newer
`runner-<sha>` release exists on GitHub, downloads the binary for its own
OS/arch → verifies against `checksums.txt` → atomically renames over its
own executable → exits 0 → systemd (`Restart=always`) brings the new
binary up. Then `selfupdate.RunPeriodically` repeats the check every
`RELAY_UPDATE_CHECK_INTERVAL` (default 10m) in a goroutine for as long as
the process runs.

**Pull, not push, deliberately.** The server checks GitHub over plain
HTTPS outbound; nothing reaches in, and no SSH key to this machine is
stored anywhere in GitHub. See the package doc comment in selfupdate.go for
the full reasoning (a leaked push-deploy SSH secret would mean remote code
execution on this box; a compromised GitHub release can only do what the
runner already does).

`version` is `"dev"` on a plain `go build` - self-update is a hard no-op for
that (`devVersion` check in `CheckOnce`), so a developer's local build is
never silently overwritten. Only `.github/workflows/runner-release.yml`
builds with a real version (`-ldflags -X main.version=runner-<short-sha>`)
and publishes it as a GitHub Release with `checksums.txt` attached - that's
what a deployed runner is actually polling for.

To disable on a given machine: `RELAY_AUTO_UPDATE_ENABLED=false` in
`/etc/relay/runner.env`, then `systemctl restart relay-runner@<user>`.

To change the check interval: `RELAY_UPDATE_CHECK_INTERVAL=<duration>`
(e.g. `1h`), same env file.

## Auto-restart on machine restart (both platforms)

**Server (systemd):** already covered by the existing install - `relay-runner@<user>.service` is
`enabled` (survives a full reboot, not just a crash - `Restart=always` in the unit only covers
crashes while already running) and was confirmed `enabled` on the actual deployed server
(2026-09-19). No action needed after a full power-off/on cycle.

**Laptop (Windows, no systemd equivalent):** a Scheduled Task needs elevated
(`Register-ScheduledTask`) rights this environment doesn't have (`Access is denied` even without
`-RunLevel Highest`), so the fallback is a Startup-folder shortcut instead -
`start-relay-runner.ps1` (sets `RELAY_LISTEN_ADDR` to this laptop's Tailscale IP, then
`Start-Process -WindowStyle Hidden` the runner binary) launched via a `.lnk` in
`shell:startup` (`%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup`) pointing at
`powershell.exe -WindowStyle Hidden -ExecutionPolicy Bypass -File start-relay-runner.ps1`.
**This fires at user logon, not at raw machine power-on** - after a full shutdown, the runner
comes back automatically once you log into Windows normally, no manual command needed; it does
NOT come back before login (no auto-logon configured, deliberately - that would need storing a
Windows credential unencrypted).

To change the laptop's bind address: edit `RELAY_LISTEN_ADDR` directly in
`install/start-relay-runner.ps1` (hardcoded, not re-detected - Tailscale IPs are stable per
device but not guaranteed permanent).
To remove: delete the `.lnk` from the Startup folder above.

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

- **Sessions are in-memory only** (a `sync.Mutex`-guarded map in session.Manager). A runner
  restart loses all session state and transcripts — no disk persistence. Acceptable for this
  milestone; revisit if restarts become common (e.g. after S5 suspend/resume cycles).
- **Project registry persists to disk** (`projects.json`), sessions do not — asymmetric by
  design for this milestone, not an oversight.
- **compose.Runner is an interface** specifically so `docker compose` calls are fakeable in
  tests — no real compose file has been exercised yet (no test project defines one).
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
  A WoL broadcast never crosses a router — it only reaches the LAN segment the *runner sending
  it* is physically on, which is the entire reason `/v1/wake` must be called on a LAN-local
  runner, never the target machine itself (see ARCHITECTURE.md "Relay device").
- **Device registrations are in-memory only** (`notify.Registry`, mutex-guarded map keyed by
  FCM token) — same limitation as `session.Manager`: a runner restart loses all registered
  devices, and the phone app must re-register (it already does this on token refresh per
  shared/API.md, but a runner restart with no token refresh means silence until the app
  re-registers for some other reason). No code currently forces a periodic re-register.
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
  Docker container running `runnerd` will genuinely try to run `systemctl poweroff` /
  `shutdown -h now` / `shutdown /s /t 0` against whatever host/container can reach that
  command — there is no sandboxing inside `internal/idle` itself, the env-var gate in
  `cmd/runnerd/main.go` is the only thing standing between "idle" and "machine off." Never
  enable it outside a real, intentional bare-metal-or-VM runner deployment.
- **S5 means fully powered off, not sleep/hibernate (S3).** ARCHITECTURE.md's "Open questions"
  section explicitly resolved this: true near-zero power was the motivating pain, at the cost
  of a ~30-60s boot to come back — and per that doc, containers on the box need
  `restart: always` so they come back up automatically after boot. `internal/idle` has no way
  to bring the machine back itself once it's off; that's `internal/wol`'s job, triggered
  externally via `/v1/wake` from another LAN-local runner. The two features are two halves of
  one loop and are meaningless without each other.
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
- **Self-update replaces the binary on-disk while it's still the running process's
  executable.** Works because Linux keeps a running process's already-open inode alive
  after the file at that path is renamed out from under it - the old process finishes
  serving whatever it's doing on the old inode, then exits and systemd starts the new
  file. This does NOT reliably work on Windows (NTFS commonly locks a running exe's
  file) - self-update's `install()` will likely just fail-and-retry-next-interval there.
  Not a blocker: Windows was always the dev/laptop-testing target, not a deployed
  self-updating runner (see ARCHITECTURE.md - the actual deployment target is Linux).
- **Rerunning install.sh always ends in `systemctl restart`, never just `enable --now`.**
  Caught during testing: on an already-active service, `enable --now` is a no-op that does
  NOT pick up a binary step 3 just overwrote - the process keeps executing its old,
  already-open inode (even if that file was deleted from disk, e.g. the old
  `/usr/local/bin/relay-runner`). Self-update updated the real deployed binary correctly,
  and it sat there completely inert - only found out because the runner's own logs showed
  zero selfupdate activity. Data over speculation: `journalctl` + `ps -o cmd -C
  relay-runner` (its cmdline points at a path `ls` says no longer exists) confirmed it
  before this was "fixed" without evidence.
- **The binary lives at `/opt/relay/bin/relay-runner`, owned by the run user, not
  `/usr/local/bin`.** Caught during testing: the service runs as a non-root user
  (`User=%i`), and self-update replaces its own binary from that same user - a
  root-owned `/usr/local/bin` would make self-update permanently, silently unable to
  write there. install.sh removes any stale binary left at the old `/usr/local/bin`
  location from before this fix.
- **A GitHub release with a corrupted/malicious binary is the entire trust boundary**
  for self-update. `checksums.txt` only proves the downloaded bytes match what the CI
  workflow published - it does NOT protect against a compromised GitHub account/token
  publishing a bad release in the first place. Same trust level as `curl | sh`-installing
  any other tool; acceptable for a single-user deployment, worth revisiting (signed
  releases, a pinned public key) before this is ever multi-tenant.
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
| Provider → command mapping | `internal/session/session.go` (`resolveProvider`, `autoDetectProviders`, env `RELAY_PROVIDER_<NAME>` override) |
| claude stream-json codec (encode/decode) | `internal/session/session.go` (`encodeClaudeStreamJSONUserMessage`, `decodeClaudeStreamJSONLine`, `codecClaudeStreamJSON`) |
| Session purge cutoff | `cmd/runnerd/main.go` ticker + `internal/history/history.go` |
| Auth header check | `internal/api/api.go` middleware |
| Docker compose invocation | `internal/compose/compose.go` |
| Bulk container start/stop across all projects | `internal/api/api.go` (`runComposeAll`) |
| HTTP endpoint routing | `internal/api/api.go` (must match `shared/API.md`) |
| WoL magic packet construction | `internal/wol/wol.go` (`BuildMagicPacket`) |
| WoL broadcast send / SO_BROADCAST | `internal/wol/broadcast_unix.go`, `broadcast_windows.go` |
| WoL target port / broadcast address | `internal/wol/wol.go` constants |
| Device registration (dedupe by token) | `internal/notify/registry.go` (`Registry.Register`) |
| FCM credential path | env `RELAY_FCM_CREDENTIALS` → `internal/notify/notifier.go` (`NewNotifier`) |
| FCM JWT/OAuth2 exchange | `internal/notify/fcm.go` (`exchangeJWT`, `accessTokenFor`) |
| Session-finish → notify wiring | `cmd/runnerd/main.go` (`sessions.OnFinished`), `internal/session/session.go` (`notifyFinished`) |
| Idle-suspend enable flag | env `RELAY_IDLE_SUSPEND_ENABLED` → `internal/idle/idle.go` (`LoadConfig`) — disabled unless exactly `"true"` |
| Idle timeout / check interval | env `RELAY_IDLE_TIMEOUT`, `RELAY_IDLE_CHECK_INTERVAL` → `internal/idle/idle.go` (`LoadConfig`, `DefaultTimeout`, `DefaultCheckInterval`) |
| Pairing QR content/rendering | `cmd/runnerd/main.go` (`printPairingQR`, flag `-qr`) |
| Tailscale / Node / CLI bootstrap | `runner/install/install.sh` steps 1-2 |
| Self-update enable/interval | env `RELAY_AUTO_UPDATE_ENABLED`, `RELAY_UPDATE_CHECK_INTERVAL` → `cmd/runnerd/main.go` |
| Self-update release source/logic | `internal/selfupdate/selfupdate.go`, `.github/workflows/runner-release.yml` |
| Own-MAC detection for pairing QR | `internal/wol/localmac.go` (`LocalMAC`, `virtualIfacePrefixes`) |
| Same-LAN subnet detection for wake-via auto-match | `internal/wol/localmac.go` (`LocalSubnet`, `pickLANInterface`), `internal/api/api.go` (`runnerInfo.LocalSubnet`) |
| Uptime interval recording / buffer window | `internal/uptime/uptime.go` (`Open`, `Close`, `MaxAge`) |
| Uptime endpoint | `internal/api/api.go` (`handleUptime`) |
| Best-effort "runner suspending" push | `cmd/runnerd/main.go` (`Monitor.BeforeShutdown` wiring), `internal/notify/fcm.go` (`NotifyRunnerSuspending`) |
| Manual suspend endpoint / busy refusal / enable gate | `internal/api/api.go` (`handleSuspend`, `anyBusy`, `Server.Suspend`), `cmd/runnerd/main.go` (`srv.Suspend` wiring) |
| Idle/busy decision source | `internal/session/session.go` (`Manager.IdleStatus`) — read-only, derived from existing session state |
| Shutdown command (S5) | `internal/idle/shutdown_unix.go` (`systemctl poweroff` / `shutdown -h now` fallback), `internal/idle/shutdown_windows.go` (`shutdown /s /t 0`) |
| Idle-suspend ticker wiring | `cmd/runnerd/main.go` (`idle.NewMonitor(...).Run()`, gated on `idleCfg.Enabled`) |
| File listing / content endpoints | `internal/api/api.go` (`handleListFiles`, `handleFileContent`) |
| Project-path escape guard | `internal/project/project.go` (`Registry.ResolvePath`) |
| Viewable file size cap | `internal/api/api.go` (`maxViewableFileSize`) |
| Register existing folder as a project | `internal/project/project.go` (`RegisterExisting`), `internal/api/api.go` (`handleCreateProject`) |
| Unscoped directory browse (project picking) | `internal/project/project.go` (`BrowseDir`), `internal/api/api.go` (`handleBrowse`) |
| Filesystem roots listing | `internal/project/roots_unix.go`, `roots_windows.go` (`listRoots`) |
| Auth-login pseudo-session / command | `internal/session/session.go` (`StartAuthLogin`, `defaultAuthLoginCommand`) — env `RELAY_PROVIDER_AUTH_LOGIN` |
| Auth-login endpoint | `internal/api/api.go` (`handleAuthLogin`) |
| Laptop auto-start at logon | `install/start-relay-runner.ps1` + Startup-folder `.lnk` (see "Auto-restart on machine restart") |
