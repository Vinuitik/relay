plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    // [NOT IMPLEMENTED]: apply "com.google.gms.google-services" once a Firebase project +
    // google-services.json exist (see FirebaseMessagingService below). Applying it now with no
    // google-services.json present would fail the build.
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

    // Background work for the widget's stop-containers action.
    implementation("androidx.work:work-runtime-ktx:2.9.1")

    // Push notifications (job-done). No google-services.json yet — see [NOT IMPLEMENTED] above
    // and in RelayFirebaseMessagingService. The dependency alone does not require the plugin.
    implementation("com.google.firebase:firebase-messaging:24.0.1")

    testImplementation("junit:junit:4.13.2")
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
    androidTestImplementation("androidx.test.espresso:espresso-core:3.6.1")
    androidTestImplementation("androidx.compose.ui:ui-test-junit4")
    debugImplementation("androidx.compose.ui:ui-tooling")
    debugImplementation("androidx.compose.ui:ui-test-manifest")
}
