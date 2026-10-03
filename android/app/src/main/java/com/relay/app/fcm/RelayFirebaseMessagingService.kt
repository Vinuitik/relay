package com.relay.app.fcm

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.workDataOf
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import com.relay.app.MainActivity
import com.relay.app.R
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.ui.navigation.Routes
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking

/**
 * Job-done and runner-suspending notifications are delivered via FCM (see ARCHITECTURE.md
 * "Notifications: FCM") — the runner calls Google's FCM API directly on a session transitioning
 * to "finished", or right before it suspends to S5 (`idle.Monitor.BeforeShutdown`,
 * `notify.Notifier.NotifyRunnerSuspending` in runner/FLOWS.md), nothing else transits Google's
 * servers.
 *
 * Token registration ([onNewToken] below) POSTs `/v1/devices` to every known runner via
 * [RegisterDeviceWorker], per shared/API.md. [onMessageReceived] posts an actual Android
 * notification, its title/body picked from the message's `type` data field (see
 * [notificationContentFor]) — requires a real Firebase project (see app/build.gradle.kts) and the
 * user having granted POST_NOTIFICATIONS at runtime (API 33+, requested in MainActivity) - if
 * that permission was denied, posting silently no-ops (Android's own behavior for an unpermitted
 * notification), so pings just won't show.
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
        ensureChannel()

        val (defaultTitle, defaultBody) = notificationContentFor(message.data)
        val title = message.notification?.title ?: message.data["title"] ?: defaultTitle
        val body = message.notification?.body ?: message.data["body"] ?: defaultBody

        val notificationId = System.currentTimeMillis().toInt()
        val contentIntent = PendingIntent.getActivity(
            this,
            notificationId,
            contentIntentFor(message.data),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )

        val notification = NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_launcher_foreground)
            .setContentTitle(title)
            .setContentText(body)
            .setAutoCancel(true)
            .setContentIntent(contentIntent)
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .build()

        // NotificationManagerCompat.notify silently no-ops if POST_NOTIFICATIONS (API 33+) was
        // never granted - there is nothing to catch here, Android just drops it.
        NotificationManagerCompat.from(this).notify(notificationId, notification)
    }

    /**
     * Tap target: the session's chat via deep link `relay://r/{runnerRef}/p/{projectId}/s/{sessionId}`
     * (RelayNavHost `Routes.CHAT_DEEP_LINK`) when the push names a session on a runner this phone
     * knows by that hostname; otherwise (old runner sending no `runnerRef`, runner since removed,
     * `runner_suspending`) a plain app launch. Runs on FCM's background thread, so the blocking
     * DataStore read is fine.
     */
    private fun contentIntentFor(data: Map<String, String>): Intent {
        val runnerRef = data["runnerRef"].orEmpty()
        val projectId = data["projectId"].orEmpty()
        val sessionId = data["sessionId"].orEmpty()
        val known = runnerRef.isNotEmpty() && projectId.isNotEmpty() && sessionId.isNotEmpty() &&
            runCatching {
                runBlocking { KnownRunnersRepository(applicationContext).runners.first() }
                    .any { it.hostname == runnerRef }
            }.getOrDefault(false)
        val intent = if (known) {
            Intent(Intent.ACTION_VIEW, Uri.parse(Routes.chatDeepLink(runnerRef, projectId, sessionId)), this, MainActivity::class.java)
        } else {
            Intent(this, MainActivity::class.java)
        }
        return intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
    }

    /**
     * Maps the runner's `data.type` field (see runner/internal/notify) to a fallback
     * title/body, used whenever the message carries no explicit `notification`/`title`/`body` of
     * its own — which is every message the runner currently sends, since it only ever sets
     * `data`. Unknown/missing type falls back to the original "Session finished" text so old
     * runner builds (sending no `type` at all) keep behaving exactly as before.
     */
    private fun notificationContentFor(data: Map<String, String>): Pair<String, String> {
        return when (data["type"]) {
            "runner_suspending" -> {
                val hostname = data["hostname"] ?: "A runner"
                "$hostname is going to sleep" to "No active session — suspending to save power."
            }
            "session_needs_input" -> "Agent needs your approval" to "A session is paused waiting for permission."
            else -> when (data["problem"]) {
                "quota" -> "Claude quota exhausted" to "Your subscription limit is used up - the turn stopped."
                "auth" -> "Claude login expired" to "The turn failed - sign in again to keep going."
                else -> "Session finished" to "A runner session finished."
            }
        }
    }

    private fun ensureChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val channel = NotificationChannel(
            CHANNEL_ID,
            "Session finished",
            NotificationManager.IMPORTANCE_DEFAULT,
        ).apply {
            description = "Notifies when a runner session finishes"
        }
        getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
    }

    companion object {
        private const val TAG = "RelayFcmService"
        private const val CHANNEL_ID = "session_finished"
    }
}
