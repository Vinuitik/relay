# node

Background service, one instance per machine. Not containerized (needs host-level power/process
control — see ARCHITECTURE.md's "Registration / connection" section for why).

Responsibilities: list known projects, start/stop a project's `docker compose`, launch a
configured CLI agent per session, track busy/idle state, serve the HTTP API over the Tailscale
interface only.

Not yet implemented.
