package com.relay.app.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material3.Button
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
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
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.relay.app.model.DirEntry
import com.relay.app.model.KnownRunner
import com.relay.app.model.Project
import com.relay.app.network.NewProjectRequest
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.components.SkeletonRows
import kotlinx.coroutines.launch

/**
 * Unscoped filesystem browser for picking an already-existing directory to register as a
 * project (see shared/API.md `GET /v1/browse`, [com.relay.app.network.RelayApiService.browse]) —
 * distinct from [FileBrowserContent], which is read-only and scoped to one already-registered
 * project's directory. This screen only ever lists directories, never file contents; "select
 * this folder" registers it directly (`POST /v1/projects {path}`) and hands the resulting
 * [Project] to [onRegistered], rather than round-tripping the path back through
 * Home and relying on that screen's list happening to refresh on return.
 *
 * Path history is a simple stack of absolute paths (empty string = the filesystem-roots view),
 * mirroring [FileBrowserContent]'s local-state back-navigation rather than a NavHost route.
 */
@Composable
fun FolderPickerScreen(
    runner: KnownRunner,
    onRegistered: (Project) -> Unit,
    onBack: () -> Unit,
) {
    val api = remember(runner) { RelayApiClient.forRunner(runner) }
    val scope = rememberCoroutineScope()

    // "~" = the runner's Documents folder (see shared/API.md `GET /v1/browse`); the real path
    // comes back in `resolvedPath`, which is what child paths and registration use.
    var pathStack by remember { mutableStateOf(listOf("~")) }
    val currentPath = pathStack.last()

    var entries by remember { mutableStateOf<List<DirEntry>>(emptyList()) }
    var resolvedPath by remember { mutableStateOf("") }
    var parentPath by remember { mutableStateOf<String?>(null) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var registering by remember { mutableStateOf(false) }
    val snackbar = remember { SnackbarHostState() }

    suspend fun load() {
        loading = true
        error = null
        try {
            val result = api.browse(currentPath)
            entries = result.entries.sortedBy { it.name.lowercase() }
            resolvedPath = result.path
            parentPath = result.parent
        } catch (e: Exception) {
            error = friendlyErrorMessage(e, runner)
        } finally {
            loading = false
        }
    }

    LaunchedEffect(currentPath) { load() }

    fun stepBack(): Boolean {
        if (pathStack.size <= 1) return false
        pathStack = pathStack.dropLast(1)
        return true
    }

    // Registration failures go to a Snackbar so the folder listing stays where it was.
    fun register() {
        registering = true
        scope.launch {
            try {
                val project = api.createProject(NewProjectRequest(name = "", path = resolvedPath))
                onRegistered(project)
            } catch (e: Exception) {
                snackbar.showSnackbar(friendlyErrorMessage(e, runner))
            } finally {
                registering = false
            }
        }
    }

    // Registering a filesystem root (no parent) or the drive list is nonsensical - only offer
    // "select this folder" once inside a real directory.
    val canRegister = !loading && error == null && resolvedPath.isNotEmpty() && parentPath != null

    BackHandler(enabled = true) {
        if (!stepBack()) onBack()
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(resolvedPath.ifEmpty { "Select folder" }) },
                navigationIcon = {
                    IconButton(onClick = { if (!stepBack()) onBack() }) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
        floatingActionButton = {
            if (canRegister && !registering) {
                ExtendedFloatingActionButton(
                    onClick = ::register,
                    icon = { Icon(Icons.Default.CheckCircle, contentDescription = null) },
                    text = { Text("Select this folder") },
                )
            }
        },
        snackbarHost = { SnackbarHost(snackbar) },
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize()) {
            val err = error
            when {
                loading -> SkeletonRows()
                err != null -> FullScreenError(err, onRetry = { scope.launch { load() } })
                else -> LazyColumn {
                    // "Up" lets the user leave the Documents start folder - the back stack only
                    // goes back to where they came from.
                    parentPath?.let { parent ->
                        item(key = "..") {
                            ListItem(
                                headlineContent = { Text("..") },
                                leadingContent = {
                                    Icon(imageVector = Icons.Default.ArrowUpward, contentDescription = "Up")
                                },
                                modifier = Modifier.clickable { pathStack = pathStack + parent },
                            )
                            HorizontalDivider()
                        }
                    }
                    if (entries.isEmpty()) {
                        item(key = "empty") {
                            Column(
                                Modifier.fillMaxWidth().padding(24.dp),
                                horizontalAlignment = Alignment.CenterHorizontally,
                                verticalArrangement = Arrangement.spacedBy(16.dp),
                            ) {
                                Text(
                                    if (canRegister) "No subfolders - register this one, or go up." else "No subfolders here - go up.",
                                    textAlign = TextAlign.Center,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                )
                                if (canRegister) {
                                    Button(onClick = ::register, enabled = !registering) { Text("Select this folder") }
                                }
                            }
                        }
                    }
                    items(entries, key = { it.name }) { entry ->
                        ListItem(
                            headlineContent = { Text(entry.name) },
                            leadingContent = {
                                Icon(imageVector = Icons.Default.Folder, contentDescription = null)
                            },
                            modifier = Modifier.clickable {
                                pathStack = pathStack + joinPath(resolvedPath, entry.name)
                            },
                        )
                        HorizontalDivider()
                    }
                }
            }
        }
    }
}

/**
 * Joins the current absolute path (as echoed back by the runner) with a child directory name.
 * The runner may be Linux or Windows, so the correct
 * separator can't be assumed - it's inferred from the path string itself: a Windows root entry
 * already comes back as "C:\" (trailing backslash) from `roots_windows.go`, a Unix root as "/",
 * so checking for a trailing separator or an existing backslash covers both. From the roots
 * view (empty base) the entry is itself an absolute root and is used as-is - prefixing "/"
 * turned "C:\" into "/C:\", which Windows rejects as not absolute.
 */
private fun joinPath(base: String, name: String): String = when {
    base.isEmpty() -> name
    base.endsWith("/") || base.endsWith("\\") -> base + name
    base.contains("\\") -> "$base\\$name"
    else -> "$base/$name"
}
