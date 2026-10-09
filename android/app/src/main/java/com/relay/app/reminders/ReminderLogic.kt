package com.relay.app.reminders

import com.relay.app.model.Occurrence
import java.time.LocalDate
import java.time.LocalTime
import java.time.ZoneId
import java.time.ZonedDateTime
import java.time.format.DateTimeFormatter

/** Which of the two daily reminders. */
enum class ReminderKind { MORNING, EVENING }

/** Pure helpers for the schedule reminders - no Android types, JVM-unit-tested. */
object ReminderLogic {

    private val HH_MM: DateTimeFormatter = DateTimeFormatter.ofPattern("HH:mm")

    /** "HH:MM" -> LocalTime; null if malformed. */
    fun parseTime(s: String?): LocalTime? =
        s?.let { runCatching { LocalTime.parse(it, HH_MM) }.getOrNull() }

    fun formatTime(t: LocalTime): String = t.format(HH_MM)

    /**
     * Next instant (epoch millis) the wall clock in [zone] reads [at], strictly after [nowMillis].
     * Today if still ahead, else tomorrow. Built from the local date + time (not "+24h"), so a
     * DST change in between is handled; a time inside a spring-forward gap moves forward by the
     * gap length (ZonedDateTime.of semantics), an ambiguous autumn time takes the earlier offset.
     */
    fun nextTriggerMillis(nowMillis: Long, at: LocalTime, zone: ZoneId): Long {
        val now = ZonedDateTime.ofInstant(java.time.Instant.ofEpochMilli(nowMillis), zone)
        var candidate = ZonedDateTime.of(now.toLocalDate(), at, zone)
        if (!candidate.isAfter(now)) candidate = ZonedDateTime.of(now.toLocalDate().plusDays(1), at, zone)
        return candidate.toInstant().toEpochMilli()
    }

    /** Occurrences whose date is [today]. Empty = no booking today, no reminder. */
    fun todays(occurrences: List<Occurrence>, today: LocalDate): List<Occurrence> {
        val d = today.toString()
        return occurrences.filter { it.date == d }
    }

    /**
     * Notification text for [kind], or null when [todays] is empty. Several occurrences on one day
     * collapse to earliest start - latest end ("HH:MM" compares correctly as a string).
     */
    fun text(kind: ReminderKind, label: String, todays: List<Occurrence>): String? {
        if (todays.isEmpty()) return null
        return when (kind) {
            ReminderKind.MORNING -> {
                val start = todays.minOf { it.start }
                val end = todays.maxOf { it.end }
                "Booked today $start–$end · turn on $label"
            }
            ReminderKind.EVENING -> "$label: done for today, power it down"
        }
    }

    /** Stable per runner + kind, so a re-fire replaces rather than stacks. */
    fun notificationId(kind: ReminderKind, hostname: String): Int =
        "schedule_reminder:${kind.name}:$hostname".hashCode()
}
