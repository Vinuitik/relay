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
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.model.KnownRunner
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import kotlinx.coroutines.launch

@Composable
fun RunnerListScreen(
    repository: KnownRunnersRepository,
    onRunnerSelected: (KnownRunner) -> Unit,
) {
    val runners by repository.runners.collectAsState(initial = emptyList())
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var showAddDialog by remember { mutableStateOf(false) }
    var showQrScan by remember { mutableStateOf(false) }
    var namingScannedRunner by remember { mutableStateOf<ScannedRunner?>(null) }
    var confirmRemoveRunner by remember { mutableStateOf<KnownRunner?>(null) }
    var confirmSuspendRunner by remember { mutableStateOf<KnownRunner?>(null) }

    /**
     * Calls `POST /v1/suspend` on the runner directly (no WorkManager indirection - the user is
     * standing right here waiting to see whether it worked). Every documented outcome gets its
     * own Toast: the runner's `409`/`503` refusals are the *normal* answers to this button, not
     * crashes, and used to be swallowed into a background worker's log where nobody saw them.
     */
    fun suspendRunner(runner: KnownRunner) {
        scope.launch {
            val message = try {
                val response = RelayApiClient.forRunner(runner).suspend()
                response.body()?.close()
                when {
                    response.isSuccessful -> "${runner.label} is going to sleep"
                    response.code() == 409 -> "${runner.label} is busy - a session is still running, nothing was stopped"
                    response.code() == 503 ->
                        "${runner.label} can't sleep on request - it wasn't started with " +
                            "RELAY_IDLE_SUSPEND_ENABLED=true"
                    else -> "${runner.label}: sleep failed (HTTP ${response.code()})"
                }
            } catch (e: Exception) {
                friendlyErrorMessage(e, runner)
            }
            Toast.makeText(context, message, Toast.LENGTH_LONG).show()
        }
    }

    if (showQrScan) {
        QrScanScreen(
            onScanned = { scanned ->
                showQrScan = false
                // Dedup BEFORE asking for a name - re-scanning a runner you already paired
                // should just refresh its key and say so, not walk you through "name this
                // runner" again as if it were new.
                val existing = runners.find { it.hostname == scanned.hostname }
                if (existing != null) {
                    scope.launch {
                        repository.updateRunner(existing.copy(key = scanned.key))
                        Toast.makeText(context, "Already added as ${existing.label} — refreshed", Toast.LENGTH_LONG).show()
                    }
                } else {
                    namingScannedRunner = scanned
                }
            },
            onManualEntry = {
                showQrScan = false
                showAddDialog = true
            },
            onClose = { showQrScan = false },
        )
        return
    }

    namingScannedRunner?.let { scanned ->
        NameRunnerDialog(
            hostname = scanned.hostname,
            onDismiss = { namingScannedRunner = null },
            onSave = { displayName ->
                val newRunner = KnownRunner(
                    hostname = scanned.hostname,
                    port = scanned.port,
                    key = scanned.key,
                    displayName = displayName,
                )
                scope.launch {
                    repository.addRunner(newRunner)
                    Toast.makeText(context, "Added $displayName", Toast.LENGTH_LONG).show()
                }
                namingScannedRunner = null
            },
        )
    }

    Scaffold(
        topBar = { TopAppBar(title = { Text("Runners") }) },
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
                            headlineContent = { Text(runner.label) },
                            supportingContent = { Text("${runner.hostname}:${runner.port}") },
                            trailingContent = {
                                Row {
                                    TextButton(onClick = { confirmSuspendRunner = runner }) { Text("Sleep") }
                                    Box {
                                        IconButton(onClick = { showMenu = true }) {
                                            Icon(Icons.Default.MoreVert, contentDescription = "More actions for ${runner.label}")
                                        }
                                        DropdownMenu(expanded = showMenu, onDismissRequest = { showMenu = false }) {
                                            DropdownMenuItem(
                                                text = { Text("Remove runner") },
                                                onClick = {
                                                    showMenu = false
                                                    confirmRemoveRunner = runner
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
            onSave = { displayName, hostname, port, key ->
                val newRunner = KnownRunner(hostname = hostname, port = port, key = key, displayName = displayName)
                scope.launch { repository.addRunner(newRunner) }
                showAddDialog = false
            },
        )
    }

    confirmRemoveRunner?.let { runner ->
        AlertDialog(
            onDismissRequest = { confirmRemoveRunner = null },
            title = { Text("Remove ${runner.label}?") },
            text = {
                Text(
                    "This only removes it from this phone's known-runners list - the runner " +
                        "itself keeps running. To add it back, scan its QR code again (or run " +
                        "its \"-qr\" flag again to reprint one; its key never changes).",
                )
            },
            confirmButton = {
                TextButton(onClick = {
                    scope.launch { repository.removeRunner(runner.hostname) }
                    confirmRemoveRunner = null
                }) { Text("Remove") }
            },
            dismissButton = { TextButton(onClick = { confirmRemoveRunner = null }) { Text("Cancel") } },
        )
    }

    confirmSuspendRunner?.let { runner ->
        AlertDialog(
            onDismissRequest = { confirmSuspendRunner = null },
            title = { Text("Put ${runner.label} to sleep now?") },
            text = {
                Text(
                    "Powers the machine off immediately instead of waiting out the idle " +
                        "timeout. Refused if a session is currently busy - nothing running gets " +
                        "killed. You'll need to wake it by hand to bring it back.",
                )
            },
            confirmButton = {
                TextButton(onClick = {
                    suspendRunner(runner)
                    confirmSuspendRunner = null
                }) { Text("Sleep") }
            },
            dismissButton = { TextButton(onClick = { confirmSuspendRunner = null }) { Text("Cancel") } },
        )
    }
}

/**
 * Shown right after a successful QR scan, before the runner is actually added - lets the user
 * give it a friendly label instead of ending up with the raw Tailscale address as its only
 * name. Starts empty; left blank, the runner's [hostname] is used as its name.
 */
@Composable
private fun NameRunnerDialog(
    hostname: String,
    onDismiss: () -> Unit,
    onSave: (displayName: String) -> Unit,
) {
    var name by remember { mutableStateOf("") }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Name this runner") },
        text = {
            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text("Display name") },
                placeholder = { Text(hostname) },
                singleLine = true,
            )
        },
        confirmButton = {
            TextButton(onClick = { onSave(name.trim().ifBlank { hostname }) }) { Text("Add runner") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun AddRunnerDialog(
    onDismiss: () -> Unit,
    onSave: (displayName: String, hostname: String, port: Int, key: String) -> Unit,
) {
    var displayName by remember { mutableStateOf("") }
    var hostname by remember { mutableStateOf("") }
    var port by remember { mutableStateOf(KnownRunner.DEFAULT_PORT.toString()) }
    var key by remember { mutableStateOf("") }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Add runner") },
        text = {
            Column {
                OutlinedTextField(
                    value = displayName,
                    onValueChange = { displayName = it },
                    label = { Text("Display name") },
                    placeholder = { Text("defaults to the hostname") },
                    singleLine = true,
                )
                Spacer(modifier = Modifier.height(8.dp))
                OutlinedTextField(
                    value = hostname,
                    onValueChange = { hostname = it },
                    label = { Text("Tailscale hostname or address") },
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
                    val trimmedHost = hostname.trim()
                    onSave(displayName.trim().ifBlank { trimmedHost }, trimmedHost, parsedPort, key.trim())
                }
            }) { Text("Save") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}
