package com.relay.app.ui.screens

import com.relay.app.data.ScheduleCache
import com.relay.app.model.Event
import com.relay.app.model.Gap
import com.relay.app.model.Schedule
import com.relay.app.ui.components.relativeTime
import java.time.DayOfWeek
import java.time.Instant
import java.time.LocalDate
import java.time.LocalTime
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.ZonedDateTime
import java.time.format.DateTimeFormatter
import java.time.format.TextStyle
import java.time.temporal.TemporalAdjusters
import java.util.Locale

/*
 * Pure helpers behind the Schedule week grid (no Compose, JVM-testable). Dates/times are wall-clock
 * in the runner's zone, like everything in shared/API.md "Schedule".
 */

/** The runner's zone, or the phone's when [tz] is missing/unknown. */
fun zoneOf(tz: String?): ZoneId =
    tz?.let { runCatching { ZoneId.of(it) }.getOrNull() } ?: ZoneId.systemDefault()

/** Monday of the (Monday-based) week [date] is in. */
fun mondayOf(date: LocalDate): LocalDate = date.with(TemporalAdjusters.previousOrSame(DayOfWeek.MONDAY))

/** Monday of the week containing "now" in [zone] - near midnight this differs from the phone's week. */
fun currentMonday(zone: ZoneId, now: Instant = Instant.now()): LocalDate = mondayOf(now.atZone(zone).toLocalDate())

/** "13 – 19 Oct"; across months "29 Sep – 5 Oct". */
fun weekLabel(monday: LocalDate): String {
    val sunday = monday.plusDays(6)
    val m1 = monday.month.getDisplayName(TextStyle.SHORT, Locale.ENGLISH)
    val m2 = sunday.month.getDisplayName(TextStyle.SHORT, Locale.ENGLISH)
    return if (m1 == m2) "${monday.dayOfMonth} – ${sunday.dayOfMonth} $m2"
    else "${monday.dayOfMonth} $m1 – ${sunday.dayOfMonth} $m2"
}

/** "HH:MM" → minutes since midnight (0..1440, "24:00" allowed); null if malformed. */
fun parseMinutes(hhmm: String): Int? {
    val parts = hhmm.split(":")
    if (parts.size != 2) return null
    val h = parts[0].toIntOrNull() ?: return null
    val m = parts[1].toIntOrNull() ?: return null
    if (h !in 0..24 || m !in 0..59 || (h == 24 && m != 0)) return null
    return h * 60 + m
}

/** Vertical offset of [minute] in a grid whose hours are [hourHeight] tall (any unit: px or dp). */
fun minuteToY(minute: Int, hourHeight: Float): Float = minute / 60f * hourHeight

/** A slice of an awake block: [sleep] = the machine is suspended for [from]..[to] (minutes). */
data class BlockSegment(val from: Int, val to: Int, val sleep: Boolean)

/**
 * Splits the awake block [start]..[end] into alternating awake / sleep slices from [sleeps].
 * Sleeps are clamped to the block; malformed, empty or overlapping ones are skipped, so a
 * slightly-off server answer still draws something sensible. Empty list if the block itself is bad.
 */
fun blockSegments(start: String, end: String, sleeps: List<Gap>): List<BlockSegment> {
    val s = parseMinutes(start) ?: return emptyList()
    val e = parseMinutes(end) ?: return emptyList()
    if (e <= s) return emptyList()
    val out = mutableListOf<BlockSegment>()
    var cursor = s
    sleeps.mapNotNull { g ->
        val f = parseMinutes(g.from) ?: return@mapNotNull null
        val t = parseMinutes(g.to) ?: return@mapNotNull null
        f to t
    }.sortedBy { it.first }.forEach { (f0, t0) ->
        val f = maxOf(f0, cursor)
        val t = minOf(t0, e)
        if (t <= f) return@forEach
        if (f > cursor) out += BlockSegment(cursor, f, sleep = false)
        out += BlockSegment(f, t, sleep = true)
        cursor = t
    }
    if (cursor < e) out += BlockSegment(cursor, e, sleep = false)
    return out
}

/** True when [cache] holds every day of the week starting [monday] (then no live fetch is needed). */
fun weekCovered(monday: LocalDate, cache: ScheduleCache?): Boolean {
    if (cache == null) return false
    val from = runCatching { LocalDate.parse(cache.from) }.getOrNull() ?: return false
    val to = runCatching { LocalDate.parse(cache.to) }.getOrNull() ?: return false
    return !monday.isBefore(from) && !monday.plusDays(6).isAfter(to)
}

/** First plan `sleep` event at or after [now] (in the runner's zone). */
fun nextSleep(plan: List<Event>, now: ZonedDateTime): Event? = plan
    .filter { it.kind == "sleep" }
    .mapNotNull { ev ->
        val d = runCatching { LocalDate.parse(ev.date) }.getOrNull() ?: return@mapNotNull null
        val m = parseMinutes(ev.at)?.takeIf { it < 1440 } ?: return@mapNotNull null
        ev to ZonedDateTime.of(d, LocalTime.of(m / 60, m % 60), now.zone)
    }
    .filter { (_, at) -> !at.isBefore(now) }
    .minByOrNull { (_, at) -> at }
    ?.first

/**
 * The line under the grid. Offline (refresh failed) → only the saved-copy age, no applied info.
 * Otherwise: applied state (nothing when the runner has no plan file / applier) + next sleep.
 * Null = nothing to say (still loading, or nothing known).
 */
fun scheduleStatusLine(
    schedule: Schedule?,
    cache: ScheduleCache?,
    offline: Boolean,
    zone: ZoneId,
    now: Instant = Instant.now(),
): String? {
    if (offline) {
        val c = cache ?: return null
        val ago = relativeTime(c.fetchedAt, now)
        val whenText = when (ago) {
            "" -> ""
            "now" -> " from just now"
            else -> " from $ago ago"
        }
        return "Offline · showing saved copy$whenText"
    }
    val s = schedule ?: return null
    val parts = mutableListOf<String>()
    if (s.planWrittenAt != null) {
        parts += if (s.applied) {
            val at = s.appliedAt?.let { a ->
                runCatching { OffsetDateTime.parse(a).atZoneSameInstant(zone).format(HHMM) }.getOrNull()
            }
            if (at != null) "Applied on server ✓ $at" else "Applied on server ✓"
        } else {
            "Not applied yet"
        }
    }
    nextSleep(s.plan, now.atZone(zone))?.let { ev ->
        val day = LocalDate.parse(ev.date).dayOfWeek.getDisplayName(TextStyle.SHORT, Locale.ENGLISH)
        parts += "next: sleep $day ${ev.at}"
    }
    return parts.joinToString(" · ").ifEmpty { null }
}

private val HHMM = DateTimeFormatter.ofPattern("HH:mm")
