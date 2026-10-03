package com.relay.app.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.relay.app.model.ContainersStatus
import com.relay.app.model.KnownRunner
import com.relay.app.network.RelayApiClient
import com.relay.app.network.StartContainersRequest
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.ui.theme.StateFinished
import com.relay.app.ui.theme.StateIdle
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Per-project docker compose control (see shared/API.md `/containers`). One card per compose file
 * (docker-compose.yml, docker-compose.dev.yml, docker-compose.prod.yml, ...), each with its own
 * Up / Down / Switch here button; Up on a file while another one is running is a switch - the
 * runner brings the old one down first, so dev and prod never run side by side. "Take everything
 * down" is always visible. Start/stop run in the background on the runner, so this screen polls
 * while an operation is in flight.
 */
@Composable
fun ContainersScreen(
    runner: KnownRunner,
    projectId: String,
    onBack: () -> Unit,
) {
    val api = remember(runner) { RelayApiClient.forRunner(runner) }
    val scope = rememberCoroutineScope()

    var status by remember { mutableStateOf<ContainersStatus?>(null) }
    var error by remember { mutableStateOf<String?>(null) }

    suspend fun refresh() {
        try {
            status = api.containers(projectId)
            error = null
        } catch (e: Exception) {
            error = friendlyErrorMessage(e, runner)
        }
    }

    // Poll fast while an up/down is in flight, slowly otherwise (containers can also die on
    // their own, so a stale "running" shouldn't sit there forever).
    LaunchedEffect(projectId) {
        while (true) {
            refresh()
            delay(if (status?.operation.isNullOrEmpty()) 10_000 else 2_000)
        }
    }

    fun act(call: suspend () -> ContainersStatus) {
        scope.launch {
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

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Containers") },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
    ) { padding ->
        val st = status
        if (st == null) {
            Column(
                modifier = Modifier.padding(padding).fillMaxSize(),
                verticalArrangement = Arrangement.Center,
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                if (error != null) Text("Error: $error", modifier = Modifier.padding(16.dp))
                else CircularProgressIndicator()
            }
            return@Scaffold
        }

        val busy = st.operation.isNotEmpty()
        val running = st.containers.filter { it.state == "running" }
        // The file whose stack is up right now - containers carry the file they came from, which
        // is more truthful than activeFile when someone ran compose by hand on the runner.
        val runningFile = running.firstOrNull()?.composeFile?.ifEmpty { null }
            ?: st.activeFile.takeIf { running.isNotEmpty() && it.isNotEmpty() }

        Column(
            modifier = Modifier
                .padding(padding)
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
                        "No compose file in this project's top-level folder."
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
                    onUp = { act { api.startContainers(projectId, StartContainersRequest(file)) } },
                    onDown = { act { api.stopContainers(projectId) } },
                )
            }

            Button(
                onClick = { act { api.stopContainers(projectId) } },
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
                        StatusDot(if (c.state == "running") StateFinished else StateIdle)
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
                            color = if (c.state == "running") StateFinished
                            else MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
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
                StatusDot(if (runningCount > 0) StateFinished else StateIdle, size = 12)
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
                    color = if (isUp) StateFinished else MaterialTheme.colorScheme.onSurfaceVariant,
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
private fun StatusDot(color: Color, size: Int = 8) {
    Box(modifier = Modifier.size(size.dp).background(color, CircleShape))
}

/**
 * Small "● 3 running" / "○ down" pill for a project's compose stack, used on the project list
 * (tap → ContainersScreen). Null [status] or no compose files renders nothing.
 */
@Composable
fun ContainersChip(status: ContainersStatus?, onClick: () -> Unit) {
    if (status == null || status.composeFiles.isEmpty()) return
    val running = status.containers.count { it.state == "running" }
    val color = if (running > 0) StateFinished else StateIdle
    OutlinedButton(
        onClick = onClick,
        shape = RoundedCornerShape(50),
        contentPadding = ButtonDefaults.TextButtonContentPadding,
    ) {
        StatusDot(color)
        Spacer(Modifier.width(6.dp))
        Text(
            text = if (running > 0) "$running up" else "down",
            style = MaterialTheme.typography.labelMedium,
        )
    }
}
