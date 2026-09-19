package com.relay.app.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
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
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.relay.app.data.db.CachedMessageEntity
import com.relay.app.data.db.CachedSessionEntity
import com.relay.app.data.db.RelayDatabase
import com.relay.app.model.KnownRunner
import com.relay.app.model.Session
import com.relay.app.network.NewSessionRequest
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.ui.theme.StateBusy
import com.relay.app.ui.theme.StateError
import com.relay.app.ui.theme.StateFinished
import com.relay.app.ui.theme.StateIdle
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

private val KNOWN_PROVIDERS = listOf("claude", "codex")

@Composable
fun SessionListScreen(
    runner: KnownRunner,
    projectId: String,
    onSessionSelected: (Session) -> Unit,
    onBack: () -> Unit,
) {
    val api = remember(runner) { RelayApiClient.forRunner(runner) }
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val sessionDao = remember(context) { RelayDatabase.get(context).sessionCacheDao() }

    var sessions by remember { mutableStateOf<List<Session>>(emptyList()) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var offline by remember { mutableStateOf(false) }
    var showNewSessionDialog by remember { mutableStateOf(false) }

    // Cached entries render instantly and double as the offline/asleep-runner fallback below -
    // messages aren't needed for this list view, so they're left empty here.
    suspend fun loadFromCache() {
        val cached = sessionDao.observeSessionsForProject(runner.hostname, projectId).first()
        if (cached.isNotEmpty()) {
            sessions = cached.map {
                Session(it.sessionId, it.projectId, it.provider, it.state, it.createdAt, it.finishedAt, emptyList())
            }
            loading = false
        }
    }

    suspend fun refresh() {
        if (sessions.isEmpty()) loading = true
        error = null
        try {
            val fetched = api.listSessions(projectId)
            sessions = fetched
            offline = false
            fetched.forEach { s ->
                sessionDao.replaceSession(
                    CachedSessionEntity(runner.hostname, s.id, s.projectId, s.provider, s.state, s.createdAt, s.finishedAt),
                    s.messages.map { CachedMessageEntity(runnerHostname = runner.hostname, sessionId = s.id, role = it.role, text = it.text, at = it.at) },
                )
            }
        } catch (e: Exception) {
            if (sessions.isEmpty()) {
                error = friendlyErrorMessage(e, runner)
            } else {
                offline = true
            }
        } finally {
            loading = false
        }
    }

    LaunchedEffect(projectId) {
        loadFromCache()
        refresh()
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Sessions") },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
        floatingActionButton = {
            FloatingActionButton(onClick = { showNewSessionDialog = true }) {
                Icon(Icons.Default.Add, contentDescription = "New session")
            }
        },
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize()) {
            when {
                loading -> CircularProgressIndicator(modifier = Modifier.align(Alignment.Center))
                error != null && sessions.isEmpty() -> Text(
                    text = "Error: $error",
                    modifier = Modifier.align(Alignment.Center).padding(16.dp),
                )
                sessions.isEmpty() -> Text("No sessions yet.", modifier = Modifier.align(Alignment.Center))
                else -> LazyColumn {
                    if (offline) {
                        item {
                            Text(
                                text = "Offline — showing last known data",
                                modifier = Modifier.background(MaterialTheme.colorScheme.errorContainer).padding(8.dp),
                                color = MaterialTheme.colorScheme.onErrorContainer,
                            )
                        }
                    }
                    items(sessions, key = { it.id }) { session ->
                        ListItem(
                            headlineContent = { Text(session.provider) },
                            supportingContent = { Text(session.createdAt) },
                            trailingContent = { StateBadge(session.state) },
                            modifier = Modifier.clickable { onSessionSelected(session) },
                        )
                        HorizontalDivider()
                    }
                }
            }
        }
    }

    if (showNewSessionDialog) {
        NewSessionDialog(
            onDismiss = { showNewSessionDialog = false },
            onCreate = { provider ->
                showNewSessionDialog = false
                scope.launch {
                    try {
                        val created = api.createSession(projectId, NewSessionRequest(provider))
                        refresh()
                        onSessionSelected(created)
                    } catch (e: Exception) {
                        error = friendlyErrorMessage(e, runner)
                    }
                }
            },
        )
    }
}

@Composable
private fun StateBadge(state: String) {
    val color = when (state) {
        "busy" -> StateBusy
        "idle" -> StateIdle
        "finished" -> StateFinished
        "error" -> StateError
        else -> StateIdle
    }
    Box(
        modifier = Modifier
            .background(color = color, shape = RoundedCornerShape(50))
            .padding(horizontal = 10.dp, vertical = 4.dp),
    ) {
        Text(text = state, color = Color.White)
    }
}

@Composable
private fun NewSessionDialog(onDismiss: () -> Unit, onCreate: (String) -> Unit) {
    var provider by remember { mutableStateOf(KNOWN_PROVIDERS.first()) }
    var expanded by remember { mutableStateOf(false) }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("New session") },
        text = {
            Box {
                OutlinedButton(onClick = { expanded = true }) { Text(provider) }
                DropdownMenu(expanded = expanded, onDismissRequest = { expanded = false }) {
                    KNOWN_PROVIDERS.forEach { option ->
                        DropdownMenuItem(
                            text = { Text(option) },
                            onClick = {
                                provider = option
                                expanded = false
                            },
                        )
                    }
                }
            }
        },
        confirmButton = { TextButton(onClick = { onCreate(provider) }) { Text("Start") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}
