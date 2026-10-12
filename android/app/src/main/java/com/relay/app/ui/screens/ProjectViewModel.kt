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
import com.relay.app.network.HandoffRequest
import com.relay.app.network.MessageRequest
import com.relay.app.network.NewSessionRequest
import com.relay.app.network.RelayApiClient
import com.relay.app.network.errorCode
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.network.peerName
import com.relay.app.ui.components.epochOf
import com.relay.app.ui.components.relativeTime
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.delay
import kotlinx.coroutines.Job
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.launch

/**
 * Project › Chats state, scoped to the Project back-stack entry (returning from a chat keeps the
 * list and refreshes it behind a thin bar). Also resolves the project's name for the title and
 * flags [missing] when the runner no longer has this project (restored route gone stale).
 */
class ProjectViewModel(
    private val runner: KnownRunner,
    private val projectId: String,
    /** Every other known runner - candidates for "On <name>" chats. */
    private val others: List<KnownRunner> = emptyList(),
) : ViewModel() {
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
    /** Chats on linked runners that have this same project (matched by git remote). */
    var remoteChats by mutableStateOf<List<RemoteChat>>(emptyList())
        private set
    /** Set when the owner runner couldn't be reached: UI offers "Take over from last copy". */
    var takeoverOffer by mutableStateOf<RemoteChat?>(null)
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
                        .onSuccess { all ->
                            laptopChats = all.filter { it.sessionId.isNullOrEmpty() }
                            loadRemoteChats(all.map { it.agentSessionId }.toSet())
                        }
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

    /** For each linked runner holding the same project, list its chats we don't have. Best effort:
     * an unreachable or unlinked runner just contributes nothing. */
    private suspend fun loadRemoteChats(have: Set<String>) {
        val key = runCatching { api.listProjects() }.getOrNull()
            ?.find { it.id == projectId }?.remoteKey ?: return
        val linked = runCatching { api.listPeers().map { it.name } }.getOrDefault(emptyList()).toSet()
        remoteChats = coroutineScope {
            others.filter { peerName(it) in linked }.map { o ->
                async {
                    runCatching {
                        val oApi = RelayApiClient.forRunner(o)
                        val p = oApi.listProjects().find { it.remoteKey == key } ?: return@runCatching emptyList()
                        oApi.listAgentChats(p.id).chats
                            .filter { it.agentSessionId !in have }
                            .map { RemoteChat(o, it) }
                    }.getOrDefault(emptyList())
                }
            }.awaitAll().flatten()
        }
    }

    fun dismissTakeover() { takeoverOffer = null }

    /** Pull [rc] here from its runner, then adopt it like a laptop chat. */
    fun continueRemoteChat(rc: RemoteChat, onCreated: (Session) -> Unit, onError: (String) -> Unit) =
        handoff(rc, takeover = false, onCreated, onError)

    fun takeOver(rc: RemoteChat, onCreated: (Session) -> Unit, onError: (String) -> Unit) =
        handoff(rc, takeover = true, onCreated, onError)

    /** Set while a pull is retrying because the owner runner is mid-turn ("busy"). */
    var waitingFor by mutableStateOf<RemoteChat?>(null)
        private set
    private var handoffJob: Job? = null

    fun cancelWaiting() { handoffJob?.cancel() }

    private fun handoff(rc: RemoteChat, takeover: Boolean, onCreated: (Session) -> Unit, onError: (String) -> Unit) {
        if (creating) return
        handoffJob = viewModelScope.launch {
            creating = true
            takeoverOffer = null
            try {
                val req = HandoffRequest(peerName(rc.runner), rc.chat.agentSessionId, projectId)
                val deadline = System.currentTimeMillis() + WAIT_LIMIT_MS
                while (true) {
                    try {
                        if (takeover) {
                            val r = api.handoffTakeover(req)
                            // Snackbar notice (onError just shows a message): the copy may lag.
                            r.copySavedAt?.let { onError("Opened from the copy saved ${relativeTime(it)} - newer messages may be missing") }
                        } else {
                            api.handoffPull(req)
                        }
                        break
                    } catch (e: CancellationException) {
                        throw e
                    } catch (e: Exception) {
                        // "exists" = already here: just open it.
                        when (errorCode(e)) {
                            "exists" -> break
                            "busy" -> {
                                if (takeover || System.currentTimeMillis() >= deadline) {
                                    onError("${rc.runner.label} is still busy - gave up waiting. Try again later")
                                    return@launch
                                }
                                waitingFor = rc
                                delay(WAIT_POLL_MS)
                            }
                            "unreachable" -> { takeoverOffer = rc; return@launch }
                            else -> throw e
                        }
                    }
                }
                waitingFor = null
                onCreated(api.createSession(projectId, NewSessionRequest("claude", agentSessionId = rc.chat.agentSessionId)))
                remoteChats = remoteChats.filter { it.chat.agentSessionId != rc.chat.agentSessionId }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                onError(friendlyErrorMessage(e, runner))
            } finally {
                waitingFor = null
                creating = false
            }
        }
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

/** A chat living on another (linked) runner. */
data class RemoteChat(val runner: KnownRunner, val chat: AgentChat)

private const val WAIT_POLL_MS = 5_000L
private const val WAIT_LIMIT_MS = 10 * 60_000L
