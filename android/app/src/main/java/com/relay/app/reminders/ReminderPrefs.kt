package com.relay.app.reminders

import android.content.Context
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import java.time.LocalTime

private val Context.reminderPrefsDataStore by preferencesDataStore(name = "reminder_prefs")

/** Global (not per runner) schedule-reminder settings. Times are phone-local wall clock. */
data class ReminderSettings(
    val enabled: Boolean = true,
    val morning: LocalTime = LocalTime.of(6, 30),
    val evening: LocalTime = LocalTime.of(22, 0),
)

/**
 * Schedule reminder prefs (DataStore `reminder_prefs`, same pattern as AppPrefsRepository).
 * Every setter re-schedules the alarms ([ReminderScheduler.reschedule]) after saving, so a
 * settings UI only needs to call the setter.
 */
class ReminderPrefs(context: Context) {

    private val appContext = context.applicationContext
    private val store = appContext.reminderPrefsDataStore

    private object Keys {
        val ENABLED = booleanPreferencesKey("schedule_reminders_enabled")
        val MORNING = stringPreferencesKey("schedule_reminders_morning") // "HH:MM"
        val EVENING = stringPreferencesKey("schedule_reminders_evening") // "HH:MM"
    }

    val settings: Flow<ReminderSettings> = store.data.map { p ->
        val d = ReminderSettings()
        ReminderSettings(
            enabled = p[Keys.ENABLED] ?: d.enabled,
            morning = ReminderLogic.parseTime(p[Keys.MORNING]) ?: d.morning,
            evening = ReminderLogic.parseTime(p[Keys.EVENING]) ?: d.evening,
        )
    }

    suspend fun current(): ReminderSettings = settings.first()

    suspend fun setEnabled(enabled: Boolean) {
        store.edit { it[Keys.ENABLED] = enabled }
        ReminderScheduler.reschedule(appContext)
    }

    suspend fun setMorning(time: LocalTime) {
        store.edit { it[Keys.MORNING] = ReminderLogic.formatTime(time) }
        ReminderScheduler.reschedule(appContext)
    }

    suspend fun setEvening(time: LocalTime) {
        store.edit { it[Keys.EVENING] = ReminderLogic.formatTime(time) }
        ReminderScheduler.reschedule(appContext)
    }
}
