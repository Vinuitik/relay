package com.relay.app.ui.screens

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.relay.app.model.AgentChat
import com.relay.app.model.KnownRunner
import com.relay.app.model.Project
import com.relay.app.model.Session
import com.relay.app.network.MessageRequest
import com.relay.app.network.NewSessionRequest
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.ui.components.epochOf
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.async
import kotlinx.coroutines.launch

/**
 * Project › Chats state, scoped to the Project back-stack entry (returning from a chat keeps the
 * list and refreshes it behind a thin bar). Also resolves the project's name for the title and
 * flags [missing] when the runner no longer has this project (restored route gone stale).
 */
class ProjectViewModel(private val runner: KnownRunner, private val projectId: String) : ViewModel() {
    private val api = RelayApiClient.forRunner(runner)

    var project by mutableStateOf<Project?>(null)
        private set
    /** True once the runner answered and this project isn't in its list. */
    var missing by mutableStateOf(false)
        private set
    /** null = never loaded. Sorted waiting first, then most recent activity. */
    var sessions by mutableStateOf<List<Session>?>(null)
        private set
    var refreshing by mutableStateOf(false)
        private set
    var error by mutableStateOf<String?>(null)
        private set
    /** Claude's own chats for this folder not yet open in Relay (VS Code's, CLI's) - "On this
     * computer". Empty until loaded or if the runner can't list them (older runner, no claude). */
    var laptopChats by mutableStateOf<List<AgentChat>>(emptyList())
        private set
    /** A New chat / first message is in flight - disables the FAB and composer. */
    var creating by mutableStateOf(false)
        private set

    private var loadJob: Job? = null

    init {
        refresh()
    }

    fun refresh(): Job {
        loadJob?.cancel()
        return viewModelScope.launch(start = CoroutineStart.LAZY) {
            refreshing = true
            try {
                val projectsAsync = async { runCatching { api.listProjects() }.getOrNull() }
                // Slower (the runner spawns the agent to list), so it never holds up the list.
                launch {
                    runCatching { api.listAgentChats(projectId).chats }
                        .onSuccess { all -> laptopChats = all.filter { it.sessionId.isNullOrEmpty() } }
                }
                val list = api.listSessions(projectId)
                sessions = list.sortedWith(
                    compareByDescending<Session> { it.state == "waiting" }.thenByDescending { epochOf(it.activityAt) },
                )
                error = null
                projectsAsync.await()?.let { all ->
                    val p = all.find { it.id == projectId }
                    project = p
                    missing = p == null
                }
            } catch (e: Exception) {
                error = friendlyErrorMessage(e, runner)
            } finally {
                if (loadJob === coroutineContext[Job]) refreshing = false
            }
        }.also { loadJob = it; it.start() }
    }

    /** Back from a chat: refresh behind the thin bar (no-op before the first load). */
    fun refreshIfLoaded() {
        if (sessions != null && !refreshing) refresh()
    }

    fun newChat(provider: String, onCreated: (Session) -> Unit, onError: (String) -> Unit) {
        if (creating) return
        viewModelScope.launch {
            creating = true
            try {
                onCreated(api.createSession(projectId, NewSessionRequest(provider)))
            } catch (e: Exception) {
                onError(friendlyErrorMessage(e, runner))
            } finally {
                creating = false
            }
        }
    }

    /** Continue one of Claude's own chats (e.g. from VS Code): the runner replays its history into a
     * Relay session (a few seconds), or returns the session already showing it. */
    fun continueLaptopChat(chat: AgentChat, onCreated: (Session) -> Unit, onError: (String) -> Unit) {
        if (creating) return
        viewModelScope.launch {
            creating = true
            try {
                onCreated(api.createSession(projectId, NewSessionRequest("claude", agentSessionId = chat.agentSessionId)))
                laptopChats = laptopChats.filter { it.agentSessionId != chat.agentSessionId }
            } catch (e: Exception) {
                onError(friendlyErrorMessage(e, runner))
            } finally {
                creating = false
            }
        }
    }

    /** Empty-state composer: the first message creates the session, then sends itself. */
    fun startWithMessage(provider: String, text: String, onCreated: (Session) -> Unit, onError: (String) -> Unit) {
        if (creating) return
        viewModelScope.launch {
            creating = true
            try {
                val created = api.createSession(projectId, NewSessionRequest(provider))
                api.sendMessage(created.id, MessageRequest(text)).body()?.close()
                onCreated(created)
            } catch (e: Exception) {
                onError(friendlyErrorMessage(e, runner))
            } finally {
                creating = false
            }
        }
    }
}
