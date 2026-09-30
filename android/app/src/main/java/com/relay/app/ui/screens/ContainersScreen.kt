package com.relay.app.ui.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.RadioButton
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
import androidx.compose.ui.unit.dp
import com.relay.app.model.ContainersStatus
import com.relay.app.model.KnownRunner
import com.relay.app.network.RelayApiClient
import com.relay.app.network.StartContainersRequest
import com.relay.app.network.friendlyErrorMessage
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Per-project docker compose control (see shared/API.md `/containers`). Lists the project's
 * compose files (docker-compose.yml, docker-compose.dev.yml, docker-compose.prod.yml, ...) as a
 * single choice; the primary button turns the chosen one on, and if a different file was active
 * the runner brings that one down first - so dev and prod never run side by side. Start/stop run
 * in the background on the runner, so this screen polls while an operation is in flight.
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
    var chosen by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }

    suspend fun refresh() {
        try {
            val st = api.containers(projectId)
            status = st
            // First load (or chosen file vanished): preselect the runner's active file.
            if (chosen == null || chosen !in st.composeFiles) {
                chosen = st.activeFile.ifEmpty { null }
            }
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

        Column(
            modifier = Modifier
                .padding(padding)
                .fillMaxSize()
                .verticalScroll(rememberScrollState()),
        ) {
            val busy = st.operation.isNotEmpty()
            if (busy) {
                LinearProgressIndicator(modifier = Modifier.fillMaxWidth())
                Text(
                    text = st.operation.replaceFirstChar { it.uppercase() } + "…",
                    modifier = Modifier.padding(16.dp),
                )
            }
            listOfNotNull(error, st.lastError.ifEmpty { null }, st.dockerError.ifEmpty { null })
                .forEach {
                    Text(
                        text = it,
                        color = MaterialTheme.colorScheme.error,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                    )
                }

            if (st.composeFiles.isEmpty()) {
                Text(
                    text = if (st.hasDockerfile) {
                        "This project has a Dockerfile but no compose file - add a " +
                            "docker-compose.yml to run it from here."
                    } else {
                        "No compose file in this project."
                    },
                    modifier = Modifier.padding(16.dp),
                )
                return@Column
            }

            SectionTitle("Compose file")
            st.composeFiles.forEach { file ->
                ListItem(
                    headlineContent = { Text(file) },
                    supportingContent = if (file == st.activeFile) ({ Text("active") }) else null,
                    leadingContent = { RadioButton(selected = file == chosen, onClick = null) },
                    modifier = Modifier.clickable(enabled = !busy) { chosen = file },
                )
            }

            val anyRunning = st.containers.any { it.state == "running" }
            val target = chosen
            val primaryLabel = when {
                target == null -> "Choose a file"
                target != st.activeFile && anyRunning -> "Switch to $target"
                else -> "Start $target"
            }
            Row(
                modifier = Modifier.padding(16.dp),
                horizontalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                Button(
                    enabled = !busy && target != null,
                    onClick = { act { api.startContainers(projectId, StartContainersRequest(target!!)) } },
                ) { Text(primaryLabel) }
                OutlinedButton(
                    enabled = !busy && st.containers.isNotEmpty(),
                    onClick = { act { api.stopContainers(projectId) } },
                ) { Text("Stop all") }
            }

            HorizontalDivider()
            SectionTitle("Containers")
            if (st.containers.isEmpty()) {
                Text("Nothing running.", modifier = Modifier.padding(horizontal = 16.dp))
            }
            st.containers.forEach { c ->
                ListItem(
                    headlineContent = { Text(c.service) },
                    supportingContent = { Text(c.composeFile) },
                    trailingContent = {
                        Text(
                            text = c.state,
                            color = if (c.state == "running") MaterialTheme.colorScheme.primary
                            else MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    },
                )
            }
        }
    }
}

@Composable
private fun SectionTitle(text: String) {
    Text(
        text = text,
        style = MaterialTheme.typography.titleSmall,
        modifier = Modifier.padding(start = 16.dp, top = 16.dp, bottom = 4.dp),
    )
}
