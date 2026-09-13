# Runner flows

Files: main.go, config.go, project.go, session.go, history.go, compose.go, api.go, wol.go, registry.go, notifier.go, fcm.go

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
