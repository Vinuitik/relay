package com.relay.app.ui.screens

import com.relay.app.model.Booking
import com.relay.app.model.BookingException
import com.relay.app.model.Day
import com.relay.app.model.Gap
import com.relay.app.model.Repeat
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.LocalDate

class BookingEditorLogicTest {

    private val wed = LocalDate.parse("2026-10-14") // a Wednesday
    private fun t(s: String) = parseMinutes(s)!!
    private fun slot(a: String, b: String) = SleepSlot(t(a), t(b))
    private fun form(vararg sleeps: SleepSlot, start: String = "08:00", end: String = "22:00") =
        BookingForm(date = wed, start = t(start), end = t(end), sleeps = sleeps.toList())

    // --- validation -------------------------------------------------------------------------

    @Test
    fun valid_formHasNoErrors() {
        val e = validateForm(form(slot("09:00", "12:00"), slot("12:10", "13:00")))
        assertTrue(e.isValid)
    }

    @Test
    fun endMustBeAfterStart() {
        assertEquals("End must be after start", validateForm(form(start = "10:00", end = "10:00")).end)
        assertEquals("End must be after start", validateForm(form(start = "11:00", end = "10:00")).end)
    }

    @Test
    fun sleep_fromBeforeTo() {
        assertEquals("Wake must be after sleep", validateForm(form(slot("12:00", "12:00"))).sleeps[0])
    }

    @Test
    fun sleep_insideBlock() {
        assertEquals("Starts before the awake block (08:00)", validateForm(form(slot("07:50", "09:00"))).sleeps[0])
        assertEquals("Ends after the awake block (22:00)", validateForm(form(slot("21:00", "22:01"))).sleeps[0])
        // Touching the block edges is fine (server: from >= start, to <= end).
        assertTrue(validateForm(form(slot("08:00", "09:00"), slot("21:00", "22:00"))).isValid)
    }

    @Test
    fun sleep_atLeastTenMinutes() {
        assertEquals("Shorter than 10 minutes", validateForm(form(slot("09:00", "09:09"))).sleeps[0])
        assertTrue(validateForm(form(slot("09:00", "09:10"))).isValid)
    }

    @Test
    fun sleeps_mustNotOverlap() {
        val e = validateForm(form(slot("09:00", "12:00"), slot("11:00", "13:00")))
        assertNull(e.sleeps[0])
        assertEquals("Overlaps the sleep before", e.sleeps[1])
    }

    @Test
    fun sleeps_needTenAwakeMinutesBetween() {
        val e = validateForm(form(slot("09:00", "12:00"), slot("12:09", "13:00")))
        assertEquals("Needs 10 awake minutes after the sleep before", e.sleeps[1])
        assertTrue(validateForm(form(slot("09:00", "12:00"), slot("12:10", "13:00"))).isValid)
    }

    @Test
    fun repeat_weeklyNeedsAWeekday() {
        val f = form().copy(freq = RepeatFreq.WEEKLY, weekdays = emptySet())
        assertEquals("Pick at least one weekday", validateForm(f).repeat)
        // Irrelevant for "this day only".
        assertNull(validateForm(f, includeRepeat = false).repeat)
        // And for a one-off.
        assertNull(validateForm(f.copy(freq = RepeatFreq.NEVER)).repeat)
    }

    @Test
    fun repeat_untilNotBeforeDate() {
        val f = form().copy(freq = RepeatFreq.DAILY, ends = RepeatEnd.ON_DATE, until = wed.minusDays(1))
        assertEquals("Ends before the first day", validateForm(f).repeat)
        assertTrue(validateForm(f.copy(until = wed)).isValid)
        assertEquals("Pick an end date", validateForm(f.copy(until = null)).repeat)
    }

    @Test
    fun repeat_countAndInterval() {
        val f = form().copy(freq = RepeatFreq.MONTHLY, ends = RepeatEnd.AFTER_COUNT, count = 0)
        assertEquals("Must happen at least once", validateForm(f).repeat)
        assertEquals("Repeat every must be at least 1", validateForm(f.copy(count = 3, interval = 0)).repeat)
    }

    // --- "+ Add sleep" proposal --------------------------------------------------------------

    private fun assertProposalValid(f: BookingForm) {
        val p = proposeSleep(f.start, f.end, f.sleeps)
        assertNotNull(p)
        val next = f.copy(sleeps = sortSleeps(f.sleeps + p!!))
        assertTrue("proposal $p invalid: ${validateForm(next)}", validateForm(next).isValid)
    }

    @Test
    fun proposal_emptyBlock_centredHalfCappedAtThreeHours() {
        // 08:00-18:00: 10 h free → 3 h, centred → 11:30-14:30.
        assertEquals(slot("11:30", "14:30"), proposeSleep(t("08:00"), t("18:00"), emptyList()))
        // 08:00-10:00: 2 h free → 1 h, centred → 08:30-09:30.
        assertEquals(slot("08:30", "09:30"), proposeSleep(t("08:00"), t("10:00"), emptyList()))
    }

    @Test
    fun proposal_respectsExistingSleepsAndBuffers() {
        assertProposalValid(form(slot("09:00", "12:00")))
        assertProposalValid(form(slot("08:00", "21:30")))
        assertProposalValid(form(slot("09:00", "12:00"), slot("14:00", "20:00"), start = "08:00", end = "21:00"))
        assertProposalValid(form(start = "08:00", end = "08:10"))
    }

    @Test
    fun proposal_nullWhenNoRoom() {
        assertNull(proposeSleep(t("08:00"), t("08:09"), emptyList()))
        // 08:00-08:30 with a sleep 08:05-08:25: no 10-min slot with 10-min buffers.
        assertNull(proposeSleep(t("08:00"), t("08:30"), listOf(slot("08:05", "08:25"))))
        assertNull(proposeSleep(t("10:00"), t("09:00"), emptyList()))
    }

    @Test
    fun warningTime_isFirstSleepMinusFive() {
        assertEquals("08:55", warningTime(listOf(slot("13:00", "14:00"), slot("09:00", "12:00"))))
        assertNull(warningTime(emptyList()))
    }

    // --- mapping ------------------------------------------------------------------------------

    @Test
    fun toBookingInput_oneOff() {
        val input = form(slot("13:00", "14:00"), slot("09:00", "12:00")).copy(title = "  Office ").toBookingInput()
        assertEquals("Office", input.title)
        assertEquals("2026-10-14", input.date)
        assertEquals("08:00", input.start)
        assertEquals("22:00", input.end)
        assertEquals(listOf(Gap("09:00", "12:00"), Gap("13:00", "14:00")), input.sleeps) // sorted
        assertNull(input.repeat)
    }

    @Test
    fun toRepeat_weeklyWeekdaysSortedMonToSun_untilOnly() {
        val f = form().copy(
            freq = RepeatFreq.WEEKLY,
            interval = 2,
            weekdays = setOf(7, 1, 3),
            ends = RepeatEnd.ON_DATE,
            until = LocalDate.parse("2026-12-31"),
            count = 5,
        )
        assertEquals(Repeat("weekly", 2, listOf(1, 3, 7), until = "2026-12-31", count = null), f.toRepeat())
    }

    @Test
    fun toRepeat_countOnly_noWeekdaysOutsideWeekly() {
        val f = form().copy(freq = RepeatFreq.DAILY, weekdays = setOf(1, 2), ends = RepeatEnd.AFTER_COUNT, count = 4,
            until = LocalDate.parse("2027-01-01"))
        assertEquals(Repeat("daily", 1, null, until = null, count = 4), f.toRepeat())
        val never = f.copy(freq = RepeatFreq.YEARLY, ends = RepeatEnd.NEVER)
        assertEquals(Repeat("yearly", 1, null, null, null), never.toRepeat())
    }

    @Test
    fun defaultWeekday_isTheDatesWeekday() {
        assertEquals(setOf(3), BookingForm(date = wed).weekdays) // Wednesday = 3
    }

    @Test
    fun toDay_carriesBlockAndSleeps() {
        val d = form(slot("09:00", "12:00"), start = "07:30", end = "23:59").toDay()
        assertEquals(Day("07:30", "23:59", listOf(Gap("09:00", "12:00"))), d)
    }

    @Test
    fun formFromBooking_seriesAndOverride() {
        val b = Booking(
            id = "b1", title = "Work", date = "2026-10-12", start = "08:00", end = "20:00",
            sleeps = listOf(Gap("09:00", "17:00")),
            repeat = Repeat("weekly", 1, listOf(1, 3), count = 8),
            exceptions = listOf(BookingException("2026-10-14", false, Day("10:00", "18:00", emptyList()))),
            createdAt = "", updatedAt = "",
        )
        val series = formFromBooking(b)
        assertEquals(t("08:00"), series.start)
        assertEquals(listOf(slot("09:00", "17:00")), series.sleeps)
        assertEquals(RepeatFreq.WEEKLY, series.freq)
        assertEquals(setOf(1, 3), series.weekdays)
        assertEquals(RepeatEnd.AFTER_COUNT, series.ends)
        assertEquals(8, series.count)
        assertEquals(b.repeat, series.toRepeat())

        val day = formFromBooking(b, "2026-10-14")
        assertEquals(t("10:00"), day.start)
        assertEquals(t("18:00"), day.end)
        assertTrue(day.sleeps.isEmpty())
        assertEquals(LocalDate.parse("2026-10-12"), day.date) // still the series' first day
        assertEquals("Work", day.title)

        // An occurrence without override → series values.
        assertEquals(t("08:00"), formFromBooking(b, "2026-10-19").start)
    }

    @Test
    fun formFromBooking_weeklyWithoutWeekdays_defaultsToDatesWeekday() {
        val b = Booking("b2", "", "2026-10-14", "08:00", "20:00", emptyList(),
            Repeat("weekly", 1, until = "2026-11-30"), emptyList(), "", "")
        val f = formFromBooking(b)
        assertEquals(setOf(3), f.weekdays)
        assertEquals(RepeatEnd.ON_DATE, f.ends)
        assertEquals(LocalDate.parse("2026-11-30"), f.until)
        assertFalse(f.freq == RepeatFreq.NEVER)
    }
}
