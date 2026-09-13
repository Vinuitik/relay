# Android app flows

Files: MainActivity.kt, RelayNavHost.kt, RunnerListScreen.kt, ProjectListScreen.kt,
SessionListScreen.kt, ChatScreen.kt, KnownRunnersRepository.kt, WidgetConfigRepository.kt,
RelayApiClient.kt, RelayApiService.kt, RelayFirebaseMessagingService.kt,
RelayWidgetProvider.kt, StopContainersWorker.kt, WakeRunnerWorker.kt, RegisterDeviceWorker.kt

## Navigation

MainActivity → RelayNavHost →
RunnerListScreen → ProjectListScreen(runner) → SessionListScreen(runner, project)
→ ChatScreen(runner, project, session)

To add a screen: RelayNavHost.kt

## Data flow

RunnerListScreen ↔ KnownRunnersRepository (DataStore Preferences) — hostname+key pairs, added
manually per ARCHITECTURE.md's manual-registration decision.

ProjectListScreen/SessionListScreen/ChatScreen → RelayApiClient.forRunner(hostname, key) →
RelayApiService (Retrofit+OkHttp+Moshi) → `X-Relay-Key` header on every call → runner's
`shared/API.md` v1 endpoints.

ChatScreen polls GET /v1/sessions/{id} every few seconds via LaunchedEffect+delay while state is
"busy" — no WebSocket/streaming (matches the API contract).

To change poll interval: ChatScreen.kt LaunchedEffect delay value
To change API base URL scheme (http vs https): RelayApiClient.kt

## Widget

RelayWidgetProvider (home-screen widget) → Stop button → StopContainersWorker (WorkManager) →
POSTs `/v1/projects/{id}/containers/stop` for the project marked default via
WidgetConfigRepository (set from a star icon on ProjectListScreen rows).
Wake button → WakeRunnerWorker (WorkManager) → same widget default project's runner, see below.

## Wake-on-LAN

`KnownRunner` (model/Models.kt) carries two optional fields per runner: `wakeMac` (the MAC to
wake) and `wakeViaRunnerId` (another known runner's `hostname` — there's no separate id concept
in this skeleton, hostname is already the unique key used everywhere else, e.g.
KnownRunnersRepository upserts by hostname). Both null/absent decode fine for runners added
before this migration (moshi-kotlin reflection respects Kotlin default parameter values for
missing JSON keys).

RunnerListScreen row → "Edit" button → EditWakeConfigDialog → KnownRunnersRepository.updateRunner
(upsert-by-hostname, same as addRunner) sets `wakeMac`/`wakeViaRunnerId` on that runner's stored
entry.

RunnerListScreen row → "Wake" button (or widget's Wake button, which has no per-runner UI and
falls back to the widget's default project's runner) → WakeRunnerWorker (WorkManager, mirrors
StopContainersWorker's pattern) →
1. looks up the target runner's `wakeMac` + `wakeViaRunnerId` from KnownRunnersRepository
2. if either is unset: logs a clear "not configured" message and fails — RunnerListScreen also
   pre-checks this and shows a Toast instead of enqueueing work in that case
3. otherwise resolves `wakeViaRunnerId` to that OTHER known runner's stored entry and POSTs
   `{"mac": wakeMac}` to `/v1/wake` **on that other runner** (using ITS key), per
   ARCHITECTURE.md's "Relay device" design — you can never call the target runner's own API to
   wake it, since if it's off it's unreachable.

To change: `widget/WakeRunnerWorker.kt`. To add a picker instead of free-text hostname entry for
`wakeViaRunnerId`: `ui/screens/RunnerListScreen.kt`'s `EditWakeConfigDialog`.

## FCM device registration

`RelayFirebaseMessagingService.onNewToken(token)` → enqueues `RegisterDeviceWorker` (WorkManager)
→ loops every known runner from KnownRunnersRepository → POSTs `{"fcmToken": token}` to
`/v1/devices` on each, using that runner's own key. One unreachable runner logs + is skipped;
does not block the others.

`MainActivity.registerCurrentFcmTokenWithAllRunners()` does the same thing once at app startup
using `FirebaseMessaging.getInstance().token` — covers a runner added AFTER the token was already
issued, without waiting for a token refresh to fire `onNewToken`.

Both paths are genuinely blocked from being exercised until a real Firebase project +
`google-services.json` exist (see Technology Notes) — `FirebaseMessaging.getInstance()` throws
`IllegalStateException` without one; `MainActivity` catches that and logs rather than crashing.
Once resolved this registration code needs no changes — only `app/build.gradle.kts` (apply the
`com.google.gms.google-services` plugin) and adding the JSON file.

To change: `fcm/RegisterDeviceWorker.kt`, `MainActivity.registerCurrentFcmTokenWithAllRunners()`.

## Technology notes

- **No build/run verification via emulator** — none available in this environment. Compile
  verified via a one-off Dockerized `./gradlew assembleDebug` (mingc/android-build-box image);
  no instrumented/UI tests have ever run.
- **FCM sending is still stubbed** — `com.google.gms.google-services` plugin is deliberately NOT
  applied (no `google-services.json` exists), so `FirebaseMessaging.getInstance()` throws at
  runtime and both registration paths above no-op via a caught exception/log. Token
  *registration* code (this app → runner) is fully wired; only "app actually has a live
  FirebaseApp to read a token from" and "runner actually sends a push" remain blocked on the
  user creating a Firebase project (see ARCHITECTURE.md, shared/API.md's `/v1/devices` notes).
- **Wake-on-LAN app-side is fully wired** — WakeRunnerWorker calls a real `/v1/wake` on a
  configured "via" runner. What's still not automatic: *which* runner is on the same LAN as a
  given wake target is entered manually per runner (`wakeViaRunnerId`), and no physical relay
  device (Pi Zero 2 W) exists yet — any already-running runner can serve that role today since
  it's the same binary everywhere (see ARCHITECTURE.md "Relay device").
- **DataStore Preferences has no encryption** — the runner key (and now the plaintext wake MAC)
  is stored in plaintext prefs. Acceptable for now (single-user, own devices) but worth
  revisiting before wider use.
- **No offline/local persistence of sessions or messages** — everything is fetched live from
  the runner each time; if the runner is unreachable, screens simply fail to load (no cache).
- **WorkManager has no dedup/backoff tuning here** — `OneTimeWorkRequestBuilder` uses defaults;
  a `RegisterDeviceWorker` run for every runner during `doWork()` is sequential, not parallel,
  so a slow/unreachable runner delays (but does not block, thanks to the try/catch) the rest.

## Change Index

| Thing | Where |
|---|---|
| Known runners storage (+ wake config fields) | `data/KnownRunnersRepository.kt`, `model/Models.kt` |
| Widget's default project | `data/WidgetConfigRepository.kt` |
| API types (must match shared/API.md) | `model/Models.kt` |
| HTTP client / auth header | `network/RelayApiClient.kt`, `network/RelayApiService.kt` |
| Navigation graph | `ui/navigation/RelayNavHost.kt` |
| Chat poll interval | `ui/screens/ChatScreen.kt` |
| Wake-on-LAN edit UI + per-row Wake button | `ui/screens/RunnerListScreen.kt` |
| Wake-on-LAN background call | `widget/WakeRunnerWorker.kt` |
| FCM token registration (on refresh) | `fcm/RelayFirebaseMessagingService.kt` |
| FCM token registration (on app startup) | `MainActivity.kt` |
| FCM registration background call (loops all runners) | `fcm/RegisterDeviceWorker.kt` |
| Widget provider / Stop + Wake actions | `widget/RelayWidgetProvider.kt`, `widget/StopContainersWorker.kt`, `widget/WakeRunnerWorker.kt` |
| Gradle/Kotlin/Compose versions | `app/build.gradle.kts`, `build.gradle.kts`, `gradle/wrapper/gradle-wrapper.properties` |
