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
  state: string        // "busy" | "idle" | "waiting" (paused on a permission request) | "finished" | "error"
  createdAt: string
  finishedAt: string?  // null while not finished
  messages: Message[]
  title: string        // first user message, one line, <=80 runes ("…" if cut); "" if none
  preview: string      // last non-empty agent message, one line, <=120 runes ("…" if cut); "" if none
  // ACP providers only (claude), omitted otherwise:
  mode?: string                         // current permission mode id, e.g. "bypassPermissions"
  modes?: {id, name, description?}[]    // what the agent offers
  lastActiveAt?: string                 // RFC3339, last change; bumped ~every 30s while busy
  pendingPermission?: {                 // set while state == "waiting"
    title: string, toolKind: string,
    options: {optionId, name, kind}[]   // kind: allow_once | allow_always | reject_once | reject_always
  }
}

Message {
  role: string         // "user" | "agent" | "tool" (one agent step, e.g. a file edit or command)
  text: string         // for "tool": one-line title, e.g. "Write hello.txt"
  at: string           // RFC3339
  toolKind?: string    // tool only: read | edit | delete | move | search | execute | think | fetch | other
  status?: string      // tool only: pending | in_progress | completed | failed
  kind?: string        // agent only: "quota" (subscription limit used up) | "auth" (login missing/expired)
}

RunnerInfo {
  hostname: string     // tailnet hostname
  busy: boolean         // true if any session across any project is busy
  version: string
}

Device {
  id: string           // stable id for this phone install, generated client-side
  fcmToken: string
  registeredAt: string // RFC3339
  runnerRef: string    // address the phone uses for this runner, as sent at registration; "" if never sent
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
  parent?: string      // one level up ("" = the filesystem-roots view); absent at the top
  entries: {name: string}[]  // subdirectories only, hidden (dotfile) dirs excluded
}
```

### ContainersStatus

```
{
  composeFiles: string[],      // file names, sorted
  activeFile: string,          // "" = several files and none chosen yet
  hasDockerfile: boolean,
  containers: { service: string, state: string, composeFile: string }[],
  operation: "" | "starting" | "switching" | "stopping",
  lastError: string,           // from the last finished start/stop, "" if it succeeded
  dockerError: string          // docker itself unreachable (not running, not installed)
}
```

`Project` also gains `activeComposeFile?: string`.

## Endpoints

| Method | Path | Body | Response | Notes |
|---|---|---|---|---|
| GET | `/v1/health` | - | `200 {"ok":true}` | no auth required, for basic reachability checks |
| GET | `/v1/runner/info` | - | `200 RunnerInfo` | |
| GET | `/v1/projects` | - | `200 Project[]` | |
| POST | `/v1/projects` | `{name: string, path?: string}` | `201 Project` | scaffolds a new project dir under the runner's configured projects root, unless `path` is set, in which case that already-existing absolute directory is registered as-is (name defaults to the directory's base name). `400` if `path` doesn't exist, isn't a directory, or is already registered. |
| GET | `/v1/browse?path=<absolute>` | - | `200 BrowseResult` | lists subdirectories of `path` — **unscoped**, unlike the project file-viewing endpoints, since its purpose is finding a directory to register via `POST /v1/projects {path}` before any project-level scoping exists. `path` omitted/empty lists filesystem roots; `path=~` lists the user's Documents folder (falls back to home) - where the picker opens. `400` if `path` isn't absolute or isn't a directory. |
| GET | `/v1/projects/{projectId}/sessions` | - | `200 Session[]` | includes finished sessions not yet purged by weekly cleanup |
| POST | `/v1/projects/{projectId}/sessions` | `{provider: string}` | `201 Session` | spawns the configured CLI command for `provider`, scoped to the project dir |
| GET | `/v1/sessions?state=<s1,s2>` | - | `200 Session[]` | sessions across **all** projects (the app's "Needs you" strip). `state` optional, comma list (e.g. `waiting,busy`); omitted = every state. Sorted: `waiting` first, then `busy`, then the rest; within each, `lastActiveAt` (else `createdAt`) newest first. |
| GET | `/v1/sessions/{sessionId}` | - | `200 Session` | full transcript so far |
| POST | `/v1/sessions/{sessionId}/message` | `{text: string}` | `202 {}` | appends a user message and starts a turn; the reply streams into `messages` (poll `GET`). `409` while the previous turn is still running. |
| POST | `/v1/sessions/{sessionId}/stop` | - | `200 Session` | kills the subprocess, marks session `finished` |
| POST | `/v1/sessions/{sessionId}/cancel` | - | `200 Session` | stops the current turn but keeps the session (the Stop button); the session goes `idle` shortly after. No-op if nothing is running. ACP only, `400` otherwise. |
| POST | `/v1/sessions/{sessionId}/mode` | `{modeId: string}` | `200 Session` | switches permission mode, one of `modes`. `400` for an unknown id. ACP only. |
| POST | `/v1/sessions/{sessionId}/permission` | `{optionId: string}` | `200 Session` | answers `pendingPermission` with one of its options. `400` if nothing is pending or the option is unknown. |
| GET | `/v1/projects/{projectId}/containers` | - | `200 ContainersStatus` | compose files found at the project's top level (`docker-compose.yml`, `docker-compose.dev.yml`, `compose-prod.yaml`, ...), the active one, and every container compose started from this directory. Only calls docker if a compose file exists; docker failures go in `dockerError`, not an HTTP error. |
| POST | `/v1/projects/{projectId}/containers/start` | `{file?: string}` (body optional) | `202 ContainersStatus` | turns on `file` (default: the active file) with `docker compose -f <file> up -d`. If a different file was active it's brought down first - a **switch**, so only one compose file runs per project. The choice is persisted. Runs in the background: poll GET until `operation` is `""`, then check `lastError`. `400` if no compose file, `file` isn't one of `composeFiles`, or several exist and none is chosen yet; `409` if an operation is already running. |
| POST | `/v1/projects/{projectId}/containers/stop` | - | `202 ContainersStatus` | `docker compose -f <active> down --remove-orphans` in the background - removes containers from any of the project's compose files. Same `400`/`409` as start. |
| POST | `/v1/devices` | `{fcmToken: string, runnerRef?: string}` | `200 Device` | registers/updates this phone's FCM push token with this runner (`runnerRef`: the address the phone uses for this runner, e.g. its Tailscale hostname - stored and echoed in every push; re-registering overwrites it), so the runner can notify it on session-finish (or on suspend, see below). Call on every known runner, and again whenever the token refreshes. Runner-side sending is a no-op until a Firebase credential is configured — see Technology Notes in runner/FLOWS.md. |
| POST | `/v1/suspend` | - | `202 {}` | manually suspends this machine to sleep (S3 on Linux, Modern Standby on Windows) right now, instead of waiting for the idle timeout. `409` if any session is currently busy (never kills active work); `503` if the runner wasn't started with `RELAY_IDLE_SUSPEND_ENABLED=true` — this endpoint deliberately reuses that same opt-in gate, see runner/FLOWS.md "Idle-suspend". |
| POST | `/v1/activity` | - | `202 {}` | tells the runner "the phone app is in the foreground right now" — one of the signals idle-suspend uses to decide whether to suspend the machine, alongside session busy/idle state and local keyboard/mouse input. Call every 30s while, and only while, the app is in the foreground; stop calling when it's backgrounded or closed. Always accepted, even if idle-suspend is disabled on this runner — see runner/FLOWS.md "Idle-suspend". |
| GET | `/v1/projects/{projectId}/files?path=<relative>` | - | `200 FileEntry[]` | lists a directory within the project. `path` omitted/empty = project root. `400` if `path` escapes the project directory (`../`) or isn't a directory. Read-only — see ARCHITECTURE.md "Runner responsibilities". |
| GET | `/v1/projects/{projectId}/files/content?path=<relative>` | - | `200 FileContent` | returns one file's text content. `400` if `path` is missing/escapes the project dir/is a directory, `404` if it doesn't exist, `413` if over 1MiB, `415` if it looks binary (a null byte in the first 512 bytes). |

Errors: `4xx/5xx` bodies are `{"error": string}`.

## FCM message `data.type` values

Every push the runner sends is data-only (no `notification` block — the app builds its own, see
`RelayFirebaseMessagingService.notificationContentFor`). Every payload below also carries
`runnerRef` — the value this device registered via `POST /v1/devices` (`""` if it never sent one),
so the app knows which runner sent the push. `data.type` is:

- `session_finished` — `data: {type, sessionId, projectId, problem, runnerRef}` (once per completed agent turn;
  `problem` is `"quota"` / `"auth"` when the turn ended on that provider problem, `""` otherwise)
- `session_needs_input` — `data: {type, sessionId, projectId, runnerRef}`, the agent is paused on a
  permission request (session `waiting`)
- `runner_suspending` — `data: {type, hostname, runnerRef}`, sent best-effort right before the runner
  suspends to sleep (never on a manual `/v1/sessions/{id}/stop`) — see runner/FLOWS.md "Idle-suspend".

An older app build (or a message missing `type` entirely) falls back to the `session_finished`
text, so this list can grow without breaking already-installed clients.

## Not covered by this contract (see ARCHITECTURE.md)

- Job-done push notifications: `/v1/devices` above covers token registration. Actually sending
  a push still needs a real Firebase project + service-account credential from the user
  (`RELAY_FCM_CREDENTIALS` env var pointing at the downloaded JSON key) — without it the runner
  logs and skips instead of sending. `[NOT IMPLEMENTED]` until that credential exists.
