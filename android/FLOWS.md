# Android app flows

Files: MainActivity.kt, RelayNavHost.kt, RunnerListScreen.kt, QrScanScreen.kt,
ProjectListScreen.kt, SessionListScreen.kt, ChatScreen.kt, FileBrowserScreen.kt,
FolderPickerScreen.kt, KnownRunnersRepository.kt, RelayApiClient.kt, RelayApiService.kt,
RelayFirebaseMessagingService.kt, RegisterDeviceWorker.kt

## Scope

The app is deliberately down to one core loop: **pair a runner → list projects → list sessions →
chat**, plus a read-only file browser and FCM push. A 2026-09-20 pass deleted everything built
ahead of that loop — home-screen widget, uptime dashboard, Room offline cache, Wake-on-LAN
config, generated friendly names, in-app update prompt, bulk container start/stop, and the
auth-login relay. See "Deleted 2026-09-20" at the bottom for what's gone and why, so nobody goes
looking for it.

## Navigation

MainActivity → RelayNavHost →
RunnerListScreen → ProjectListScreen(runner) → SessionListScreen(runner, project)
→ ChatScreen(runner, project, session)

Side branches off ProjectListScreen: FileBrowserScreen (read-only, per project) and
FolderPickerScreen (register an existing folder as a project).

To add a screen: RelayNavHost.kt

## Data flow

RunnerListScreen ↔ KnownRunnersRepository (DataStore Preferences) — `hostname` + `port` + `key`
+ optional `displayName` per runner, one JSON blob under a single prefs key. `hostname` is the
de-facto id: `addRunner`/`updateRunner` upsert by it and `removeRunner` deletes by it. Added via
QR scan by default (see "Pairing"); manual entry is the fallback.

Every screen: `RelayApiClient.forRunner(runner)` → `RelayApiService` (Retrofit+OkHttp+Moshi) →
`X-Relay-Key` header on every call → runner's `shared/API.md` v1 endpoints. **Network only — no
local cache of sessions/messages anymore**, so an unreachable runner means an error message, not
stale data.

ChatScreen polls `GET /v1/sessions/{id}` every 3s via LaunchedEffect+delay while state is
"busy" — no WebSocket/streaming (matches the API contract).

To change poll interval: `ChatScreen.kt` (`POLL_INTERVAL_MS`)
To change API base URL scheme (http vs https): `RelayApiClient.kt`
To change the default port: `model/Models.kt` (`KnownRunner.DEFAULT_PORT` — **7777**, matching
the runner's own default; this was wrong at 8080 until 2026-09-20)

## Display name (label a runner instead of showing its raw address)

Files: model/Models.kt (`KnownRunner.displayName`, `.label`), RunnerListScreen.kt
(`NameRunnerDialog`, `AddRunnerDialog`)

`KnownRunner.hostname` is the Tailscale address/IP used to actually connect (e.g.
"100.124.46.7") — a meaningless string of digits to read as a name. `displayName` is a separate,
purely cosmetic field set once at pairing time and never recomputed; `KnownRunner.label`
(`displayName` if set, else `hostname`) is what every screen shows.

Both name dialogs start **empty** with the hostname as placeholder text; leaving the field blank
falls back to the hostname. (There used to be a `FriendlyNameGenerator` producing an "Adjective
Noun" default — deleted; a random name nobody chose is worse than the address.)

`hostname` itself is untouched everywhere it's used as an actual identifier
(`RelayApiClient`'s base URL, `KnownRunnersRepository`'s upsert key) — only display surfaces use
`.label`.

## Pairing (QR scan)

Files: QrScanScreen.kt, RunnerListScreen.kt

RunnerListScreen's "+" FAB → `QrScanScreen` (full-screen, replaces the whole screen content
while open — see the `if (showQrScan) { ...; return }` early-return at the top of
`RunnerListScreen`) → requests CAMERA permission → CameraX preview + ML Kit on-device barcode
decode → `parseRelayQrContent(rawValue)` → on a valid `relay://host:port?key=...` match:

- **dedup check first** (`runners.find { it.hostname == scanned.hostname }`) — a re-scan of an
  already-known runner just refreshes its `key` in place via `updateRunner` and shows an
  "Already added as X — refreshed" toast. No naming dialog, no re-add flow.
- **new hostname** → `NameRunnerDialog` → confirmed name (or the hostname if left blank) →
  `KnownRunnersRepository.addRunner(...)`.

Unknown query parameters in the QR URI (the runner still emits `mac=` for the wake feature it
once fed) are ignored by `parseRelayQrContent`.

Manual entry is reachable two ways: `AddRunnerDialog` directly, or tapping "Enter key manually
instead" from inside the scan screen (denied camera permission, no camera, or preference).

## Connection error messages

Files: network/RelayApiClient.kt (`friendlyErrorMessage`)

Every screen's network catch block routes its exception through `friendlyErrorMessage(e,
runner)` instead of using `e.message` directly — a raw `ConnectException`/`SocketTimeoutException`
message like "Failed to connect to /100.124.46.7 (port 7777) after 1000ms: ECONNREFUSED" reads
as a slow timeout when it's usually actually a near-instant refusal, and gives no hint what to
check. The friendly version names the runner, its address, and walks through the three real
causes found so far: runner not running, this phone's Tailscale not connected, or — confirmed by
hand once already (2026-09-19, a Windows Firewall auto-generated inbound Block rule on the
runner's Public-profile connection silently dropped every connection from other Tailscale peers
while working fine from the runner's own machine) — a host firewall blocking inbound connections
from other devices specifically.

To change the wording or add another exception type: `network/RelayApiClient.kt`
(`friendlyErrorMessage`).

## Manual suspend ("Sleep") / remove runner

Files: RunnerListScreen.kt (`suspendRunner`), network/RelayApiService.kt (`suspend()`)

RunnerListScreen row → **"Sleep" button** → confirm dialog (explains: refused if busy) →
`RelayApiClient.forRunner(runner).suspend()` called **directly from a coroutine on the screen**
→ every outcome Toasted, because 409/503 are the normal answers to this button, not crashes:

| Response | Toast |
|---|---|
| 2xx | "<label> is going to sleep" |
| 409 | "<label> is busy - a session is still running, nothing was stopped" |
| 503 | "<label> can't sleep on request - it wasn't started with RELAY_IDLE_SUSPEND_ENABLED=true" |
| other HTTP | "<label>: sleep failed (HTTP nnn)" |
| exception | `friendlyErrorMessage(e, runner)` |

This used to go through a `SuspendRunnerWorker` (WorkManager) whose 409/503 handling was a
`Log.w` line — the user tapped Sleep and saw nothing at all. The worker is deleted; the direct
call is both simpler and the only way the result is actually visible.

To change: `ui/screens/RunnerListScreen.kt` (`suspendRunner`).

"Remove runner" (overflow ⋮ menu) → confirm dialog → `repository.removeRunner(hostname)` —
local-only, the runner itself is untouched; re-adding is just a rescan since the runner's key
never changes.

## New project: scaffold vs. register existing folder

Files: ProjectListScreen.kt, FolderPickerScreen.kt

ProjectListScreen's "+" FAB opens a small `DropdownMenu`: "Scaffold new empty project"
(`POST /v1/projects {name}`) or "Register existing folder" → `onPickFolder` →
`FolderPickerScreen`.

`FolderPickerScreen` is an unscoped filesystem browser (`GET /v1/browse`, see runner/FLOWS.md) —
deliberately separate from `FileBrowserScreen`, which is read-only and scoped to one
already-registered project. Path history is a stack of absolute paths (empty string = the
filesystem-roots view), same local-state back-navigation pattern as `FileBrowserScreen`. Its
"select this folder" FAB (hidden at the roots view — registering `/` or `C:\` itself is
nonsensical) calls `POST /v1/projects {path}` **directly from this screen**, then hands the
resulting `Project` to `onRegistered` — registration happens here, not back in
`ProjectListScreen`, specifically to avoid the awkwardness of popping back to a list screen that
has no way to know it should re-fetch.

`RelayNavHost`'s `FOLDER_PICKER` route wires `onRegistered` to navigate straight into that
project's session list (`popUpTo` the project list), skipping an intermediate "now go tap the
project you just made" step.

To change: `ui/screens/FolderPickerScreen.kt`, `ui/screens/ProjectListScreen.kt`'s add-menu.

## File browser (read-only)

Files: FileBrowserScreen.kt, RelayApiService.kt

ProjectListScreen row → folder icon → `FileBrowserScreen`. Directory drill-down and file viewing
are both local Compose state here, not `RelayNavHost` routes — tapping a directory appends to a
`pathSegments` list, tapping a file sets `viewingFile` and fetches its content; the
hardware/gesture back button (`BackHandler`) steps back one level (out of a file, then up one
directory) before finally calling `onBack` to leave the screen. `GET /v1/projects/{id}/files`
lists a directory, `GET /v1/projects/{id}/files/content` returns one file's text — see
runner/FLOWS.md "File viewing" for the server side. Read-only — no edit/save affordance exists.

To change: `ui/screens/FileBrowserScreen.kt`.

## FCM device registration

`RelayFirebaseMessagingService.onNewToken(token)` → enqueues `RegisterDeviceWorker` (WorkManager)
→ loops every known runner from KnownRunnersRepository → POSTs `{"fcmToken": token}` to
`/v1/devices` on each, using that runner's own key. One unreachable runner logs + is skipped;
does not block the others.

`MainActivity.registerCurrentFcmTokenWithAllRunners()` does the same thing once at app startup
using `FirebaseMessaging.getInstance().token` — covers a runner added AFTER the token was already
issued, without waiting for a token refresh to fire `onNewToken`.

A real Firebase project (`relay-sizonenko`) exists — `google-services.json` is present
(gitignored, generated via `firebase apps:sdkconfig`) and `app/build.gradle.kts` applies the
`com.google.gms.google-services` plugin conditionally on that file existing, so a checkout
without it (any fresh clone) still builds, just without live FCM. The try/catch in
`MainActivity.registerCurrentFcmTokenWithAllRunners()` stays as a safety net for that case.

`onMessageReceived` posts a real Android notification — tapping it opens `MainActivity`.
Requires POST_NOTIFICATIONS granted at runtime on API 33+ (requested once at startup in
`MainActivity.requestNotificationPermissionIfNeeded()`); if denied, pushes still arrive and
register but no banner shows (Android silently drops it, not something this code can detect).

To change: `fcm/RegisterDeviceWorker.kt`, `MainActivity.registerCurrentFcmTokenWithAllRunners()`,
`fcm/RelayFirebaseMessagingService.onMessageReceived()` (notification content/channel).

## Building (no local JDK/Gradle — Docker only)

There is no JDK or Android SDK on the dev machine. Builds run in `mingc/android-build-box`
(bundles JDK 17 + Android SDK + Gradle), reusing a persistent `relay-gradle-cache` volume so the
Gradle distro/dependencies aren't re-downloaded every time:

```
docker run --rm -v "$PWD:/project" -v relay-gradle-cache:/root/.gradle \
  -w /project mingc/android-build-box bash -c "./gradlew assembleDebug"
```

(run from `android/`; the image is ~10 GB, first pull is slow).

## Distribution (Firebase App Distribution)

Files: .firebaserc, .github/workflows/android-deploy.yml

Not on the Play Store by design (see ARCHITECTURE.md), so updates ship via Firebase App
Distribution — reuses the same `relay-sizonenko` Firebase project already wired for FCM.

**Shipping a build is automatic**: a push to `main` touching `android/**` triggers
`.github/workflows/android-deploy.yml`, which builds and distributes. Manual equivalent:
`npx firebase appdistribution:distribute <path-to-apk> --app
1:960396843466:android:fd2974c630731375591733 --testers sizonenkodima6@gmail.com` (run from
`android/`, project comes from `.firebaserc`).

Firebase emails testers on every release — no dev-side toggle exists to suppress it (a
firebase-tools feature request for one was filed and closed "not planned"); only the tester can
opt out from their own App Distribution web view. **There is no in-app update prompt anymore**
(the `firebase-appdistribution` SDK and `MainActivity.checkForUpdate` were deleted 2026-09-20) —
installing a new build means going through the emailed link / the App Distribution app.

**A force-push to `main` never triggers this workflow.** GitHub's `push` event is not emitted for
a non-fast-forward ref update — confirmed 2026-09-16 by diffing the repo's public events feed:
every ordinary push to `main` shows up as a `PushEvent`, a force-pushed commit showed zero
matching event and zero workflow run. Not a bug, not a quota limit — GitHub simply never tells
Actions a push happened. After ANY force-push to `main`, fire the build manually:
`gh workflow run android-deploy.yml --ref main`.

To change who gets releases: swap `--testers` for `--groups <name>` (create the group in the
Firebase console first). To change the app targeted: the `--app` id comes from
`android/app/google-services.json` (`mobilesdk_app_id`).

**`--release-notes` is passed via an `env:` var (`$RELEASE_NOTES`), never
`"${{ github.event.head_commit.message }}"` interpolated directly into a quoted shell string.**
A commit message containing a literal `"` closes that shell string early and the rest gets parsed
as stray arguments — `appdistribution:distribute` failed outright with "Too many arguments."
`$VAR` expansion doesn't re-parse its value as shell syntax. Same fix in runner-release.yml's
`--notes`.

## Technology notes

- **Cleartext HTTP was blocked on every real device until 2026-09-16.** `RelayApiClient` talks
  plain `http://` deliberately (ARCHITECTURE.md: Tailscale's tunnel is the transport security,
  not app-level TLS), but Android has blocked cleartext traffic by default since API 28 and the
  manifest never declared `android:usesCleartextTraffic="true"` to opt back in. Every API call
  failed with "not permitted by network security policy" the first time this app ever ran against
  a real runner — invisible to the Docker-only compile check, since that never executes the app.
  Fixed in `AndroidManifest.xml`.
- **No build/run verification via emulator** — none available. Compile verified via the
  Dockerized `./gradlew assembleDebug` above; no instrumented/UI tests have ever run, so every
  flow here is compile-checked only, never seen on a device.
- **`KnownRunner.DEFAULT_PORT` was 8080 until 2026-09-20 while the runner defaults to 7777.**
  Only ever hit by manual entry / a QR code with no explicit port, which is why it survived this
  long. If the runner's default ever changes, this constant must change with it — nothing
  enforces the pairing.
- **No offline cache at all anymore.** Sessions, messages and project lists are fetched live on
  every screen entry; an asleep or unreachable runner shows `friendlyErrorMessage`, not a stale
  transcript. The Room cache that used to back this was deleted 2026-09-20 (it was a
  write-through mirror, so nothing unrecoverable was stored in it). If offline reading is wanted
  back, it's a fresh decision, not a revert.
- **DataStore Preferences has no encryption** — the runner key is stored in plaintext prefs.
  Acceptable for now (single-user, own devices) but worth revisiting before wider use.
- **Chat polling is a fixed 3s loop, only while `state == "busy"`.** It stops on any exception
  (and shows the error) rather than retrying — a runner that goes away mid-session needs the
  screen re-entered. There is no backoff and no WebSocket.
- **WorkManager is now used for exactly one thing** (`RegisterDeviceWorker`, FCM token
  registration) and has no dedup/backoff tuning — `OneTimeWorkRequestBuilder` defaults. Its loop
  over runners is sequential, so a slow/unreachable runner delays (but does not block, thanks to
  the try/catch) the rest.
- **FCM is live end-to-end in code**, gated only on the runner side having
  `RELAY_FCM_CREDENTIALS` pointed at a real service-account key (see runner/FLOWS.md) — without
  that the runner still registers devices but silently skips sending.

## Deleted 2026-09-20

Removed as premature/broken, so the app is only the core loop. Listed so nobody hunts for them:

| Gone | Was | Why |
|---|---|---|
| `widget/` package, `data/WidgetConfigRepository.kt`, `res/layout/relay_widget.xml`, `res/xml/relay_widget_info.xml`, manifest `<receiver>` | home-screen widget + its Stop/Wake workers, the star "widget default project" toggle | never finished; widget actions were invisible-failure background workers |
| `ui/screens/DashboardScreen.kt`, `data/UptimeSyncWorker.kt`, `MainActivity.scheduleUptimeSync`, `GET /v1/uptime` client call, `UptimeInterval` | per-app uptime history | built before there was anything to measure |
| `data/db/` (Room: Entities/Daos/RelayDatabase), Room+KSP gradle deps, all cache reads/writes in ChatScreen + SessionListScreen | offline cache of sessions/messages | see Technology notes above |
| `data/WakeViaMatcher.kt`, `KnownRunner.wakeMac`/`wakeViaRunnerId`, "Wake settings" menu item + dialog, "Wake" button, `mac` on `ScannedRunner`, `wake()` API call | Wake-on-LAN config held on the phone | wake is moving to a separate always-on Pi daemon; the phone will not hold wake config |
| `data/FriendlyNameGenerator.kt` | generated "Adjective Noun" default runner name | replaced by an empty field defaulting to the hostname |
| "Authenticate agent" menu item, `startAuthLogin()`, `AUTH_LOGIN_CHAT` route, `onAuthLoginStarted` | headless OAuth relay through ChatScreen | premature |
| `MainActivity.checkForUpdate` / `UpdateAvailableDialog` / `pendingUpdate`, `firebase-appdistribution` dep | in-app "update available" prompt | premature |
| "Start/Stop all containers" menu items, `ContainersAllWorker`, `startAllContainers()`/`stopAllContainers()`, `ContainerActionResult` | bulk container control per runner | premature; failures were never surfaced |

`SuspendRunnerWorker` is also gone, but the **Sleep button stayed** and now calls
`RelayApiService.suspend()` directly with visible results — see "Manual suspend" above.
Per-project `startContainers`/`stopContainers` remain on `RelayApiService` (unused by any screen
today).

## Change Index

| Thing | Where |
|---|---|
| Known runners storage | `data/KnownRunnersRepository.kt`, `model/Models.kt` (`KnownRunner`) |
| Default runner port (7777) | `model/Models.kt` (`KnownRunner.DEFAULT_PORT`) |
| QR pairing content parsing | `ui/screens/QrScanScreen.kt` (`parseRelayQrContent`) |
| Runner display name / blank fallback | `model/Models.kt` (`.displayName`, `.label`), `ui/screens/RunnerListScreen.kt` (`NameRunnerDialog`, `AddRunnerDialog`) |
| API types (must match shared/API.md) | `model/Models.kt` |
| HTTP client / auth header | `network/RelayApiClient.kt`, `network/RelayApiService.kt` |
| Connection error wording | `network/RelayApiClient.kt` (`friendlyErrorMessage`) |
| Navigation graph / routes | `ui/navigation/RelayNavHost.kt` (`Routes`) |
| Chat poll interval | `ui/screens/ChatScreen.kt` (`POLL_INTERVAL_MS`) |
| Sleep button + its 409/503/error Toasts | `ui/screens/RunnerListScreen.kt` (`suspendRunner`) |
| Remove runner | `data/KnownRunnersRepository.kt` (`removeRunner`), `ui/screens/RunnerListScreen.kt` |
| Session provider list ("claude"/"codex") | `ui/screens/SessionListScreen.kt` (`KNOWN_PROVIDERS`) |
| Register existing folder / new-project menu | `ui/screens/ProjectListScreen.kt`, `ui/screens/FolderPickerScreen.kt` |
| File browser screen | `ui/screens/FileBrowserScreen.kt` |
| FCM token registration (on refresh) | `fcm/RelayFirebaseMessagingService.kt` |
| FCM token registration (on app startup) | `MainActivity.kt` |
| FCM registration background call (loops all runners) | `fcm/RegisterDeviceWorker.kt` |
| Notification content/channel | `fcm/RelayFirebaseMessagingService.onMessageReceived()` |
| Cleartext HTTP opt-in | `app/src/main/AndroidManifest.xml` (`android:usesCleartextTraffic`) |
| Gradle/Kotlin/Compose versions | `app/build.gradle.kts`, `build.gradle.kts`, `gradle/wrapper/gradle-wrapper.properties` |
| Dockerized build command | this file, "Building" |
| Distribution target (Firebase project/app) | `.firebaserc`, `app/google-services.json` |
| Auto-distribute on push to main | `.github/workflows/android-deploy.yml` |
