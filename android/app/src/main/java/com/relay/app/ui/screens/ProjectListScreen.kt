package com.relay.app.ui.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Star
import androidx.compose.material.icons.filled.StarBorder
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FloatingActionButton
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
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.relay.app.data.WidgetConfigRepository
import com.relay.app.data.WidgetTarget
import com.relay.app.model.KnownRunner
import com.relay.app.model.Project
import com.relay.app.network.NewProjectRequest
import com.relay.app.network.RelayApiClient
import kotlinx.coroutines.launch

@Composable
fun ProjectListScreen(
    runner: KnownRunner,
    widgetConfigRepository: WidgetConfigRepository,
    onProjectSelected: (Project) -> Unit,
    onBack: () -> Unit,
) {
    val api = remember(runner) { RelayApiClient.forRunner(runner) }
    val scope = rememberCoroutineScope()

    var projects by remember { mutableStateOf<List<Project>>(emptyList()) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var showAddDialog by remember { mutableStateOf(false) }

    val widgetTarget by widgetConfigRepository.defaultTarget.collectAsState(initial = null)

    suspend fun refresh() {
        loading = true
        error = null
        try {
            projects = api.listProjects()
        } catch (e: Exception) {
            error = e.message ?: "Failed to load projects"
        } finally {
            loading = false
        }
    }

    LaunchedEffect(runner) { refresh() }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(runner.hostname) },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
        floatingActionButton = {
            FloatingActionButton(onClick = { showAddDialog = true }) {
                Icon(Icons.Default.Add, contentDescription = "New project")
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
                projects.isEmpty() -> Text("No projects yet.", modifier = Modifier.align(Alignment.Center))
                else -> LazyColumn {
                    items(projects, key = { it.id }) { project ->
                        val isWidgetDefault = widgetTarget?.runner?.hostname == runner.hostname &&
                            widgetTarget?.projectId == project.id
                        ListItem(
                            headlineContent = { Text(project.name) },
                            supportingContent = { Text(project.path) },
                            trailingContent = {
                                IconButton(onClick = {
                                    scope.launch {
                                        widgetConfigRepository.setDefault(WidgetTarget(runner, project.id))
                                    }
                                }) {
                                    Icon(
                                        imageVector = if (isWidgetDefault) Icons.Default.Star else Icons.Default.StarBorder,
                                        contentDescription = "Set as widget default project",
                                    )
                                }
                            },
                            modifier = Modifier.clickable { onProjectSelected(project) },
                        )
                        HorizontalDivider()
                    }
                }
            }
        }
    }

    if (showAddDialog) {
        NewProjectDialog(
            onDismiss = { showAddDialog = false },
            onCreate = { name ->
                showAddDialog = false
                scope.launch {
                    try {
                        api.createProject(NewProjectRequest(name))
                        refresh()
                    } catch (e: Exception) {
                        error = e.message ?: "Failed to create project"
                    }
                }
            },
        )
    }
}

@Composable
private fun NewProjectDialog(onDismiss: () -> Unit, onCreate: (String) -> Unit) {
    var name by remember { mutableStateOf("") }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("New project") },
        text = {
            Row {
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it },
                    label = { Text("Name") },
                    singleLine = true,
                )
            }
        },
        confirmButton = {
            TextButton(onClick = { if (name.isNotBlank()) onCreate(name.trim()) }) { Text("Create") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}
