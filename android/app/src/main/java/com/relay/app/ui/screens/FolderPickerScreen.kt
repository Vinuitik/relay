package com.relay.app.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
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
import androidx.compose.ui.unit.dp
import com.relay.app.model.DirEntry
import com.relay.app.model.KnownRunner
import com.relay.app.model.Project
import com.relay.app.network.NewProjectRequest
import com.relay.app.network.RelayApiClient
import kotlinx.coroutines.launch

/**
 * Unscoped filesystem browser for picking an already-existing directory to register as a
 * project (see shared/API.md `GET /v1/browse`, [com.relay.app.network.RelayApiService.browse]) —
 * distinct from [FileBrowserScreen], which is read-only and scoped to one already-registered
 * project's directory. This screen only ever lists directories, never file contents; "select
 * this folder" registers it directly (`POST /v1/projects {path}`) and hands the resulting
 * [Project] to [onRegistered], rather than round-tripping the path back through
 * [ProjectListScreen] and relying on that screen's list happening to refresh on return.
 *
 * Path history is a simple stack of absolute paths (empty string = the filesystem-roots view),
 * mirroring [FileBrowserScreen]'s local-state back-navigation rather than a NavHost route.
 */
@Composable
fun FolderPickerScreen(
    runner: KnownRunner,
    onRegistered: (Project) -> Unit,
    onBack: () -> Unit,
) {
    val api = remember(runner) { RelayApiClient.forRunner(runner) }
    val scope = rememberCoroutineScope()

    var pathStack by remember { mutableStateOf(listOf("")) }
    val currentPath = pathStack.last()

    var entries by remember { mutableStateOf<List<DirEntry>>(emptyList()) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var registering by remember { mutableStateOf(false) }

    suspend fun load() {
        loading = true
        error = null
        try {
            entries = api.browse(currentPath).entries.sortedBy { it.name.lowercase() }
        } catch (e: Exception) {
            error = e.message ?: "Failed to browse"
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

    BackHandler(enabled = true) {
        if (!stepBack()) onBack()
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(currentPath.ifEmpty { "Select folder" }) },
                navigationIcon = {
                    IconButton(onClick = { if (!stepBack()) onBack() }) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
        floatingActionButton = {
            // Registering the filesystem root itself is nonsensical (never a real project) -
            // only offer "select this folder" once inside a real directory.
            if (currentPath.isNotEmpty() && !registering) {
                FloatingActionButton(onClick = {
                    registering = true
                    scope.launch {
                        try {
                            val project = api.createProject(NewProjectRequest(name = "", path = currentPath))
                            onRegistered(project)
                        } catch (e: Exception) {
                            error = e.message ?: "Failed to register folder"
                        } finally {
                            registering = false
                        }
                    }
                }) {
                    Icon(Icons.Default.CheckCircle, contentDescription = "Select this folder")
                }
            }
        },
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize()) {
            when {
                loading -> CircularProgressIndicator(modifier = Modifier.align(Alignment.Center))
                error != null -> Text(
                    text = "Error: $error",
                    modifier = Modifier.align(Alignment.Center).padding(16.dp),
                )
                entries.isEmpty() -> Text("No subfolders.", modifier = Modifier.align(Alignment.Center))
                else -> LazyColumn {
                    items(entries, key = { it.name }) { entry ->
                        ListItem(
                            headlineContent = { Text(entry.name) },
                            leadingContent = {
                                Icon(imageVector = Icons.Default.Folder, contentDescription = null)
                            },
                            modifier = Modifier.clickable {
                                pathStack = pathStack + joinPath(currentPath, entry.name)
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
 * The runner may be Linux or Windows (see ARCHITECTURE.md "OS-independent"), so the correct
 * separator can't be assumed - it's inferred from the path string itself: a Windows root entry
 * already comes back as "C:\" (trailing backslash) from `roots_windows.go`, a Unix root as "/",
 * so checking for a trailing separator or an existing backslash covers both.
 */
private fun joinPath(base: String, name: String): String = when {
    base.endsWith("/") || base.endsWith("\\") -> base + name
    base.contains("\\") -> "$base\\$name"
    else -> "$base/$name"
}
