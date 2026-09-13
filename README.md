# Relay

Remote, low-power control plane for running CLI coding agents (Claude Code, Codex, ...) on your
own machines from your phone — see [ARCHITECTURE.md](ARCHITECTURE.md) for the full design.

## Layout

- `runner/` — the background service that runs on each machine (laptop, server, ...). Lists
  projects, starts/stops per-project sessions with a configured CLI agent, tracks busy/idle
  state, exposes an HTTP API over Tailscale. One instance per machine, not containerized.
- `android/` — the native Android app: known-runners list, project/session browser, wake /
  stop-containers controls, job-done notifications.
- `shared/` — the HTTP API contract between `runner` and `android`.

## Status

Early scaffolding — see ARCHITECTURE.md's "Open questions" section for what's still undecided.
