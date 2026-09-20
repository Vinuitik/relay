package com.relay.app

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.util.Log
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.ui.Modifier
import androidx.core.content.ContextCompat
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.workDataOf
import com.google.firebase.messaging.FirebaseMessaging
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.fcm.RegisterDeviceWorker
import com.relay.app.ui.navigation.RelayNavHost
import com.relay.app.ui.theme.RelayTheme

class MainActivity : ComponentActivity() {

    private val requestNotificationPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            if (!granted) {
                Log.w(TAG, "Notification permission denied — job-done pushes won't show a banner")
            }
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val runnersRepository = KnownRunnersRepository(applicationContext)

        registerCurrentFcmTokenWithAllRunners()
        requestNotificationPermissionIfNeeded()

        setContent {
            RelayTheme {
                Surface(modifier = Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    RelayNavHost(runnersRepository = runnersRepository)
                }
            }
        }
    }

    /**
     * Registers whatever FCM token currently exists with every known runner (see
     * `POST /v1/devices` in shared/API.md and [RegisterDeviceWorker]) — so a runner added AFTER
     * the token was already issued still gets registered without waiting for
     * [com.relay.app.fcm.RelayFirebaseMessagingService.onNewToken] to fire on refresh.
     *
     * A Firebase project now exists and `google-services.json` is present (see
     * app/build.gradle.kts — the `com.google.gms.google-services` plugin applies automatically
     * when that file is there). The try/catch below stays as a safety net: a checkout without
     * that gitignored file (e.g. a fresh clone before it's regenerated) would otherwise crash
     * app startup instead of just skipping registration.
     */
    private fun registerCurrentFcmTokenWithAllRunners() {
        try {
            FirebaseMessaging.getInstance().token.addOnCompleteListener { task ->
                val token = task.result
                if (task.isSuccessful && !token.isNullOrBlank()) {
                    val request = OneTimeWorkRequestBuilder<RegisterDeviceWorker>()
                        .setInputData(workDataOf(RegisterDeviceWorker.KEY_FCM_TOKEN to token))
                        .build()
                    WorkManager.getInstance(applicationContext).enqueue(request)
                } else {
                    Log.w(TAG, "Could not fetch FCM token at startup", task.exception)
                }
            }
        } catch (e: IllegalStateException) {
            Log.w(TAG, "Firebase not configured yet (no google-services.json) — skipping startup FCM registration", e)
        }
    }

    /**
     * Android 13+ (API 33) requires explicit runtime consent to show any notification,
     * including the job-done push in [com.relay.app.fcm.RelayFirebaseMessagingService]. Below
     * API 33 this permission doesn't exist and notifications just work.
     */
    private fun requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        val alreadyGranted = ContextCompat.checkSelfPermission(
            this,
            Manifest.permission.POST_NOTIFICATIONS,
        ) == PackageManager.PERMISSION_GRANTED
        if (!alreadyGranted) {
            requestNotificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }

    companion object {
        private const val TAG = "MainActivity"
    }
}
