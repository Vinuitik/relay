package com.relay.app.ui.screens

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.relay.app.data.ScheduleCache
import com.relay.app.data.ScheduleRepository
import com.relay.app.model.KnownRunner
import com.relay.app.model.Occurrence
import com.relay.app.model.Schedule
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import java.time.LocalDate
import java.time.ZoneId

/**
 * Schedule week grid state. Weeks fully inside the repository's cached 14 days are read from
 * [ScheduleRepository.cached]; any other week (incl. the current one, whose past days aren't
 * cached) is fetched live with `GET /v1/schedule/occurrences` - the cached part shows meanwhile.
 * [refresh] = `ScheduleRepository.refresh` (schedule + plan/applied state, rewrites the cache).
 */
class ScheduleViewModel(private val runner: KnownRunner, private val repo: ScheduleRepository) : ViewModel() {
    private val api = RelayApiClient.forRunner(runner)

    /** One live-fetched week; [generation] = which refresh it belongs to (re-fetched after each). */
    private data class LiveWeek(
        val monday: LocalDate,
        val generation: Int,
        val occurrences: List<Occurrence>? = null,
        val error: String? = null,
    )

    var cache by mutableStateOf<ScheduleCache?>(null)
        private set
    /** Live answer of the last successful [refresh] this screen made; null before / offline. */
    var schedule by mutableStateOf<Schedule?>(null)
        private set
    var refreshing by mutableStateOf(false)
        private set
    /** Last [refresh] failed - the grid shows the saved copy, the status line says so. */
    var refreshError by mutableStateOf<String?>(null)
        private set
    /** 0 = the runner's current week. Relative, so the week follows the zone once it's known. */
    var weekOffset by mutableStateOf(0)
        private set
    private var live by mutableStateOf<LiveWeek?>(null)

    private var generation = 0
    private var refreshJob: Job? = null
    private var weekJob: Job? = null

    val zone: ZoneId get() = zoneOf(schedule?.timezone ?: cache?.timezone)
    val monday: LocalDate get() = currentMonday(zone).plusWeeks(weekOffset.toLong())

    /** What the grid draws for [monday]: the live fetch once it's in, else whatever the cache has. */
    val weekOccurrences: List<Occurrence>
        get() {
            val m = monday
            live?.takeIf { it.monday == m }?.occurrences?.let { return it }
            val from = m.toString()
            val to = m.plusDays(6).toString()
            return cache?.occurrences.orEmpty().filter { it.date in from..to }
        }
    val weekLoading: Boolean get() = live?.let { it.monday == monday && it.occurrences == null && it.error == null } ?: false
    val weekError: String? get() = live?.takeIf { it.monday == monday }?.error

    init {
        viewModelScope.launch {
            repo.cached(runner).collect { cache = it; ensureWeek() }
        }
        refresh()
    }

    fun refresh(): Job {
        refreshJob?.cancel()
        refreshing = true
        return viewModelScope.launch(start = CoroutineStart.LAZY) {
            try {
                schedule = repo.refresh(runner)
                refreshError = null
                generation++
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                refreshError = friendlyErrorMessage(e, runner)
                // Re-try the uncached week too: the user asked for fresh data.
                generation++
            } finally {
                if (refreshJob === coroutineContext[Job]) {
                    refreshing = false
                    ensureWeek()
                }
            }
        }.also { refreshJob = it; it.start() }
    }

    fun previousWeek() { weekOffset--; ensureWeek() }
    fun nextWeek() { weekOffset++; ensureWeek() }
    fun thisWeek() { weekOffset = 0; ensureWeek() }

    /** Starts a live fetch for the shown week unless the cache covers it or it's already fetched. */
    private fun ensureWeek() {
        if (refreshing) return // refresh() calls back when done; avoids a fetch per cache write
        val m = monday
        if (weekCovered(m, cache)) {
            weekJob?.cancel()
            live = null
            return
        }
        val current = live
        if (current != null && current.monday == m && current.generation == generation) return
        weekJob?.cancel()
        // Keep showing the previous answer for this week while re-fetching it.
        val keep = current?.takeIf { it.monday == m }?.occurrences
        val gen = generation
        live = LiveWeek(m, gen, occurrences = keep)
        weekJob = viewModelScope.launch {
            val result = try {
                LiveWeek(m, gen, occurrences = api.getOccurrences(m.toString(), m.plusDays(6).toString()))
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                LiveWeek(m, gen, occurrences = keep, error = friendlyErrorMessage(e, runner))
            }
            if (live?.monday == m && live?.generation == gen) live = result
        }
    }
}
