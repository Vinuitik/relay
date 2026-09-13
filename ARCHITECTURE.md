# Relay — architecture

Remote, low-power, OS-independent control plane for working on projects from a phone during
commute. Grew out of: 2h commute doing nothing, a server that gets fully powered off, and not
wanting to keep a laptop on all day to bridge the gap.

## Problem

- Server should sit at S5 (soft off, ~0W) between uses, not idle-on or sleeping.
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
- **Runner responsibilities** (per machine):
  - list known projects (directories it's been told about)
  - start/stop that project's `docker compose`
  - launch the configured CLI agent (`claude` / `codex` / whatever) scoped to a project dir,
    per session (see Data model)
  - track busy/idle state explicitly (own state file written by the agent-runner wrapper,
    not inferred from CPU/session presence) — this is what the idle-suspend script checks
    before ever suspending
  - file read/write scoped to that project's directory only, never the whole filesystem
  - create-new-project endpoint (scaffolds a dir, registers it)
- **Wake path:** Android app sends WoL magic packet over the tailnet to the target machine's
  always-reachable Tailscale peer (or a relay peer, if the target machine is the one that's
  fully off and thus off the tailnet too — needs a second always-on-ish device, e.g. the home
  router if it can run Tailscale, or a $5/mo VPS, to actually deliver the LAN broadcast).
  Open question, see below.
- **Sleep path:** cron/timer on each runner checks its own busy/idle state; suspends to S5
  only when idle past a threshold AND no active agent task.
- **Android app:** native (not PWA) — chosen specifically for Doze-proof background job-done
  notifications and a home-screen widget for wake/stop-containers without opening the app.
  Not targeting Play Store, so no store review constraints on what it can do.

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
  same "plug in/plug out" principle as the S5 idle threshold.)

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

## Open questions / not yet decided

- WoL relay when the *target* machine is fully off (and thus unreachable on the tailnet
  itself, since Tailscale needs the OS running): needs some always-on-ish device on the LAN to
  receive the tailnet message and broadcast the magic packet locally. Candidate: router, if
  it can run Tailscale/a relay client; otherwise a cheap always-on device.
- S5 vs S3 per machine — leaning S5 (true near-zero power) but eats a ~30-60s boot; containers
  need `restart: always` to come back without manual intervention.
- Auto-register a machine's runner on first Tailscale-up, vs manual add-to-known-list — leaning
  manual for now (avoids accidentally trusting a rogue machine on the tailnet).
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
