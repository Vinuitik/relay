package com.relay.app.data

import android.content.Context
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import com.relay.app.model.Booking
import com.relay.app.model.BookingInput
import com.relay.app.model.CancelOccurrenceResult
import com.relay.app.model.Day
import com.relay.app.model.KnownRunner
import com.relay.app.model.Occurrence
import com.relay.app.model.Schedule
import com.relay.app.network.RelayApiClient
import com.relay.app.network.RelayApiService
import com.squareup.moshi.Moshi
import com.squareup.moshi.kotlin.reflect.KotlinJsonAdapterFactory
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import retrofit2.HttpException
import java.time.DayOfWeek
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.time.temporal.TemporalAdjusters

private val Context.scheduleCacheDataStore by preferencesDataStore(name = "schedule_cache")

/**
 * Last successfully fetched schedule window for one runner, readable offline.
 * [from]..[to] = the dates [occurrences] cover (inclusive, runner's [timezone]);
 * [fetchedAt] = RFC3339 instant of the fetch (phone clock).
 */
data class ScheduleCache(
    val timezone: String,
    val fetchedAt: String,
    val from: String,
    val to: String,
    val occurrences: List<Occurrence>,
)

/**
 * The runner's sleep/wake bookings (shared/API.md "Schedule"), with this week's Monday through
 * today+[DAYS]-1 of occurrences cached per runner hostname in DataStore (`schedule_cache`, key `schedule_<hostname>`,
 * one Moshi JSON blob - same pattern as [KnownRunnersRepository]).
 *
 * Errors are thrown as-is (HttpException / IOException), like direct [RelayApiService] calls -
 * show them with `friendlyErrorMessage`. A failed refresh leaves the cache untouched.
 */
class ScheduleRepository(
    context: Context,
    private val api: (KnownRunner) -> RelayApiService = RelayApiClient::forRunner,
) {
    private val store = context.applicationContext.scheduleCacheDataStore

    /** Cached window for [runner]; null until the first successful [refresh]. */
    fun cached(runner: KnownRunner): Flow<ScheduleCache?> =
        store.data.map { ScheduleCacheCodec.decode(it[key(runner)]) }

    /** One-shot read for non-UI callers (e.g. a reminder worker). */
    suspend fun cachedOccurrences(runner: KnownRunner): ScheduleCache? = cached(runner).first()

    /**
     * Fetches the schedule, then occurrences for [cacheWindow] (this week's Monday..today+13 in the
     * runner's zone), and caches them. Returns the live [Schedule] (bookings, plan, applied) - not cached.
     */
    suspend fun refresh(runner: KnownRunner): Schedule {
        val service = api(runner)
        val schedule = service.getSchedule()
        val zone = runCatching { ZoneId.of(schedule.timezone) }.getOrDefault(ZoneId.systemDefault())
        val (from, to) = cacheWindow(LocalDate.now(zone))
        val occurrences = service.getOccurrences(from.toString(), to.toString())
        val cache = ScheduleCache(
            timezone = schedule.timezone,
            fetchedAt = Instant.now().toString(),
            from = from.toString(),
            to = to.toString(),
            occurrences = occurrences,
        )
        store.edit { it[key(runner)] = ScheduleCacheCodec.encode(cache) }
        return schedule
    }

    suspend fun createBooking(runner: KnownRunner, input: BookingInput): Booking =
        api(runner).createBooking(input).also { refreshQuietly(runner) }

    suspend fun updateSeries(runner: KnownRunner, id: String, input: BookingInput): Booking =
        api(runner).updateSeries(id, input).also { refreshQuietly(runner) }

    suspend fun deleteSeries(runner: KnownRunner, id: String) {
        val response = api(runner).deleteSeries(id)
        response.body()?.close()
        if (!response.isSuccessful) throw HttpException(response)
        refreshQuietly(runner)
    }

    suspend fun updateOccurrence(runner: KnownRunner, id: String, date: String, day: Day): Booking =
        api(runner).updateOccurrence(id, date, day).also { refreshQuietly(runner) }

    suspend fun cancelOccurrence(runner: KnownRunner, id: String, date: String): CancelOccurrenceResult {
        val response = api(runner).cancelOccurrence(id, date)
        if (!response.isSuccessful) throw HttpException(response)
        val booking = response.body()
        val result = if (response.code() == 204 || booking == null) {
            CancelOccurrenceResult.BookingDeleted
        } else {
            CancelOccurrenceResult.Updated(booking)
        }
        refreshQuietly(runner)
        return result
    }

    /** Drops [runner]'s cache (e.g. when the runner is removed). */
    suspend fun clear(runner: KnownRunner) {
        store.edit { it.remove(key(runner)) }
    }

    /** After a successful mutation the change already happened on the runner - a failed
     * re-fetch must not make the caller think it didn't. The cache just stays stale. */
    private suspend fun refreshQuietly(runner: KnownRunner) {
        try {
            refresh(runner)
        } catch (e: Exception) {
            if (e is kotlinx.coroutines.CancellationException) throw e
        }
    }

    private fun key(runner: KnownRunner) = stringPreferencesKey("schedule_${runner.hostname}")

    companion object {
        /** Days ahead cached, counting today: the window ends at today+13. */
        const val DAYS = 14

        /** Cached dates (inclusive) for [today]: Monday of its week (so the current week draws fully
         * from cache offline) .. today+[DAYS]-1. 14..20 days depending on the weekday. */
        fun cacheWindow(today: LocalDate): Pair<LocalDate, LocalDate> =
            today.with(TemporalAdjusters.previousOrSame(DayOfWeek.MONDAY)) to today.plusDays(DAYS - 1L)
    }
}

/** JSON (de)serialization of [ScheduleCache]; pure, so it's unit-testable on the JVM. */
internal object ScheduleCacheCodec {
    private val adapter = Moshi.Builder().add(KotlinJsonAdapterFactory()).build()
        .adapter(ScheduleCache::class.java)

    fun encode(cache: ScheduleCache): String = adapter.toJson(cache)

    /** Unreadable / missing blob = no cache (e.g. a future app version changed the shape). */
    fun decode(json: String?): ScheduleCache? {
        if (json.isNullOrBlank()) return null
        return runCatching { adapter.fromJson(json) }.getOrNull()
    }
}
