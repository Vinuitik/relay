# Relay HTTP API v1 — contract between runner and android

Transport: plain HTTPS/HTTP over the Tailscale tailnet only (runner binds to the tailnet
interface, never 0.0.0.0 — see ARCHITECTURE.md). No TLS termination is Relay's own concern for
v1; Tailscale's tunnel is the transport security.

Auth: every request carries header `X-Relay-Key: <key>`. The key is generated locally by the
runner on first run and copied into the phone app manually (see ARCHITECTURE.md
"Registration"). A request with a missing/wrong key gets `401`.

Content type: `application/json` for all request/response bodies.

## Types

```
Project {
  id: string          // stable slug, e.g. "relay" — also the directory name
  name: string
  path: string         // absolute path on the runner's machine
  createdAt: string    // RFC3339
}

Session {
  id: string
  projectId: string
  provider: string     // "claude" | "codex" | ... (matches a configured CLI command)
  state: string        // "busy" | "idle" | "finished" | "error"
  createdAt: string
  finishedAt: string?  // null while not finished
  messages: Message[]
}

Message {
  role: string         // "user" | "agent"
  text: string
  at: string           // RFC3339
}

RunnerInfo {
  hostname: string     // tailnet hostname
  busy: boolean         // true if any session across any project is busy
  version: string
}
```

## Endpoints

| Method | Path | Body | Response | Notes |
|---|---|---|---|---|
| GET | `/v1/health` | - | `200 {"ok":true}` | no auth required, for basic reachability checks |
| GET | `/v1/runner/info` | - | `200 RunnerInfo` | |
| GET | `/v1/projects` | - | `200 Project[]` | |
| POST | `/v1/projects` | `{name: string}` | `201 Project` | scaffolds a new project dir under the runner's configured projects root |
| GET | `/v1/projects/{projectId}/sessions` | - | `200 Session[]` | includes finished sessions not yet purged by weekly cleanup |
| POST | `/v1/projects/{projectId}/sessions` | `{provider: string}` | `201 Session` | spawns the configured CLI command for `provider`, scoped to the project dir |
| GET | `/v1/sessions/{sessionId}` | - | `200 Session` | full transcript so far |
| POST | `/v1/sessions/{sessionId}/message` | `{text: string}` | `202 {}` | appends a user message and feeds it to the running agent subprocess's stdin |
| POST | `/v1/sessions/{sessionId}/stop` | - | `200 Session` | kills the subprocess, marks session `finished` |
| POST | `/v1/projects/{projectId}/containers/start` | - | `200 {}` | runs `docker compose up -d` in the project dir |
| POST | `/v1/projects/{projectId}/containers/stop` | - | `200 {}` | runs `docker compose down` in the project dir |

Errors: `4xx/5xx` bodies are `{"error": string}`.

## Not covered by this contract (see ARCHITECTURE.md)

- Wake-on-LAN: not an HTTP call to the target runner (it's off) — the phone sends the WoL
  packet itself, or asks the relay-device runner to. `[NOT IMPLEMENTED]` in this pass.
- Job-done push notifications: delivered via FCM, not this API — the runner calls Google's FCM
  API directly when a session transitions to `finished`. `[NOT IMPLEMENTED]` in this pass
  (needs a Firebase project + credentials from the user).
