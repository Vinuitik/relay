package com.relay.app.ui.screens

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.relay.app.model.KnownRunner
import com.relay.app.model.Session
import com.relay.app.network.MessageRequest
import com.relay.app.network.PermissionRequest
import com.relay.app.network.RelayApiClient
import com.relay.app.network.SetModeRequest
import com.relay.app.network.friendlyErrorMessage
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.receiveAsFlow
import kotlinx.coroutines.launch

// Fast while the agent works so streamed text/tool steps appear near-live; slower while it
// waits on a permission decision (nothing changes until you answer).
private const val POLL_BUSY_MS = 1000L
private const val POLL_WAITING_MS = 3000L
// After a send, keep polling an idle session this many 1s ticks waiting for the runner to echo
// the message (it may not have flipped to busy yet); then give up and drop the optimistic bubble.
private const val ECHO_WAIT_TICKS = 10

/** One Snackbar: plain cause, plus Retry when there's something to retry. */
class ChatEvent(val message: String, val retry: (() -> Unit)?)

/**
 * Chat state, scoped to the Chat back-stack entry: the session (polled 1s busy / 3s waiting /
 * off otherwise), the project name for the subtitle, the composer text, and the optimistic
 * user bubble ([pendingText]) shown until the runner echoes it.
 */
class ChatViewModel(
    private val runner: KnownRunner,
    private val projectId: String,
    private val sessionId: String,
) : ViewModel() {
    private val api = RelayApiClient.forRunner(runner)

    var session by mutableStateOf<Session?>(null)
        private set
    /** Load error while there's no session to show (full-screen); otherwise errors go to [events]. */
    var loadError by mutableStateOf<String?>(null)
        private set
    var projectName by mutableStateOf<String?>(null)
        private set
    var input by mutableStateOf("")
    var sending by mutableStateOf(false)
        private set
    /** Sent but not yet in [session]'s messages - rendered as a 60%-alpha user bubble. */
    var pendingText by mutableStateOf<String?>(null)
        private set
    private var pendingBase = 0

    private val _events = Channel<ChatEvent>(Channel.BUFFERED)
    val events: Flow<ChatEvent> = _events.receiveAsFlow()

    private var pollJob: Job? = null

    init {
        startPolling()
        viewModelScope.launch {
            projectName = runCatching { api.listProjects() }.getOrNull()?.find { it.id == projectId }?.name
        }
    }

    fun retryLoad() {
        loadError = null
        startPolling()
    }

    /** Polls while the agent is busy/waiting (or a sent message hasn't echoed), then stops. */
    private fun startPolling() {
        pollJob?.cancel()
        pollJob = viewModelScope.launch {
            var echoTicks = 0
            while (true) {
                try {
                    apply(api.getSession(sessionId))
                    loadError = null
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    val msg = friendlyErrorMessage(e, runner)
                    if (session == null) loadError = msg else _events.trySend(ChatEvent(msg, ::startPolling))
                    return@launch
                }
                when {
                    session?.state == "busy" -> delay(POLL_BUSY_MS)
                    session?.state == "waiting" -> delay(POLL_WAITING_MS)
                    pendingText != null && echoTicks++ < ECHO_WAIT_TICKS -> delay(POLL_BUSY_MS)
                    else -> {
                        pendingText = null
                        return@launch
                    }
                }
            }
        }
    }

    private fun apply(s: Session) {
        session = s
        if (pendingText != null && s.messages.drop(pendingBase).any { it.role == "user" }) pendingText = null
    }

    /** Run an action that returns the updated session, then resume polling. */
    private fun act(call: suspend () -> Session) {
        viewModelScope.launch {
            try {
                apply(call())
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                _events.trySend(ChatEvent(friendlyErrorMessage(e, runner), null))
            }
            startPolling()
        }
    }

    fun setMode(modeId: String) = act { api.setMode(sessionId, SetModeRequest(modeId)) }
    fun answerPermission(optionId: String) = act { api.answerPermission(sessionId, PermissionRequest(optionId)) }
    fun cancelTurn() = act { api.cancelTurn(sessionId) }

    /** Optimistic: bubble + cleared input now; on failure the text comes back + Snackbar Retry. */
    fun send() {
        val text = input.trim()
        if (text.isEmpty() || sending) return
        input = ""
        sending = true
        pollJob?.cancel() // an in-flight idle poll would otherwise drop the bubble before the echo
        pendingBase = session?.messages?.size ?: 0
        pendingText = text
        viewModelScope.launch {
            val failure = try {
                val response = api.sendMessage(sessionId, MessageRequest(text))
                response.body()?.close()
                if (response.isSuccessful) null else "The runner refused the message (HTTP ${response.code()})."
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                friendlyErrorMessage(e, runner)
            }
            sending = false
            if (failure != null) {
                pendingText = null
                if (input.isBlank()) input = text
                _events.trySend(ChatEvent(failure, ::send))
            }
            startPolling()
        }
    }
}
