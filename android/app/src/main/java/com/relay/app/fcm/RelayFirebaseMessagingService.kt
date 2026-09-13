package com.relay.app.fcm

import android.util.Log
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.workDataOf
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage

/**
 * Job-done notifications are delivered via FCM (see ARCHITECTURE.md "Notifications: FCM") — the
 * runner calls Google's FCM API directly when a session transitions to "finished", nothing else
 * transits Google's servers.
 *
 * Token registration ([onNewToken] below) works today: it POSTs `/v1/devices` to every known
 * runner via [RegisterDeviceWorker], per shared/API.md.
 *
 * [NOT IMPLEMENTED]: actually RECEIVING a push still needs a Firebase project + credential from
 * the user — see shared/API.md's `/v1/devices` notes and runner/FLOWS.md's Technology Notes.
 * The `com.google.gms.google-services` Gradle plugin is deliberately NOT applied (no
 * google-services.json exists yet — see app/build.gradle.kts); this service is manifest-
 * registered and will start receiving callbacks once a real Firebase project exists, but until
 * then there is no default FirebaseApp configured, so [onMessageReceived] just logs.
 */
class RelayFirebaseMessagingService : FirebaseMessagingService() {

    override fun onNewToken(token: String) {
        super.onNewToken(token)
        Log.d(TAG, "FCM token refreshed — registering with all known runners")
        val request = OneTimeWorkRequestBuilder<RegisterDeviceWorker>()
            .setInputData(workDataOf(RegisterDeviceWorker.KEY_FCM_TOKEN to token))
            .build()
        WorkManager.getInstance(applicationContext).enqueue(request)
    }

    override fun onMessageReceived(message: RemoteMessage) {
        super.onMessageReceived(message)
        Log.d(TAG, "FCM message received: data=${message.data} notification=${message.notification?.body}")
        // [NOT IMPLEMENTED]: post an actual Android notification for the finished session once
        // the runner side sends real pushes.
    }

    companion object {
        private const val TAG = "RelayFcmService"
    }
}
