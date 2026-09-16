package com.relay.app.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.InsertDriveFile
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material3.CircularProgressIndicator
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
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.relay.app.model.FileEntry
import com.relay.app.model.KnownRunner
import com.relay.app.network.RelayApiClient
import kotlinx.coroutines.launch

/**
 * Read-only file browser scoped to one project, per ARCHITECTURE.md "Runner responsibilities"
 * (file read/write scoped to that project's directory only). Navigation between directories and
 * into a file's content is all local state here, not [com.relay.app.ui.navigation.RelayNavHost]
 * routes - the hardware/gesture back button drills back out one level at a time via
 * [BackHandler] before finally invoking [onBack] to leave the screen entirely.
 */
@Composable
fun FileBrowserScreen(
    runner: KnownRunner,
    projectId: String,
    onBack: () -> Unit,
) {
    val api = remember(runner) { RelayApiClient.forRunner(runner) }
    val scope = rememberCoroutineScope()

    var pathSegments by remember { mutableStateOf(listOf<String>()) }
    val currentPath = pathSegments.joinToString("/")

    var entries by remember { mutableStateOf<List<FileEntry>>(emptyList()) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }

    // Non-null while showing a file's content instead of a directory listing.
    var viewingFile by remember { mutableStateOf<String?>(null) }
    var fileText by remember { mutableStateOf<String?>(null) }
    var fileLoading by remember { mutableStateOf(false) }
    var fileError by remember { mutableStateOf<String?>(null) }

    suspend fun loadDir() {
        loading = true
        error = null
        try {
            entries = api.listFiles(projectId, currentPath)
                .sortedWith(compareBy({ !it.isDir }, { it.name.lowercase() }))
        } catch (e: Exception) {
            error = e.message ?: "Failed to load directory"
        } finally {
            loading = false
        }
    }

    LaunchedEffect(currentPath) {
        if (viewingFile == null) loadDir()
    }

    fun openFile(relPath: String) {
        viewingFile = relPath
        scope.launch {
            fileLoading = true
            fileError = null
            try {
                fileText = api.fileContent(projectId, relPath).content
            } catch (e: Exception) {
                fileError = e.message ?: "Failed to load file"
            } finally {
                fileLoading = false
            }
        }
    }

    // Drills back one level (out of a file view, or up one directory) before ever calling
    // onBack - returns true if it consumed the back press itself.
    fun stepBack(): Boolean = when {
        viewingFile != null -> {
            viewingFile = null
            fileText = null
            true
        }
        pathSegments.isNotEmpty() -> {
            pathSegments = pathSegments.dropLast(1)
            true
        }
        else -> false
    }

    BackHandler(enabled = true) {
        if (!stepBack()) onBack()
    }

    val title = when {
        viewingFile != null -> viewingFile!!.substringAfterLast('/')
        currentPath.isEmpty() -> "Files"
        else -> currentPath
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(title) },
                navigationIcon = {
                    IconButton(onClick = { if (!stepBack()) onBack() }) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize()) {
            when {
                viewingFile != null -> when {
                    fileLoading -> CircularProgressIndicator(modifier = Modifier.align(Alignment.Center))
                    fileError != null -> Text(
                        text = "Error: $fileError",
                        modifier = Modifier.align(Alignment.Center).padding(16.dp),
                    )
                    else -> SelectionContainer {
                        Text(
                            text = fileText.orEmpty(),
                            fontFamily = FontFamily.Monospace,
                            fontSize = 12.sp,
                            modifier = Modifier
                                .fillMaxSize()
                                .verticalScroll(rememberScrollState())
                                .padding(12.dp),
                        )
                    }
                }
                loading -> CircularProgressIndicator(modifier = Modifier.align(Alignment.Center))
                error != null -> Text(
                    text = "Error: $error",
                    modifier = Modifier.align(Alignment.Center).padding(16.dp),
                )
                entries.isEmpty() -> Text("Empty directory.", modifier = Modifier.align(Alignment.Center))
                else -> LazyColumn {
                    items(entries, key = { it.name }) { entry ->
                        ListItem(
                            headlineContent = { Text(entry.name) },
                            supportingContent = if (!entry.isDir) {
                                { Text("${entry.size} bytes") }
                            } else null,
                            leadingContent = {
                                Icon(
                                    imageVector = if (entry.isDir) Icons.Default.Folder else Icons.AutoMirrored.Filled.InsertDriveFile,
                                    contentDescription = null,
                                )
                            },
                            modifier = Modifier.clickable {
                                if (entry.isDir) {
                                    pathSegments = pathSegments + entry.name
                                } else {
                                    val relPath = if (currentPath.isEmpty()) entry.name else "$currentPath/${entry.name}"
                                    openFile(relPath)
                                }
                            },
                        )
                        HorizontalDivider()
                    }
                }
            }
        }
    }
}
