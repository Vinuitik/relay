package com.relay.app.ui.screens

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.relay.app.model.KnownRunner
import com.relay.app.model.TokensReport
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch

/** Tokens screen state: one `GET /v1/tokens` report for the chosen window, kept across refreshes. */
class TokensViewModel(private val runner: KnownRunner) : ViewModel() {
    private val api = RelayApiClient.forRunner(runner)

    var days by mutableStateOf(7)
        private set
    /** null = never loaded. */
    var report by mutableStateOf<TokensReport?>(null)
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
                report = api.tokens(days)
                error = null
            } catch (e: Exception) {
                error = friendlyErrorMessage(e, runner)
            } finally {
                if (loadJob === coroutineContext[Job]) refreshing = false
            }
        }.also { loadJob = it; it.start() }
    }
}
