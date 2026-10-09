package com.relay.app.reminders

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import java.time.LocalTime
import java.time.ZoneId

/**
 * Keeps exactly one pending AlarmManager alarm per [ReminderKind] (next morning, next evening),
 * phone-local time. Called at app start, from every [ReminderPrefs] setter, after each alarm
 * fires ([ReminderReceiver]) and on boot / clock / timezone change ([ReminderRescheduleReceiver]).
 *
 * Exact (`setExactAndAllowWhileIdle`) when allowed: always below API 31, else only when the user
 * granted "Alarms & reminders" (SCHEDULE_EXACT_ALARM, off by default on 33+). Otherwise inexact
 * `setAndAllowWhileIdle` - Doze may delay it by minutes, acceptable for a reminder.
 */
object ReminderScheduler {

    private const val TAG = "ReminderScheduler"
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    /** Fire-and-forget [reschedule] for non-suspending callers (MainActivity). */
    fun rescheduleAsync(context: Context) {
        val app = context.applicationContext
        scope.launch { runCatching { reschedule(app) }.onFailure { Log.w(TAG, "reschedule failed", it) } }
    }

    suspend fun reschedule(context: Context) {
        val settings = ReminderPrefs(context).current()
        val am = context.getSystemService(AlarmManager::class.java) ?: return
        for (kind in ReminderKind.values()) {
            val pi = pendingIntent(context, kind)
            am.cancel(pi)
            if (!settings.enabled) continue
            val at: LocalTime = if (kind == ReminderKind.MORNING) settings.morning else settings.evening
            val trigger = ReminderLogic.nextTriggerMillis(System.currentTimeMillis(), at, ZoneId.systemDefault())
            set(am, trigger, pi)
            Log.d(TAG, "$kind reminder at ${java.time.Instant.ofEpochMilli(trigger)}")
        }
    }

    private fun set(am: AlarmManager, trigger: Long, pi: PendingIntent) {
        val exact = Build.VERSION.SDK_INT < Build.VERSION_CODES.S || am.canScheduleExactAlarms()
        if (exact) {
            try {
                am.setExactAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, trigger, pi)
                return
            } catch (e: SecurityException) {
                // Permission revoked between the check and the call - fall through to inexact.
            }
        }
        am.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, trigger, pi)
    }

    private fun pendingIntent(context: Context, kind: ReminderKind): PendingIntent {
        val intent = Intent(context, ReminderReceiver::class.java)
            .setAction(ReminderReceiver.ACTION_FIRE)
            .putExtra(ReminderReceiver.EXTRA_KIND, kind.name)
        return PendingIntent.getBroadcast(
            context,
            kind.ordinal + REQUEST_CODE_BASE,
            intent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
    }

    private const val REQUEST_CODE_BASE = 7300
}
