package com.relay.app.ui.screens

import android.os.SystemClock
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.Crossfade
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.relay.app.model.ContainerInfo
import com.relay.app.model.ContainersStatus
import com.relay.app.model.KnownRunner
import com.relay.app.network.RelayApiClient
import com.relay.app.network.StartContainersRequest
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.components.SkeletonRows
import com.relay.app.ui.theme.RelayMotion
import com.relay.app.ui.theme.RelayStatus
import com.relay.app.ui.theme.statusColors
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Per-project docker compose control state (see shared/API.md `/containers`), scoped to the
 * Project back-stack entry so the Containers tab keeps its last status across tab switches.
 * Start/stop run in the background on the runner, so [pollWhileVisible] polls fast while an
 * operation is in flight and slowly otherwise.
 */
class ContainersViewModel(private val runner: KnownRunner, private val projectId: String) : ViewModel() {
    private val api = RelayApiClient.forRunner(runner)

    var status by mutableStateOf<ContainersStatus?>(null)
        private set
    var error by mutableStateOf<String?>(null)
        private set

    /**
     * `SystemClock.elapsedRealtime()` when the current operation was first seen, for the
     * "Starting… 0:42" line. Client-side: opening the tab mid-operation counts from then.
     */
    var opStartedAt by mutableStateOf<Long?>(null)
        private set

    private fun applyStatus(s: ContainersStatus) {
        status = s
        opStartedAt = when {
            s.operation.isEmpty() -> null
            else -> opStartedAt ?: SystemClock.elapsedRealtime()
        }
    }

    suspend fun refresh() {
        try {
            applyStatus(api.containers(projectId))
            error = null
        } catch (e: Exception) {
            error = friendlyErrorMessage(e, runner)
        }
    }

    /** Runs only while the tab is composed (caller's LaunchedEffect) - no polling in the background. */
    suspend fun pollWhileVisible() {
        while (true) {
            refresh()
            // Containers can also die on their own, so a stale "running" shouldn't sit forever.
            delay(if (status?.operation.isNullOrEmpty()) 10_000 else 2_000)
        }
    }

    fun refreshNow() = viewModelScope.launch { refresh() }

    fun up(file: String) = act { api.startContainers(projectId, StartContainersRequest(file)) }
    fun down() = act { api.stopContainers(projectId) }

    private fun act(call: suspend () -> ContainersStatus) {
        viewModelScope.launch {
            try {
                applyStatus(call())
                error = null
            } catch (e: Exception) {
                error = friendlyErrorMessage(e, runner)
            }
            // Pick up the in-flight operation promptly instead of waiting a slow poll tick.
            delay(1_500)
            refresh()
        }
    }
}

/** What a compose file card's button does right now. */
private enum class FileAction { Up, Switch, Down }

/**
 * Containers tab body (DESIGN.md "Project › Containers"). Status card on top; one card per compose
 * file (a single card when there's only one, no list header); Services; and, separated at the very
 * bottom, "Take everything down" behind a confirm dialog. Up on a file while another one is running
 * is a switch - the runner brings the old one down first, so dev and prod never run side by side.
 */
@Composable
fun ContainersContent(vm: ContainersViewModel) {
    LaunchedEffect(vm) { vm.pollWhileVisible() }

    val st = vm.status
    val error = vm.error
    if (st == null) {
        if (error != null) FullScreenError(error, onRetry = { vm.refreshNow() }) else SkeletonRows(3)
        return
    }

    val busy = st.operation.isNotEmpty()
    val running = st.containers.filter { it.state == "running" }
    // The file whose stack is up right now - containers carry the file they came from, which
    // is more truthful than activeFile when someone ran compose by hand on the runner.
    val runningFile = running.firstOrNull()?.composeFile?.ifEmpty { null }
        ?: st.activeFile.takeIf { running.isNotEmpty() && it.isNotEmpty() }
    var confirmDown by remember { mutableStateOf(false) }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        StatusCard(st, running.size, runningFile, busy, vm.opStartedAt, error)

        if (st.composeFiles.isEmpty()) {
            Text(
                text = if (st.hasDockerfile) {
                    "Add a docker-compose.yml next to this project's Dockerfile to run it from here."
                } else {
                    "Add a docker-compose.yml to this project's top-level folder to run it from here."
                },
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            return@Column
        }

        if (st.composeFiles.size > 1) Text("Compose files", style = MaterialTheme.typography.titleSmall)
        st.composeFiles.forEach { file ->
            key(file) {
                ComposeFileCard(
                    file = file,
                    action = when {
                        file == runningFile -> FileAction.Down
                        runningFile != null -> FileAction.Switch
                        else -> FileAction.Up
                    },
                    enabled = !busy,
                    onUp = { vm.up(file) },
                    onDown = { vm.down() },
                )
            }
        }

        if (st.containers.isNotEmpty()) {
            Text(
                "Services",
                style = MaterialTheme.typography.titleSmall,
                modifier = Modifier.padding(top = 12.dp),
            )
            Column {
                st.containers.forEach { c ->
                    key(c.composeFile, c.service) { ServiceRow(c) }
                }
            }
        }

        // 12 (column gap) + 12 = 24dp: the destructive action sits apart from everything else.
        OutlinedButton(
            onClick = { confirmDown = true },
            enabled = !busy,
            modifier = Modifier.fillMaxWidth().padding(top = 12.dp),
            colors = ButtonDefaults.outlinedButtonColors(contentColor = MaterialTheme.colorScheme.error),
            border = BorderStroke(
                1.dp,
                if (busy) MaterialTheme.colorScheme.onSurface.copy(alpha = 0.12f) else MaterialTheme.colorScheme.error,
            ),
        ) { Text("Take everything down") }
    }

    if (confirmDown) {
        val haptic = LocalHapticFeedback.current
        AlertDialog(
            onDismissRequest = { confirmDown = false },
            title = { Text("Take everything down?") },
            text = { Text("Stops every container this project's compose files started.") },
            confirmButton = {
                TextButton(
                    onClick = {
                        haptic.performHapticFeedback(HapticFeedbackType.LongPress)
                        confirmDown = false
                        vm.down()
                    },
                    colors = ButtonDefaults.textButtonColors(contentColor = MaterialTheme.colorScheme.error),
                ) { Text("Take down") }
            },
            dismissButton = { TextButton(onClick = { confirmDown = false }) { Text("Cancel") } },
        )
    }
}

private val ColorSpec = tween<Color>(250, easing = RelayMotion.EaseOut)

@Composable
private fun StatusCard(
    st: ContainersStatus,
    runningCount: Int,
    runningFile: String?,
    busy: Boolean,
    opStartedAt: Long?,
    requestError: String?,
) {
    val status = when {
        busy -> RelayStatus.Busy
        runningCount > 0 -> RelayStatus.Success
        else -> RelayStatus.Idle
    }
    val lampColor by animateColorAsState(MaterialTheme.statusColors[status].fg, ColorSpec, label = "lamp")
    val barAlpha by animateFloatAsState(
        if (busy) 1f else 0f,
        tween(200, easing = RelayMotion.EaseOut),
        label = "progress",
    )
    val offsetPx = with(LocalDensity.current) { 4.dp.roundToPx() }

    Card(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Lamp(lampColor, lit = status != RelayStatus.Idle)
                Spacer(Modifier.width(12.dp))
                AnimatedContent(
                    targetState = when {
                        busy -> st.operation.replaceFirstChar { it.uppercase() } + "…"
                        runningCount > 0 -> "Up · $runningCount of ${st.containers.size} running"
                        else -> "Everything is down"
                    },
                    transitionSpec = {
                        (
                            fadeIn(tween(RelayMotion.DurationShort, easing = RelayMotion.EaseOut)) +
                                slideInVertically(tween(RelayMotion.DurationShort, easing = RelayMotion.EaseOut)) { offsetPx }
                            ).togetherWith(fadeOut(tween(100, easing = RelayMotion.EaseOut)))
                    },
                    label = "statusTitle",
                ) { title -> Text(title, style = MaterialTheme.typography.titleMedium) }
                // Outside AnimatedContent so the once-a-second tick doesn't re-run the transition.
                if (busy && opStartedAt != null) {
                    Spacer(Modifier.width(8.dp))
                    Elapsed(opStartedAt)
                }
            }
            if (runningFile != null && !busy) {
                Text("from $runningFile", color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            // Fixed 4dp slot: the bar fades in/out instead of the card growing and shrinking.
            Box(Modifier.fillMaxWidth().height(4.dp)) {
                if (barAlpha > 0f) LinearProgressIndicator(Modifier.fillMaxWidth().alpha(barAlpha))
            }
            listOfNotNull(requestError, st.lastError.ifEmpty { null }, st.dockerError.ifEmpty { null })
                .forEach { Text(it, color = MaterialTheme.colorScheme.error) }
        }
    }
}

/** "0:42" since [startedAt] (`elapsedRealtime`), ticking once a second - slow vs stuck. */
@Composable
private fun Elapsed(startedAt: Long) {
    var now by remember { mutableLongStateOf(SystemClock.elapsedRealtime()) }
    LaunchedEffect(startedAt) {
        while (true) {
            now = SystemClock.elapsedRealtime()
            delay(1_000)
        }
    }
    val s = ((now - startedAt) / 1000).coerceAtLeast(0)
    Text(
        "%d:%02d".format(s / 60, s % 60),
        style = MaterialTheme.typography.titleMedium,
        color = MaterialTheme.colorScheme.onSurfaceVariant,
    )
}

@Composable
private fun ComposeFileCard(
    file: String,
    action: FileAction,
    enabled: Boolean,
    onUp: () -> Unit,
    onDown: () -> Unit,
) {
    val isUp = action == FileAction.Down
    val container by animateColorAsState(
        if (isUp) MaterialTheme.colorScheme.secondaryContainer else MaterialTheme.colorScheme.surfaceContainerHighest,
        ColorSpec,
        label = "card",
    )
    val stateColor by animateColorAsState(
        if (isUp) MaterialTheme.statusColors.success.fg else MaterialTheme.colorScheme.onSurfaceVariant,
        ColorSpec,
        label = "cardState",
    )
    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(containerColor = container),
    ) {
        Row(
            modifier = Modifier.padding(horizontal = 16.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(file, fontWeight = FontWeight.Medium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(
                    text = if (isUp) "running" else "stopped",
                    style = MaterialTheme.typography.bodySmall,
                    color = stateColor,
                )
            }
            Spacer(Modifier.width(12.dp))
            Crossfade(targetState = action, animationSpec = tween(RelayMotion.DurationShort), label = "fileAction") { a ->
                when (a) {
                    FileAction.Down -> OutlinedButton(onClick = onDown, enabled = enabled) { Text("Down") }
                    FileAction.Switch -> FilledTonalButton(onClick = onUp, enabled = enabled) { Text("Switch here") }
                    FileAction.Up -> Button(onClick = onUp, enabled = enabled) { Text("Up") }
                }
            }
        }
    }
}

@Composable
private fun ServiceRow(c: ContainerInfo) {
    val up = c.state == "running"
    val color by animateColorAsState(
        MaterialTheme.statusColors[if (up) RelayStatus.Success else RelayStatus.Idle].fg,
        ColorSpec,
        label = "service",
    )
    Row(
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp),
    ) {
        Lamp(color, lit = up)
        Spacer(Modifier.width(12.dp))
        Column(modifier = Modifier.weight(1f)) {
            Text(c.service, fontWeight = FontWeight.Medium)
            if (c.composeFile.isNotEmpty()) {
                Text(
                    c.composeFile,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
        Text(c.state, style = MaterialTheme.typography.bodySmall, color = color)
    }
}

/**
 * [com.relay.app.ui.components.StatusLamp] with an animated colour (DESIGN.md Motion "Container
 * status"): same 10dp disc + 1.5dp bezel; lit = filled, dark = bezel only.
 */
@Composable
private fun Lamp(color: Color, lit: Boolean) {
    Box(
        Modifier
            .size(10.dp)
            .border(1.5.dp, color, CircleShape)
            .then(if (lit) Modifier.background(color, CircleShape) else Modifier),
    )
}
