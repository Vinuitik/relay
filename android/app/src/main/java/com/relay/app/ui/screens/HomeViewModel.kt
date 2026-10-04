package com.relay.app.ui.screens

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.relay.app.model.ContainersStatus
import com.relay.app.model.KnownRunner
import com.relay.app.model.Project
import com.relay.app.model.Session
import com.relay.app.model.UpdateStatus
import com.relay.app.network.NewProjectRequest
import com.relay.app.network.NewSessionRequest
import com.relay.app.network.RelayApiClient
import com.relay.app.network.StartContainersRequest
import com.relay.app.network.friendlyErrorMessage
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull

/**
 * Home's state, scoped to the Home back-stack entry: survives navigating into a project and back
 * (no refetch spinner), dies when Home leaves the back stack. Holds data for ONE runner at a time
 * ([runner]); switching runners via [selectRunner] clears and reloads.
 */
class HomeViewModel : ViewModel() {

    var runner by mutableStateOf<KnownRunner?>(null)
        private set
    /** null = never loaded (show skeleton); non-null = last good list, kept across refreshes. */
    var projects by mutableStateOf<List<Project>?>(null)
        private set
    /** Waiting/busy sessions on this runner, waiting first (runner sorts). */
    var needsYou by mutableStateOf<List<Session>>(emptyList())
        private set
    /** Per-project compose status, filled in per row after the list shows. Missing = unknown. */
    var containers by mutableStateOf<Map<String, ContainersStatus>>(emptyMap())
        private set
    var refreshing by mutableStateOf(false)
        private set
    var error by mutableStateOf<String?>(null)
        private set
    /** hostname → reachable (GET /v1/health within 3s). Missing = not checked yet. */
    var online by mutableStateOf<Map<String, Boolean>>(emptyMap())
        private set

    /** keeperd's updates have been failing for a while (see [UpdateStatus.stuck]); null if fine/unknown. */
    var updateStuck by mutableStateOf<UpdateStatus?>(null)
        private set

    private var loadJob: Job? = null
    private val containerJobs = mutableMapOf<String, Job>()

    /** Called each time Home (re)enters composition: same runner = background refresh, old list stays. */
    fun selectRunner(r: KnownRunner) {
        val switched = runner?.hostname != r.hostname
        runner = r
        if (switched) {
            projects = null
            needsYou = emptyList()
            updateStuck = null
            containers = emptyMap()
            error = null
        }
        refresh()
    }

    fun refresh(): Job {
        val r = runner ?: return Job().apply { complete() }
        loadJob?.cancel()
        return viewModelScope.launch(start = CoroutineStart.LAZY) {
            refreshing = true
            val api = RelayApiClient.forRunner(r)
            try {
                val list = api.listProjects()
                projects = list
                error = null
                online = online + (r.hostname to true)
                // "Needs you" is secondary - its failure doesn't fail the screen.
                needsYou = runCatching { api.listAllSessions("waiting,busy") }.getOrDefault(needsYou)
                runCatching { api.runnerInfo() }.onSuccess { info -> updateStuck = info.update?.takeIf { it.stuck } }
                list.forEach { refreshContainers(it.id) }
            } catch (e: Exception) {
                error = friendlyErrorMessage(e, r)
                online = online + (r.hostname to false)
            } finally {
                // A newer refresh() may have replaced this one; only the latest clears the bar.
                if (loadJob === coroutineContext[Job]) refreshing = false
            }
        }.also { loadJob = it; it.start() }
    }

    /** Fetches one row's compose status; never blocks the list. */
    private fun refreshContainers(projectId: String) {
        val r = runner ?: return
        containerJobs[projectId]?.cancel()
        containerJobs[projectId] = viewModelScope.launch {
            runCatching { RelayApiClient.forRunner(r).containers(projectId) }
                .onSuccess { containers = containers + (projectId to it) }
        }
    }

    /** Cheap reachability check for the switcher lamps. */
    fun checkOnline(runners: List<KnownRunner>) {
        runners.forEach { r ->
            viewModelScope.launch {
                val ok = withTimeoutOrNull(3_000) {
                    runCatching { RelayApiClient.forRunner(r).health().ok }.getOrDefault(false)
                } ?: false
                online = online + (r.hostname to ok)
            }
        }
    }

    fun createProject(name: String, onError: (String) -> Unit) {
        val r = runner ?: return
        viewModelScope.launch {
            try {
                RelayApiClient.forRunner(r).createProject(NewProjectRequest(name))
                refresh()
            } catch (e: Exception) {
                onError(friendlyErrorMessage(e, r))
            }
        }
    }

    fun newChat(projectId: String, provider: String, onCreated: (Session) -> Unit, onError: (String) -> Unit) {
        val r = runner ?: return
        viewModelScope.launch {
            try {
                onCreated(RelayApiClient.forRunner(r).createSession(projectId, NewSessionRequest(provider)))
            } catch (e: Exception) {
                onError(friendlyErrorMessage(e, r))
            }
        }
    }

    /** Row ⋮ Up/Down. Polls that row's status until the runner's operation finishes. */
    fun containersUp(projectId: String, file: String, onError: (String) -> Unit) =
        containerAction(projectId, onError) { it.startContainers(projectId, StartContainersRequest(file)) }

    fun containersDown(projectId: String, onError: (String) -> Unit) =
        containerAction(projectId, onError) { it.stopContainers(projectId) }

    private fun containerAction(
        projectId: String,
        onError: (String) -> Unit,
        call: suspend (com.relay.app.network.RelayApiService) -> ContainersStatus,
    ) {
        val r = runner ?: return
        containerJobs[projectId]?.cancel()
        containerJobs[projectId] = viewModelScope.launch {
            val api = RelayApiClient.forRunner(r)
            try {
                containers = containers + (projectId to call(api))
            } catch (e: Exception) {
                onError(friendlyErrorMessage(e, r))
                return@launch
            }
            do {
                delay(2_000)
                val st = runCatching { api.containers(projectId) }.getOrNull() ?: break
                containers = containers + (projectId to st)
            } while (st.operation.isNotEmpty())
        }
    }
}
