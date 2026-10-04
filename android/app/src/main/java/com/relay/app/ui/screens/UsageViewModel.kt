package com.relay.app.ui.screens

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.relay.app.data.AppPrefsRepository.UsageCosts
import com.relay.app.model.KnownRunner
import com.relay.app.model.UsageLimit
import com.relay.app.model.UsageReport
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch

/** Usage screen state: one `GET /v1/usage` report for the chosen window, kept across refreshes. */
class UsageViewModel(private val runner: KnownRunner) : ViewModel() {
    private val api = RelayApiClient.forRunner(runner)

    var days by mutableStateOf(14)
        private set
    /** null = never loaded. */
    var report by mutableStateOf<UsageReport?>(null)
        private set
    var refreshing by mutableStateOf(false)
        private set
    var error by mutableStateOf<String?>(null)
        private set

    private var loadJob: Job? = null

    init {
        refresh()
    }

    fun selectDays(d: Int) {
        if (d == days) return
        days = d
        refresh()
    }

    fun refresh(): Job {
        loadJob?.cancel()
        return viewModelScope.launch(start = CoroutineStart.LAZY) {
            refreshing = true
            try {
                report = api.usage(days)
                error = null
            } catch (e: Exception) {
                error = friendlyErrorMessage(e, runner)
            } finally {
                if (loadJob === coroutineContext[Job]) refreshing = false
            }
        }.also { loadJob = it; it.start() }
    }
}

/** Money outcome of one simulated idle limit, per year. */
data class LimitSavings(val savedPerYear: Double, val netPerYear: Double, val paybackMonths: Double?)

private const val HOURS_PER_YEAR = 24 * 365.0

/**
 * Sleeping saves (idle - asleep) watts for every hour the simulation says the machine would be
 * asleep; the wake device costs its own draw around the clock. Payback = device price / net
 * monthly saving, null when it never pays back.
 */
fun savings(limit: UsageLimit, c: UsageCosts): LimitSavings {
    val asleepHours = (1 - limit.awakePct / 100) * HOURS_PER_YEAR
    val saved = asleepHours * (c.idleWatts - c.sleepWatts).coerceAtLeast(0f) / 1000 * c.pricePerKwh
    val deviceRun = c.deviceWatts * HOURS_PER_YEAR / 1000 * c.pricePerKwh
    val net = saved - deviceRun
    return LimitSavings(saved, net, if (net > 0) c.deviceCost / (net / 12) else null)
}
