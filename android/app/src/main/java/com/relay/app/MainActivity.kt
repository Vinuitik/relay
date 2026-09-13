package com.relay.app

import android.os.Bundle
import android.util.Log
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.ui.Modifier
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.workDataOf
import com.google.firebase.messaging.FirebaseMessaging
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.data.WidgetConfigRepository
import com.relay.app.fcm.RegisterDeviceWorker
import com.relay.app.ui.navigation.RelayNavHost
import com.relay.app.ui.theme.RelayTheme

class MainActivity : ComponentActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val runnersRepository = KnownRunnersRepository(applicationContext)
        val widgetConfigRepository = WidgetConfigRepository(applicationContext)

        registerCurrentFcmTokenWithAllRunners()

        setContent {
            RelayTheme {
                Surface(modifier = Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    RelayNavHost(
                        runnersRepository = runnersRepository,
                        widgetConfigRepository = widgetConfigRepository,
                    )
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
     * [NOT IMPLEMENTED] beyond this point: `FirebaseMessaging.getInstance()` throws
     * `IllegalStateException` until a real Firebase project exists — the
     * `com.google.gms.google-services` plugin is deliberately NOT applied (no
     * google-services.json yet, see app/build.gradle.kts). This is genuinely blocked on the user
     * creating a Firebase project; the try/catch below just keeps that from crashing app startup
     * in the meantime.
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

    companion object {
        private const val TAG = "MainActivity"
    }
}
