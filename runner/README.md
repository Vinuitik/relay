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

## Deploying to a real Linux server (systemd)

See `install/`. Steps:

1. Get a `relay-runner` binary onto the server. Either build it there (needs Go installed:
   `go build -o relay-runner ./cmd/runnerd`), or cross-compile elsewhere and copy it over
   (`GOOS=linux GOARCH=amd64 go build -o relay-runner-linux-amd64 ./cmd/runnerd`, then `scp`).
2. Install [Tailscale](https://tailscale.com/download) on the server and `tailscale up`. Note
   its tailnet IP: `tailscale ip -4`.
3. `sudo ./install/install.sh /path/to/relay-runner-linux-amd64 youruser` — installs the binary
   to `/usr/local/bin`, drops `install/runner.env.example` into `/etc/relay/runner.env` (edit
   this — nothing works until you set `RELAY_LISTEN_ADDR` to the Tailscale IP from step 2), and
   installs+starts a systemd service (`relay-runner@youruser`, never runs as root).
4. `journalctl -u relay-runner@youruser -f` to watch it start and grab the printed key (also at
   `~youruser/.relay/key.txt`) — copy that into the phone app's known-runners list along with
   the Tailscale IP from step 2.
5. Leave `RELAY_IDLE_SUSPEND_ENABLED` unset/false until you've actually tested wake — enabling
   it powers this machine off. See `FLOWS.md`'s "Idle-suspend (S5)" section first.

This does not touch Docker, project directories, or Tailscale itself — those are separate,
one-time setup steps you do yourself before or after running the installer.

## Deploying to Windows (laptop)

No installer script yet (Windows service wrapping — e.g. via NSSM or `sc.exe create` — is
`[NOT IMPLEMENTED]`). For now: cross-compile
(`GOOS=windows GOARCH=amd64 go build -o relay-runner-windows-amd64.exe ./cmd/runnerd`) and run
the `.exe` directly in a terminal, or via Task Scheduler pointed at it, setting the same env
vars from `install/runner.env.example` as system/user environment variables instead of an
env file.
