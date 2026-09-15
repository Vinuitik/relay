package com.relay.app.ui.screens

import android.widget.Toast
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.DateRange
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
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
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.workDataOf
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.data.WakeViaMatcher
import com.relay.app.model.KnownRunner
import com.relay.app.widget.ContainersAllWorker
import com.relay.app.widget.WakeRunnerWorker
import kotlinx.coroutines.launch

@Composable
fun RunnerListScreen(
    repository: KnownRunnersRepository,
    onRunnerSelected: (KnownRunner) -> Unit,
    onDashboard: () -> Unit,
) {
    val runners by repository.runners.collectAsState(initial = emptyList())
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var showAddDialog by remember { mutableStateOf(false) }
    var showQrScan by remember { mutableStateOf(false) }
    var editingRunner by remember { mutableStateOf<KnownRunner?>(null) }

    if (showQrScan) {
        QrScanScreen(
            onScanned = { scanned ->
                val newRunner = KnownRunner(
                    hostname = scanned.hostname,
                    port = scanned.port,
                    key = scanned.key,
                    wakeMac = scanned.mac,
                )
                scope.launch {
                    repository.addRunner(newRunner)
                    val matchedHostname = WakeViaMatcher.autoMatch(repository, newRunner)
                    val message = when {
                        matchedHostname != null ->
                            "Added runner ${scanned.hostname} — auto-matched wake-via $matchedHostname (same LAN)"
                        scanned.mac != null ->
                            "Added runner ${scanned.hostname} (wake MAC captured — set \"wake via\" in Edit once you have a second runner)"
                        else -> "Added runner ${scanned.hostname}"
                    }
                    Toast.makeText(context, message, Toast.LENGTH_LONG).show()
                }
                showQrScan = false
            },
            onManualEntry = {
                showQrScan = false
                showAddDialog = true
            },
            onClose = { showQrScan = false },
        )
        return
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Runners") },
                actions = {
                    IconButton(onClick = onDashboard) {
                        Icon(Icons.Default.DateRange, contentDescription = "Dashboard")
                    }
                },
            )
        },
        floatingActionButton = {
            FloatingActionButton(onClick = { showQrScan = true }) {
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
                        var showMenu by remember { mutableStateOf(false) }
                        ListItem(
                            headlineContent = { Text(runner.hostname) },
                            supportingContent = { Text("port ${runner.port}") },
                            trailingContent = {
                                Row {
                                    TextButton(onClick = {
                                        if (runner.wakeMac.isNullOrBlank() || runner.wakeViaRunnerId.isNullOrBlank()) {
                                            Toast.makeText(
                                                context,
                                                "Wake not configured for ${runner.hostname} — tap Edit to set MAC + via-runner",
                                                Toast.LENGTH_LONG,
                                            ).show()
                                        } else {
                                            val request = OneTimeWorkRequestBuilder<WakeRunnerWorker>()
                                                .setInputData(workDataOf(WakeRunnerWorker.KEY_TARGET_HOSTNAME to runner.hostname))
                                                .build()
                                            WorkManager.getInstance(context).enqueue(request)
                                        }
                                    }) { Text("Wake") }
                                    TextButton(onClick = { editingRunner = runner }) { Text("Edit") }
                                    Box {
                                        IconButton(onClick = { showMenu = true }) {
                                            Icon(Icons.Default.MoreVert, contentDescription = "More actions for ${runner.hostname}")
                                        }
                                        DropdownMenu(expanded = showMenu, onDismissRequest = { showMenu = false }) {
                                            DropdownMenuItem(
                                                text = { Text("Start all containers") },
                                                onClick = {
                                                    showMenu = false
                                                    enqueueContainersAll(context, runner.hostname, start = true)
                                                    Toast.makeText(context, "Starting all containers on ${runner.hostname}…", Toast.LENGTH_SHORT).show()
                                                },
                                            )
                                            DropdownMenuItem(
                                                text = { Text("Stop all containers") },
                                                onClick = {
                                                    showMenu = false
                                                    enqueueContainersAll(context, runner.hostname, start = false)
                                                    Toast.makeText(context, "Stopping all containers on ${runner.hostname}…", Toast.LENGTH_SHORT).show()
                                                },
                                            )
                                        }
                                    }
                                }
                            },
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
                val newRunner = KnownRunner(hostname = hostname, port = port, key = key)
                scope.launch {
                    repository.addRunner(newRunner)
                    val matchedHostname = WakeViaMatcher.autoMatch(repository, newRunner)
                    if (matchedHostname != null) {
                        Toast.makeText(context, "Auto-matched wake-via $matchedHostname (same LAN)", Toast.LENGTH_LONG).show()
                    }
                }
                showAddDialog = false
            },
        )
    }

    editingRunner?.let { runner ->
        EditWakeConfigDialog(
            runner = runner,
            onDismiss = { editingRunner = null },
            onSave = { wakeMac, wakeViaRunnerId ->
                scope.launch {
                    repository.updateRunner(
                        runner.copy(
                            wakeMac = wakeMac.ifBlank { null },
                            wakeViaRunnerId = wakeViaRunnerId.ifBlank { null },
                        ),
                    )
                }
                editingRunner = null
            },
        )
    }
}

private fun enqueueContainersAll(context: android.content.Context, hostname: String, start: Boolean) {
    val request = OneTimeWorkRequestBuilder<ContainersAllWorker>()
        .setInputData(
            workDataOf(
                ContainersAllWorker.KEY_TARGET_HOSTNAME to hostname,
                ContainersAllWorker.KEY_START to start,
            ),
        )
        .build()
    WorkManager.getInstance(context).enqueue(request)
}

/**
 * Small edit affordance for the two Wake-on-LAN fields on a known runner (see
 * [com.relay.app.model.KnownRunner]). `wakeViaRunnerId` is entered as plain text (another known
 * runner's hostname) rather than a picker — keeps this a "skeleton small edit" per scope, not a
 * full relationship UI.
 */
@Composable
private fun EditWakeConfigDialog(
    runner: KnownRunner,
    onDismiss: () -> Unit,
    onSave: (wakeMac: String, wakeViaRunnerId: String) -> Unit,
) {
    var wakeMac by remember { mutableStateOf(runner.wakeMac.orEmpty()) }
    var wakeViaRunnerId by remember { mutableStateOf(runner.wakeViaRunnerId.orEmpty()) }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Wake config: ${runner.hostname}") },
        text = {
            Column {
                OutlinedTextField(
                    value = wakeMac,
                    onValueChange = { wakeMac = it },
                    label = { Text("MAC address to wake") },
                    singleLine = true,
                )
                Spacer(modifier = Modifier.height(8.dp))
                OutlinedTextField(
                    value = wakeViaRunnerId,
                    onValueChange = { wakeViaRunnerId = it },
                    label = { Text("Wake via runner (hostname)") },
                    singleLine = true,
                )
            }
        },
        confirmButton = {
            TextButton(onClick = { onSave(wakeMac.trim(), wakeViaRunnerId.trim()) }) { Text("Save") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
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
