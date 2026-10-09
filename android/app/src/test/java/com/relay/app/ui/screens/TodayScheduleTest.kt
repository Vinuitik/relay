package com.relay.app.ui.screens

import com.relay.app.data.ScheduleCache
import com.relay.app.model.Gap
import com.relay.app.model.Occurrence
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import java.time.LocalDateTime
import java.time.ZoneId
import java.time.ZonedDateTime

class TodayScheduleTest {

    private val london = ZoneId.of("Europe/London")

    private fun occ(date: String, start: String, end: String, vararg sleeps: Pair<String, String>) = Occurrence(
        bookingId = "b-$start",
        title = "",
        date = date,
        start = start,
        end = end,
        sleeps = sleeps.map { Gap(it.first, it.second) },
        recurring = false,
        edited = false,
    )

    private fun cache(vararg o: Occurrence) = ScheduleCache(
        timezone = "Europe/London",
        fetchedAt = "2026-10-09T06:00:00Z",
        from = "2026-10-05",
        to = "2026-10-22",
        occurrences = o.toList(),
    )

    private fun at(hhmm: String, date: String = "2026-10-09"): ZonedDateTime =
        LocalDateTime.parse("${date}T$hhmm").atZone(london)

    private val day = cache(occ("2026-10-09", "08:30", "18:00", "09:00" to "12:00"))

    @Test
    fun title_isEarliestStartToLatestEnd() {
        assertEquals("Today · 08:30–18:00", todaySummary(day, at("10:00"))!!.title)
    }

    @Test
    fun beforeTheBooking_asleepUntilStart() {
        assertEquals("Asleep until 08:30", todaySummary(day, at("07:00"))!!.transition)
    }

    @Test
    fun awakeBeforeASleep_saysWhenItSleepsAndWakes() {
        assertEquals("Sleeps at 09:00 → wakes 12:00", todaySummary(day, at("08:45"))!!.transition)
    }

    @Test
    fun insideASleep_asleepUntilWake() {
        assertEquals("Asleep until 12:00", todaySummary(day, at("09:00"))!!.transition)
        assertEquals("Asleep until 12:00", todaySummary(day, at("11:59"))!!.transition)
    }

    @Test
    fun afterTheLastSleep_awakeUntilEnd() {
        assertEquals("Awake until 18:00", todaySummary(day, at("12:00"))!!.transition)
    }

    @Test
    fun afterTheBooking_asleepSince() {
        assertEquals("Asleep since 18:00", todaySummary(day, at("19:00"))!!.transition)
    }

    @Test
    fun severalOccurrences_gapBetweenThemIsASleep() {
        val c = cache(
            occ("2026-10-09", "14:00", "20:00"),
            occ("2026-10-09", "07:00", "10:00", "08:00" to "09:00"),
            occ("2026-10-10", "06:00", "23:00"), // tomorrow: ignored
        )
        val s = todaySummary(c, at("09:30"))!!
        assertEquals("Today · 07:00–20:00", s.title)
        assertEquals("Sleeps at 10:00 → wakes 14:00", s.transition)
        assertEquals("Asleep until 14:00", todaySummary(c, at("12:00"))!!.transition)
        assertEquals("Awake until 20:00", todaySummary(c, at("15:00"))!!.transition)
        assertEquals(4, s.segments.size)
    }

    @Test
    fun adjacentOccurrences_mergeIntoOneAwakeStretch() {
        val c = cache(occ("2026-10-09", "08:00", "12:00"), occ("2026-10-09", "12:00", "16:00"))
        assertEquals("Awake until 16:00", todaySummary(c, at("09:00"))!!.transition)
    }

    @Test
    fun sleepRunningToTheEnd_isNotAWake() {
        val c = cache(occ("2026-10-09", "08:00", "12:00", "11:00" to "12:00"))
        assertEquals("Awake until 11:00", todaySummary(c, at("09:00"))!!.transition)
        assertEquals("Asleep since 11:00", todaySummary(c, at("11:30"))!!.transition)
    }

    @Test
    fun noBookingToday_orNoCache_isNull() {
        assertNull(todaySummary(cache(occ("2026-10-10", "08:00", "18:00")), at("10:00")))
        assertNull(todaySummary(cache(), at("10:00")))
        assertNull(todaySummary(null, at("10:00")))
    }

    @Test
    fun nowMinute_isWallClockInGivenZone() {
        assertEquals(10 * 60 + 15, todaySummary(day, at("10:15"))!!.nowMinute)
    }
}
