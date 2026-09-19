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
import androidx.compose.material3.MaterialTheme
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
import com.relay.app.data.FriendlyNameGenerator
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.data.WakeViaMatcher
import com.relay.app.model.KnownRunner
import com.relay.app.network.RelayApiClient
import com.relay.app.widget.ContainersAllWorker
import com.relay.app.widget.SuspendRunnerWorker
import com.relay.app.widget.WakeRunnerWorker
import kotlinx.coroutines.launch

@Composable
fun RunnerListScreen(
    repository: KnownRunnersRepository,
    onRunnerSelected: (KnownRunner) -> Unit,
    onDashboard: () -> Unit,
    // Started via POST /v1/auth/login (see shared/API.md) - a one-time, per-machine admin
    // action for headless OAuth login, not a coding session, hence its own callback rather than
    // going through onRunnerSelected -> ProjectListScreen -> ... -> ChatScreen's normal path.
    onAuthLoginStarted: (KnownRunner, sessionId: String) -> Unit,
) {
    val runners by repository.runners.collectAsState(initial = emptyList())
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var showAddDialog by remember { mutableStateOf(false) }
    var showQrScan by remember { mutableStateOf(false) }
    var namingScannedRunner by remember { mutableStateOf<ScannedRunner?>(null) }
    var editingRunner by remember { mutableStateOf<KnownRunner?>(null) }
    var confirmRemoveRunner by remember { mutableStateOf<KnownRunner?>(null) }
    var confirmSuspendRunner by remember { mutableStateOf<KnownRunner?>(null) }

    fun addScannedRunner(scanned: ScannedRunner, displayName: String) {
        val newRunner = KnownRunner(
            hostname = scanned.hostname,
            port = scanned.port,
            key = scanned.key,
            displayName = displayName,
            wakeMac = scanned.mac,
        )
        scope.launch {
            repository.addRunner(newRunner)
            val matchedHostname = WakeViaMatcher.autoMatch(repository, newRunner)
            val message = when {
                matchedHostname != null -> "Added $displayName — auto-matched wake-via $matchedHostname (same LAN)"
                else -> "Added $displayName"
            }
            Toast.makeText(context, message, Toast.LENGTH_LONG).show()
        }
    }

    if (showQrScan) {
        QrScanScreen(
            onScanned = { scanned ->
                showQrScan = false
                namingScannedRunner = scanned
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
            onDismiss = { namingScannedRunner = null },
            onSave = { displayName ->
                addScannedRunner(scanned, displayName)
                namingScannedRunner = null
            },
        )
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
                            headlineContent = { Text(runner.label) },
                            supportingContent = { Text("${runner.hostname}:${runner.port}") },
                            trailingContent = {
                                Row {
                                    TextButton(onClick = {
                                        if (runner.wakeMac.isNullOrBlank() || runner.wakeViaRunnerId.isNullOrBlank()) {
                                            Toast.makeText(
                                                context,
                                                "Wake not configured for ${runner.label} — set it from the ⋮ menu (\"Wake settings\")",
                                                Toast.LENGTH_LONG,
                                            ).show()
                                        } else {
                                            val request = OneTimeWorkRequestBuilder<WakeRunnerWorker>()
                                                .setInputData(workDataOf(WakeRunnerWorker.KEY_TARGET_HOSTNAME to runner.hostname))
                                                .build()
                                            WorkManager.getInstance(context).enqueue(request)
                                        }
                                    }) { Text("Wake") }
                                    // Symmetric with Wake - both primary, visible actions rather
                                    // than burying "put it to sleep" one tap deeper than "wake it
                                    // up." Reuses the same confirm dialog / worker as before.
                                    TextButton(onClick = { confirmSuspendRunner = runner }) { Text("Sleep") }
                                    Box {
                                        IconButton(onClick = { showMenu = true }) {
                                            Icon(Icons.Default.MoreVert, contentDescription = "More actions for ${runner.label}")
                                        }
                                        DropdownMenu(expanded = showMenu, onDismissRequest = { showMenu = false }) {
                                            DropdownMenuItem(
                                                text = { Text("Start all containers") },
                                                onClick = {
                                                    showMenu = false
                                                    enqueueContainersAll(context, runner.hostname, start = true)
                                                    Toast.makeText(context, "Starting all containers on ${runner.label}…", Toast.LENGTH_SHORT).show()
                                                },
                                            )
                                            DropdownMenuItem(
                                                text = { Text("Stop all containers") },
                                                onClick = {
                                                    showMenu = false
                                                    enqueueContainersAll(context, runner.hostname, start = false)
                                                    Toast.makeText(context, "Stopping all containers on ${runner.label}…", Toast.LENGTH_SHORT).show()
                                                },
                                            )
                                            // Only needed for waking a machine that's fully off
                                            // (S5) via a same-LAN peer - see ARCHITECTURE.md
                                            // "Relay device". Tucked in the overflow, not a
                                            // top-level button, since most pairings never need
                                            // to touch it (WakeViaMatcher auto-fills it on
                                            // same-LAN pairing already).
                                            DropdownMenuItem(
                                                text = { Text("Wake settings") },
                                                onClick = {
                                                    showMenu = false
                                                    editingRunner = runner
                                                },
                                            )
                                            DropdownMenuItem(
                                                text = { Text("Authenticate agent") },
                                                onClick = {
                                                    showMenu = false
                                                    scope.launch {
                                                        try {
                                                            val api = RelayApiClient.forRunner(runner)
                                                            val session = api.startAuthLogin()
                                                            onAuthLoginStarted(runner, session.id)
                                                        } catch (e: Exception) {
                                                            Toast.makeText(
                                                                context,
                                                                e.message ?: "Failed to start auth login",
                                                                Toast.LENGTH_LONG,
                                                            ).show()
                                                        }
                                                    }
                                                },
                                            )
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
        WakeSettingsDialog(
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
                        "killed. You'll need Wake-on-LAN to bring it back.",
                )
            },
            confirmButton = {
                TextButton(onClick = {
                    val request = OneTimeWorkRequestBuilder<SuspendRunnerWorker>()
                        .setInputData(workDataOf(SuspendRunnerWorker.KEY_TARGET_HOSTNAME to runner.hostname))
                        .build()
                    WorkManager.getInstance(context).enqueue(request)
                    Toast.makeText(context, "Suspending ${runner.hostname}…", Toast.LENGTH_SHORT).show()
                    confirmSuspendRunner = null
                }) { Text("Sleep") }
            },
            dismissButton = { TextButton(onClick = { confirmSuspendRunner = null }) { Text("Cancel") } },
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
 * Edit affordance for the two Wake-on-LAN fields on a known runner (see
 * [com.relay.app.model.KnownRunner]). Only matters for waking a machine that's fully powered
 * off (S5) - a WoL broadcast can't cross a router, so it has to come from a second runner on the
 * *same physical LAN* as the sleeping one (ARCHITECTURE.md "Relay device"), which is what
 * "wake via" identifies. `WakeViaMatcher` already auto-fills both fields at pairing time when
 * exactly one other known runner shares a subnet - this dialog is only needed when that
 * auto-match couldn't happen (different LANs, or a runner was unreachable at pairing time).
 * `wakeViaRunnerId` is entered as plain text (another known runner's hostname) rather than a
 * picker — keeps this a "skeleton small edit" per scope, not a full relationship UI.
 */
@Composable
private fun WakeSettingsDialog(
    runner: KnownRunner,
    onDismiss: () -> Unit,
    onSave: (wakeMac: String, wakeViaRunnerId: String) -> Unit,
) {
    var wakeMac by remember { mutableStateOf(runner.wakeMac.orEmpty()) }
    var wakeViaRunnerId by remember { mutableStateOf(runner.wakeViaRunnerId.orEmpty()) }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Wake settings: ${runner.label}") },
        text = {
            Column {
                Text(
                    "Only needed to wake this machine after it's fully powered off. Leave blank " +
                        "if you never power this machine fully down, or if it was already " +
                        "auto-filled when you paired it.",
                    style = MaterialTheme.typography.bodySmall,
                )
                Spacer(modifier = Modifier.height(8.dp))
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
                    label = { Text("Wake via runner (hostname, same LAN)") },
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

/**
 * Shown right after a successful QR scan, before the runner is actually added - lets the user
 * give it a friendly label instead of ending up with the raw Tailscale address as its only
 * name. Prefilled with a generated "Adjective Noun" default (see [FriendlyNameGenerator]) that
 * the user can accept as-is or overwrite.
 */
@Composable
private fun NameRunnerDialog(
    onDismiss: () -> Unit,
    onSave: (displayName: String) -> Unit,
) {
    var name by remember { mutableStateOf(FriendlyNameGenerator.generate()) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Name this runner") },
        text = {
            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text("Display name") },
                singleLine = true,
            )
        },
        confirmButton = {
            TextButton(onClick = {
                onSave(name.trim().ifBlank { FriendlyNameGenerator.generate() })
            }) { Text("Add runner") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun AddRunnerDialog(
    onDismiss: () -> Unit,
    onSave: (displayName: String, hostname: String, port: Int, key: String) -> Unit,
) {
    var displayName by remember { mutableStateOf(FriendlyNameGenerator.generate()) }
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
                    val name = displayName.trim().ifBlank { FriendlyNameGenerator.generate() }
                    onSave(name, hostname.trim(), parsedPort, key.trim())
                }
            }) { Text("Save") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}
