# Android app flows

Files: MainActivity.kt, RelayNavHost.kt, RunnerListScreen.kt, ProjectListScreen.kt,
SessionListScreen.kt, ChatScreen.kt, KnownRunnersRepository.kt, WidgetConfigRepository.kt,
RelayApiClient.kt, RelayApiService.kt, RelayFirebaseMessagingService.kt,
RelayWidgetProvider.kt, StopContainersWorker.kt

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
Wake button: `[NOT IMPLEMENTED]` — logs only. See RelayWidgetProvider.kt.

## Technology notes

- **No build/run verification via emulator** — none available in this environment. Compile
  verified via a one-off Dockerized `./gradlew assembleDebug` (mingc/android-build-box image);
  no instrumented/UI tests have ever run.
- **FCM is stubbed** — `com.google.gms.google-services` plugin is deliberately NOT applied
  (no `google-services.json` exists). `RelayFirebaseMessagingService` only logs. Wiring real
  push notifications needs a Firebase project created by the user first (see ARCHITECTURE.md).
- **WoL is stubbed** — the widget's Wake button has no packet-send logic; needs the relay-device
  design (Pi Zero 2 W) finalized first, per ARCHITECTURE.md's rollout order.
- **DataStore Preferences has no encryption** — the runner key is stored in plaintext prefs.
  Acceptable for now (single-user, own devices) but worth revisiting before wider use.
- **No offline/local persistence of sessions or messages** — everything is fetched live from
  the runner each time; if the runner is unreachable, screens simply fail to load (no cache).

## Change Index

| Thing | Where |
|---|---|
| Known runners storage | `data/KnownRunnersRepository.kt` |
| Widget's default project | `data/WidgetConfigRepository.kt` |
| API types (must match shared/API.md) | `model/Models.kt` |
| HTTP client / auth header | `network/RelayApiClient.kt`, `network/RelayApiService.kt` |
| Navigation graph | `ui/navigation/RelayNavHost.kt` |
| Chat poll interval | `ui/screens/ChatScreen.kt` |
| FCM stub | `fcm/RelayFirebaseMessagingService.kt` |
| Widget provider / Stop action | `widget/RelayWidgetProvider.kt`, `widget/StopContainersWorker.kt` |
| Gradle/Kotlin/Compose versions | `app/build.gradle.kts`, `build.gradle.kts`, `gradle/wrapper/gradle-wrapper.properties` |
