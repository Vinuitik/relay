package com.relay.app.ui.screens

import com.relay.app.data.ScheduleCache
import java.time.ZonedDateTime

/*
 * Pure logic behind Home's "Today" card (no Compose, JVM-testable). Minutes are since midnight in
 * the runner's zone. Outside booked awake time the server is asleep, so gaps between bookings and
 * sleeps inside them read the same way.
 */

/**
 * Today's bookings flattened for the card. [segments] = every occurrence's awake/sleep slices
 * (for the day bar), [start]..[end] = earliest start .. latest end, [transition] = the next-change
 * line, [nowMinute] = where the now marker goes.
 */
data class TodaySummary(
    val start: Int,
    val end: Int,
    val segments: List<BlockSegment>,
    val transition: String,
    val nowMinute: Int,
) {
    val title: String get() = "Today · ${formatMinutes(start)}–${formatMinutes(end)}"
}

/** Null when [cache] is missing or has no (drawable) occurrence on [now]'s date. */
fun todaySummary(cache: ScheduleCache?, now: ZonedDateTime): TodaySummary? {
    val today = now.toLocalDate().toString()
    val segments = cache?.occurrences.orEmpty()
        .filter { it.date == today }
        .flatMap { blockSegments(it.start, it.end, it.sleeps) }
        .sortedBy { it.from }
    if (segments.isEmpty()) return null
    val nowMinute = now.hour * 60 + now.minute
    return TodaySummary(
        start = segments.minOf { it.from },
        end = segments.maxOf { it.to },
        segments = segments,
        transition = nextTransition(awakeIntervals(segments), nowMinute),
        nowMinute = nowMinute,
    )
}

/** Awake time [from]..[to) in minutes. */
internal data class Span(val from: Int, val to: Int)

/** Awake slices merged into sorted, non-touching spans (overlapping/adjacent bookings collapse). */
internal fun awakeIntervals(segments: List<BlockSegment>): List<Span> {
    val out = mutableListOf<Span>()
    segments.filter { !it.sleep }.sortedBy { it.from }.forEach { s ->
        val last = out.lastOrNull()
        if (last != null && s.from <= last.to) {
            out[out.lastIndex] = Span(last.from, maxOf(last.to, s.to))
        } else {
            out += Span(s.from, s.to)
        }
    }
    return out
}

/**
 * "Sleeps at 09:00 → wakes 12:00" (awake, another awake stretch follows today) /
 * "Awake until 18:00" (awake, last stretch) / "Asleep until 12:00" (asleep, wakes later today) /
 * "Asleep since 18:00" (today's bookings are over). [awake] from [awakeIntervals].
 */
internal fun nextTransition(awake: List<Span>, nowMinute: Int): String {
    val current = awake.firstOrNull { nowMinute >= it.from && nowMinute < it.to }
    if (current != null) {
        val next = awake.firstOrNull { it.from >= current.to }
        return if (next != null) {
            "Sleeps at ${formatMinutes(current.to)} → wakes ${formatMinutes(next.from)}"
        } else {
            "Awake until ${formatMinutes(current.to)}"
        }
    }
    val next = awake.firstOrNull { it.from > nowMinute }
    if (next != null) return "Asleep until ${formatMinutes(next.from)}"
    return "Asleep since ${formatMinutes(awake.lastOrNull()?.to ?: 0)}"
}
