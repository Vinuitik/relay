package com.relay.app.reminders

import com.relay.app.model.Occurrence
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant
import java.time.LocalDate
import java.time.LocalTime
import java.time.ZoneId
import java.time.ZonedDateTime

class ReminderLogicTest {

    private val london = ZoneId.of("Europe/London")

    private fun millis(s: String, zone: ZoneId = london) =
        ZonedDateTime.of(java.time.LocalDateTime.parse(s), zone).toInstant().toEpochMilli()

    private fun next(now: String, at: String, zone: ZoneId = london) =
        Instant.ofEpochMilli(ReminderLogic.nextTriggerMillis(millis(now, zone), LocalTime.parse(at), zone))

    @Test fun laterToday() {
        assertEquals(Instant.parse("2026-10-09T05:30:00Z"), next("2026-10-09T05:00", "06:30")) // BST
    }

    @Test fun alreadyPastGoesToTomorrow() {
        assertEquals(Instant.parse("2026-10-10T05:30:00Z"), next("2026-10-09T07:00", "06:30"))
    }

    @Test fun exactlyNowGoesToTomorrow() {
        assertEquals(Instant.parse("2026-10-10T21:00:00Z"), next("2026-10-09T22:00", "22:00"))
    }

    @Test fun acrossAutumnClockChange() {
        // 2026-10-25 01:00 GMT clocks go back: 06:30 on the 25th is GMT (+00:00), 24.5h later.
        assertEquals(Instant.parse("2026-10-25T06:30:00Z"), next("2026-10-24T07:00", "06:30"))
        // Evening of the 24th (BST) -> evening of the 25th (GMT): 25h apart.
        assertEquals(Instant.parse("2026-10-25T22:00:00Z"), next("2026-10-24T22:30", "22:00"))
    }

    @Test fun onClockChangeDayBeforeTheChange() {
        // 00:30 BST on the 25th, alarm 06:30 the same day (after the change) -> 06:30 GMT.
        assertEquals(Instant.parse("2026-10-25T06:30:00Z"), next("2026-10-25T00:30", "06:30"))
    }

    @Test fun springGapMovesForward() {
        // 2027-03-28 01:00 GMT -> 02:00 BST; 01:30 doesn't exist, becomes 02:30 BST = 01:30Z.
        assertEquals(Instant.parse("2027-03-28T01:30:00Z"), next("2027-03-27T23:00", "01:30"))
    }

    private fun occ(date: String, start: String, end: String) =
        Occurrence("b", "", date, start, end, emptyList(), recurring = false, edited = false)

    @Test fun todaysFiltersByDate() {
        val list = listOf(occ("2026-10-08", "08:30", "18:00"), occ("2026-10-09", "08:30", "18:00"))
        assertEquals(1, ReminderLogic.todays(list, LocalDate.parse("2026-10-09")).size)
        assertTrue(ReminderLogic.todays(list, LocalDate.parse("2026-10-10")).isEmpty())
    }

    @Test fun morningText() {
        val today = listOf(occ("2026-10-09", "08:30", "18:00"))
        assertEquals("Booked today 08:30–18:00 · turn on Dell",
            ReminderLogic.text(ReminderKind.MORNING, "Dell", today))
    }

    @Test fun morningTextSpansSeveralOccurrences() {
        val today = listOf(occ("2026-10-09", "13:00", "17:00"), occ("2026-10-09", "08:30", "11:00"),
            occ("2026-10-09", "09:00", "21:15"))
        assertEquals("Booked today 08:30–21:15 · turn on Dell",
            ReminderLogic.text(ReminderKind.MORNING, "Dell", today))
    }

    @Test fun eveningText() {
        assertEquals("Dell: done for today, power it down",
            ReminderLogic.text(ReminderKind.EVENING, "Dell", listOf(occ("2026-10-09", "08:30", "18:00"))))
    }

    @Test fun noBookingNoText() {
        assertNull(ReminderLogic.text(ReminderKind.MORNING, "Dell", emptyList()))
        assertNull(ReminderLogic.text(ReminderKind.EVENING, "Dell", emptyList()))
    }

    @Test fun parseTime() {
        assertEquals(LocalTime.of(6, 30), ReminderLogic.parseTime("06:30"))
        assertNull(ReminderLogic.parseTime("6:30pm"))
        assertNull(ReminderLogic.parseTime(null))
        assertEquals("22:00", ReminderLogic.formatTime(LocalTime.of(22, 0)))
    }

    @Test fun notificationIdStablePerRunnerAndKind() {
        val a = ReminderLogic.notificationId(ReminderKind.MORNING, "100.1.1.1")
        assertEquals(a, ReminderLogic.notificationId(ReminderKind.MORNING, "100.1.1.1"))
        assertTrue(a != ReminderLogic.notificationId(ReminderKind.EVENING, "100.1.1.1"))
        assertTrue(a != ReminderLogic.notificationId(ReminderKind.MORNING, "100.1.1.2"))
    }
}
