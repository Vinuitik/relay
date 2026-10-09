package com.relay.app.data

import com.relay.app.model.BookingInput
import com.relay.app.model.Day
import com.relay.app.model.Gap
import com.relay.app.model.Occurrence
import com.relay.app.model.Repeat
import com.relay.app.model.Schedule
import com.squareup.moshi.Moshi
import com.squareup.moshi.Types
import com.squareup.moshi.kotlin.reflect.KotlinJsonAdapterFactory
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Wire shapes from shared/API.md "Schedule", parsed with the same Moshi setup as RelayApiClient. */
class ScheduleJsonTest {

    private val moshi = Moshi.Builder().add(KotlinJsonAdapterFactory()).build()

    private val scheduleJson = """
        {
          "timezone": "Europe/London",
          "bookings": [
            {
              "id": "b1", "title": "", "date": "2026-10-09",
              "start": "08:00", "end": "23:00",
              "sleeps": [{"from": "09:00", "to": "17:00"}],
              "repeat": null,
              "exceptions": [],
              "createdAt": "2026-10-01T10:00:00Z", "updatedAt": "2026-10-01T10:00:00Z"
            },
            {
              "id": "b2", "title": "Commute", "date": "2026-10-12",
              "start": "07:00", "end": "22:00",
              "sleeps": [{"from": "08:00", "to": "12:00"}, {"from": "13:00", "to": "18:00"}],
              "repeat": {"freq": "weekly", "interval": 1, "weekdays": [1, 3, 5], "until": "2026-12-31"},
              "exceptions": [
                {"date": "2026-10-14", "cancelled": true},
                {"date": "2026-10-16", "cancelled": false,
                 "override": {"start": "06:00", "end": "21:00", "sleeps": [{"from": "07:00", "to": "15:00"}]}}
              ],
              "createdAt": "2026-10-01T10:00:00Z", "updatedAt": "2026-10-02T10:00:00Z"
            },
            {
              "id": "b3", "title": "Monthly", "date": "2026-10-31",
              "start": "09:00", "end": "20:00", "sleeps": [],
              "repeat": {"freq": "monthly", "interval": 2, "count": 5},
              "exceptions": [],
              "createdAt": "2026-10-01T10:00:00Z", "updatedAt": "2026-10-01T10:00:00Z"
            }
          ],
          "plan": [
            {"kind": "warn", "date": "2026-10-09", "at": "08:55"},
            {"kind": "sleep", "date": "2026-10-09", "at": "09:00"},
            {"kind": "wake", "date": "2026-10-09", "at": "17:00"}
          ],
          "planWrittenAt": "2026-10-09T00:05:00+01:00",
          "appliedAt": null,
          "applied": false
        }
    """.trimIndent()

    private val occurrencesJson = """
        [
          {"bookingId": "b1", "title": "", "date": "2026-10-09", "start": "08:00", "end": "23:00",
           "sleeps": [{"from": "09:00", "to": "17:00"}], "recurring": false, "edited": false},
          {"bookingId": "b2", "title": "Commute", "date": "2026-10-16", "start": "06:00", "end": "21:00",
           "sleeps": [{"from": "07:00", "to": "15:00"}], "recurring": true, "edited": true}
        ]
    """.trimIndent()

    private val occurrenceListAdapter = moshi.adapter<List<Occurrence>>(
        Types.newParameterizedType(List::class.java, Occurrence::class.java),
    )

    @Test
    fun parsesSchedule() {
        val s = moshi.adapter(Schedule::class.java).fromJson(scheduleJson)!!
        assertEquals("Europe/London", s.timezone)
        assertEquals(3, s.bookings.size)

        val oneOff = s.bookings[0]
        assertNull(oneOff.repeat)
        assertEquals("", oneOff.title)
        assertEquals(listOf(Gap("09:00", "17:00")), oneOff.sleeps)

        val weekly = s.bookings[1].repeat!!
        assertEquals("weekly", weekly.freq)
        assertEquals(listOf(1, 3, 5), weekly.weekdays)
        assertEquals("2026-12-31", weekly.until)
        assertNull(weekly.count)

        val ex = s.bookings[1].exceptions
        assertTrue(ex[0].cancelled)
        assertNull(ex[0].override)
        assertEquals(Day("06:00", "21:00", listOf(Gap("07:00", "15:00"))), ex[1].override)

        val monthly = s.bookings[2].repeat!!
        assertEquals(5, monthly.count)
        assertNull(monthly.until)
        assertNull(monthly.weekdays)
        assertTrue(s.bookings[2].sleeps.isEmpty())

        assertEquals(listOf("warn", "sleep", "wake"), s.plan.map { it.kind })
        assertEquals("2026-10-09T00:05:00+01:00", s.planWrittenAt)
        assertNull(s.appliedAt)
        assertFalse(s.applied)
    }

    @Test
    fun parsesOccurrences() {
        val occ = occurrenceListAdapter.fromJson(occurrencesJson)!!
        assertEquals(2, occ.size)
        assertFalse(occ[0].recurring)
        assertFalse(occ[0].edited)
        assertTrue(occ[1].recurring)
        assertTrue(occ[1].edited)
        assertEquals("b2", occ[1].bookingId)
    }

    @Test
    fun scheduleRoundTrips() {
        val adapter = moshi.adapter(Schedule::class.java)
        val s = adapter.fromJson(scheduleJson)!!
        assertEquals(s, adapter.fromJson(adapter.toJson(s)))
    }

    @Test
    fun bookingInputOmitsNullOptionals() {
        val adapter = moshi.adapter(BookingInput::class.java)
        val oneOff = adapter.toJson(BookingInput("x", "2026-10-09", "08:00", "20:00", emptyList()))
        assertFalse(oneOff.contains("repeat"))

        val series = adapter.toJson(
            BookingInput("x", "2026-10-09", "08:00", "20:00", emptyList(), Repeat("daily", 1, count = 3)),
        )
        assertTrue(series.contains("\"count\":3"))
        assertFalse(series.contains("until"))
        assertFalse(series.contains("weekdays"))
    }

    @Test
    fun cacheRoundTrips() {
        val occ = occurrenceListAdapter.fromJson(occurrencesJson)!!
        val cache = ScheduleCache(
            timezone = "Europe/London",
            fetchedAt = "2026-10-09T12:00:00Z",
            from = "2026-10-09",
            to = "2026-10-22",
            occurrences = occ,
        )
        assertEquals(cache, ScheduleCacheCodec.decode(ScheduleCacheCodec.encode(cache)))
    }

    @Test
    fun cacheDecodeToleratesMissingOrGarbage() {
        assertNull(ScheduleCacheCodec.decode(null))
        assertNull(ScheduleCacheCodec.decode(""))
        assertNull(ScheduleCacheCodec.decode("{not json"))
        assertNull(ScheduleCacheCodec.decode("""{"timezone":"x"}"""))
    }
}
