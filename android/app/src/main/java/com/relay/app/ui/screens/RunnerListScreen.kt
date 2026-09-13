package com.relay.app.ui.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.ListItem
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.model.KnownRunner
import kotlinx.coroutines.launch

@Composable
fun RunnerListScreen(
    repository: KnownRunnersRepository,
    onRunnerSelected: (KnownRunner) -> Unit,
) {
    val runners by repository.runners.collectAsState(initial = emptyList())
    val scope = rememberCoroutineScope()
    var showAddDialog by remember { mutableStateOf(false) }

    Scaffold(
        topBar = { TopAppBar(title = { Text("Runners") }) },
        floatingActionButton = {
            FloatingActionButton(onClick = { showAddDialog = true }) {
                Icon(Icons.Default.Add, contentDescription = "Add runner")
            }
        },
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize()) {
            if (runners.isEmpty()) {
                Text(
                    text = "No known runners yet. Add one with the + button — hostname and key" +
                        " are printed by the runner on first launch (see ARCHITECTURE.md).",
                    modifier = Modifier.align(Alignment.Center).padding(24.dp),
                )
            } else {
                LazyColumn {
                    items(runners, key = { it.hostname }) { runner ->
                        ListItem(
                            headlineContent = { Text(runner.hostname) },
                            supportingContent = { Text("port ${runner.port}") },
                            modifier = Modifier.clickable { onRunnerSelected(runner) },
                        )
                        HorizontalDivider()
                    }
                }
            }
        }
    }

    if (showAddDialog) {
        AddRunnerDialog(
            onDismiss = { showAddDialog = false },
            onSave = { hostname, port, key ->
                scope.launch { repository.addRunner(KnownRunner(hostname = hostname, port = port, key = key)) }
                showAddDialog = false
            },
        )
    }
}

@Composable
private fun AddRunnerDialog(
    onDismiss: () -> Unit,
    onSave: (hostname: String, port: Int, key: String) -> Unit,
) {
    var hostname by remember { mutableStateOf("") }
    var port by remember { mutableStateOf(KnownRunner.DEFAULT_PORT.toString()) }
    var key by remember { mutableStateOf("") }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Add runner") },
        text = {
            Column {
                OutlinedTextField(
                    value = hostname,
                    onValueChange = { hostname = it },
                    label = { Text("Tailscale hostname") },
                    singleLine = true,
                )
                Spacer(modifier = Modifier.height(8.dp))
                OutlinedTextField(
                    value = port,
                    onValueChange = { port = it },
                    label = { Text("Port") },
                    singleLine = true,
                )
                Spacer(modifier = Modifier.height(8.dp))
                OutlinedTextField(
                    value = key,
                    onValueChange = { key = it },
                    label = { Text("Key (X-Relay-Key)") },
                    singleLine = true,
                )
            }
        },
        confirmButton = {
            TextButton(onClick = {
                val parsedPort = port.toIntOrNull() ?: KnownRunner.DEFAULT_PORT
                if (hostname.isNotBlank() && key.isNotBlank()) {
                    onSave(hostname.trim(), parsedPort, key.trim())
                }
            }) { Text("Save") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}
