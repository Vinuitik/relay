package com.relay.app.ui.screens

import android.widget.Toast
import androidx.compose.animation.Crossfade
import androidx.compose.animation.core.tween
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
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.QrCodeScanner
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
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
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.model.KnownRunner
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.ui.components.ClaudeSignInSheet
import com.relay.app.ui.components.EmptyState
import com.relay.app.ui.theme.codeSmall
import kotlinx.coroutines.launch

@Composable
fun RunnerListScreen(
    repository: KnownRunnersRepository,
    onRunnerSelected: (KnownRunner) -> Unit,
    /** Null when this is the root screen (no runners yet) - no back arrow then. */
    onBack: (() -> Unit)?,
) {
    val runners by repository.runners.collectAsState(initial = emptyList())
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var showAddDialog by remember { mutableStateOf(false) }
    var showQrScan by remember { mutableStateOf(false) }
    var namingScannedRunner by remember { mutableStateOf<ScannedRunner?>(null) }
    var confirmRemoveRunner by remember { mutableStateOf<KnownRunner?>(null) }
    var confirmSuspendRunner by remember { mutableStateOf<KnownRunner?>(null) }
    var renamingRunner by remember { mutableStateOf<KnownRunner?>(null) }
    var signingInRunner by remember { mutableStateOf<KnownRunner?>(null) }

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

    // QR scan: fade only, 200ms (DESIGN.md Motion) - it replaces the whole screen while open.
    Crossfade(targetState = showQrScan, animationSpec = tween(200), label = "qrScan") { scanning ->
    if (scanning) {
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
    } else {
        RunnerList(
            runners = runners,
            onBack = onBack,
            onPair = { showQrScan = true },
            onRunnerSelected = onRunnerSelected,
            onRename = { renamingRunner = it },
            onSleep = { confirmSuspendRunner = it },
            onSignIn = { signingInRunner = it },
            onRemove = { confirmRemoveRunner = it },
        )
    }
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

    renamingRunner?.let { runner ->
        NameRunnerDialog(
            hostname = runner.hostname,
            initial = runner.displayName.orEmpty(),
            title = "Rename runner",
            confirmLabel = "Save",
            onDismiss = { renamingRunner = null },
            onSave = { name ->
                scope.launch { repository.updateRunner(runner.copy(displayName = name)) }
                renamingRunner = null
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
                TextButton(
                    onClick = {
                        scope.launch { repository.removeRunner(runner.hostname) }
                        confirmRemoveRunner = null
                    },
                    colors = ButtonDefaults.textButtonColors(contentColor = MaterialTheme.colorScheme.error),
                ) { Text("Remove") }
            },
            dismissButton = { TextButton(onClick = { confirmRemoveRunner = null }) { Text("Cancel") } },
        )
    }

    // Success state stays up (with Done) - there's no chat here to go back to.
    signingInRunner?.let { runner ->
        ClaudeSignInSheet(runner = runner, onDismiss = { signingInRunner = null }, onSignedIn = {})
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
 * Manage runners body. Row tap = make it the current runner (Home). Rename / Sleep / Sign in to
 * Claude / Remove live in the row ⋮; Sleep and Remove are confirmed by the caller's dialogs.
 */
@Composable
private fun RunnerList(
    runners: List<KnownRunner>,
    onBack: (() -> Unit)?,
    onPair: () -> Unit,
    onRunnerSelected: (KnownRunner) -> Unit,
    onRename: (KnownRunner) -> Unit,
    onSleep: (KnownRunner) -> Unit,
    onSignIn: (KnownRunner) -> Unit,
    onRemove: (KnownRunner) -> Unit,
) {
    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Runners") },
                navigationIcon = {
                    if (onBack != null) {
                        IconButton(onClick = onBack) {
                            Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                        }
                    }
                },
            )
        },
        floatingActionButton = {
            ExtendedFloatingActionButton(
                onClick = onPair,
                icon = { Icon(Icons.Default.QrCodeScanner, contentDescription = null) },
                text = { Text("Pair runner") },
            )
        },
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize()) {
            if (runners.isEmpty()) {
                EmptyState(
                    text = "Pair a runner by scanning the QR code it prints on first launch.",
                    actionLabel = "Pair runner",
                    onAction = onPair,
                )
            } else {
                LazyColumn {
                    items(runners, key = { it.hostname }) { runner ->
                        var showMenu by remember { mutableStateOf(false) }
                        ListItem(
                            headlineContent = { Text(runner.label) },
                            supportingContent = {
                                Text("${runner.hostname}:${runner.port}", style = MaterialTheme.typography.codeSmall)
                            },
                            trailingContent = {
                                Box {
                                    IconButton(onClick = { showMenu = true }) {
                                        Icon(Icons.Default.MoreVert, contentDescription = "More actions for ${runner.label}")
                                    }
                                    DropdownMenu(expanded = showMenu, onDismissRequest = { showMenu = false }) {
                                        DropdownMenuItem(
                                            text = { Text("Rename") },
                                            onClick = { showMenu = false; onRename(runner) },
                                        )
                                        DropdownMenuItem(
                                            text = { Text("Sleep") },
                                            onClick = { showMenu = false; onSleep(runner) },
                                        )
                                        DropdownMenuItem(
                                            text = { Text("Sign in to Claude") },
                                            onClick = { showMenu = false; onSignIn(runner) },
                                        )
                                        HorizontalDivider()
                                        DropdownMenuItem(
                                            text = { Text("Remove", color = MaterialTheme.colorScheme.error) },
                                            onClick = { showMenu = false; onRemove(runner) },
                                        )
                                    }
                                }
                            },
                            modifier = Modifier.clickable { onRunnerSelected(runner) },
                        )
                    }
                }
            }
        }
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
    initial: String = "",
    title: String = "Name this runner",
    confirmLabel: String = "Add runner",
) {
    var name by remember { mutableStateOf(initial) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
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
            TextButton(onClick = { onSave(name.trim().ifBlank { hostname }) }) { Text(confirmLabel) }
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
