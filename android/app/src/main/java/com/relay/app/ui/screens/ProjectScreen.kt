package com.relay.app.ui.screens

import androidx.compose.animation.Crossfade
import androidx.compose.animation.core.tween
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.sizeIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Add
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FloatingActionButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.PrimaryTabRow
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Surface
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.relay.app.data.AppPrefsRepository
import com.relay.app.model.Session
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.components.RefreshableBox
import com.relay.app.ui.components.SessionStateChip
import com.relay.app.ui.components.SkeletonRows
import com.relay.app.ui.components.relativeTime
import com.relay.app.ui.theme.RelayMotion
import kotlinx.coroutines.launch

private val TABS = listOf(ProjectTab.CHATS to "Chats", ProjectTab.FILES to "Files", ProjectTab.GIT to "Git", ProjectTab.CONTAINERS to "Containers")

/**
 * Project screen: title = project name, tabs Chats | Files | Git | Containers (crossfade 150ms, no
 * slide - DESIGN.md Motion). Each tab's state lives in its own ViewModel scoped to this back-stack
 * entry, so tab switches and returning from a chat never blank the content.
 */
@Composable
fun ProjectScreen(
    vm: ProjectViewModel,
    filesVm: FileBrowserViewModel,
    containersVm: ContainersViewModel,
    gitVm: GitViewModel,
    initialTab: String,
    lastProvider: String,
    onProviderUsed: (String) -> Unit,
    onBack: () -> Unit,
    onOpenChat: (sessionId: String) -> Unit,
    onMissing: () -> Unit,
) {
    var tab by rememberSaveable { mutableStateOf(initialTab.takeIf { t -> TABS.any { it.first == t } } ?: ProjectTab.CHATS) }
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    fun showError(msg: String) { scope.launch { snackbar.showSnackbar(msg) } }

    LaunchedEffect(vm.missing) { if (vm.missing) onMissing() }
    LaunchedEffect(Unit) { vm.refreshIfLoaded() }

    fun create(provider: String) {
        onProviderUsed(provider)
        vm.newChat(provider, onCreated = { onOpenChat(it.id) }, onError = ::showError)
    }

    Scaffold(
        topBar = {
            Column {
                TopAppBar(
                    title = { Text(vm.project?.name ?: "", maxLines = 1, overflow = TextOverflow.Ellipsis) },
                    navigationIcon = {
                        IconButton(onClick = onBack) {
                            Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                        }
                    },
                )
                PrimaryTabRow(selectedTabIndex = TABS.indexOfFirst { it.first == tab }) {
                    TABS.forEach { (id, label) ->
                        Tab(selected = tab == id, onClick = { tab = id }, text = { Text(label) })
                    }
                }
            }
        },
        floatingActionButton = {
            if (tab == ProjectTab.CHATS && vm.sessions?.isNotEmpty() == true) {
                NewChatFab(
                    enabled = !vm.creating,
                    onClick = { create(lastProvider) },
                    onPickProvider = ::create,
                )
            }
        },
        snackbarHost = { SnackbarHost(snackbar) },
    ) { padding ->
        Crossfade(
            targetState = tab,
            animationSpec = tween(RelayMotion.DurationShort),
            modifier = Modifier.padding(padding),
            label = "projectTab",
        ) { t ->
            when (t) {
                ProjectTab.FILES -> FileBrowserContent(filesVm)
                ProjectTab.GIT -> GitContent(gitVm)
                ProjectTab.CONTAINERS -> ContainersContent(containersVm)
                else -> ChatsContent(
                    vm = vm,
                    onOpenChat = onOpenChat,
                    onStart = { text ->
                        onProviderUsed(lastProvider)
                        vm.startWithMessage(lastProvider, text, onCreated = { onOpenChat(it.id) }, onError = ::showError)
                    },
                )
            }
        }
    }
}

@Composable
private fun ChatsContent(vm: ProjectViewModel, onOpenChat: (String) -> Unit, onStart: (String) -> Unit) {
    RefreshableBox(refreshing = vm.refreshing, onRefresh = { vm.refresh() }) {
        val sessions = vm.sessions
        val error = vm.error
        when {
            sessions == null && error != null -> FullScreenError(error, onRetry = { vm.refresh() })
            sessions == null -> SkeletonRows()
            sessions.isEmpty() -> FirstMessageComposer(enabled = !vm.creating, onSend = onStart)
            else -> LazyColumn(Modifier.fillMaxWidth()) {
                items(sessions, key = { it.id }) { s -> SessionRow(s) { onOpenChat(s.id) } }
                item(key = "fab-space") { Box(Modifier.padding(bottom = 88.dp)) }
            }
        }
    }
}

@Composable
private fun SessionRow(s: Session, onClick: () -> Unit) {
    val meta = listOfNotNull(relativeTime(s.activityAt).ifEmpty { null }, s.preview?.ifBlank { null })
        .joinToString(" · ")
    ListItem(
        headlineContent = { Text(s.displayTitle, maxLines = 1, overflow = TextOverflow.Ellipsis) },
        supportingContent = if (meta.isNotEmpty()) {
            { Text(meta, maxLines = 1, overflow = TextOverflow.Ellipsis) }
        } else {
            null
        },
        trailingContent = { SessionStateChip(s.state) },
        modifier = Modifier.clickable(onClick = onClick),
    )
}

/** Empty Chats: the first message creates the session (DESIGN.md "Project › Chats"). */
@Composable
private fun FirstMessageComposer(enabled: Boolean, onSend: (String) -> Unit) {
    var text by rememberSaveable { mutableStateOf("") }
    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text(
            "No chats yet.",
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(top = 32.dp),
        )
        Row(verticalAlignment = Alignment.CenterVertically) {
            OutlinedTextField(
                value = text,
                onValueChange = { text = it },
                placeholder = { Text("What should the agent do?") },
                modifier = Modifier.weight(1f),
                enabled = enabled,
            )
            IconButton(
                onClick = { if (text.isNotBlank()) onSend(text.trim()) },
                enabled = enabled && text.isNotBlank(),
            ) {
                Icon(Icons.AutoMirrored.Filled.Send, contentDescription = "Start chat")
            }
        }
    }
}

/**
 * ExtendedFAB look-alike that also takes a long-press (M3's FAB only exposes onClick): tap =
 * new chat with the last-used provider, long-press = pick a provider.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun NewChatFab(enabled: Boolean, onClick: () -> Unit, onPickProvider: (String) -> Unit) {
    var menu by remember { mutableStateOf(false) }
    Box {
        Surface(
            shape = FloatingActionButtonDefaults.extendedFabShape,
            color = MaterialTheme.colorScheme.primaryContainer,
            contentColor = MaterialTheme.colorScheme.onPrimaryContainer,
            shadowElevation = 6.dp,
        ) {
            Row(
                Modifier
                    .sizeIn(minHeight = 56.dp)
                    .combinedClickable(
                        enabled = enabled,
                        onClickLabel = "New chat",
                        onLongClickLabel = "Choose provider",
                        onClick = onClick,
                        onLongClick = { menu = true },
                    )
                    .padding(start = 16.dp, end = 20.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                Icon(Icons.Default.Add, contentDescription = null)
                Text("New chat", style = MaterialTheme.typography.labelLarge)
            }
        }
        DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
            AppPrefsRepository.KNOWN_PROVIDERS.forEach { p ->
                DropdownMenuItem(text = { Text("New $p chat") }, onClick = { menu = false; onPickProvider(p) })
            }
        }
    }
}
