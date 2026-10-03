package com.relay.app.ui.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.outlined.CreateNewFolder
import androidx.compose.material.icons.outlined.FolderOpen
import androidx.compose.material.icons.outlined.Settings
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.relay.app.model.ContainersStatus
import com.relay.app.model.KnownRunner
import com.relay.app.model.Project
import com.relay.app.model.Session
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.components.RefreshableBox
import com.relay.app.ui.components.SessionStateChip
import com.relay.app.ui.components.SkeletonRows
import com.relay.app.ui.components.StatusLamp
import com.relay.app.ui.components.relativeTime
import com.relay.app.ui.theme.RelayStatus
import com.relay.app.ui.theme.codeSmall
import kotlinx.coroutines.launch

/** Project tabs, also the `tab` arg of the Project route. */
object ProjectTab {
    const val CHATS = "chats"
    const val FILES = "files"
    const val CONTAINERS = "containers"
}

/**
 * Home (DESIGN.md "Navigation & flow"): the current runner's projects + a "Needs you" section of
 * waiting/busy sessions. The runner switcher lives in the title; with one runner it's just a
 * title and "Manage runners" moves to the ⋮.
 */
@Composable
fun HomeScreen(
    vm: HomeViewModel,
    runner: KnownRunner,
    runners: List<KnownRunner>,
    lastProvider: String,
    onSwitchRunner: (KnownRunner) -> Unit,
    onManageRunners: () -> Unit,
    onOpenProject: (projectId: String, tab: String) -> Unit,
    onOpenChat: (projectId: String, sessionId: String) -> Unit,
    onPickFolder: () -> Unit,
) {
    LaunchedEffect(runner) { vm.selectRunner(runner) }

    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    fun showError(msg: String) { scope.launch { snackbar.showSnackbar(msg) } }

    var showAddSheet by remember { mutableStateOf(false) }
    var showNewProject by remember { mutableStateOf(false) }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    RunnerSwitcher(
                        current = runner,
                        runners = runners,
                        online = vm.online,
                        onOpen = { vm.checkOnline(runners) },
                        onSwitch = onSwitchRunner,
                        onManageRunners = onManageRunners,
                    )
                },
                actions = {
                    if (runners.size <= 1) {
                        var menu by remember { mutableStateOf(false) }
                        Box {
                            IconButton(onClick = { menu = true }) {
                                Icon(Icons.Default.MoreVert, contentDescription = "More")
                            }
                            DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                                DropdownMenuItem(
                                    text = { Text("Manage runners") },
                                    leadingIcon = { Icon(Icons.Outlined.Settings, contentDescription = null) },
                                    onClick = { menu = false; onManageRunners() },
                                )
                            }
                        }
                    }
                },
            )
        },
        floatingActionButton = {
            ExtendedFloatingActionButton(
                onClick = { showAddSheet = true },
                icon = { Icon(Icons.Default.Add, contentDescription = null) },
                text = { Text("Add project") },
            )
        },
        snackbarHost = { SnackbarHost(snackbar) },
    ) { padding ->
        RefreshableBox(
            refreshing = vm.refreshing,
            onRefresh = { vm.refresh() },
            modifier = Modifier.padding(padding),
        ) {
            val projects = vm.projects
            val error = vm.error
            when {
                projects == null && error != null -> FullScreenError(error, onRetry = { vm.refresh() })
                projects == null -> SkeletonRows()
                else -> HomeList(
                    projects = projects,
                    needsYou = vm.needsYou,
                    containers = vm.containers,
                    offline = error != null,
                    onRetry = { vm.refresh() },
                    onOpenProject = onOpenProject,
                    onOpenChat = onOpenChat,
                    onNewChat = { p ->
                        vm.newChat(p.id, lastProvider, onCreated = { onOpenChat(p.id, it.id) }, onError = ::showError)
                    },
                    onUp = { p, file -> vm.containersUp(p.id, file, ::showError) },
                    onDown = { p -> vm.containersDown(p.id, ::showError) },
                )
            }
        }
    }

    if (showAddSheet) {
        val sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)
        ModalBottomSheet(onDismissRequest = { showAddSheet = false }, sheetState = sheetState) {
            ListItem(
                headlineContent = { Text("Register existing folder") },
                supportingContent = { Text("Pick a folder on ${runner.label}") },
                leadingContent = { Icon(Icons.Outlined.FolderOpen, contentDescription = null) },
                modifier = Modifier.clickable { showAddSheet = false; onPickFolder() },
            )
            ListItem(
                headlineContent = { Text("New empty project") },
                supportingContent = { Text("Created under the runner's projects folder") },
                leadingContent = { Icon(Icons.Outlined.CreateNewFolder, contentDescription = null) },
                modifier = Modifier.clickable { showAddSheet = false; showNewProject = true },
            )
            Box(Modifier.padding(bottom = 24.dp))
        }
    }

    if (showNewProject) {
        NewProjectDialog(
            onDismiss = { showNewProject = false },
            onCreate = { name ->
                showNewProject = false
                vm.createProject(name, ::showError)
            },
        )
    }
}

@Composable
private fun RunnerSwitcher(
    current: KnownRunner,
    runners: List<KnownRunner>,
    online: Map<String, Boolean>,
    onOpen: () -> Unit,
    onSwitch: (KnownRunner) -> Unit,
    onManageRunners: () -> Unit,
) {
    val single = runners.size <= 1
    var expanded by remember { mutableStateOf(false) }
    Box {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            modifier = if (single) Modifier else Modifier.clickable { expanded = true; onOpen() },
        ) {
            OnlineLamp(online[current.hostname])
            Text(current.label, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (!single) Icon(Icons.Default.ArrowDropDown, contentDescription = "Switch runner")
        }
        DropdownMenu(expanded = expanded, onDismissRequest = { expanded = false }) {
            runners.forEach { r ->
                DropdownMenuItem(
                    text = { Text(r.label) },
                    leadingIcon = { OnlineLamp(online[r.hostname]) },
                    onClick = { expanded = false; onSwitch(r) },
                )
            }
            HorizontalDivider()
            DropdownMenuItem(
                text = { Text("Manage runners") },
                leadingIcon = { Icon(Icons.Outlined.Settings, contentDescription = null) },
                onClick = { expanded = false; onManageRunners() },
            )
        }
    }
}

/** Lit green = reachable, dark = unreachable or not checked yet. */
@Composable
private fun OnlineLamp(online: Boolean?) {
    StatusLamp(
        status = if (online == true) RelayStatus.Success else RelayStatus.Idle,
        lit = online == true,
        modifier = Modifier.semantics {
            contentDescription = when (online) {
                true -> "online"
                false -> "offline"
                null -> "checking"
            }
        },
    )
}

@Composable
private fun HomeList(
    projects: List<Project>,
    needsYou: List<Session>,
    containers: Map<String, ContainersStatus>,
    offline: Boolean,
    onRetry: () -> Unit,
    onOpenProject: (String, String) -> Unit,
    onOpenChat: (String, String) -> Unit,
    onNewChat: (Project) -> Unit,
    onUp: (Project, String) -> Unit,
    onDown: (Project) -> Unit,
) {
    val names = remember(projects) { projects.associate { it.id to it.name } }
    LazyColumn(Modifier.fillMaxWidth()) {
        if (offline) {
            item(key = "offline") {
                Surface(color = MaterialTheme.colorScheme.errorContainer, modifier = Modifier.fillMaxWidth()) {
                    Row(
                        Modifier.padding(start = 16.dp, end = 8.dp, top = 4.dp, bottom = 4.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Text(
                            "Runner offline · showing the last list",
                            modifier = Modifier.weight(1f),
                            style = MaterialTheme.typography.bodyMedium,
                        )
                        TextButton(onClick = onRetry) { Text("Retry") }
                    }
                }
            }
        }
        if (needsYou.isNotEmpty()) {
            item(key = "needs-header") { SectionLabel("Needs you") }
            items(needsYou, key = { "s-" + it.id }) { s ->
                val meta = listOfNotNull(
                    names[s.projectId],
                    relativeTime(s.activityAt).ifEmpty { null },
                    s.preview?.ifBlank { null },
                ).joinToString(" · ")
                ListItem(
                    headlineContent = { Text(s.displayTitle, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                    supportingContent = { Text(meta, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                    trailingContent = { SessionStateChip(s.state) },
                    modifier = Modifier.clickable { onOpenChat(s.projectId, s.id) },
                )
            }
            item(key = "projects-header") { SectionLabel("Projects") }
        }
        if (projects.isEmpty()) {
            item(key = "empty") {
                Text(
                    "No projects yet. Add one with \"Add project\".",
                    modifier = Modifier.padding(24.dp),
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
        items(projects, key = { "p-" + it.id }) { p ->
            ProjectRow(
                project = p,
                status = containers[p.id],
                modifier = if (offline) Modifier.alpha(0.5f) else Modifier,
                onOpen = { onOpenProject(p.id, ProjectTab.CHATS) },
                onNewChat = { onNewChat(p) },
                onFiles = { onOpenProject(p.id, ProjectTab.FILES) },
                onContainers = { onOpenProject(p.id, ProjectTab.CONTAINERS) },
                onUp = { file -> onUp(p, file) },
                onDown = { onDown(p) },
            )
        }
        item(key = "fab-space") { Box(Modifier.padding(bottom = 88.dp)) }
    }
}

@Composable
private fun SectionLabel(text: String) {
    Text(
        text,
        style = MaterialTheme.typography.titleSmall,
        color = MaterialTheme.colorScheme.primary,
        modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 16.dp, bottom = 4.dp),
    )
}

@Composable
private fun ProjectRow(
    project: Project,
    status: ContainersStatus?,
    modifier: Modifier,
    onOpen: () -> Unit,
    onNewChat: () -> Unit,
    onFiles: () -> Unit,
    onContainers: () -> Unit,
    onUp: (String) -> Unit,
    onDown: () -> Unit,
) {
    val hasCompose = status != null && status.composeFiles.isNotEmpty()
    val running = status?.containers?.count { it.state == "running" } ?: 0
    var menu by remember { mutableStateOf(false) }
    ListItem(
        headlineContent = { Text(project.name, maxLines = 1, overflow = TextOverflow.Ellipsis) },
        supportingContent = {
            Text(
                project.path,
                style = MaterialTheme.typography.codeSmall,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        },
        trailingContent = {
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (hasCompose) {
                    // Lamp only (DESIGN.md "Dropped"): lit = something running.
                    StatusLamp(
                        status = if (running > 0) RelayStatus.Success else RelayStatus.Idle,
                        lit = running > 0,
                        modifier = Modifier.padding(end = 4.dp).size(10.dp).semantics {
                            contentDescription = if (running > 0) "containers: $running up" else "containers down"
                        },
                    )
                }
                Box {
                    IconButton(onClick = { menu = true }) {
                        Icon(Icons.Default.MoreVert, contentDescription = "More actions for ${project.name}")
                    }
                    DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                        DropdownMenuItem(text = { Text("New chat") }, onClick = { menu = false; onNewChat() })
                        DropdownMenuItem(text = { Text("Files") }, onClick = { menu = false; onFiles() })
                        DropdownMenuItem(text = { Text("Containers") }, onClick = { menu = false; onContainers() })
                        if (hasCompose && status != null) {
                            val busy = status.operation.isNotEmpty()
                            when {
                                running > 0 -> DropdownMenuItem(
                                    text = { Text("Containers down") },
                                    enabled = !busy,
                                    onClick = { menu = false; onDown() },
                                )
                                status.composeFiles.size == 1 -> DropdownMenuItem(
                                    text = { Text("Containers up") },
                                    enabled = !busy,
                                    onClick = { menu = false; onUp(status.composeFiles.first()) },
                                )
                                // Several files: picking one needs the Containers tab.
                                else -> DropdownMenuItem(
                                    text = { Text("Containers up…") },
                                    enabled = !busy,
                                    onClick = { menu = false; onContainers() },
                                )
                            }
                        }
                    }
                }
            }
        },
        modifier = modifier.clickable(onClick = onOpen),
    )
}

@Composable
private fun NewProjectDialog(onDismiss: () -> Unit, onCreate: (String) -> Unit) {
    var name by remember { mutableStateOf("") }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("New empty project") },
        text = {
            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text("Name") },
                singleLine = true,
            )
        },
        confirmButton = {
            TextButton(onClick = { if (name.isNotBlank()) onCreate(name.trim()) }) { Text("Create") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}
