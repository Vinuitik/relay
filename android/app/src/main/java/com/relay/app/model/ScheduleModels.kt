package com.relay.app.model

/*
 * Schedule (bookings for the server's sleep/wake) - mirrors shared/API.md "### Schedule" exactly.
 * Dates are "YYYY-MM-DD", times "HH:MM" 24h, wall-clock in the runner's zone ([Schedule.timezone]).
 * Optional/nullable contract fields are nullable here; Moshi omits nulls when serializing.
 */

/** One sleep inside a booking's awake block: suspend at [from], wake at [to]. */
data class Gap(val from: String, val to: String)

/** What one occurrence looks like. Also the body of "edit this day only". */
data class Day(val start: String, val end: String, val sleeps: List<Gap>)

data class Repeat(
    val freq: String, // "daily" | "weekly" | "monthly" | "yearly"
    val interval: Int, // every N, >= 1
    val weekdays: List<Int>? = null, // weekly only, 1=Mon..7=Sun; null = the date's weekday
    val until: String? = null, // last possible date, inclusive - at most one of until/count
    val count: Int? = null, // total occurrences
)

/** API.md's `Exception` (renamed: it would shadow kotlin.Exception). [date] = the occurrence's
 * original date; [override] set when not cancelled. */
data class BookingException(
    val date: String,
    val cancelled: Boolean,
    val override: Day? = null,
)

data class Booking(
    val id: String,
    val title: String, // may be ""
    val date: String, // the only day (one-off) or first day (series)
    val start: String,
    val end: String,
    val sleeps: List<Gap>,
    val repeat: Repeat?, // null = one-off
    val exceptions: List<BookingException>,
    val createdAt: String,
    val updatedAt: String,
)

/** Body of create / edit-whole-series: [Booking] minus id/exceptions/createdAt/updatedAt. */
data class BookingInput(
    val title: String,
    val date: String,
    val start: String,
    val end: String,
    val sleeps: List<Gap>,
    val repeat: Repeat? = null,
)

/** One concrete booked day after expanding series and applying exceptions. */
data class Occurrence(
    val bookingId: String,
    val title: String,
    val date: String,
    val start: String,
    val end: String,
    val sleeps: List<Gap>,
    val recurring: Boolean, // from a series
    val edited: Boolean, // this day has an override
)

/** One line of the runner's plan file. */
data class Event(
    val kind: String, // "warn" | "sleep" | "wake"
    val date: String,
    val at: String,
)

data class Schedule(
    val timezone: String, // e.g. "Europe/London"
    val bookings: List<Booking>,
    val plan: List<Event>, // what the runner wants applied, next 14 days
    val planWrittenAt: String?, // RFC3339, null if no plan file (non-Linux, not installed)
    val appliedAt: String?, // when the root applier last applied it
    val applied: Boolean, // applier's copy matches the current plan
)

/** Result of "cancel this day only": a series answers `200 Booking`, a one-off is deleted
 * outright (`204`). */
sealed interface CancelOccurrenceResult {
    data class Updated(val booking: Booking) : CancelOccurrenceResult
    object BookingDeleted : CancelOccurrenceResult
}
