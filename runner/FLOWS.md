# Runner flows

Files: main.go, config.go, project.go, session.go, history.go, compose.go, api.go

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
