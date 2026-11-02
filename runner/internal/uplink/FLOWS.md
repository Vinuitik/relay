# Hub uplink (runner → HabitTracker)

Files: config.go, pair.go, claim.go, uplink.go, ../../cmd/runnerd/pair.go, ../../cmd/runnerd/main.go (wiring)

Contract: `shared/HUB.md` (hub side lives in HabitTracker `habitTracker/relay/`). Lets a browser reach
this runner through the hub without Tailscale or inbound ports: the runner dials OUT.

## Pairing (once per machine)
Hub for both flows: `--hub`, else `$RELAY_HUB`, else `uplink.DefaultHub` (`https://habittrackerdima.me`). To change the default: `uplink.DefaultHub` (+ relay.js `DEFAULT_HUB` in HabitTracker).

### QR (recommended, phone first)
`runnerd pair [--name laptop]` (installed binary: `/opt/relay/bin/relay-runner pair`; install.sh / install.ps1 run it as their last step unless hub.json exists)
→ `runPair()` → `pairByQR()` → `uplink.StartClaim()` → `POST <hub>/relay/pair/start {"name"}` → `Claim{ID, PollSecret, ExpiresIn}`
→ `qrterminal.GenerateHalfBlock(Claim.URL(hub))` = `<hub>/relay/claim/<id>` + the link as text
→ `uplink.WaitForApproval()` every `pollEvery` (2s) `POST <hub>/relay/pair/poll {"claimId","pollSecret"}`
→ `200` → Config · `404` → `ErrClaimGone` (declined / expired / hub restarted) · `202`, other status, network error → retry until `ExpiresIn`
→ `uplink.Save()` (below).

### Code (desktop alternative)
`runnerd pair --code ABCD2345 [--name laptop]`
→ `main()` (after `config.Load`, so `RELAY_HOME` exists) → `runPair()` (cmd/runnerd/pair.go; name defaults to `os.Hostname()`)
→ `uplink.Pair()` → `uplink.CheckHubURL()` (https only; plain http allowed only for localhost/loopback)
→ `POST <hub>/relay/pair {"code","name"}` → `200 {"deviceId","token"}` (`400/404` → "wrong, expired or already used")
→ `uplink.Save()` → `$RELAY_HOME/hub.json` mode 0600 `{"hub","deviceId","token"}` → prints `paired as <name>`.
To change the pairing request/response shape: `uplink.Pair()`. Re-pairing just overwrites hub.json. First pairing: a running runnerd picks it up by itself (`uplink.WaitForConfig`, 5s poll, started in `main()` when hub.json is absent). Re-pairing an already-paired runner still needs a restart.

## Uplink (every runnerd start)
`main()` → `uplink.Load($RELAY_HOME)` → absent → nothing (not paired) / invalid → log `uplink disabled`
→ `go Client.Run(ctx)` next to `http.ListenAndServe`, sharing the same `srv.Routes()` handler
→ `Client.runOnce()` → `websocket.Dial(wss://<host>/relay/uplink, Authorization: Bearer <token>)`
→ loop `conn.Read()` (also answers hub pings) → `req` frame → goroutine per request → `Client.dispatch()`
→ path must parse and start with `/v1/` (no `/..`) else `404`; body > 4 MiB → `413`
→ build in-process `http.Request` (+`X-Relay-Key: <own key>`, `Content-Type: application/json` if body, `RemoteAddr=relay-hub:0`)
→ `Handler.ServeHTTP(recorder)` (recorder caps response at 4 MiB → `413`; panics → `500`)
→ `res` frame `{"type":"res","id","status","contentType","body"}` → `conn.Write` under `writeMu`.
To change caps/timeouts: `MaxBody`, `readLimit`, `requestTimeout` in uplink.go.

## Reconnect / revoke
- Any drop (hub restart, network, replaced by a newer socket) → `Run()` waits `backoff` (1s, doubling, cap 60s) and redials;
  backoff resets to 1s after any successful handshake. To change: `Client.MinBackoff` / `Client.MaxBackoff`.
- Handshake `401` (device revoked in the browser, or token wrong) → log
  `uplink: hub said 401 - device revoked or token invalid - re-pair with runnerd pair` → retry every 60s (cap), forever.
- Hub-side revoke closes the socket → reconnect → 401 → the path above.

## Technology Notes
- **Library: `github.com/coder/websocket` v1.8.12** (ex nhooyr.io/websocket; last version supporting go 1.22). Ping/pong is
  handled inside `conn.Read` - pongs only go out while a Read is pending, which the read loop guarantees. Writes are
  serialized with `writeMu` (the library allows concurrent writers, the mutex keeps that explicit).
- **Token stored raw in hub.json (0600)**: it must be presented on every connect, so it cannot be hashed locally - same
  model as an SSH private key. The hub stores only `sha256(token)`. Anyone who can read hub.json can impersonate this
  device to the hub (not the other way round). On Windows 0600 is not enforced (NTFS ACLs of the user profile apply).
- **The hub never sees the runner key**: `dispatch()` adds `X-Relay-Key` itself, so the normal `api.Server.auth` check
  still runs. Consequence: anyone with hub access to this device (the owning user after session + 2FA) has full `/v1/` access.
- **Hub restarts drop the socket**; the runner reconnects within the current backoff (≤ 60s, usually 1s). Requests in
  flight at drop time are lost (hub answers its browser with 503/504); their handler contexts are cancelled.
- **In-memory buffering, no streaming**: each response is fully buffered (≤ 4 MiB) and sent as one frame. Long-poll
  endpoints work only up to the hub's 30s wait; `requestTimeout` (60s) is a backstop. Binary bodies are out of scope
  (body is a UTF-8 string per HUB.md).
- **No request concurrency limit**: one goroutine per `req` frame; the hub is trusted not to flood.
- **hub.json is read once at startup**: pairing or revoking does not affect a running runnerd until restart.
- **URL**: hub may carry a path prefix (`https://h/prefix` → `wss://h/prefix/relay/uplink`); `http://localhost` → `ws://`.

- **QR claim is in hub memory:** a hub restart while you're scanning → the runner gets `404` → "declined, expired or the hub restarted"; just run pair again.
- **Pair must run as the service user:** hub.json goes to that user's `$RELAY_HOME` (default `~/.relay`). Pairing as another user writes a file the service never reads.
- **`DefaultHub` is hardcoded:** a different HabitTracker host needs `--hub` / `RELAY_HUB`, and relay.js `DEFAULT_HUB` decides whether the browser's command shows `--hub`.

## Change Index
| Thing | Where |
|---|---|
| Pair subcommand / flags / default name | `cmd/runnerd/pair.go` `runPair()`, dispatch in `cmd/runnerd/main.go` |
| Pair HTTP call + error text | `uplink.Pair()` (pair.go) |
| QR claim start / poll / link | `uplink.StartClaim()`, `WaitForApproval()`, `Claim.URL()` (claim.go) |
| QR poll interval | `pollEvery` (cmd/runnerd/pair.go) |
| Default hub | `uplink.DefaultHub`, env `RELAY_HUB` |
| Pick up first pairing without restart | `uplink.WaitForConfig()` + goroutine in `cmd/runnerd/main.go` (5s) |
| Installer pairing step (Linux) | `runner/install/install.sh` `main()` step 8 (last; runs as the service user via `runuser`, `RELAY_HOME`/`RELAY_HUB` from runner.env; skipped if hub.json exists) |
| Installer pairing step (Windows) | `runner/install/install.ps1` step 7 (last; current elevated user, `RELAY_HOME`/`RELAY_HUB` from relay.env; skipped if hub.json exists) |
| Pair-only command for installed machines | Linux `sudo -u <user> /opt/relay/bin/relay-runner pair`; Windows `& "$env:LOCALAPPDATA\Relay\bin\relay-runner.exe" pair` (see runner/FLOWS.md "Install") |
| https-only rule (localhost exception) | `uplink.CheckHubURL()` (config.go) |
| hub.json location / format / mode | `uplink.FileName`, `Load()`, `Save()` (config.go), dir = `RELAY_HOME` |
| Uplink URL derivation | `uplinkURL()` (config.go) |
| Auth header on dial | `Client.runOnce()` |
| Backoff 1s→60s, 401 handling | `Client.Run()`, `MinBackoff`/`MaxBackoff` |
| Frame shape | `uplink.Frame` (uplink.go), must match `shared/HUB.md` |
| /v1/ path filter, 413 caps, X-Relay-Key injection | `Client.dispatch()` |
| Response buffering | `recorder` (uplink.go) |
| Uplink start | `cmd/runnerd/main.go` (before `http.ListenAndServe`) |
