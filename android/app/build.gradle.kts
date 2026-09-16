plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("com.google.devtools.ksp")
}

// Applied only if google-services.json exists (gitignored - see runner/README.md for how to
// generate it). A checkout without it must still build; the plugin fails the build if applied
// with no config file present, so we can't apply it unconditionally.
if (file("google-services.json").exists()) {
    apply(plugin = "com.google.gms.google-services")
}

android {
    namespace = "com.relay.app"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.relay.app"
        minSdk = 26
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
        freeCompilerArgs += listOf("-opt-in=androidx.compose.material3.ExperimentalMaterial3Api")
    }

    buildFeatures {
        compose = true
    }

    composeOptions {
        kotlinCompilerExtensionVersion = "1.5.14"
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
    }
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2024.06.00")
    implementation(composeBom)
    androidTestImplementation(composeBom)

    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.3")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.8.3")
    implementation("androidx.activity:activity-compose:1.9.0")

    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-graphics")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-core")
    // -extended for Folder/InsertDriveFile (FileBrowserScreen) - core only ships a small curated
    // subset, these two aren't in it.
    implementation("androidx.compose.material:material-icons-extended")
    implementation("androidx.navigation:navigation-compose:2.7.7")

    // Networking: Retrofit + OkHttp + Moshi. Chosen over plain OkHttp because the API surface
    // (shared/API.md) is a handful of typed JSON resources (Project/Session/Message/RunnerInfo) —
    // Retrofit's interface-based calls + Moshi codegen keep each endpoint a one-line declaration
    // instead of hand-rolling request building and JSON parsing for every call site.
    implementation("com.squareup.retrofit2:retrofit:2.11.0")
    implementation("com.squareup.retrofit2:converter-moshi:2.11.0")
    implementation("com.squareup.moshi:moshi-kotlin:1.15.1")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("com.squareup.okhttp3:logging-interceptor:4.12.0")

    // Persistence for the known-runners list.
    implementation("androidx.datastore:datastore-preferences:1.1.1")

    // On-device cache: mirrors runner-side sessions/messages (so chats are readable offline or
    // while a runner is asleep) and the phone's own merged uptime history (the runner only keeps
    // a short-term buffer - see runner/FLOWS.md "Uptime tracking"). Room over raw SQLite for
    // typed DAOs/Flow queries with compile-time-checked SQL - the natural fit given Retrofit
    // already gives typed models to store.
    implementation("androidx.room:room-runtime:2.6.1")
    implementation("androidx.room:room-ktx:2.6.1")
    ksp("androidx.room:room-compiler:2.6.1")

    // Background work for the widget's stop-containers action.
    implementation("androidx.work:work-runtime-ktx:2.9.1")

    // Firebase BoM: keeps every com.google.firebase:* artifact below on mutually-compatible
    // versions instead of hand-pinning each one separately.
    implementation(platform("com.google.firebase:firebase-bom:34.19.0"))

    // Push notifications (job-done). No google-services.json yet — see [NOT IMPLEMENTED] above
    // and in RelayFirebaseMessagingService. The dependency alone does not require the plugin.
    implementation("com.google.firebase:firebase-messaging")

    // In-app "update available" prompt (MainActivity.checkForUpdate) - same Firebase project as
    // distribution itself, no new infra. Fine to ship since this app was never headed for the
    // Play Store anyway (see ARCHITECTURE.md) - that's the only reason this SDK is normally
    // discouraged in a production build.
    //
    // Not managed by the BoM above (App Distribution isn't part of its version set), so pinned
    // directly to the latest version actually published to Google's Maven repo (checked via
    // dl.google.com's maven-metadata.xml, not the SDK source repo - that had an unreleased
    // beta21 ahead of what's actually published).
    implementation("com.google.firebase:firebase-appdistribution:16.0.0-beta20")

    // In-app QR scanning for pairing a runner (see ui/screens/QrScanScreen.kt) — CameraX for the
    // preview/frame pipeline, ML Kit for on-device barcode decoding (no network call, no
    // Firebase project dependency despite the com.google.mlkit group id).
    val cameraxVersion = "1.3.4"
    implementation("androidx.camera:camera-core:$cameraxVersion")
    implementation("androidx.camera:camera-camera2:$cameraxVersion")
    implementation("androidx.camera:camera-lifecycle:$cameraxVersion")
    implementation("androidx.camera:camera-view:$cameraxVersion")
    implementation("com.google.mlkit:barcode-scanning:17.3.0")

    testImplementation("junit:junit:4.13.2")
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
    androidTestImplementation("androidx.test.espresso:espresso-core:3.6.1")
    androidTestImplementation("androidx.compose.ui:ui-test-junit4")
    debugImplementation("androidx.compose.ui:ui-tooling")
    debugImplementation("androidx.compose.ui:ui-test-manifest")
}
