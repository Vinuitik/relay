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
  localSubnet: string? // this runner's LAN network in CIDR form, e.g. "192.168.1.0/24" —
                        // omitted if it couldn't be detected. Used to auto-match which two
                        // known runners are on the same physical LAN for Wake-on-LAN; see
                        // ARCHITECTURE.md "Relay device".
}

Device {
  id: string           // stable id for this phone install, generated client-side
  fcmToken: string
  registeredAt: string // RFC3339
}

UptimeInterval {
  start: string        // RFC3339
  end: string?         // RFC3339, null while this interval is still ongoing (runner is up now)
}

FileEntry {
  name: string
  isDir: boolean
  size: number         // bytes; 0 for directories
}

FileContent {
  path: string         // echoes the requested project-relative path
  content: string      // raw file text (UTF-8/ASCII assumed)
}

BrowseResult {
  path: string         // the absolute path that was listed (empty when listing filesystem roots)
  entries: {name: string}[]  // subdirectories only, hidden (dotfile) dirs excluded
}
```

## Endpoints

| Method | Path | Body | Response | Notes |
|---|---|---|---|---|
| GET | `/v1/health` | - | `200 {"ok":true}` | no auth required, for basic reachability checks |
| GET | `/v1/runner/info` | - | `200 RunnerInfo` | |
| GET | `/v1/projects` | - | `200 Project[]` | |
| POST | `/v1/projects` | `{name: string, path?: string}` | `201 Project` | scaffolds a new project dir under the runner's configured projects root, unless `path` is set, in which case that already-existing absolute directory is registered as-is (name defaults to the directory's base name). `400` if `path` doesn't exist, isn't a directory, or is already registered. |
| GET | `/v1/browse?path=<absolute>` | - | `200 BrowseResult` | lists subdirectories of `path` — **unscoped**, unlike the project file-viewing endpoints, since its purpose is finding a directory to register via `POST /v1/projects {path}` before any project-level scoping exists. `path` omitted/empty lists filesystem roots. `400` if `path` isn't absolute or isn't a directory. |
| GET | `/v1/projects/{projectId}/sessions` | - | `200 Session[]` | includes finished sessions not yet purged by weekly cleanup |
| POST | `/v1/projects/{projectId}/sessions` | `{provider: string}` | `201 Session` | spawns the configured CLI command for `provider`, scoped to the project dir |
| POST | `/v1/auth/login` | - | `201 Session` | starts the `claude`/`codex` CLI's OAuth login as a session with no associated project (`projectId` is `""`) — a one-time, per-machine admin action for when there's no local browser to complete auth with. The OAuth URL the CLI prints shows up as an "agent" `Message`; paste the code it asks for via the ordinary `POST /v1/sessions/{id}/message`. Which command runs is `RELAY_PROVIDER_AUTH_LOGIN` (defaults to `claude auth login`) — see runner/FLOWS.md "Auth-login". |
| GET | `/v1/sessions/{sessionId}` | - | `200 Session` | full transcript so far |
| POST | `/v1/sessions/{sessionId}/message` | `{text: string}` | `202 {}` | appends a user message and feeds it to the running agent subprocess's stdin |
| POST | `/v1/sessions/{sessionId}/stop` | - | `200 Session` | kills the subprocess, marks session `finished` |
| POST | `/v1/projects/{projectId}/containers/start` | - | `200 {}` | runs `docker compose up -d` in the project dir |
| POST | `/v1/projects/{projectId}/containers/stop` | - | `200 {}` | runs `docker compose down` in the project dir |
| POST | `/v1/containers/start-all` | - | `200 [{projectId, ok, error?}]` | runs `docker compose up -d` across every project known to this runner; best-effort per project, one failure doesn't block the rest |
| POST | `/v1/containers/stop-all` | - | `200 [{projectId, ok, error?}]` | runs `docker compose down` across every project known to this runner; best-effort per project, one failure doesn't block the rest |
| POST | `/v1/wake` | `{mac: string}` | `202 {}` | sends a Wake-on-LAN magic packet as a UDP broadcast on **this runner's own local network**. Call this on whichever known runner is on the same LAN as the machine you want to wake — never on the target itself, since if it's off it can't be reached. See ARCHITECTURE.md "Relay device". |
| POST | `/v1/devices` | `{fcmToken: string}` | `200 Device` | registers/updates this phone's FCM push token with this runner, so the runner can notify it on session-finish (or on suspend, see below). Call on every known runner, and again whenever the token refreshes. Runner-side sending is a no-op until a Firebase credential is configured — see Technology Notes in runner/FLOWS.md. |
| GET | `/v1/uptime` | - | `200 UptimeInterval[]` | this runner's own up/down interval history, oldest first. **Short-term buffer only** (14 days, see runner/FLOWS.md "Uptime tracking") — the phone app is expected to poll this whenever a runner is reachable and persist its own merged weekly history locally, since a runner going to sleep is exactly when it becomes unreachable to ask. |
| POST | `/v1/suspend` | - | `202 {}` | manually suspends this machine to S5 right now, instead of waiting for the idle timeout. `409` if any session is currently busy (never kills active work); `503` if the runner wasn't started with `RELAY_IDLE_SUSPEND_ENABLED=true` — this endpoint deliberately reuses that same opt-in gate, see runner/FLOWS.md "Idle-suspend". |
| GET | `/v1/projects/{projectId}/files?path=<relative>` | - | `200 FileEntry[]` | lists a directory within the project. `path` omitted/empty = project root. `400` if `path` escapes the project directory (`../`) or isn't a directory. Read-only — see ARCHITECTURE.md "Runner responsibilities". |
| GET | `/v1/projects/{projectId}/files/content?path=<relative>` | - | `200 FileContent` | returns one file's text content. `400` if `path` is missing/escapes the project dir/is a directory, `404` if it doesn't exist, `413` if over 1MiB, `415` if it looks binary (a null byte in the first 512 bytes). |

Errors: `4xx/5xx` bodies are `{"error": string}`.

## FCM message `data.type` values

Every push the runner sends is data-only (no `notification` block — the app builds its own, see
`RelayFirebaseMessagingService.notificationContentFor`). `data.type` is:

- `session_finished` — `data: {type, sessionId, projectId}`
- `runner_suspending` — `data: {type, hostname}`, sent best-effort right before the runner
  suspends to S5 (never on a manual `/v1/sessions/{id}/stop`) — see runner/FLOWS.md "Idle-suspend".

An older app build (or a message missing `type` entirely) falls back to the `session_finished`
text, so this list can grow without breaking already-installed clients.

## Not covered by this contract (see ARCHITECTURE.md)

- Wake-on-LAN: `/v1/wake` above covers "make some runner broadcast a magic packet." What's
  still open: how the phone app decides *which* known runner is on the same LAN as a given
  wake target (today: the user configures this manually per target — see android/FLOWS.md).
  No physical relay device exists yet; any already-running runner instance can serve this role
  since it's the same binary everywhere.
- Job-done push notifications: `/v1/devices` above covers token registration. Actually sending
  a push still needs a real Firebase project + service-account credential from the user
  (`RELAY_FCM_CREDENTIALS` env var pointing at the downloaded JSON key) — without it the runner
  logs and skips instead of sending. `[NOT IMPLEMENTED]` until that credential exists.
