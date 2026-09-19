package com.relay.app.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
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
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.relay.app.data.db.CachedMessageEntity
import com.relay.app.data.db.CachedSessionEntity
import com.relay.app.data.db.RelayDatabase
import com.relay.app.model.KnownRunner
import com.relay.app.model.Message
import com.relay.app.model.Session
import com.relay.app.network.MessageRequest
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

private const val POLL_INTERVAL_MS = 3000L

@Composable
fun ChatScreen(
    runner: KnownRunner,
    sessionId: String,
    onBack: () -> Unit,
) {
    val api = remember(runner) { RelayApiClient.forRunner(runner) }
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val sessionDao = remember(context) { RelayDatabase.get(context).sessionCacheDao() }

    var session by remember { mutableStateOf<Session?>(null) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var offline by remember { mutableStateOf(false) }
    var input by remember { mutableStateOf("") }
    var sending by remember { mutableStateOf(false) }

    // Show whatever's cached immediately - lets the transcript render instantly, and stays as
    // the fallback view if the live poll below fails (runner asleep/unreachable).
    suspend fun loadFromCache() {
        val cached = sessionDao.observeSession(runner.hostname, sessionId).first() ?: return
        val messages = sessionDao.observeMessages(runner.hostname, sessionId).first()
        session = Session(
            id = cached.sessionId,
            projectId = cached.projectId,
            provider = cached.provider,
            state = cached.state,
            createdAt = cached.createdAt,
            finishedAt = cached.finishedAt,
            messages = messages.map { Message(role = it.role, text = it.text, at = it.at) },
        )
        loading = false
    }

    suspend fun pollUntilIdle() {
        while (true) {
            try {
                val fetched = api.getSession(sessionId)
                session = fetched
                error = null
                offline = false
                sessionDao.replaceSession(
                    CachedSessionEntity(
                        runnerHostname = runner.hostname,
                        sessionId = fetched.id,
                        projectId = fetched.projectId,
                        provider = fetched.provider,
                        state = fetched.state,
                        createdAt = fetched.createdAt,
                        finishedAt = fetched.finishedAt,
                    ),
                    fetched.messages.map {
                        CachedMessageEntity(runnerHostname = runner.hostname, sessionId = fetched.id, role = it.role, text = it.text, at = it.at)
                    },
                )
            } catch (e: Exception) {
                if (session == null) {
                    error = friendlyErrorMessage(e, runner)
                } else {
                    // Already have cached/last-known data on screen - keep showing it rather
                    // than replacing the transcript with an error.
                    offline = true
                }
                return
            }
            loading = false
            if (session?.state != "busy") return
            delay(POLL_INTERVAL_MS)
        }
    }

    LaunchedEffect(sessionId) {
        loadFromCache()
        pollUntilIdle()
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(session?.provider ?: "Session") },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
    ) { padding ->
        Column(modifier = Modifier.padding(padding).fillMaxSize()) {
            if (offline) {
                Text(
                    text = "Offline — showing last known data",
                    modifier = Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.errorContainer).padding(8.dp),
                    color = MaterialTheme.colorScheme.onErrorContainer,
                )
            }
            when {
                loading -> Box(modifier = Modifier.weight(1f).fillMaxSize()) {
                    CircularProgressIndicator(modifier = Modifier.align(Alignment.Center))
                }
                error != null && session == null -> Box(modifier = Modifier.weight(1f).fillMaxSize()) {
                    Text("Error: $error", modifier = Modifier.align(Alignment.Center).padding(16.dp))
                }
                else -> LazyColumn(
                    modifier = Modifier.weight(1f).fillMaxWidth(),
                    contentPadding = PaddingValues(12.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    items(session?.messages.orEmpty()) { message ->
                        ChatBubble(message)
                    }
                }
            }

            Row(
                modifier = Modifier.fillMaxWidth().padding(8.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                OutlinedTextField(
                    value = input,
                    onValueChange = { input = it },
                    modifier = Modifier.weight(1f),
                    placeholder = { Text("Message the agent...") },
                    enabled = !sending,
                )
                IconButton(
                    enabled = input.isNotBlank() && !sending,
                    onClick = {
                        val text = input.trim()
                        input = ""
                        sending = true
                        scope.launch {
                            try {
                                val response = api.sendMessage(sessionId, MessageRequest(text))
                                response.body()?.close()
                                if (!response.isSuccessful) {
                                    error = "Send failed: HTTP ${response.code()}"
                                }
                            } catch (e: Exception) {
                                error = friendlyErrorMessage(e, runner)
                            } finally {
                                sending = false
                            }
                            pollUntilIdle()
                        }
                    },
                ) {
                    Icon(Icons.AutoMirrored.Filled.Send, contentDescription = "Send")
                }
            }
        }
    }
}

@Composable
private fun ChatBubble(message: Message) {
    val isUser = message.role == "user"
    val bubbleColor = if (isUser) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.secondaryContainer
    val textColor = if (isUser) MaterialTheme.colorScheme.onPrimary else MaterialTheme.colorScheme.onSecondaryContainer

    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = if (isUser) Arrangement.End else Arrangement.Start,
    ) {
        Box(
            modifier = Modifier
                .widthIn(max = 280.dp)
                .background(color = bubbleColor, shape = RoundedCornerShape(12.dp))
                .padding(horizontal = 12.dp, vertical = 8.dp),
        ) {
            Text(text = message.text, color = textColor)
        }
    }
}

