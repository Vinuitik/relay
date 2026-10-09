package com.relay.app.ui.screens

import com.relay.app.model.Booking
import com.relay.app.model.BookingInput
import com.relay.app.model.Day
import com.relay.app.model.Gap
import com.relay.app.model.Repeat
import java.time.LocalDate

/*
 * Pure state + rules behind the booking editor (no Compose, JVM-testable). Times are minutes since
 * midnight in the runner's zone; validation mirrors runner/internal/schedule ValidateDay/Validate
 * (shared/API.md "Schedule") so Save is only enabled for something the server will accept -
 * overlaps with *other* bookings (409) are still only known to the server.
 */

/** Server's minimum sleep length and minimum awake time between two sleeps (MinGapMinutes). */
const val MIN_GAP_MINUTES = 10

/** The plan's `warn` event fires this long before each sleep (API.md "Schedule plan file"). */
const val WARN_LEAD_MINUTES = 5

/** One sleep row: suspend at [from], wake at [to] (minutes since midnight). */
data class SleepSlot(val from: Int, val to: Int)

enum class RepeatFreq(val api: String?) { NEVER(null), DAILY("daily"), WEEKLY("weekly"), MONTHLY("monthly"), YEARLY("yearly") }

enum class RepeatEnd { NEVER, ON_DATE, AFTER_COUNT }

/** What the route asked for: `date` only / `bookingId` + `date` / `bookingId` only. */
enum class EditorMode { NEW, OCCURRENCE, SERIES }

/**
 * Everything the editor form holds. [date] = the booking's date (one-off) or first day (series);
 * in occurrence mode it stays the series' first day and the occurrence date lives in the VM.
 * [weekdays] 1=Mon..7=Sun, only sent for [RepeatFreq.WEEKLY].
 */
data class BookingForm(
    val title: String = "",
    val date: LocalDate,
    val start: Int = 8 * 60,
    val end: Int = 22 * 60,
    val sleeps: List<SleepSlot> = emptyList(),
    val freq: RepeatFreq = RepeatFreq.NEVER,
    val interval: Int = 1,
    val weekdays: Set<Int> = setOf(date.dayOfWeek.value),
    val ends: RepeatEnd = RepeatEnd.NEVER,
    val until: LocalDate? = null,
    val count: Int = 10,
) {
    /** The repeat-related part, to tell whether the user touched it. */
    fun repeatPart(): Any = listOf(freq, interval, weekdays, ends, until, count)
}

/** Client-side validation result; every message is shown next to its row. */
data class FormErrors(
    val end: String? = null,
    /** Keyed by index in [BookingForm.sleeps] (which the editor keeps sorted). */
    val sleeps: Map<Int, String> = emptyMap(),
    val repeat: String? = null,
) {
    val isValid: Boolean get() = end == null && sleeps.isEmpty() && repeat == null
}

/** Sleeps sorted the way the server requires (by start, then end). */
fun sortSleeps(sleeps: List<SleepSlot>): List<SleepSlot> = sleeps.sortedWith(compareBy({ it.from }, { it.to }))

/**
 * Mirrors the server's checks. Day rules always; repeat rules only when [includeRepeat] (they're
 * irrelevant for "this day only"). One message per sleep row - the first rule it breaks, in the
 * server's order.
 */
fun validateForm(form: BookingForm, includeRepeat: Boolean = true): FormErrors {
    val end = if (form.start >= form.end) "End must be after start" else null
    val sleepErrors = mutableMapOf<Int, String>()
    var prevTo = -1
    form.sleeps.forEachIndexed { i, s ->
        val msg = when {
            s.from >= s.to -> "Wake must be after sleep"
            s.from < form.start -> "Starts before the awake block (${formatMinutes(form.start)})"
            s.to > form.end -> "Ends after the awake block (${formatMinutes(form.end)})"
            s.to - s.from < MIN_GAP_MINUTES -> "Shorter than $MIN_GAP_MINUTES minutes"
            s.from < prevTo -> "Overlaps the sleep before"
            prevTo >= 0 && s.from - prevTo < MIN_GAP_MINUTES -> "Needs $MIN_GAP_MINUTES awake minutes after the sleep before"
            else -> null
        }
        if (msg != null) sleepErrors[i] = msg
        prevTo = maxOf(prevTo, s.to)
    }
    val repeat = if (!includeRepeat || form.freq == RepeatFreq.NEVER) null else when {
        form.interval < 1 -> "Repeat every must be at least 1"
        form.freq == RepeatFreq.WEEKLY && form.weekdays.isEmpty() -> "Pick at least one weekday"
        form.freq == RepeatFreq.WEEKLY && form.weekdays.any { it !in 1..7 } -> "Weekdays must be Mon..Sun"
        form.ends == RepeatEnd.ON_DATE && form.until == null -> "Pick an end date"
        form.ends == RepeatEnd.ON_DATE && form.until!!.isBefore(form.date) -> "Ends before the first day"
        form.ends == RepeatEnd.AFTER_COUNT && form.count < 1 -> "Must happen at least once"
        else -> null
    }
    return FormErrors(end = end, sleeps = sleepErrors, repeat = repeat)
}

private fun roundUpTo5(m: Int): Int = (m + 4) / 5 * 5

/**
 * What "+ Add sleep" proposes: a sleep in the largest free stretch of the awake block, keeping the
 * [MIN_GAP_MINUTES] awake buffer around existing sleeps - half that stretch long (10 min..3 h),
 * centred, on a 5-minute boundary. Null when nothing valid fits (the button is then disabled).
 */
fun proposeSleep(start: Int, end: Int, sleeps: List<SleepSlot>): SleepSlot? {
    if (start >= end) return null
    val sorted = sortSleeps(sleeps)
    val windows = mutableListOf<Pair<Int, Int>>()
    var cursor = start
    var afterSleep = false
    for (s in sorted) {
        val a = if (afterSleep) cursor + MIN_GAP_MINUTES else cursor
        val b = s.from - MIN_GAP_MINUTES
        if (b - a >= MIN_GAP_MINUTES) windows += a to b
        cursor = maxOf(cursor, s.to)
        afterSleep = true
    }
    val a = if (afterSleep) cursor + MIN_GAP_MINUTES else cursor
    if (end - a >= MIN_GAP_MINUTES) windows += a to end
    val (wa, wb) = windows.maxByOrNull { it.second - it.first } ?: return null
    val len = wb - wa
    val dur = (len / 2).coerceIn(MIN_GAP_MINUTES, 180) / 5 * 5
    var from = roundUpTo5(wa + (len - dur) / 2)
    if (from + dur > wb) from = wa
    return SleepSlot(from, from + dur)
}

/** When the first sleep's warning notification goes out ("HH:MM"), or null with no sleeps. */
fun warningTime(sleeps: List<SleepSlot>): String? =
    sortSleeps(sleeps).firstOrNull()?.let { formatMinutes((it.from - WARN_LEAD_MINUTES).coerceAtLeast(0)) }

fun BookingForm.toGaps(): List<Gap> = sortSleeps(sleeps).map { Gap(formatMinutes(it.from), formatMinutes(it.to)) }

/** Body of "this day only". */
fun BookingForm.toDay(): Day = Day(formatMinutes(start), formatMinutes(end), toGaps())

/** Body of create / edit-whole-series. */
fun BookingForm.toBookingInput(): BookingInput = BookingInput(
    title = title.trim(),
    date = date.toString(),
    start = formatMinutes(start),
    end = formatMinutes(end),
    sleeps = toGaps(),
    repeat = toRepeat(),
)

fun BookingForm.toRepeat(): Repeat? {
    val api = freq.api ?: return null
    return Repeat(
        freq = api,
        interval = interval,
        weekdays = if (freq == RepeatFreq.WEEKLY) weekdays.sorted() else null,
        until = if (ends == RepeatEnd.ON_DATE) until?.toString() else null,
        count = if (ends == RepeatEnd.AFTER_COUNT) count else null,
    )
}

/**
 * Prefills the form from [booking]. With [occurrenceDate] and an override for that day, the
 * awake block + sleeps come from the override; title/date/repeat always come from the series.
 * Unparseable times fall back to the defaults instead of crashing the screen.
 */
fun formFromBooking(booking: Booking, occurrenceDate: String? = null): BookingForm {
    val date = runCatching { LocalDate.parse(booking.date) }.getOrElse { LocalDate.now() }
    val override = occurrenceDate?.let { d ->
        booking.exceptions.firstOrNull { it.date == d && !it.cancelled }?.override
    }
    val day = override ?: Day(booking.start, booking.end, booking.sleeps)
    val defaults = BookingForm(date = date)
    val r = booking.repeat
    val freq = RepeatFreq.entries.firstOrNull { it.api != null && it.api == r?.freq } ?: RepeatFreq.NEVER
    return BookingForm(
        title = booking.title,
        date = date,
        start = parseMinutes(day.start) ?: defaults.start,
        end = parseMinutes(day.end) ?: defaults.end,
        sleeps = sortSleeps(day.sleeps.mapNotNull { g ->
            val f = parseMinutes(g.from) ?: return@mapNotNull null
            val t = parseMinutes(g.to) ?: return@mapNotNull null
            SleepSlot(f, t)
        }),
        freq = freq,
        interval = r?.interval?.coerceAtLeast(1) ?: 1,
        weekdays = r?.weekdays?.toSet()?.takeIf { it.isNotEmpty() } ?: setOf(date.dayOfWeek.value),
        ends = when {
            r?.until != null -> RepeatEnd.ON_DATE
            r?.count != null -> RepeatEnd.AFTER_COUNT
            else -> RepeatEnd.NEVER
        },
        until = r?.until?.let { runCatching { LocalDate.parse(it) }.getOrNull() },
        count = r?.count ?: defaults.count,
    )
}

/** Unit word for the "every N" stepper: "every 2 weeks". */
fun intervalUnit(freq: RepeatFreq, n: Int): String {
    val base = when (freq) {
        RepeatFreq.DAILY -> "day"
        RepeatFreq.WEEKLY -> "week"
        RepeatFreq.MONTHLY -> "month"
        RepeatFreq.YEARLY -> "year"
        RepeatFreq.NEVER -> ""
    }
    return if (n == 1) base else "${base}s"
}
