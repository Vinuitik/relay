# android

Native Android app (not PWA — see [ARCHITECTURE.md](../ARCHITECTURE.md) for why). Holds the
known-runners list (hostname + key per runner), browses projects/sessions per runner, sends
messages to a session and polls its transcript, sends wake/stop-containers commands from a
home-screen widget, and stubs per-session job-done notifications pending a real Firebase project.

Implements the v1 contract in [shared/API.md](../shared/API.md) exactly (types and endpoints).

## Status

Skeleton — compiles by inspection, **not build-verified** (see "What was and wasn't verified"
below). No JDK/Android SDK/emulator exists on the machine this was written on.

## Layout

Standard single-module Gradle project, Kotlin DSL:

```
android/
  settings.gradle.kts       - module list, plugin/dependency repositories
  build.gradle.kts          - top-level plugin versions (AGP 8.5.2, Kotlin 1.9.24)
  gradle/wrapper/           - Gradle 8.7 wrapper (jar fetched, see below)
  app/
    build.gradle.kts        - app module: compileSdk 34, minSdk 26, targetSdk 34, Compose on
    src/main/
      AndroidManifest.xml
      java/com/relay/app/
        MainActivity.kt              - sets Compose content, wires the NavHost
        model/Models.kt              - Project/Session/Message/RunnerInfo/KnownRunner data classes
        network/RelayApiService.kt   - Retrofit interface mirroring shared/API.md's endpoint table
        network/RelayApiClient.kt    - builds+caches a Retrofit instance per known runner
        data/KnownRunnersRepository.kt   - DataStore-backed known-runners list
        data/WidgetConfigRepository.kt   - DataStore-backed widget default-project target
        ui/theme/                    - Compose Material3 theme (Color/Type/Theme)
        ui/navigation/RelayNavHost.kt - NavHost + route definitions
        ui/screens/
          RunnerListScreen.kt   - known runners + "add runner" form
          ProjectListScreen.kt  - GET /v1/projects, "new project" POST, set-as-widget-default
          SessionListScreen.kt  - GET sessions, state badges, "new session" (provider picker)
          ChatScreen.kt         - message transcript, send box, polls while state == "busy"
        fcm/RelayFirebaseMessagingService.kt - stub, logs only (see FCM section below)
        widget/RelayWidgetProvider.kt    - home-screen widget (Wake / Stop buttons)
        widget/StopContainersWorker.kt   - WorkManager job for the Stop button
      res/                        - manifest-required resources (strings, adaptive icon, widget
                                    layout/metadata, base platform theme)
```

## Building

```
cd android
./gradlew assembleDebug
```

**Gradle wrapper jar**: this host has outbound network access via `curl`, so the actual
`gradle-wrapper.jar` (Gradle 8.7) was fetched from `raw.githubusercontent.com/gradle/gradle` and
committed — `./gradlew` is not a stub. If it's ever missing, regenerate it with a local Gradle
install: `gradle wrapper --gradle-version 8.7`.

### What was and wasn't verified

No JDK, Android SDK, or emulator exists on the machine this was written on, so the code was
**written to compile by inspection**, not run. A best-effort Docker sanity build was attempted
(`mingc/android-build-box`, which bundles JDK17 + Android SDK + Gradle) — Docker itself is
available on this host — but the image pull did not complete within the time budget for this
task, so **no build was actually executed and this app has not been confirmed to compile**. Do
not treat `./gradlew assembleDebug` as known-good; the first real build on a machine with the
toolchain installed should be treated as the actual first compile check, and issues found there
(version mismatches between AGP/Kotlin/Compose-compiler, resource references, etc.) are expected
to need a pass.

Versions were chosen for mutual compatibility per Google's published AGP/Kotlin/Compose-compiler
compatibility map: AGP 8.5.2, Kotlin 1.9.24, Compose compiler extension 1.5.14, Compose BOM
2024.06.00, requires Gradle 8.7+ (wrapper pinned to 8.7).

## Networking

**Retrofit + OkHttp + Moshi** (`com.squareup.retrofit2:retrofit`, `com.squareup.moshi:moshi-kotlin`),
not plain OkHttp. The API surface in `shared/API.md` is a small, fully-typed JSON resource set
(`Project`/`Session`/`Message`/`RunnerInfo`) — Retrofit's interface-based declaration
(`RelayApiService`) plus Moshi's Kotlin reflection adapter keeps each endpoint a one-line
declaration and each model a plain data class, instead of hand-building requests and parsing JSON
per call site with raw OkHttp. `RelayApiClient` builds one Retrofit/OkHttp instance per known
runner (cached by hostname/port/key), with an interceptor adding the `X-Relay-Key` header the
whole API requires. Endpoints whose response body isn't worth parsing (message send,
containers start/stop — `202 {}` / `200 {}`) return `Response<ResponseBody>` rather than a typed
or `Unit` body, since Retrofit has no built-in converter for bare `Unit` and `ResponseBody` is
always supported without one.

Base URL is `http://<hostname>:<port>/` per this task's spec. ARCHITECTURE.md notes the real
transport security boundary is the Tailscale tunnel itself, not TLS — v1 does no TLS termination
of its own.

## Persistence

Known runners and the widget's default-project target are both stored via **Jetpack DataStore
(Preferences)**, each as one JSON blob (encoded/decoded with Moshi) under a single preferences
key, in two separate DataStore files (`known_runners`, `widget_config`). This is a single-user,
small-list use case — a typed DataStore-proto schema would be more ceremony than the data
warrants.

## What's stubbed / NOT IMPLEMENTED

- **FCM push notifications** (`fcm/RelayFirebaseMessagingService.kt`): the `firebase-messaging`
  dependency is present and the service is manifest-registered, but the
  `com.google.gms.google-services` Gradle plugin is **not applied** — applying it with no
  `google-services.json` present would fail the build outright. `onNewToken`/`onMessageReceived`
  just `Log.d`. Needs: a real Firebase project, `google-services.json` dropped into `app/`, the
  plugin applied in `app/build.gradle.kts`, and a runner-side endpoint to register device tokens.
- **Wake-on-LAN** (`widget/RelayWidgetProvider.kt`, `ACTION_WAKE` branch): just logs. The actual
  magic-packet send depends on the relay-device design in ARCHITECTURE.md ("Relay device") not
  being finalized yet (Pi Zero 2 W, not yet purchased/built).
- **Widget default-project UX**: the widget always acts on whatever project was last marked via
  the star icon in `ProjectListScreen`; there's no in-widget project picker (out of scope per the
  task — "keep this minimal").

## Non-goals for this pass (per task scope)

No WebSocket/streaming (the API contract has none — `ChatScreen` polls `GET /v1/sessions/{id}`
every 3s while `state == "busy"`), no Play Store packaging concerns, no production ProGuard rules
(minification is off in `app/build.gradle.kts`).
