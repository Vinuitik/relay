package com.relay.app.ui.screens

import com.relay.app.data.ScheduleCache
import com.relay.app.model.Event
import com.relay.app.model.Gap
import com.relay.app.model.Schedule
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId

class ScheduleLayoutTest {

    private val london = ZoneId.of("Europe/London")

    @Test
    fun mondayOf_isMondayBased() {
        assertEquals(LocalDate.parse("2026-10-05"), mondayOf(LocalDate.parse("2026-10-05"))) // Mon
        assertEquals(LocalDate.parse("2026-10-05"), mondayOf(LocalDate.parse("2026-10-09"))) // Fri
        assertEquals(LocalDate.parse("2026-10-05"), mondayOf(LocalDate.parse("2026-10-11"))) // Sun
    }

    @Test
    fun currentMonday_usesRunnerZoneNotPhone() {
        // Sun 2026-10-11 23:30 UTC = already Mon 2026-10-12 in Tokyo.
        val now = Instant.parse("2026-10-11T23:30:00Z")
        assertEquals(LocalDate.parse("2026-10-05"), currentMonday(ZoneId.of("UTC"), now))
        assertEquals(LocalDate.parse("2026-10-12"), currentMonday(ZoneId.of("Asia/Tokyo"), now))
    }

    @Test
    fun zoneOf_fallsBackOnGarbage() {
        assertEquals(london, zoneOf("Europe/London"))
        assertEquals(ZoneId.systemDefault(), zoneOf("Not/AZone"))
        assertEquals(ZoneId.systemDefault(), zoneOf(null))
    }

    @Test
    fun weekLabel_sameAndCrossMonth() {
        assertEquals("12 – 18 Oct", weekLabel(LocalDate.parse("2026-10-12")))
        assertEquals("28 Sep – 4 Oct", weekLabel(LocalDate.parse("2026-09-28")))
    }

    @Test
    fun parseMinutes_acceptsValidOnly() {
        assertEquals(0, parseMinutes("00:00"))
        assertEquals(9 * 60 + 30, parseMinutes("09:30"))
        assertEquals(1440, parseMinutes("24:00"))
        assertNull(parseMinutes("24:30"))
        assertNull(parseMinutes("9"))
        assertNull(parseMinutes("ab:cd"))
    }

    @Test
    fun minuteToY_scalesByHourHeight() {
        assertEquals(0f, minuteToY(0, 40f), 0.001f)
        assertEquals(240f, minuteToY(6 * 60, 40f), 0.001f)
        assertEquals(20f, minuteToY(30, 40f), 0.001f)
    }

    @Test
    fun blockSegments_alternatesAwakeAndSleep() {
        val segs = blockSegments("08:00", "23:00", listOf(Gap("09:00", "17:00")))
        assertEquals(
            listOf(
                BlockSegment(480, 540, sleep = false),
                BlockSegment(540, 1020, sleep = true),
                BlockSegment(1020, 1380, sleep = false),
            ),
            segs,
        )
    }

    @Test
    fun blockSegments_noSleeps_oneAwakeSlice() {
        assertEquals(listOf(BlockSegment(360, 1440, sleep = false)), blockSegments("06:00", "24:00", emptyList()))
    }

    @Test
    fun blockSegments_clampsAndSkipsBadGaps() {
        val segs = blockSegments(
            "08:00", "20:00",
            listOf(
                Gap("12:00", "13:00"),
                Gap("07:00", "09:00"), // starts before the block → clamped to 08:00
                Gap("12:30", "14:00"), // overlaps the previous sleep → starts at 13:00
                Gap("bad", "10:00"),
                Gap("19:00", "21:00"), // runs past the end → clamped to 20:00
            ),
        )
        assertEquals(
            listOf(
                BlockSegment(480, 540, sleep = true),
                BlockSegment(540, 720, sleep = false),
                BlockSegment(720, 780, sleep = true),
                BlockSegment(780, 840, sleep = true),
                BlockSegment(840, 1140, sleep = false),
                BlockSegment(1140, 1200, sleep = true),
            ),
            segs,
        )
    }

    @Test
    fun blockSegments_badBlock_isEmpty() {
        assertTrue(blockSegments("10:00", "09:00", emptyList()).isEmpty())
        assertTrue(blockSegments("x", "09:00", emptyList()).isEmpty())
    }

    private fun cache(from: String, to: String) = ScheduleCache("Europe/London", "2026-10-09T10:00:00Z", from, to, emptyList())

    @Test
    fun weekCovered_onlyWhenWholeWeekCached() {
        val c = cache("2026-10-09", "2026-10-22") // Fri..Thu
        assertFalse(weekCovered(LocalDate.parse("2026-10-05"), c)) // current week: Mon-Thu missing
        assertTrue(weekCovered(LocalDate.parse("2026-10-12"), c))
        assertFalse(weekCovered(LocalDate.parse("2026-10-19"), c)) // Fri-Sun missing
        assertFalse(weekCovered(LocalDate.parse("2026-10-12"), null))
    }

    @Test
    fun nextSleep_firstFutureSleepEvent() {
        val plan = listOf(
            Event("sleep", "2026-10-09", "08:00"), // past
            Event("warn", "2026-10-13", "08:55"),
            Event("sleep", "2026-10-14", "09:00"),
            Event("sleep", "2026-10-13", "09:00"),
        )
        val now = LocalDate.parse("2026-10-09").atTime(12, 0).atZone(london)
        assertEquals(Event("sleep", "2026-10-13", "09:00"), nextSleep(plan, now))
        assertNull(nextSleep(emptyList(), now))
    }

    private fun schedule(planWrittenAt: String?, applied: Boolean, appliedAt: String? = null) = Schedule(
        timezone = "Europe/London",
        bookings = emptyList(),
        plan = listOf(Event("sleep", "2026-10-13", "09:00")),
        planWrittenAt = planWrittenAt,
        appliedAt = appliedAt,
        applied = applied,
    )

    private val now = Instant.parse("2026-10-09T12:00:00Z")

    @Test
    fun statusLine_applied() {
        val s = schedule("2026-10-09T10:00:00Z", applied = true, appliedAt = "2026-10-09T10:05:00Z")
        assertEquals("Applied on server ✓ 11:05 · next: sleep Tue 09:00", scheduleStatusLine(s, null, false, london, now))
    }

    @Test
    fun statusLine_notApplied() {
        val s = schedule("2026-10-09T10:00:00Z", applied = false)
        assertEquals("Not applied yet · next: sleep Tue 09:00", scheduleStatusLine(s, null, false, london, now))
    }

    @Test
    fun statusLine_noApplier_onlyNextSleep() {
        val s = schedule(null, applied = false)
        assertEquals("next: sleep Tue 09:00", scheduleStatusLine(s, null, false, london, now))
    }

    @Test
    fun statusLine_offline_showsCacheAgeOnly() {
        val s = schedule("2026-10-09T10:00:00Z", applied = true)
        assertEquals(
            "Offline · showing saved copy from 2h ago",
            scheduleStatusLine(s, cache("2026-10-09", "2026-10-22"), true, london, now),
        )
        assertNull(scheduleStatusLine(null, null, true, london, now))
    }

    @Test
    fun statusLine_loading_isNull() {
        assertNull(scheduleStatusLine(null, null, false, london, now))
    }
}
