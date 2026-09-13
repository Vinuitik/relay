// Top-level build file where you can add configuration options common to all sub-projects/modules.
plugins {
    id("com.android.application") version "8.5.2" apply false
    id("org.jetbrains.kotlin.android") version "1.9.24" apply false
    // Applied per-module in app/build.gradle.kts - requires app/google-services.json to exist
    // (gitignored, generated via `firebase apps:sdkconfig`, see runner/README.md /
    // ARCHITECTURE.md for the provisioning steps). Without that file this plugin fails the
    // build, which is why it's NOT applied here unconditionally.
    id("com.google.gms.google-services") version "4.4.2" apply false
}
