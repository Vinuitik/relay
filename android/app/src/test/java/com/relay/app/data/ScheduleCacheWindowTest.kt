package com.relay.app.data

import org.junit.Assert.assertEquals
import org.junit.Test
import java.time.LocalDate

class ScheduleCacheWindowTest {

    private fun window(today: String) = ScheduleRepository.cacheWindow(LocalDate.parse(today))
        .let { (from, to) -> from.toString() to to.toString() }

    @Test
    fun startsOnThisWeeksMonday_endsTodayPlus13() {
        // Fri 2026-10-09: Mon 10-05 .. 10-22 (18 days).
        assertEquals("2026-10-05" to "2026-10-22", window("2026-10-09"))
    }

    @Test
    fun onMonday_isExactlyDaysLong() {
        assertEquals("2026-10-05" to "2026-10-18", window("2026-10-05"))
    }

    @Test
    fun onSunday_reachesBackSixDays() {
        assertEquals("2026-10-05" to "2026-10-24", window("2026-10-11"))
    }

    @Test
    fun acrossMonthBoundary() {
        // Thu 2026-10-01 → Mon 2026-09-28.
        assertEquals("2026-09-28" to "2026-10-14", window("2026-10-01"))
    }
}
