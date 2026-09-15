# Runner flows

Files: main.go, config.go, project.go, session.go, history.go, compose.go, api.go, wol.go,
localmac.go, registry.go, notifier.go, fcm.go, idle.go, shutdown_unix.go, shutdown_windows.go,
selfupdate.go

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
→ provider resolved via env `RELAY_PROVIDER_<NAME>` (falls back to built-in "echo-agent" test
  provider = `sh -c cat`) → os/exec.Command spawned, scoped to project dir
→ stdout/stderr scanner goroutine appends to in-memory transcript (role "agent")
→ state: busy → idle/finished/error

POST /v1/sessions/{id}/message → session.Manager.SendMessage → io.WriteString to subprocess stdin
POST /v1/sessions/{id}/stop → session.Manager.Stop → kill process, state → finished

To change real agent commands: set env `RELAY_PROVIDER_CLAUDE=claude`, `RELAY_PROVIDER_CODEX=codex`
To add a provider: no code change needed, just set its env var

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

To change the idle threshold: env `RELAY_IDLE_TIMEOUT` (Go duration string, e.g. "45m";
default `idle.DefaultTimeout` = 3m — a starting heuristic, tune once real usage data exists)
To change the poll interval: env `RELAY_IDLE_CHECK_INTERVAL` (default `idle.DefaultCheckInterval` = 1m)
To enable this feature at all: env `RELAY_IDLE_SUSPEND_ENABLED=true` - **disabled by default,
see Technology notes below before ever setting this locally.**

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
| Provider → command mapping | `internal/session/session.go` (env `RELAY_PROVIDER_<NAME>`) |
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
| Idle/busy decision source | `internal/session/session.go` (`Manager.IdleStatus`) — read-only, derived from existing session state |
| Shutdown command (S5) | `internal/idle/shutdown_unix.go` (`systemctl poweroff` / `shutdown -h now` fallback), `internal/idle/shutdown_windows.go` (`shutdown /s /t 0`) |
| Idle-suspend ticker wiring | `cmd/runnerd/main.go` (`idle.NewMonitor(...).Run()`, gated on `idleCfg.Enabled`) |
