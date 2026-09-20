package com.relay.app.ui.navigation

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.navigation.NavType
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import androidx.navigation.navArgument
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.ui.screens.ChatScreen
import com.relay.app.ui.screens.FileBrowserScreen
import com.relay.app.ui.screens.FolderPickerScreen
import com.relay.app.ui.screens.ProjectListScreen
import com.relay.app.ui.screens.RunnerListScreen
import com.relay.app.ui.screens.SessionListScreen

/** Route templates, kept next to their builder functions so a nav arg never gets typo'd. */
object Routes {
    const val RUNNER_LIST = "runners"
    const val PROJECT_LIST = "runners/{hostname}/projects"
    const val FOLDER_PICKER = "runners/{hostname}/projects/pick-folder"
    const val SESSION_LIST = "runners/{hostname}/projects/{projectId}/sessions"
    const val CHAT = "runners/{hostname}/projects/{projectId}/sessions/{sessionId}/chat"
    const val FILES = "runners/{hostname}/projects/{projectId}/files"
    fun projectList(hostname: String) = "runners/$hostname/projects"
    fun folderPicker(hostname: String) = "runners/$hostname/projects/pick-folder"
    fun sessionList(hostname: String, projectId: String) = "runners/$hostname/projects/$projectId/sessions"
    fun chat(hostname: String, projectId: String, sessionId: String) =
        "runners/$hostname/projects/$projectId/sessions/$sessionId/chat"
    fun files(hostname: String, projectId: String) = "runners/$hostname/projects/$projectId/files"
}

@Composable
fun RelayNavHost(
    runnersRepository: KnownRunnersRepository,
) {
    val navController = rememberNavController()
    val runners by runnersRepository.runners.collectAsState(initial = emptyList())

    NavHost(navController = navController, startDestination = Routes.RUNNER_LIST) {
        composable(Routes.RUNNER_LIST) {
            RunnerListScreen(
                repository = runnersRepository,
                onRunnerSelected = { runner -> navController.navigate(Routes.projectList(runner.hostname)) },
            )
        }

        composable(
            route = Routes.PROJECT_LIST,
            arguments = listOf(navArgument("hostname") { type = NavType.StringType }),
        ) { backStackEntry ->
            val hostname = backStackEntry.arguments?.getString("hostname").orEmpty()
            val runner = runners.find { it.hostname == hostname }
            if (runner == null) {
                UnknownRunnerPlaceholder()
            } else {
                ProjectListScreen(
                    runner = runner,
                    onProjectSelected = { project ->
                        navController.navigate(Routes.sessionList(hostname, project.id))
                    },
                    onFilesSelected = { project ->
                        navController.navigate(Routes.files(hostname, project.id))
                    },
                    onPickFolder = { navController.navigate(Routes.folderPicker(hostname)) },
                    onBack = { navController.popBackStack() },
                )
            }
        }

        composable(
            route = Routes.FOLDER_PICKER,
            arguments = listOf(navArgument("hostname") { type = NavType.StringType }),
        ) { backStackEntry ->
            val hostname = backStackEntry.arguments?.getString("hostname").orEmpty()
            val runner = runners.find { it.hostname == hostname }
            if (runner == null) {
                UnknownRunnerPlaceholder()
            } else {
                FolderPickerScreen(
                    runner = runner,
                    onRegistered = { project ->
                        // Skip back to the project list and go straight into the newly
                        // registered project - picking a folder is already the "create" action,
                        // there's nothing left to confirm by returning to an empty-feeling list.
                        navController.navigate(Routes.sessionList(hostname, project.id)) {
                            popUpTo(Routes.projectList(hostname))
                        }
                    },
                    onBack = { navController.popBackStack() },
                )
            }
        }

        composable(
            route = Routes.SESSION_LIST,
            arguments = listOf(
                navArgument("hostname") { type = NavType.StringType },
                navArgument("projectId") { type = NavType.StringType },
            ),
        ) { backStackEntry ->
            val hostname = backStackEntry.arguments?.getString("hostname").orEmpty()
            val projectId = backStackEntry.arguments?.getString("projectId").orEmpty()
            val runner = runners.find { it.hostname == hostname }
            if (runner == null) {
                UnknownRunnerPlaceholder()
            } else {
                SessionListScreen(
                    runner = runner,
                    projectId = projectId,
                    onSessionSelected = { session ->
                        navController.navigate(Routes.chat(hostname, projectId, session.id))
                    },
                    onBack = { navController.popBackStack() },
                )
            }
        }

        composable(
            route = Routes.FILES,
            arguments = listOf(
                navArgument("hostname") { type = NavType.StringType },
                navArgument("projectId") { type = NavType.StringType },
            ),
        ) { backStackEntry ->
            val hostname = backStackEntry.arguments?.getString("hostname").orEmpty()
            val projectId = backStackEntry.arguments?.getString("projectId").orEmpty()
            val runner = runners.find { it.hostname == hostname }
            if (runner == null) {
                UnknownRunnerPlaceholder()
            } else {
                FileBrowserScreen(
                    runner = runner,
                    projectId = projectId,
                    onBack = { navController.popBackStack() },
                )
            }
        }

        composable(
            route = Routes.CHAT,
            arguments = listOf(
                navArgument("hostname") { type = NavType.StringType },
                navArgument("projectId") { type = NavType.StringType },
                navArgument("sessionId") { type = NavType.StringType },
            ),
        ) { backStackEntry ->
            val hostname = backStackEntry.arguments?.getString("hostname").orEmpty()
            val sessionId = backStackEntry.arguments?.getString("sessionId").orEmpty()
            val runner = runners.find { it.hostname == hostname }
            if (runner == null) {
                UnknownRunnerPlaceholder()
            } else {
                ChatScreen(
                    runner = runner,
                    sessionId = sessionId,
                    onBack = { navController.popBackStack() },
                )
            }
        }

    }
}

@Composable
private fun UnknownRunnerPlaceholder() {
    Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        Text("Unknown runner (it may have been removed).")
    }
}
