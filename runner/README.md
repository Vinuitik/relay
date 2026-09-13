# runner

Background service, one instance per machine. Not containerized (needs host-level power/process
control — see ARCHITECTURE.md's "Registration / connection" section for why).

Responsibilities: list known projects, start/stop a project's `docker compose`, launch a
configured CLI agent per session, track busy/idle state, serve the HTTP API over the Tailscale
interface only. Implements the contract in `shared/API.md` exactly.

## How to run

```
go run ./cmd/runnerd
```

On first run it generates a random key under `~/.relay/key.txt` (or `$RELAY_HOME/key.txt` if
set) and prints it once — copy it into the phone app. It also creates `~/.relay/projects.json`
(the project registry) and `~/.relay/projects/` (where new projects are scaffolded).

Env vars:
- `RELAY_HOME` — overrides the `~/.relay` state directory (mainly for tests/multiple instances).
- `RELAY_LISTEN_ADDR` — overrides the listen address (default `127.0.0.1:7777`). Production
  deployments must point this at the Tailscale interface, never `0.0.0.0` — see ARCHITECTURE.md.
- `RELAY_PROVIDER_<NAME>` — configures the shell command run for provider `<name>` (e.g.
  `RELAY_PROVIDER_CLAUDE="claude"`). `echo-agent` (`sh -c cat`) is built in as a no-dependency
  test provider.

## How to test

No Go toolchain needed on the host — run tests via Docker:

```
docker run --rm -v "$(pwd)":/app -w /app/runner golang:1.22 go test ./...
```
