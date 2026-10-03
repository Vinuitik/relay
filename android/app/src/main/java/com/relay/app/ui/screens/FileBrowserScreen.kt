package com.relay.app.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
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
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.relay.app.model.FileEntry
import com.relay.app.model.KnownRunner
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.ui.components.EmptyState
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.components.RefreshableBox
import com.relay.app.ui.components.SkeletonRows
import com.relay.app.ui.theme.codeMedium
import com.relay.app.ui.theme.codeSmall
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch

/**
 * Read-only file browser state for one project (ARCHITECTURE.md "Runner responsibilities": file
 * access scoped to that project's directory). Directory drill-down and file viewing are local
 * state here, not NavHost routes; scoped to the Project back-stack entry, so switching tabs or
 * returning from a chat keeps the folder you were in.
 */
class FileBrowserViewModel(private val runner: KnownRunner, private val projectId: String) : ViewModel() {
    private val api = RelayApiClient.forRunner(runner)

    var pathSegments by mutableStateOf(listOf<String>())
        private set
    val currentPath: String get() = pathSegments.joinToString("/")

    /** null = not loaded yet for [currentPath]. */
    var entries by mutableStateOf<List<FileEntry>?>(null)
        private set
    var refreshing by mutableStateOf(false)
        private set
    var error by mutableStateOf<String?>(null)
        private set

    /** Non-null while showing a file's content instead of a directory listing. */
    var viewingFile by mutableStateOf<String?>(null)
        private set
    var fileText by mutableStateOf<String?>(null)
        private set
    var fileError by mutableStateOf<String?>(null)
        private set

    private var dirJob: Job? = null

    init {
        loadDir()
    }

    fun loadDir(): Job {
        dirJob?.cancel()
        val path = currentPath
        return viewModelScope.launch {
            refreshing = true
            try {
                entries = api.listFiles(projectId, path)
                    .sortedWith(compareBy({ !it.isDir }, { it.name.lowercase() }))
                error = null
            } catch (e: Exception) {
                error = friendlyErrorMessage(e, runner)
            } finally {
                if (path == currentPath) refreshing = false
            }
        }.also { dirJob = it }
    }

    fun openDir(name: String) {
        pathSegments = pathSegments + name
        entries = null
        error = null
        loadDir()
    }

    fun openFile(relPath: String) {
        viewingFile = relPath
        fileText = null
        fileError = null
        viewModelScope.launch {
            try {
                fileText = api.fileContent(projectId, relPath).content
            } catch (e: Exception) {
                fileError = friendlyErrorMessage(e, runner)
            }
        }
    }

    val canStepBack: Boolean get() = viewingFile != null || pathSegments.isNotEmpty()

    /** Out of a file view, else up one directory. Returns false if already at the root. */
    fun stepBack(): Boolean = when {
        viewingFile != null -> {
            viewingFile = null
            fileText = null
            true
        }
        pathSegments.isNotEmpty() -> {
            pathSegments = pathSegments.dropLast(1)
            entries = null
            error = null
            loadDir()
            true
        }
        else -> false
    }
}

/**
 * Files tab body. Back (gesture/hardware) steps out of a file, then up one directory; at the root
 * it falls through to normal navigation.
 */
@Composable
fun FileBrowserContent(vm: FileBrowserViewModel) {
    BackHandler(enabled = vm.canStepBack) { vm.stepBack() }

    Column(Modifier.fillMaxSize()) {
        if (vm.canStepBack) {
            ListItem(
                leadingContent = { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Up") },
                headlineContent = {
                    Text(
                        vm.viewingFile ?: vm.currentPath,
                        style = MaterialTheme.typography.codeSmall,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                },
                modifier = Modifier.clickable { vm.stepBack() },
            )
            HorizontalDivider()
        }
        val file = vm.viewingFile
        if (file != null) {
            val fileError = vm.fileError
            when {
                fileError != null -> FullScreenError(fileError, onRetry = { vm.openFile(file) })
                vm.fileText == null -> SkeletonRows(3)
                else -> SelectionContainer {
                    Text(
                        text = vm.fileText.orEmpty(),
                        style = MaterialTheme.typography.codeMedium,
                        modifier = Modifier
                            .fillMaxSize()
                            .verticalScroll(rememberScrollState())
                            .padding(12.dp),
                    )
                }
            }
            return@Column
        }
        RefreshableBox(refreshing = vm.refreshing, onRefresh = { vm.loadDir() }) {
            val entries = vm.entries
            val error = vm.error
            when {
                entries == null && error != null -> FullScreenError(error, onRetry = { vm.loadDir() })
                entries == null -> SkeletonRows()
                entries.isEmpty() -> EmptyState("Empty folder.")
                else -> LazyColumn(Modifier.fillMaxWidth()) {
                    items(entries, key = { it.name }) { entry ->
                        ListItem(
                            headlineContent = { Text(entry.name) },
                            supportingContent = if (!entry.isDir) {
                                { Text("${entry.size} bytes", style = MaterialTheme.typography.codeSmall) }
                            } else {
                                null
                            },
                            leadingContent = {
                                Icon(
                                    imageVector = if (entry.isDir) Icons.Default.Folder else Icons.AutoMirrored.Filled.InsertDriveFile,
                                    contentDescription = null,
                                )
                            },
                            modifier = Modifier.clickable {
                                if (entry.isDir) {
                                    vm.openDir(entry.name)
                                } else {
                                    val cur = vm.currentPath
                                    vm.openFile(if (cur.isEmpty()) entry.name else "$cur/${entry.name}")
                                }
                            },
                        )
                    }
                }
            }
        }
    }
}
