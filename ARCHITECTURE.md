# Relay — architecture

Remote, low-power, OS-independent control plane for working on projects from a phone during
commute. Grew out of: 2h commute doing nothing, a server that gets fully powered off, and not
wanting to keep a laptop on all day to bridge the gap.

## Problem

- Server should sit at near-zero power between uses, not idle-on. (**Resolved to S3 sleep,
  ~1W** — not S5 soft-off. See "Sleep/wake states" below for why.)
- Must be wakeable and reachable from a phone on 4G — not home WiFi, no port-forwarding
  (CGNAT/mobile networks usually block inbound anyway).
- Must not suspend/kill itself mid-task — "is Claude/Codex actually busy" has to be a real
  signal, not "is an SSH session attached."
- Multiple machines (this laptop's project lives only here, not on the server) — the platform
  must not hardcode to one box. OS-independent: server is Linux, laptop is Windows+Docker.
- Not vendor-locked to Claude Code specifically — user is considering Codex too. The runner
  shells out to a configured CLI agent; swapping agents is a config change, not a rebuild.
- Phone control surface needs reliable background notifications ("job's done") and a
  home-screen widget (wake / stop-containers) — not going on the Play Store, so no store
  constraints, but still want native Android for Doze-proof notifications + widgets (PWA
  service workers get throttled under Android battery optimization; native w/ foreground
  service or FCM does not).

## Shape

```
[Android app]  <-- Tailscale tailnet -->  [runner on machine A]  (e.g. home server, Linux)
     |                                    [runner on machine B]  (e.g. this laptop, Windows)
     |                                    [runner on machine N]  ...
     buttons: wake, stop containers,
     switch project, new project,
     notifications on job-done (per session)
```

- **"Runner"**, not "daemon" — same thing (a background service process per machine), renamed
  because "daemon" reads badly out of context (also avoided "node" — collides with Node.js).
  One runner process per machine.
- **Tailscale**, not DDNS + port-forward. DDNS only solves "what's my current IP" — still
  needs an inbound open port, which mobile/CGNAT networks often block outright. Tailscale
  gives every machine an outbound-only tunnel connection and a stable name, works from 4G,
  no router config.
- **Same runner binary/script on every machine.** Not hardcoded to "the server." Each
  instance knows its own local projects and exposes them identically.
- **Language: Go.** Cross-compiles to a single static binary for both Linux and Windows from
  one codebase, no runtime/interpreter needed on the target machine, no cgo (keeps it truly
  static) — fits "same binary on every machine" directly.
- **Runner responsibilities** (per machine):
  - list known projects (directories it's been told about)
  - start/stop that project's `docker compose`
  - launch the configured CLI agent (`claude` / `codex` / whatever) scoped to a project dir,
    per session (see Data model)
  - track busy/idle state explicitly (own state file written by the agent-runner wrapper,
    not inferred from CPU/session presence) — this is what the idle-suspend script checks
    before ever suspending
  - file reading scoped to that project's directory only, never the whole filesystem
  - git (status/diff/stage/commit/branches/push/pull) on the project's repo - the one way the
    phone changes project files (decided 2026-10-04; there is still no general file write)
  - sign-in relay: runs CLI logins (claude, gh, gcloud) and hands the browser step to the phone
  - create-new-project endpoint (scaffolds a dir, registers it)
- **Wake path:** Android app sends WoL magic packet over the tailnet to the target machine's
  always-reachable Tailscale peer — or, if the target machine is fully off (and thus off the
  tailnet too), to a second always-on-ish device physically on that machine's LAN, which then
  broadcasts the magic packet locally. A WoL broadcast cannot cross a router or be delivered
  by a remote VPS — it only reaches devices on the same LAN segment, so this relay device must
  be local. See "Relay device" below.
- **Sleep path:** a ticker inside `runnerd` checks its own busy/idle state; suspends to S3
  only when idle past a threshold AND no active agent task. See "Sleep/wake states".
- **Android app:** native (not PWA) — chosen specifically for Doze-proof background job-done
  notifications and a home-screen widget for wake/stop-containers without opening the app.
  Not targeting Play Store, so no store review constraints on what it can do.
- **Notifications: Firebase Cloud Messaging (FCM).** Free, unlimited, and piggybacks on the
  single OS-level push connection every Android phone with Google Play Services already
  maintains — no extra battery cost, no persistent per-runner connection to build/maintain.
  Each runner needs normal outbound internet (already required anyway) to call Google's API
  when a session finishes; only the fact "session X in project Y finished" transits Google's
  servers, nothing else. Considered and rejected: an app-held persistent connection per
  runner — real ongoing battery cost (keep-alive pings, foreground service, no Doze
  exemption) and reinventing reconnect/reliability handling FCM already solved.

## Two daemons

The whole system is two Go binaries with no overlap and no direct link between them. Getting
these two words straight removes most of the confusion about what can wake what:

| Term | Binary | Runs on | Job |
|---|---|---|---|
| **runner** | `runnerd` (`runner/cmd/runnerd`) | every work machine (laptop, server) | runs projects, sessions and the agent CLI; **puts its own machine to sleep** |
| **waker** | `wakerd` (`runner/cmd/wakerd`) | one always-on box on the LAN (Pi Zero 2 W) | **wakes other machines** by LAN broadcast; nothing else |

One sentence to hold it: **sleep is the runner's job, wake is the waker's job, and the two
never talk to each other.** The phone talks to both, separately.

```
phone --(tailnet, key auth)--> runnerd   : projects, sessions, POST /v1/suspend
phone --(tailnet, key auth)--> wakerd    : POST /v1/machines/{id}/wake
wakerd --(LAN L2 broadcast)--> sleeping NIC --> machine boots --> runnerd starts
```

There is deliberately **no** "wake agent" on the target machine, and there never can be: while
the machine is asleep, no software on it is running. Wake is the network card's firmware
reacting to a magic packet — there is nothing there to write or name.

(`runnerd` and `wakerd` differ by one letter, which is a hazard when speaking rather than
writing. In prose prefer "the runner" and "the waker".)

## Sleep/wake states

**Resolved: S3 (suspend-to-RAM), not S5 (soft off).** The power delta is only ~1W, while S5
costs a ~30-60s boot back and is far more fragile to wake reliably.

```
S0 awake  --runnerd idle timeout, or phone "Sleep" (POST /v1/suspend)-->  S3 sleep
S3 sleep  --magic packet from wakerd on the LAN-->                        S0 awake
```

How each OS is actually put into that state (`runner/internal/idle`):

- **Linux:** `systemctl suspend` → S3. Needs the service user to hold suspend rights (a polkit
  rule for `org.freedesktop.login1.suspend`, or sudoers); without them the call fails loudly.
- **Windows:** `rundll32.exe powrprof.dll,SetSuspendState 0,1,0`.

### Why LAN is not optional

At S3 the OS is frozen: Tailscale is not running, so the machine is off the tailnet entirely
and nothing can route to it — not the phone on 4G, not a VPS. Only the NIC stays powered, and
it listens for exactly one thing: a magic packet on its own LAN segment. Broadcasts do not
cross routers. That is the entire reason `wakerd` has to exist and has to be local.

The only way to remove the LAN requirement is to never truly sleep — which is the cost this
project exists to avoid.

### Per-machine caveats that bite in practice

- **Windows Modern Standby machines have no S3 at all.** Check with `powercfg /a`: if it
  reports "Standby (S0 Low Power Idle)" and lists S3 as unavailable, the machine only does
  S0ix. Such a machine may stay network-connected while "asleep" (so it can remain reachable
  over the tailnet and need no WoL), or may drop to a deeper state — verify per machine, do
  not assume.
- **Windows hibernation silently overrides S3.** If hibernation is enabled, `SetSuspendState`
  hibernates (S4 — RAM written to disk, machine powered off) instead of suspending, and
  Windows treats the "don't hibernate" argument as advisory. Run `powercfg /hibernate off` on
  any machine where true S3 is required. See `runner/internal/idle/shutdown_windows.go`.
- **WoL over WiFi is usually unavailable.** WoWLAN requires the NIC to stay associated to the
  AP while suspended; most laptop WiFi cards and drivers do not. Ethernet is the reliable
  path. Check the target's wake-armed devices (`powercfg /devicequery wake_armed` on Windows,
  `ethtool <iface> | grep Wake-on` — needs root — on Linux; `g` in the Wake-on field means
  magic-packet wake is enabled).
- **Fast Startup (Windows)** changes what a "shutdown" leaves behind and can break wake
  assumptions; disable it on any machine being used as a wake target.

## Data model

```
Runner (one per machine)
 └─ Project (a folder the runner has been told about)
     └─ Session (one running or finished chat with a CLI agent, scoped to that project)
```

- A runner can have multiple projects; a project can have **multiple concurrent sessions**
  (e.g. two chats going in the same repo at once).
- Each session carries: which provider ran it (`claude` / `codex` / ...), busy/idle state,
  and its own job-done notification — notifications are per-session, not per-project or
  per-runner, so a ping tells you exactly which chat in which project finished.
- Provider independence lives at the session level: the runner doesn't know or care what a
  "claude" or "codex" session means beyond "a configured CLI command to shell out to,
  scoped to this project dir." Swapping providers is a config change on new sessions, not a
  runner rebuild — existing sessions keep whatever provider they started with.

## Navigation model (phone app)

```
Device menu (icons, one per registered runner)
 └─ Project list (for that runner)
     └─ Chat interface (one per session — like VS Code's chat panel, not a terminal)
         + History (past finished sessions for that project)
```

- Chat, not terminal — the app renders each session as a conversation, mirroring how you'd
  already interact with Claude Code / Codex in an editor. No raw shell view (see resolved
  transport question above).
- **History is cleaned weekly** — finished sessions older than a week are purged rather than
  kept indefinitely. Keeps the per-project history list short and avoids unbounded storage
  growth on the runner. (Exact cutoff/retention length is a config value, not hardcoded —
  same "plug in/plug out" principle as the sleep idle threshold.)

## Registration / connection (no Docker for Relay itself)

Relay's own runner + phone app are **not containerized** — Docker only shows up as something a
*managed project* may use (`docker compose` a project defines), never as how the runner or app
ship. The runner needs host-level access (power state, WoL, process control) that containerizing
would only get in the way of.

- **Install:** manual, once, per machine — you put the runner binary on the machine yourself
  (laptop, server, ...). No zero-touch/remote install.
- **Key:** the runner generates its own key **locally on first run** (not embedded in the
  binary — the binary is identical across machines, so a baked-in key would trust every
  install equally, which defeats per-machine identity). It prints its Tailscale hostname +
  key once on first launch.
- **Registration:** manual — you copy that (hostname, key) pair into the phone app's known-
  runners list yourself. No auto-register-on-tailnet-join, specifically to avoid silently
  trusting a rogue tailnet peer.
- **Connection:** no scanning, no discovery protocol. Both phone and runner are already
  addressable peers on the Tailscale tailnet (stable hostname, WireGuard tunnel negotiated
  by Tailscale regardless of where the phone physically is). The runner's HTTP(S) listener is
  bound only to the Tailscale interface, never 0.0.0.0. The phone app just does a normal
  HTTPS request to `https://<runner>.<tailnet>.ts.net:<port>/...` with the stored key in a
  header — Tailscale's tunnel is the entire transport, the key is the entire auth.

## wakerd (the wake daemon)

`wakerd` is the second of the project's two daemons (see "Two daemons" above). It runs on a
small always-on box physically on the target's LAN, and does exactly one thing: turn a wake
request arriving over the tailnet into a magic-packet broadcast on the local segment.

A WoL magic packet is a broadcast — it cannot cross a router or be delivered by a remote VPS,
only by something physically on the same LAN as the sleeping machine. So waking a sleeping
machine needs a second always-on device, on that LAN, running Tailscale, that receives the wake
request over the tailnet and fires the local broadcast. That device is the one running
`wakerd`; earlier drafts of this document called it "the relay device".

Implementation: `runner/cmd/wakerd` + `runner/internal/waker` (HTTP API) and
`runner/internal/wol` (packet construction and directed broadcast). `wakerd` never talks to
`runnerd` — only the phone talks to `wakerd`.

- **Router ruled out:** it's a Netgear D-series (DSL modem-router). Stock firmware has no
  Tailscale/package support, and the D-series isn't practically flashable to OpenWrt (closed
  modem chipset). Not usable as the wakerd host.
- **Phone ruled out:** it's only on the home LAN when you're already home on WiFi — exactly
  the situation this project doesn't need solving. On commute/4G, the phone can't deliver a
  LAN broadcast to a network it isn't on.
- **Chosen device: Raspberry Pi Zero 2 W (~£14–16 new).** Cheapest option that satisfies
  "just runs Tailscale, nothing else" — ~1W idle draw, no wasted capability (GL.iNet-style
  travel routers were considered and rejected: £25–70, and they're routers under the hood,
  more capability than needed). Solar/battery power for this device was considered and
  rejected — its own electricity cost is only ~£2.30/year, so no kit's payback period would
  ever close, and a small battery risks under-power during UK winter, which would break the
  one hard requirement (always-on).
- **Rollout order:** build the app and runner first, install Tailscale + the runner directly
  on the laptop/server (already-owned hardware) and prove the whole flow — registration, wake,
  sessions — end to end. Only buy the Pi Zero 2 W afterward, once the design is confirmed
  working, and move the `wakerd` role onto it last.

## Status as of 2026-09-16 (pick up here next session)

**Built tonight (2026-09-16):**
- Read-only file viewing: `GET /v1/projects/{id}/files` + `/files/content` on the runner,
  folder-icon → `FileBrowserScreen` in the app. See runner/FLOWS.md and android/FLOWS.md.
- **Fixed a real on-device blocker**: cleartext HTTP was silently rejected by Android (no
  `usesCleartextTraffic` declared) — every single API call failed on a real phone until this
  was fixed, including `WakeViaMatcher`'s auto-match, which is why wake-via looked broken/
  unconfigurable. This had never been caught because verification was Docker-compile-only,
  never run-on-device.
- First real end-to-end pairing attempt: laptop runner (Windows, Tailscale IP
  100.124.46.7:7777) and server runner (Linux, Tailscale IP 100.119.134.101:7777) both
  confirmed live and reachable over the tailnet. Tailscale was already installed and logged in
  on all three devices (laptop, server, phone) from prior setup — that step was already done,
  not part of tonight's work.
- Git history rewrite: stripped a stray `Co-Authored-By: Claude` trailer that had been added to
  16 past commits, contrary to this project's own no-attribution convention. Force-pushed to
  `origin/main`. Learned the hard way that force-pushing does not fire GitHub's `push` event, so
  `android-deploy.yml` silently didn't run until manually triggered via `workflow_dispatch` —
  documented in android/FLOWS.md "Distribution".

**Not yet actually confirmed** (cleartext fix was pushed and should be building/distributing,
but the resulting APK had not been re-paired against either runner by the end of this session):
- Phone successfully pairing with both runners post-fix.
- Wake-via auto-match actually succeeding now that the underlying HTTP calls work.
- Chat/session/container flows exercised for real against either runner.
- S3 suspend + WoL wake-back, actually exercised.

## Status as of 2026-09-15

> **Superseded — read this first.** Several features listed below were later deliberately
> removed from `main` in commit `2b78806` ("Remove selfupdate, uptime, history and
> runner-to-runner wake", ~1500 lines deleted). **Not in the code today:**
> `internal/uptime` + `GET /v1/uptime`, `internal/history`, `internal/selfupdate`,
> runner-to-runner wake (`internal/wol/localmac.go`, `internal/api/wake_test.go`),
> bulk `/v1/containers/start-all` / `stop-all`, and the app's `WakeViaMatcher` /
> `wakeViaRunnerId` auto-match, Room cache, Dashboard screen and `UptimeSyncWorker`.
> The only trace left is a vestigial `localSubnet` field in the app's `Models.kt`.
> The entry below is kept as a dated record of what existed at the time, not as a
> description of the current tree.

**Built and merged** (runner + app, both compile/test clean - runner via `go test ./...`,
Android via a Dockerized `./gradlew assembleDebug`, see runner/FLOWS.md and android/FLOWS.md for
details of each):
- Idle-suspend default lowered to 3 min after a session finishes (was 30m); never fires while a
  session is busy, regardless of timeout.
- Bulk `/v1/containers/start-all` / `/stop-all` per runner (app: overflow menu per runner row).
- Runner uptime tracking (`internal/uptime`, 14-day local buffer) + `GET /v1/uptime`.
- `GET /v1/runner/info` now reports `localSubnet`, used by the app's `WakeViaMatcher` to
  auto-fill `wakeViaRunnerId` on pairing (no more manual Edit step for the common case).
- Best-effort FCM push (`runner_suspending`) right before an idle-triggered suspend.
- **Manual suspend**: `POST /v1/suspend` (409 if busy, 503 if `RELAY_IDLE_SUSPEND_ENABLED` isn't
  set) + a "Suspend now" action in the app, so you don't have to wait out the idle timer.
- On-device Room cache for sessions/messages (offline/asleep-runner fallback in ChatScreen and
  SessionListScreen) and a per-app Dashboard screen (this-week/past-weeks uptime, synced from
  each runner's short-term buffer via `UptimeSyncWorker`).
- "Remove runner" action added to the app - `KnownRunnersRepository.removeRunner` existed but no
  screen ever called it, so there was previously no way to un-pair a runner.
- Gradle build verification now reuses a persistent Docker volume (`relay-gradle-cache`) instead
  of re-downloading the Gradle distro/deps on every Dockerized build check.

**Not yet done:**
- **End-to-end test never completed.** A test run was started (fresh binaries cross-compiled to
  `runner/install/relay-runner-{linux-amd64,windows-amd64.exe}` - gitignored build artifacts,
  rebuild before reusing since they'll be stale) and briefly run on this laptop bound to its LAN
  IP for a same-WiFi phone pairing test, but was stopped before actually pairing the phone or
  exercising anything. **Nothing is running right now** - process was killed, no firewall rule
  was added (attempted, failed for lack of admin rights in that shell), nothing persisted beyond
  the isolated test dir `C:\Users\sizon\relay-test-runner` (safe to delete or reuse).
- Tailscale is **not installed** on this laptop yet - the aborted test used a plain LAN IP
  instead, which is fine for a same-WiFi smoke test but doesn't exercise the actual
  over-Tailscale-from-anywhere path the real design depends on.
- The remote SSH server has **not been touched** - no commands have been run there yet. Doing so
  needs the freshly-built `relay-runner-linux-amd64` copied over (e.g. `scp`) plus
  `runner/install/install.sh` and `relay-runner.service`, then `sudo ./install.sh <binary> <user>`
  on the server (see that script's own header comment for exactly what it does).
- File viewing (read-only browser scoped to a project dir) - discussed, designed, not started.
- Pi Zero 2 W has not been purchased - still pre-rollout per "Rollout order" above.

**Next session, in order:** (1) decide whether to install Tailscale on this laptop first or keep
testing over plain LAN a bit longer, (2) actually run a fresh test runner + pair the phone (QR or
manual entry) and exercise session/chat/dashboard/suspend end-to-end, (3) hand over the SSH
commands for the remote server once the laptop path is proven, (4) file viewing.

## Open questions / not yet decided

- S5 vs S3 per machine — **resolved: S3** (suspend-to-RAM). The power delta vs S5 is only
  ~1W, while S5 costs a ~30-60s boot back and is far more fragile to wake. Containers still
  need `restart: always` for the S4/S5 cases. Idle-timeout-before-suspend is a config value,
  not hardcoded. Implemented in `runner/internal/idle`. See "Sleep/wake states".
- Auto-register a machine's runner on first Tailscale-up, vs manual add-to-known-list —
  **resolved: manual** (avoids accidentally trusting a rogue machine on the tailnet).
- ~~Exact runner transport~~ **Resolved:** HTTP API only. SSH+tmux was considered as a
  raw-terminal fallback but dropped — the phone app is a chat interface (like VS Code's chat
  panel), not a terminal emulator, and there's no intent to ever SSH in directly. The runner
  still spawns each session as a plain subprocess and captures stdout/stderr itself; no tmux
  needed for that.

## Non-goals

- No in-process fallback if the runner can't reach Docker/the agent CLI — failures should be
  visible, not silently degraded (mirrors the load-bearing decision in the KaggleAgent forge
  project's sandbox design: an unavailable capability should be loud, not papered over).
- Not building a general multi-tenant/multi-user system — this is single-user, personal
  infrastructure across your own machines.
