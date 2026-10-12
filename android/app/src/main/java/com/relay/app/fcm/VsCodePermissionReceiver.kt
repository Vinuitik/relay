package com.relay.app.fcm

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import com.relay.app.R
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.network.RelayApiClient
import com.relay.app.network.VsCodePermissionAnswer
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

/**
 * Allow/Deny button on a `vscode_permission` push. Not exported (own PendingIntents only).
 * POSTs the answer to the runner named by `runnerRef` off the main thread (goAsync), then
 * replaces the notification with the outcome, which times out after a few seconds.
 */
class VsCodePermissionReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != ACTION_ANSWER) return
        val runnerRef = intent.getStringExtra(EXTRA_RUNNER_REF).orEmpty()
        val id = intent.getStringExtra(EXTRA_PERMISSION_ID).orEmpty()
        val behavior = intent.getStringExtra(EXTRA_BEHAVIOR).orEmpty()
        val notifId = intent.getIntExtra(EXTRA_NOTIF_ID, 0)
        val app = context.applicationContext
        val pending = goAsync()
        scope.launch {
            val outcome = try {
                val runner = KnownRunnersRepository(app).runners.first().firstOrNull { it.hostname == runnerRef }
                if (runner == null) {
                    "Runner not found"
                } else {
                    val resp = RelayApiClient.forRunner(runner).answerVscodePermission(id, VsCodePermissionAnswer(behavior))
                    when {
                        resp.isSuccessful -> if (behavior == "allow") "Allowed" else "Denied"
                        resp.code() == 404 -> "Already answered or expired"
                        else -> "Failed (${resp.code()})"
                    }
                }
            } catch (e: Exception) {
                Log.w(TAG, "answer failed", e)
                "Couldn't reach the runner"
            }
            val n = NotificationCompat.Builder(app, RelayFirebaseMessagingService.VSCODE_CHANNEL_ID)
                .setSmallIcon(R.drawable.ic_launcher_foreground)
                .setContentTitle("VS Code")
                .setContentText(outcome)
                .setAutoCancel(true)
                .setTimeoutAfter(5_000L)
                .setOnlyAlertOnce(true)
                .build()
            NotificationManagerCompat.from(app).notify(notifId, n)
            pending.finish()
        }
    }

    companion object {
        private const val TAG = "VsCodePermReceiver"
        const val ACTION_ANSWER = "com.relay.app.VSCODE_PERMISSION_ANSWER"
        const val EXTRA_RUNNER_REF = "runnerRef"
        const val EXTRA_PERMISSION_ID = "permissionId"
        const val EXTRA_BEHAVIOR = "behavior"
        const val EXTRA_NOTIF_ID = "notifId"
        private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    }
}
