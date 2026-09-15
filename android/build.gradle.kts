// Top-level build file where you can add configuration options common to all sub-projects/modules.
plugins {
    id("com.android.application") version "8.5.2" apply false
    id("org.jetbrains.kotlin.android") version "1.9.24" apply false
    // Applied per-module in app/build.gradle.kts - requires app/google-services.json to exist
    // (gitignored, generated via `firebase apps:sdkconfig`, see runner/README.md /
    // ARCHITECTURE.md for the provisioning steps). Without that file this plugin fails the
    // build, which is why it's NOT applied here unconditionally.
    id("com.google.gms.google-services") version "4.4.2" apply false
    // Room's annotation processor (DAO/entity codegen) - KSP instead of kapt because kapt is
    // deprecated upstream and noticeably slower; this Kotlin/AGP pairing (1.9.24) needs the
    // matching "1.9.24-1.0.20" KSP release, not just any KSP version.
    id("com.google.devtools.ksp") version "1.9.24-1.0.20" apply false
}
