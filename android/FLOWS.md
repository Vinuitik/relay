# Android app flows

Files: MainActivity.kt, RelayNavHost.kt, RunnerListScreen.kt, QrScanScreen.kt,
ProjectListScreen.kt, SessionListScreen.kt, ChatScreen.kt, KnownRunnersRepository.kt,
WidgetConfigRepository.kt, RelayApiClient.kt, RelayApiService.kt,
RelayFirebaseMessagingService.kt, RelayWidgetProvider.kt, StopContainersWorker.kt,
WakeRunnerWorker.kt, RegisterDeviceWorker.kt

## Navigation

MainActivity → RelayNavHost →
RunnerListScreen → ProjectListScreen(runner) → SessionListScreen(runner, project)
→ ChatScreen(runner, project, session)

To add a screen: RelayNavHost.kt

## Data flow

RunnerListScreen ↔ KnownRunnersRepository (DataStore Preferences) — hostname+key(+wakeMac)
triples. Added via QR scan by default (see "Pairing" below); manual entry (the original
ARCHITECTURE.md decision) is still there as a fallback.

## Pairing (QR scan)

Files: QrScanScreen.kt, RunnerListScreen.kt

RunnerListScreen's "+" FAB → `QrScanScreen` (full-screen, replaces the whole screen content
while open - see the `if (showQrScan) { ...; return }` early-return at the top of
`RunnerListScreen`) → requests CAMERA permission → CameraX preview + ML Kit on-device barcode
decode → `parseRelayQrContent(rawValue)` → on a valid `relay://host:port?key=...&mac=...`
match → `KnownRunnersRepository.addRunner(...)`, `wakeMac` set directly from the scanned `mac`
if the runner's QR included one (see runner/FLOWS.md "Own-MAC detection for pairing").

`wakeViaRunnerId` (which *other* runner should broadcast the wake packet) is NOT auto-filled
by a scan - it can't be: that's a LAN-topology fact (which runners share a physical network
segment), not something either runner's own QR code can know about itself. Still requires the
manual "Edit" affordance once a second runner exists.

Manual entry is reachable two ways: `AddRunnerDialog` directly (bypassed by default now), or
tapping "Enter key manually instead" from inside the scan screen (denied camera permission, no
camera, or just preference).

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

A real Firebase project (`relay-sizonenko`) now exists — `google-services.json` is present
(gitignored, generated via `firebase apps:sdkconfig`) and `app/build.gradle.kts` applies the
`com.google.gms.google-services` plugin conditionally on that file existing, so a checkout
without it (any fresh clone) still builds, just without live FCM. The try/catch in
`MainActivity.registerCurrentFcmTokenWithAllRunners()` stays as a safety net for that case.

`onMessageReceived` in `RelayFirebaseMessagingService` now posts a real Android notification
(not just a log line) — tapping it opens `MainActivity`. Requires POST_NOTIFICATIONS granted at
runtime on API 33+ (requested once at startup in `MainActivity.requestNotificationPermissionIfNeeded()`);
if denied, pushes still arrive and register but no banner shows (Android silently drops it, not
something this code can detect or force).

To change: `fcm/RegisterDeviceWorker.kt`, `MainActivity.registerCurrentFcmTokenWithAllRunners()`,
`fcm/RelayFirebaseMessagingService.onMessageReceived()` (notification content/channel).

## Distribution (Firebase App Distribution)

Files: .firebaserc, .github/workflows/android-deploy.yml, MainActivity.kt

Not on the Play Store by design (see ARCHITECTURE.md), so updates ship via
Firebase App Distribution instead - reuses the same `relay-sizonenko`
Firebase project already wired for FCM, no new infra.

**Shipping a build is automatic**: a push to `main` touching `android/**`
triggers `.github/workflows/android-deploy.yml`, which builds and runs the
same distribute command below. Manual equivalent (e.g. to force a build
with no code change): `npx firebase appdistribution:distribute
<path-to-apk> --app 1:960396843466:android:fd2974c630731375591733 --testers
sizonenkodima6@gmail.com` (run from `android/`, project comes from
`.firebaserc`).

**Getting notified of a new build vs. installing it are two different
things.** Distribution only uploads the release and pings testers - it does
NOT install anything by itself:
- Firebase always emails testers on every release - no dev-side toggle
  exists to suppress this (a firebase-tools feature request for one was
  filed and closed "not planned"). Only the tester can opt out, from their
  own Firebase App Distribution web view - not something this repo controls.
- **In-app prompt** (`MainActivity.checkForUpdate`, `firebase-appdistribution`
  SDK): on every app launch, calls `checkForNewRelease()`; if newer than
  what's installed, shows a "Relay X.X is ready to install" dialog -
  tapping Update calls the SDK's `updateApp()`, which downloads and walks
  through the install prompt itself (no custom download/install code
  needed). First launch ever on a device triggers the SDK's own one-time
  tester sign-in (a Custom Tab). This SDK is normally discouraged in a
  Play Store production build (its own update flow can read as policy
  friction) - moot here since this app was never headed for the Store.

To change who gets releases: swap `--testers` for `--groups <name>` once
more than one person is testing (create the group in the Firebase console
first). To change the app being targeted: the `--app` id comes from
`android/app/google-services.json` (`mobilesdk_app_id`).

**`--release-notes` is passed via an `env:` var (`$RELEASE_NOTES` in the
run: script), never `"${{ github.event.head_commit.message }}"`
interpolated directly into a quoted shell string.** Found this the hard
way: a commit message containing a literal `"` (e.g. quoting "update
available" in the message itself) closes that shell string early and the
rest gets parsed as stray arguments - `appdistribution:distribute` failed
outright with "Too many arguments." `$VAR` expansion doesn't re-parse its
value as shell syntax, so it's immune regardless of what the message
contains. Same fix, same reasoning, in runner-release.yml's `--notes`.

## Technology notes

- **No build/run verification via emulator** — none available in this environment. Compile
  verified via a one-off Dockerized `./gradlew assembleDebug` (mingc/android-build-box image);
  no instrumented/UI tests have ever run, so the notification/permission flow above is
  compile-checked only, never actually seen on a device.
- **FCM is live end-to-end in code**, gated only on the runner side having
  `RELAY_FCM_CREDENTIALS` pointed at a real service-account key (see runner/FLOWS.md) — without
  that the runner still registers devices but silently skips sending. The Android side has no
  remaining Firebase-project blocker.
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
| QR pairing content parsing | `ui/screens/QrScanScreen.kt` (`parseRelayQrContent`) |
| Widget's default project | `data/WidgetConfigRepository.kt` |
| Distribution target (Firebase project/app) | `.firebaserc`, `app/google-services.json` |
| Auto-distribute on push to main | `.github/workflows/android-deploy.yml` |
| In-app update check/prompt | `MainActivity.kt` (`checkForUpdate`, `UpdateAvailableDialog`) |
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
