package com.relay.app.fcm

import android.util.Log
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage

/**
 * Job-done notifications are delivered via FCM (see ARCHITECTURE.md "Notifications: FCM") — the
 * runner calls Google's FCM API directly when a session transitions to "finished", nothing else
 * transits Google's servers.
 *
 * [NOT IMPLEMENTED]: needs a Firebase project + google-services.json from the user before the
 * com.google.gms.google-services Gradle plugin/config can be wired in (see app/build.gradle.kts).
 * Until then this service exists and is manifest-registered, but there is no default FirebaseApp
 * configured, no token registration endpoint on the runner, and no real notification is shown —
 * both callbacks just log.
 */
class RelayFirebaseMessagingService : FirebaseMessagingService() {

    override fun onNewToken(token: String) {
        super.onNewToken(token)
        Log.d(TAG, "FCM token refreshed: $token")
        // [NOT IMPLEMENTED]: send this token to the runner so it knows where to push
        // "session finished" events for this device.
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
