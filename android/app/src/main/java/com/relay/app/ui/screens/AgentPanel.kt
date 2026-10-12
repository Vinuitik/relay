package com.relay.app.ui.screens

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.relay.app.model.Message
import com.relay.app.model.SubAgent
import com.relay.app.ui.components.StatusLamp
import com.relay.app.ui.theme.FullShape
import com.relay.app.ui.theme.RelayMotion
import com.relay.app.ui.theme.RelayStatus
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay

private const val AGENT_MESSAGES_POLL_MS = 3000L

/**
 * Inline sub-chat panel pinned above the transcript (hidden when there are no agents). Collapsed:
 * one summary bar. Expanded: agent tabs + the selected agent's read-only feed. State lives in
 * [ChatViewModel] (`agentsExpanded`, `selectedAgentId`).
 */
@Composable
internal fun AgentPanel(vm: ChatViewModel, maxHeight: Dp) {
    val agents = vm.agents
    if (agents.isEmpty()) return
    val expanded = vm.agentsExpanded
    val running = agents.count { it.status == "running" }
    val selected = agents.find { it.agentId == vm.selectedAgentId } ?: agents.last()
    val chevron by animateFloatAsState(
        targetValue = if (expanded) 180f else 0f,
        animationSpec = tween(200, easing = RelayMotion.EaseOut),
        label = "agentChevron",
    )

    Surface(color = MaterialTheme.colorScheme.surfaceContainerLow, modifier = Modifier.fillMaxWidth()) {
        Column {
            Surface(onClick = { vm.agentsExpanded = !expanded }, color = MaterialTheme.colorScheme.surfaceContainerLow) {
                Row(
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 10.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    StatusLamp(if (running > 0) RelayStatus.Busy else RelayStatus.Success, size = 8.dp, lit = running > 0)
                    Text(
                        "${agents.size} agent${if (agents.size == 1) "" else "s"} · " +
                            if (running > 0) "$running running" else "all done",
                        style = MaterialTheme.typography.labelLarge,
                        modifier = Modifier.weight(1f),
                    )
                    Icon(
                        Icons.Filled.KeyboardArrowDown,
                        contentDescription = if (expanded) "Collapse agents" else "Expand agents",
                        modifier = Modifier.rotate(chevron),
                    )
                }
            }
            AnimatedVisibility(
                visible = expanded,
                enter = expandVertically(tween(200, easing = RelayMotion.EaseOut)) + fadeIn(tween(200)),
                exit = shrinkVertically(tween(200, easing = RelayMotion.EaseOut)) + fadeOut(tween(120)),
            ) {
                Column {
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .horizontalScroll(rememberScrollState())
                            .padding(horizontal = 16.dp, vertical = 4.dp),
                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        agents.forEach { a ->
                            val isRunning = a.status == "running"
                            val isSel = a.agentId == selected.agentId
                            Surface(
                                onClick = { vm.selectedAgentId = a.agentId },
                                shape = FullShape,
                                color = if (isSel) MaterialTheme.colorScheme.secondaryContainer
                                else MaterialTheme.colorScheme.surfaceContainerHighest,
                                contentColor = if (isSel) MaterialTheme.colorScheme.onSecondaryContainer
                                else MaterialTheme.colorScheme.onSurfaceVariant,
                            ) {
                                Row(
                                    modifier = Modifier.padding(horizontal = 12.dp, vertical = 6.dp),
                                    verticalAlignment = Alignment.CenterVertically,
                                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                                ) {
                                    StatusLamp(if (isRunning) RelayStatus.Busy else RelayStatus.Success, size = 8.dp, lit = isRunning)
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
                    Column(Modifier.height(maxHeight)) { AgentFeed(vm, selected) }
                }
            }
            HorizontalDivider()
        }
    }
}

/**
 * Read-only feed of one subagent. Composed only while the panel is expanded, so polling
 * (`/agents/{id}/messages?after=` every 3s while running) stops on collapse and restarts from 0.
 * When it flips to done, refetches from 0 once because a tool row's status change is not
 * re-delivered via `after`. Keyed on the agent id so switching tabs resets it.
 */
@Composable
private fun AgentFeed(vm: ChatViewModel, agent: SubAgent) {
    key(agent.agentId) {
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
        val listState = rememberLazyListState()
        // Follow the tail unless the user scrolled away; re-evaluated whenever a scroll gesture ends.
        var following by remember { mutableStateOf(true) }
        LaunchedEffect(listState) {
            snapshotFlow { listState.isScrollInProgress }.collect { scrolling ->
                if (!scrolling) following = !listState.canScrollForward
            }
        }
        LaunchedEffect(messages.size, status) {
            if (following && messages.isNotEmpty()) listState.animateScrollToItem(messages.size - 1)
        }
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
            modifier = Modifier.fillMaxWidth(),
            contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp),
        ) {
            itemsIndexed(messages, key = { i, _ -> i }) { index, message ->
                val prev = messages.getOrNull(index - 1)
                val top = when {
                    index == 0 -> 0.dp
                    prev?.role == "tool" && message.role == "tool" -> 4.dp
                    else -> 8.dp
                }
                val isTask = index == 0 && message.role == "user"
                val isFinal = !running && message.role == "agent" && index == messages.lastIndex
                Column(Modifier.padding(top = top)) {
                    val label = when {
                        isTask -> "Task from main agent"
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
                        isTask -> TaskBlock(message.text)
                        message.role == "tool" -> ToolRow(message)
                        message.role == "user" -> UserBubble(message.text, optimistic = false)
                        isFinal -> Surface(
                            shape = MaterialTheme.shapes.medium,
                            color = MaterialTheme.colorScheme.primaryContainer,
                            contentColor = MaterialTheme.colorScheme.onPrimaryContainer,
                        ) {
                            Column(Modifier.padding(12.dp)) {
                                MarkdownText(text = message.text, color = MaterialTheme.colorScheme.onPrimaryContainer)
                            }
                        }
                        else -> MarkdownText(text = message.text, color = MaterialTheme.colorScheme.onSurface)
                    }
                }
            }
        }
    }
}

/** The parent's prompt, clamped to 4 lines with a Show more toggle when it overflows. */
@Composable
private fun TaskBlock(text: String) {
    var open by rememberSaveable(text) { mutableStateOf(false) }
    var overflows by remember(text) { mutableStateOf(false) }
    Surface(shape = MaterialTheme.shapes.medium, color = MaterialTheme.colorScheme.surfaceContainerHighest) {
        Column(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp)) {
            Text(
                text,
                style = MaterialTheme.typography.bodyMedium,
                maxLines = if (open) Int.MAX_VALUE else 4,
                overflow = TextOverflow.Ellipsis,
                onTextLayout = { if (!open) overflows = it.hasVisualOverflow },
            )
            if (overflows || open) {
                TextButton(onClick = { open = !open }, contentPadding = PaddingValues(0.dp)) {
                    Text(if (open) "Show less" else "Show more")
                }
            }
        }
    }
}
