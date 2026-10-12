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
        val type = message.data["type"]
        if (type == "vscode_permission") {
            postVsCodePermission(message.data)
            return
        }

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

        val notification = NotificationCompat.Builder(this, if (type == "vscode_waiting" || type == "chat_moved") VSCODE_CHANNEL_ID else CHANNEL_ID)
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
            "vscode_waiting" -> "VS Code is waiting on you" to "Claude needs your attention."
            "chat_moved" -> "Chat moved" to "A chat was handed off to another machine."
            "session_needs_input" -> "Agent needs your approval" to "A session is paused waiting for permission."
            else -> when (data["problem"]) {
                "quota" -> "Claude quota exhausted" to "Your subscription limit is used up - the turn stopped."
                "auth" -> "Claude login expired" to "The turn failed - sign in again to keep going."
                else -> "Session finished" to "A runner session finished."
            }
        }
    }

    /** Allow/Deny notification; buttons go to [VsCodePermissionReceiver]. Self-cancels after ~130s. */
    private fun postVsCodePermission(data: Map<String, String>) {
        val permissionId = data["permissionId"].orEmpty()
        val runnerRef = data["runnerRef"].orEmpty()
        if (permissionId.isEmpty()) return
        val notifId = permissionId.hashCode()
        fun action(behavior: String, label: String, code: Int): NotificationCompat.Action {
            val i = Intent(this, VsCodePermissionReceiver::class.java).apply {
                action = VsCodePermissionReceiver.ACTION_ANSWER
                putExtra(VsCodePermissionReceiver.EXTRA_RUNNER_REF, runnerRef)
                putExtra(VsCodePermissionReceiver.EXTRA_PERMISSION_ID, permissionId)
                putExtra(VsCodePermissionReceiver.EXTRA_BEHAVIOR, behavior)
                putExtra(VsCodePermissionReceiver.EXTRA_NOTIF_ID, notifId)
            }
            val pi = PendingIntent.getBroadcast(
                this, notifId * 2 + code, i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
            return NotificationCompat.Action.Builder(0, label, pi).build()
        }
        val tap = PendingIntent.getActivity(
            this, notifId,
            Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val body = data["body"] ?: data["summary"] ?: "Permission needed"
        val n = NotificationCompat.Builder(this, VSCODE_CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_launcher_foreground)
            .setContentTitle(data["title"] ?: "VS Code asks")
            .setContentText(body)
            .setStyle(NotificationCompat.BigTextStyle().bigText(body))
            .setContentIntent(tap)
            .setAutoCancel(true)
            .setTimeoutAfter(130_000L)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .addAction(action("allow", "Allow", 0))
            .addAction(action("deny", "Deny", 1))
            .build()
        NotificationManagerCompat.from(this).notify(notifId, n)
    }

    private fun ensureChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        getSystemService(NotificationManager::class.java).createNotificationChannel(
            NotificationChannel(VSCODE_CHANNEL_ID, "VS Code prompts", NotificationManager.IMPORTANCE_HIGH).apply {
                description = "Permission questions and status from Claude in VS Code"
            },
        )
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
        const val VSCODE_CHANNEL_ID = "vscode_prompts"
    }
}
