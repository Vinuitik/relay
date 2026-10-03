package com.relay.app.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Stop
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilledIconButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.relay.app.model.KnownRunner
import com.relay.app.model.Message
import com.relay.app.model.PendingPermission
import com.relay.app.model.Session
import com.relay.app.network.MessageRequest
import com.relay.app.network.PermissionRequest
import com.relay.app.network.RelayApiClient
import com.relay.app.network.SetModeRequest
import com.relay.app.network.friendlyErrorMessage
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

// Fast while the agent works so streamed text/tool steps appear near-live; slower while it
// waits on a permission decision (nothing changes until you answer).
private const val POLL_BUSY_MS = 1000L
private const val POLL_WAITING_MS = 3000L

@Composable
fun ChatScreen(
    runner: KnownRunner,
    sessionId: String,
    onBack: () -> Unit,
) {
    val api = remember(runner) { RelayApiClient.forRunner(runner) }
    val scope = rememberCoroutineScope()
    val listState = rememberLazyListState()

    var session by remember { mutableStateOf<Session?>(null) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var input by remember { mutableStateOf("") }
    var sending by remember { mutableStateOf(false) }

    // Polls while the agent is busy or waiting on you, stops once it's idle/finished.
    suspend fun pollWhileActive() {
        while (true) {
            try {
                session = api.getSession(sessionId)
                error = null
            } catch (e: Exception) {
                error = friendlyErrorMessage(e, runner)
                loading = false
                return
            }
            loading = false
            when (session?.state) {
                "busy" -> delay(POLL_BUSY_MS)
                "waiting" -> delay(POLL_WAITING_MS)
                else -> return
            }
        }
    }

    // Run an action that returns the updated session, then resume polling.
    fun act(call: suspend () -> Session) {
        scope.launch {
            try {
                session = call()
                error = null
            } catch (e: Exception) {
                error = friendlyErrorMessage(e, runner)
            }
            pollWhileActive()
        }
    }

    LaunchedEffect(sessionId) {
        pollWhileActive()
    }

    // Follow the conversation as it streams in.
    val messages = session?.messages.orEmpty()
    LaunchedEffect(messages.size, messages.lastOrNull()?.text?.length, session?.pendingPermission) {
        val count = messages.size + if (session?.pendingPermission != null) 1 else 0
        if (count > 0) listState.animateScrollToItem(count - 1)
    }

    val state = session?.state
    val working = state == "busy" || state == "waiting"

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(session?.provider ?: "Session") },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                actions = {
                    val s = session
                    if (s != null && !s.modes.isNullOrEmpty()) {
                        ModePicker(s) { modeId -> act { api.setMode(sessionId, SetModeRequest(modeId)) } }
                    }
                },
            )
        },
    ) { padding ->
        Column(modifier = Modifier.padding(padding).fillMaxSize()) {
            when {
                loading -> Box(modifier = Modifier.weight(1f).fillMaxSize()) {
                    CircularProgressIndicator(modifier = Modifier.align(Alignment.Center))
                }
                error != null && session == null -> Box(modifier = Modifier.weight(1f).fillMaxSize()) {
                    Text("Error: $error", modifier = Modifier.align(Alignment.Center).padding(16.dp))
                }
                else -> LazyColumn(
                    state = listState,
                    modifier = Modifier.weight(1f).fillMaxWidth(),
                    contentPadding = PaddingValues(12.dp),
                    verticalArrangement = Arrangement.spacedBy(6.dp),
                ) {
                    items(messages) { message ->
                        when {
                            message.role == "tool" -> ToolRow(message)
                            message.kind == "quota" || message.kind == "auth" -> ProblemCard(message)
                            else -> ChatBubble(message)
                        }
                    }
                    session?.pendingPermission?.let { pp ->
                        item {
                            PermissionCard(pp) { optionId ->
                                act { api.answerPermission(sessionId, PermissionRequest(optionId)) }
                            }
                        }
                    }
                }
            }

            if (error != null && session != null) {
                Text(
                    text = error.orEmpty(),
                    color = MaterialTheme.colorScheme.error,
                    style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.padding(horizontal = 12.dp),
                )
            }
            if (state == "busy") {
                LinearProgressIndicator(modifier = Modifier.fillMaxWidth())
            }

            Row(
                modifier = Modifier.fillMaxWidth().padding(8.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                OutlinedTextField(
                    value = input,
                    onValueChange = { input = it },
                    modifier = Modifier.weight(1f),
                    placeholder = { Text(if (working) "Agent is working…" else "Message the agent...") },
                    enabled = !sending,
                )
                if (working) {
                    // The brake for no-permission mode: stops this turn, keeps the session.
                    FilledIconButton(
                        onClick = { act { api.cancelTurn(sessionId) } },
                        colors = IconButtonDefaults.filledIconButtonColors(
                            containerColor = MaterialTheme.colorScheme.error,
                            contentColor = MaterialTheme.colorScheme.onError,
                        ),
                    ) {
                        Icon(Icons.Default.Stop, contentDescription = "Stop")
                    }
                } else {
                    IconButton(
                        enabled = input.isNotBlank() && !sending,
                        onClick = {
                            val text = input.trim()
                            input = ""
                            sending = true
                            scope.launch {
                                try {
                                    val response = api.sendMessage(sessionId, MessageRequest(text))
                                    response.body()?.close()
                                    if (!response.isSuccessful) {
                                        error = "Send failed: HTTP ${response.code()}"
                                    }
                                } catch (e: Exception) {
                                    error = friendlyErrorMessage(e, runner)
                                } finally {
                                    sending = false
                                }
                                pollWhileActive()
                            }
                        },
                    ) {
                        Icon(Icons.AutoMirrored.Filled.Send, contentDescription = "Send")
                    }
                }
            }
        }
    }
}

@Composable
private fun ModePicker(session: Session, onSelect: (String) -> Unit) {
    var open by remember { mutableStateOf(false) }
    val current = session.modes?.find { it.id == session.mode }
    Box {
        TextButton(onClick = { open = true }) { Text(current?.name ?: session.mode.orEmpty()) }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            session.modes.orEmpty().forEach { mode ->
                DropdownMenuItem(
                    text = {
                        Column {
                            Text(mode.name)
                            mode.description?.let {
                                Text(it, style = MaterialTheme.typography.bodySmall)
                            }
                        }
                    },
                    onClick = {
                        open = false
                        if (mode.id != session.mode) onSelect(mode.id)
                    },
                )
            }
        }
    }
}

/** One compact line per agent step: "✓ Write hello.txt", "⋯ npm test". */
@Composable
private fun ToolRow(message: Message) {
    val (mark, color) = when (message.status) {
        "completed" -> "✓" to MaterialTheme.colorScheme.primary
        "failed" -> "✗" to MaterialTheme.colorScheme.error
        else -> "⋯" to MaterialTheme.colorScheme.onSurfaceVariant
    }
    Row(modifier = Modifier.fillMaxWidth().padding(horizontal = 4.dp)) {
        Text(mark, color = color, modifier = Modifier.padding(end = 8.dp))
        Text(
            text = message.text.ifBlank { message.toolKind.orEmpty() },
            style = MaterialTheme.typography.bodySmall,
            fontFamily = FontFamily.Monospace,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun PermissionCard(pp: PendingPermission, onChoose: (String) -> Unit) {
    Card(
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.tertiaryContainer),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(modifier = Modifier.padding(12.dp)) {
            Text("Allow this?", style = MaterialTheme.typography.titleSmall)
            Text(
                text = pp.title,
                style = MaterialTheme.typography.bodySmall,
                fontFamily = FontFamily.Monospace,
                modifier = Modifier.padding(vertical = 8.dp),
            )
            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                pp.options.forEach { option ->
                    if (option.kind.startsWith("allow")) {
                        Button(onClick = { onChoose(option.optionId) }) { Text(option.name) }
                    } else {
                        OutlinedButton(
                            onClick = { onChoose(option.optionId) },
                            colors = ButtonDefaults.outlinedButtonColors(
                                contentColor = MaterialTheme.colorScheme.error,
                            ),
                        ) { Text(option.name) }
                    }
                }
            }
        }
    }
}

/**
 * A provider problem the runner flagged (Message.kind): quota used up, or login expired.
 * Shown as a card so it can't be mistaken for an agent reply.
 */
@Composable
private fun ProblemCard(message: Message) {
    val (title, hint) = when (message.kind) {
        "quota" -> "Quota exhausted" to "Your Claude subscription limit is used up. Sessions work " +
            "again once it resets."
        else -> "Claude login expired" to "Sign in again on the runner (claude auth login)."
    }
    Card(
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.errorContainer),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(modifier = Modifier.padding(12.dp)) {
            Text(
                title,
                style = MaterialTheme.typography.titleSmall,
                color = MaterialTheme.colorScheme.onErrorContainer,
            )
            Text(
                text = hint,
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onErrorContainer,
                modifier = Modifier.padding(top = 4.dp),
            )
            // The provider's own wording carries the reset time ("resets 5pm").
            Text(
                text = message.text,
                style = MaterialTheme.typography.bodySmall,
                fontFamily = FontFamily.Monospace,
                color = MaterialTheme.colorScheme.onErrorContainer,
                modifier = Modifier.padding(top = 8.dp),
            )
        }
    }
}

@Composable
private fun ChatBubble(message: Message) {
    val isUser = message.role == "user"
    val bubbleColor = if (isUser) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.secondaryContainer
    val textColor = if (isUser) MaterialTheme.colorScheme.onPrimary else MaterialTheme.colorScheme.onSecondaryContainer

    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = if (isUser) Arrangement.End else Arrangement.Start,
    ) {
        // Agent replies get the full width: they carry code blocks and lists.
        Box(
            modifier = Modifier
                .then(if (isUser) Modifier.widthIn(max = 300.dp) else Modifier.fillMaxWidth())
                .background(color = bubbleColor, shape = RoundedCornerShape(12.dp))
                .padding(horizontal = 12.dp, vertical = 8.dp),
        ) {
            if (isUser) {
                Text(text = message.text, color = textColor)
            } else {
                MarkdownText(text = message.text, color = textColor)
            }
        }
    }
}
