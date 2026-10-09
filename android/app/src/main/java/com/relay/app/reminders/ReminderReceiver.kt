package com.relay.app.reminders

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import com.relay.app.MainActivity
import com.relay.app.R
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.data.ScheduleRepository
import com.relay.app.model.KnownRunner
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import java.time.LocalDate

/**
 * Alarm target (not exported - only our own PendingIntent reaches it). On fire: re-arm the next
 * alarm, then for each known runner with an occurrence dated today (phone-local date) post one
 * notification. Evening first refreshes each runner's schedule cache (best effort, bounded by
 * [REFRESH_TIMEOUT_MS]) so tomorrow morning's check reads fresh data. Work runs off the main
 * thread via goAsync().
 */
class ReminderReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != ACTION_FIRE) return
        val kind = intent.getStringExtra(EXTRA_KIND)
            ?.let { runCatching { ReminderKind.valueOf(it) }.getOrNull() } ?: return
        val app = context.applicationContext
        val pending = goAsync()
        scope.launch {
            try {
                ReminderScheduler.reschedule(app)
                if (ReminderPrefs(app).current().enabled) fire(app, kind)
            } catch (e: Exception) {
                Log.w(TAG, "$kind reminder failed", e)
            } finally {
                pending.finish()
            }
        }
    }

    private suspend fun fire(context: Context, kind: ReminderKind) {
        val runners = KnownRunnersRepository(context).runners.first()
        if (runners.isEmpty()) return
        val schedules = ScheduleRepository(context)
        if (kind == ReminderKind.EVENING) refreshAll(schedules, runners)

        val today = LocalDate.now()
        for (runner in runners) {
            val cache = schedules.cachedOccurrences(runner) ?: continue
            val text = ReminderLogic.text(kind, runner.label, ReminderLogic.todays(cache.occurrences, today)) ?: continue
            post(context, kind, runner, text)
        }
    }

    /** Server is usually still on at the evening reminder; failures/timeouts just leave the cache. */
    private suspend fun refreshAll(schedules: ScheduleRepository, runners: List<KnownRunner>) {
        withTimeoutOrNull(REFRESH_TIMEOUT_MS) {
            coroutineScope {
                runners.map { r -> async { runCatching { schedules.refresh(r) } } }.awaitAll()
            }
        }
    }

    private fun post(context: Context, kind: ReminderKind, runner: KnownRunner, text: String) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            return // denied: same outcome as the FCM path (nothing shows), just without the call
        }
        ensureChannel(context)
        val id = ReminderLogic.notificationId(kind, runner.hostname)
        val launch = Intent(context, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        val contentIntent = PendingIntent.getActivity(
            context, id, launch, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val notification = NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_launcher_foreground)
            .setContentTitle(if (kind == ReminderKind.MORNING) "Server booked today" else "Booking over")
            .setContentText(text)
            .setAutoCancel(true)
            .setContentIntent(contentIntent)
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .build()
        NotificationManagerCompat.from(context).notify(id, notification)
    }

    private fun ensureChannel(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val channel = NotificationChannel(CHANNEL_ID, "Schedule reminders", NotificationManager.IMPORTANCE_DEFAULT)
            .apply { description = "Morning and evening reminders on days the server is booked" }
        context.getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
    }

    companion object {
        private const val TAG = "ReminderReceiver"
        const val ACTION_FIRE = "com.relay.app.reminders.FIRE"
        const val EXTRA_KIND = "kind"
        const val CHANNEL_ID = "schedule_reminders"
        private const val REFRESH_TIMEOUT_MS = 6_000L
        private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    }
}

/**
 * Re-arms the reminder alarms after events that drop or invalidate them: reboot (alarms don't
 * survive it), clock/timezone change (trigger instants were computed for the old wall clock),
 * app update, and the exact-alarm permission being granted (switch inexact -> exact).
 * Exported because these are system broadcasts; all of them are protected (only the system can
 * send them), and the only effect is re-scheduling anyway.
 */
class ReminderRescheduleReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        ReminderScheduler.rescheduleAsync(context)
    }
}
