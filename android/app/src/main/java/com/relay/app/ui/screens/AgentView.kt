package com.relay.app.ui.screens

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import com.relay.app.model.Message
import com.relay.app.model.SubAgent
import com.relay.app.ui.components.StatusLamp
import com.relay.app.ui.theme.FullShape
import com.relay.app.ui.theme.RelayStatus
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay

private const val AGENT_MESSAGES_POLL_MS = 3000L

/** Row of one chip per background subagent: lamp (lit = running) + short description. Hidden when empty. */
@Composable
internal fun AgentChips(agents: List<SubAgent>, onOpen: (String) -> Unit) {
    if (agents.isEmpty()) return
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .horizontalScroll(rememberScrollState())
            .padding(horizontal = 16.dp, vertical = 8.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        agents.forEach { a ->
            val running = a.status == "running"
            Surface(
                onClick = { onOpen(a.agentId) },
                shape = FullShape,
                color = MaterialTheme.colorScheme.secondaryContainer,
                contentColor = MaterialTheme.colorScheme.onSecondaryContainer,
            ) {
                Row(
                    modifier = Modifier.padding(horizontal = 12.dp, vertical = 6.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    StatusLamp(if (running) RelayStatus.Busy else RelayStatus.Success, size = 8.dp, lit = running)
                    Text(
                        a.description.ifBlank { "Agent" },
                        style = MaterialTheme.typography.labelLarge,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
        }
    }
}

/**
 * Read-only view of one subagent's chat (full-screen dialog; Back closes it). Polls
 * `/agents/{id}/messages?after=` every 3s while running; when it flips to done, refetches from 0
 * once because a tool row's status change is not re-delivered via `after`.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun AgentView(vm: ChatViewModel, agent: SubAgent, onBack: () -> Unit) {
    var messages by remember { mutableStateOf<List<Message>>(emptyList()) }
    var status by remember { mutableStateOf(agent.status) }
    var error by remember { mutableStateOf<String?>(null) }
    var loaded by remember { mutableStateOf(false) }

    LaunchedEffect(agent.agentId) {
        var after = 0
        var wasRunning = false
        while (true) {
            try {
                val res = vm.agentMessages(agent.agentId, after)
                messages = if (after == 0) res.messages else messages + res.messages
                after = res.next
                status = res.status
                error = null
                loaded = true
                if (res.status == "running") {
                    wasRunning = true
                } else {
                    if (wasRunning) messages = vm.agentMessages(agent.agentId, 0).messages
                    break
                }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = vm.friendly(e)
            }
            delay(AGENT_MESSAGES_POLL_MS)
        }
    }

    val running = status == "running"
    Dialog(onDismissRequest = onBack, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Surface(modifier = Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
            Scaffold(
                topBar = {
                    TopAppBar(
                        title = {
                            Column {
                                Text(agent.description.ifBlank { "Agent" }, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                Row(
                                    verticalAlignment = Alignment.CenterVertically,
                                    horizontalArrangement = Arrangement.spacedBy(6.dp),
                                ) {
                                    StatusLamp(if (running) RelayStatus.Busy else RelayStatus.Success, size = 8.dp, lit = running)
                                    Text(
                                        if (running) "Running" else "Done",
                                        style = MaterialTheme.typography.bodySmall,
                                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    )
                                }
                            }
                        },
                        navigationIcon = {
                            IconButton(onClick = onBack) {
                                Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                            }
                        },
                    )
                },
            ) { padding ->
                val listState = rememberLazyListState()
                LaunchedEffect(messages.size) {
                    if (messages.isNotEmpty() && !listState.canScrollForward) listState.animateScrollToItem(messages.size - 1)
                }
                Column(Modifier.padding(padding).fillMaxSize()) {
                    error?.let {
                        Text(
                            it,
                            color = MaterialTheme.colorScheme.error,
                            style = MaterialTheme.typography.bodySmall,
                            modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
                        )
                    }
                    if (loaded && messages.isEmpty()) {
                        Text(
                            "No messages yet",
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            modifier = Modifier.padding(16.dp),
                        )
                    }
                    LazyColumn(
                        state = listState,
                        modifier = Modifier.fillMaxSize(),
                        contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp),
                    ) {
                        itemsIndexed(messages, key = { i, _ -> i }) { index, message ->
                            val prev = messages.getOrNull(index - 1)
                            val top = when {
                                index == 0 -> 0.dp
                                prev?.role == "tool" && message.role == "tool" -> 4.dp
                                else -> 8.dp
                            }
                            val isFinal = !running && message.role == "agent" && index == messages.lastIndex
                            Column(Modifier.padding(top = top)) {
                                val label = when {
                                    index == 0 && message.role == "user" -> "Task from parent"
                                    isFinal -> "Final report"
                                    else -> null
                                }
                                if (label != null) {
                                    Text(
                                        label,
                                        style = MaterialTheme.typography.labelMedium,
                                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                                        modifier = Modifier.padding(bottom = 4.dp),
                                    )
                                }
                                when {
                                    message.role == "tool" -> ToolRow(message)
                                    message.role == "user" -> UserBubble(message.text, optimistic = false)
                                    else -> MarkdownText(text = message.text, color = MaterialTheme.colorScheme.onSurface)
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}