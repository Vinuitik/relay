package com.relay.app.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.relay.app.model.ContainersStatus
import com.relay.app.model.KnownRunner
import com.relay.app.network.RelayApiClient
import com.relay.app.network.StartContainersRequest
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.components.SkeletonRows
import com.relay.app.ui.components.StatusLamp
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

    suspend fun refresh() {
        try {
            status = api.containers(projectId)
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
                status = call()
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

/**
 * Containers tab body. One card per compose file (docker-compose.yml, docker-compose.dev.yml,
 * ...), each with its own Up / Down / Switch here button; Up on a file while another one is
 * running is a switch - the runner brings the old one down first, so dev and prod never run side
 * by side. "Take everything down" is always visible.
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

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        StatusCard(st, running.size, runningFile, busy, error)

        if (st.composeFiles.isEmpty()) {
            Text(
                text = if (st.hasDockerfile) {
                    "This project has a Dockerfile but no compose file - add a " +
                        "docker-compose.yml to its top-level folder to run it from here."
                } else {
                    "No compose file in this project's top-level folder (expected docker-compose.yml)."
                },
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            return@Column
        }

        Text("Compose files", style = MaterialTheme.typography.titleSmall)
        st.composeFiles.forEach { file ->
            ComposeFileCard(
                file = file,
                isUp = file == runningFile,
                otherIsUp = runningFile != null && file != runningFile,
                enabled = !busy,
                onUp = { vm.up(file) },
                onDown = { vm.down() },
            )
        }

        Button(
            onClick = { vm.down() },
            enabled = !busy,
            modifier = Modifier.fillMaxWidth(),
            colors = ButtonDefaults.buttonColors(
                containerColor = MaterialTheme.colorScheme.error,
                contentColor = MaterialTheme.colorScheme.onError,
            ),
        ) { Text("Take everything down") }

        if (st.containers.isNotEmpty()) {
            Text(
                "Services",
                style = MaterialTheme.typography.titleSmall,
                modifier = Modifier.padding(top = 8.dp),
            )
            st.containers.forEach { c ->
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp),
                ) {
                    StatusDot(running = c.state == "running")
                    Spacer(Modifier.width(10.dp))
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
                    Text(
                        text = c.state,
                        style = MaterialTheme.typography.bodySmall,
                        color = if (c.state == "running") MaterialTheme.statusColors.success.fg
                        else MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
        }
    }
}

@Composable
private fun StatusCard(
    st: ContainersStatus,
    runningCount: Int,
    runningFile: String?,
    busy: Boolean,
    requestError: String?,
) {
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                StatusDot(running = runningCount > 0)
                Spacer(Modifier.width(10.dp))
                Text(
                    text = when {
                        busy -> st.operation.replaceFirstChar { it.uppercase() } + "…"
                        runningCount > 0 -> "Up · $runningCount of ${st.containers.size} running"
                        else -> "Everything is down"
                    },
                    style = MaterialTheme.typography.titleMedium,
                )
            }
            if (runningFile != null && !busy) {
                Text(
                    "from $runningFile",
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            if (busy) LinearProgressIndicator(modifier = Modifier.fillMaxWidth())
            listOfNotNull(requestError, st.lastError.ifEmpty { null }, st.dockerError.ifEmpty { null })
                .forEach { Text(it, color = MaterialTheme.colorScheme.error) }
        }
    }
}

@Composable
private fun ComposeFileCard(
    file: String,
    isUp: Boolean,
    otherIsUp: Boolean,
    enabled: Boolean,
    onUp: () -> Unit,
    onDown: () -> Unit,
) {
    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = if (isUp) {
            CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.secondaryContainer)
        } else {
            CardDefaults.cardColors()
        },
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
                    color = if (isUp) MaterialTheme.statusColors.success.fg else MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Spacer(Modifier.width(12.dp))
            when {
                isUp -> OutlinedButton(onClick = onDown, enabled = enabled) { Text("Down") }
                otherIsUp -> FilledTonalButton(onClick = onUp, enabled = enabled) { Text("Switch here") }
                else -> Button(onClick = onUp, enabled = enabled) { Text("Up") }
            }
        }
    }
}

@Composable
private fun StatusDot(running: Boolean) {
    StatusLamp(if (running) RelayStatus.Success else RelayStatus.Idle, lit = running)
}
