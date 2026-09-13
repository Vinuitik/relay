# Runner flows

Files: main.go, config.go, project.go, session.go, history.go, compose.go, api.go, wol.go, registry.go, notifier.go, fcm.go, idle.go, shutdown_unix.go, shutdown_windows.go

## Startup

main() → config.Load() → generates ~/.relay/key.txt + projects.json if absent, prints key once
→ api.NewServer(project.Registry, session.Manager) → http.ListenAndServe(config.Addr)
→ goroutine: time.Ticker(1h) → session.Manager.PurgeFinishedBefore(7d)

To change key/projects root: config.Load() (env `RELAY_HOME`)
To change listen address: config.Load() (env `RELAY_ADDR`, default 127.0.0.1:7777 — binds to
Tailscale interface only in production, not enforced by code)
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
default `idle.DefaultTimeout` = 30m)
To change the poll interval: env `RELAY_IDLE_CHECK_INTERVAL` (default `idle.DefaultCheckInterval` = 1m)
To enable this feature at all: env `RELAY_IDLE_SUSPEND_ENABLED=true` - **disabled by default,
see Technology notes below before ever setting this locally.**

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
tests; real FCM delivery is `[NOT IMPLEMENTED IN TESTS]` (requires a real Firebase project).

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

## Change Index

| Thing | Where |
|---|---|
| Key generation / storage | `internal/config/config.go` |
| Projects root / registry file location | `internal/config/config.go` (env `RELAY_HOME`) |
| Listen address | `internal/config/config.go` (env `RELAY_ADDR`) |
| Provider → command mapping | `internal/session/session.go` (env `RELAY_PROVIDER_<NAME>`) |
| Session purge cutoff | `cmd/runnerd/main.go` ticker + `internal/history/history.go` |
| Auth header check | `internal/api/api.go` middleware |
| Docker compose invocation | `internal/compose/compose.go` |
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
| Idle/busy decision source | `internal/session/session.go` (`Manager.IdleStatus`) — read-only, derived from existing session state |
| Shutdown command (S5) | `internal/idle/shutdown_unix.go` (`systemctl poweroff` / `shutdown -h now` fallback), `internal/idle/shutdown_windows.go` (`shutdown /s /t 0`) |
| Idle-suspend ticker wiring | `cmd/runnerd/main.go` (`idle.NewMonitor(...).Run()`, gated on `idleCfg.Enabled`) |
