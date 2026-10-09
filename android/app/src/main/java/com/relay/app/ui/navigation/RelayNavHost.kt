package com.relay.app.ui.navigation

import android.net.Uri
import android.os.Bundle
import androidx.compose.animation.AnimatedContentTransitionScope
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import androidx.navigation.NavBackStackEntry
import androidx.navigation.NavHostController
import androidx.navigation.NavType
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import androidx.navigation.navArgument
import androidx.navigation.navDeepLink
import com.relay.app.data.AppPrefsRepository
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.data.ScheduleRepository
import com.relay.app.model.KnownRunner
import com.relay.app.ui.screens.ChatScreen
import com.relay.app.ui.screens.ChatViewModel
import com.relay.app.ui.screens.ContainersViewModel
import com.relay.app.ui.screens.GitViewModel
import com.relay.app.ui.screens.FileBrowserViewModel
import com.relay.app.ui.screens.FolderPickerScreen
import com.relay.app.ui.screens.HomeScreen
import com.relay.app.ui.screens.HomeViewModel
import com.relay.app.ui.screens.ProjectScreen
import com.relay.app.ui.screens.ProjectTab
import com.relay.app.ui.screens.ProjectViewModel
import com.relay.app.ui.screens.BookingEditorScreen
import com.relay.app.ui.screens.BookingEditorViewModel
import com.relay.app.ui.screens.ScheduleScreen
import com.relay.app.ui.screens.ScheduleViewModel
import com.relay.app.ui.screens.UsageScreen
import com.relay.app.ui.screens.UsageViewModel
import com.relay.app.ui.screens.RunnerListScreen
import com.relay.app.ui.theme.RelayMotion
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

/**
 * Route templates, next to their builders so a nav arg never gets typo'd. Runner-scoped routes
 * carry the runner's hostname (`host`) so a restored route or a push deep link is self-contained.
 */
object Routes {
    const val HOME = "home"
    const val RUNNERS = "runners"
    const val FOLDER_PICKER = "r/{host}/pick-folder"
    const val PROJECT = "r/{host}/p/{projectId}?tab={tab}"
    const val CHAT = "r/{host}/p/{projectId}/s/{sessionId}"
    const val USAGE = "r/{host}/usage"
    const val SCHEDULE = "r/{host}/schedule"
    const val SCHEDULE_EDIT = "r/{host}/schedule/edit?bookingId={bookingId}&date={date}"

    /** Push notifications open this (RelayFirebaseMessagingService builds it). */
    const val CHAT_DEEP_LINK = "relay://r/{host}/p/{projectId}/s/{sessionId}"

    fun folderPicker(host: String) = "r/${enc(host)}/pick-folder"
    fun usage(host: String) = "r/${enc(host)}/usage"
    fun schedule(host: String) = "r/${enc(host)}/schedule"
    /** Booking editor: an occurrence (bookingId + date), a new booking on [date], or both null. */
    fun scheduleEdit(host: String, bookingId: String? = null, date: String? = null): String {
        val q = listOfNotNull(bookingId?.let { "bookingId=${enc(it)}" }, date?.let { "date=${enc(it)}" })
        return "r/${enc(host)}/schedule/edit" + if (q.isEmpty()) "" else q.joinToString("&", prefix = "?")
    }
    fun project(host: String, projectId: String, tab: String = ProjectTab.CHATS) =
        "r/${enc(host)}/p/${enc(projectId)}?tab=${enc(tab)}"
    fun chat(host: String, projectId: String, sessionId: String) =
        "r/${enc(host)}/p/${enc(projectId)}/s/${enc(sessionId)}"
    fun chatDeepLink(host: String, projectId: String, sessionId: String) = "relay://" + chat(host, projectId, sessionId)

    private fun enc(s: String) = Uri.encode(s)

    /** Concrete route worth restoring on next launch (Home/Project/Chat), else null. */
    fun restorable(template: String?, args: Bundle?): String? {
        fun a(k: String) = args?.getString(k).orEmpty()
        return when (template) {
            HOME -> HOME
            PROJECT -> project(a("host"), a("projectId"), a("tab").ifEmpty { ProjectTab.CHATS })
            CHAT -> chat(a("host"), a("projectId"), a("sessionId"))
            else -> null
        }
    }
}

private val RESTORE_PATTERN = Regex("^r/([^/]+)/p/([^/?]+)(?:\\?tab=([^/&]*))?(?:/s/([^/?]+))?$")

// DESIGN.md Motion: forward = 10% slide + fade (fade 200ms after 60ms), 280ms EaseOut, old screen
// fades out in 100ms; back mirrors it, faster (240/200ms). Nothing over 300ms.
private val forwardEnter: AnimatedContentTransitionScope<NavBackStackEntry>.() -> EnterTransition = {
    slideInHorizontally(tween(RelayMotion.DurationScreen, easing = RelayMotion.EaseOut)) { it / 10 } +
        fadeIn(tween(200, delayMillis = 60, easing = RelayMotion.EaseOut))
}
private val forwardExit: AnimatedContentTransitionScope<NavBackStackEntry>.() -> ExitTransition = {
    fadeOut(tween(100, easing = RelayMotion.EaseOut))
}
private val backEnter: AnimatedContentTransitionScope<NavBackStackEntry>.() -> EnterTransition = {
    fadeIn(tween(200, easing = RelayMotion.EaseOut))
}
private val backExit: AnimatedContentTransitionScope<NavBackStackEntry>.() -> ExitTransition = {
    slideOutHorizontally(tween(240, easing = RelayMotion.EaseOut)) { it / 10 } +
        fadeOut(tween(200, easing = RelayMotion.EaseOut))
}

private data class Initial(val runners: List<KnownRunner>, val lastRoute: String?, val currentHost: String?)

/**
 * App navigation (DESIGN.md "Navigation & flow"):
 * Home (current runner) → Project (tabs) → Chat; Home's switcher → Manage runners.
 * [restoreLastRoute] = cold launch without a deep link: re-open the last Project/Chat.
 */
@Composable
fun RelayNavHost(
    runnersRepository: KnownRunnersRepository,
    prefs: AppPrefsRepository,
    restoreLastRoute: Boolean,
) {
    // Wait for the first DataStore read so the start destination is decided once, correctly.
    val initial by produceState<Initial?>(null) {
        value = Initial(runnersRepository.runners.first(), prefs.lastRoute.first(), prefs.currentRunner.first())
    }
    val init = initial ?: return

    val navController = rememberNavController()
    val scope = rememberCoroutineScope()
    val runners by runnersRepository.runners.collectAsState(initial = init.runners)
    val currentHost by prefs.currentRunner.collectAsState(initial = init.currentHost)
    val lastProvider by prefs.lastProvider.collectAsState(initial = AppPrefsRepository.DEFAULT_PROVIDER)
    // Runner selection is always the root, so Back from Home lands on it (then exits the app).
    // With runners paired, Home is pushed on top at launch.
    val startDestination = Routes.RUNNERS

    fun runnerFor(entry: NavBackStackEntry): KnownRunner? {
        val host = entry.arguments?.getString("host").orEmpty()
        return runners.find { it.hostname == host }
    }

    // Persist the last Home/Project/Chat route.
    DisposableEffect(navController) {
        val listener = androidx.navigation.NavController.OnDestinationChangedListener { _, dest, args ->
            Routes.restorable(dest.route, args)?.let { r -> scope.launch { prefs.setLastRoute(r) } }
        }
        navController.addOnDestinationChangedListener(listener)
        onDispose { navController.removeOnDestinationChangedListener(listener) }
    }

    // Once per launch: push Home over the runner list, then (cold launch only) restore the last
    // Project/Chat. Skipped when a push deep link already opened a chat. A stale project is
    // caught by ProjectViewModel.missing.
    var restored by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(Unit) {
        if (restored) return@LaunchedEffect
        restored = true
        if (init.runners.isEmpty() || navController.currentDestination?.route != Routes.RUNNERS) return@LaunchedEffect
        navController.navigate(Routes.HOME)
        if (!restoreLastRoute) return@LaunchedEffect
        val m = RESTORE_PATTERN.find(init.lastRoute.orEmpty()) ?: return@LaunchedEffect
        val host = Uri.decode(m.groupValues[1])
        if (init.runners.none { it.hostname == host }) return@LaunchedEffect
        val projectId = Uri.decode(m.groupValues[2])
        val tab = m.groupValues[3].ifEmpty { ProjectTab.CHATS }
        val sessionId = m.groupValues[4]
        prefs.setCurrentRunner(host)
        navController.navigate(Routes.project(host, projectId, Uri.decode(tab)))
        if (sessionId.isNotEmpty()) navController.navigate(Routes.chat(host, projectId, Uri.decode(sessionId)))
    }

    NavHost(
        navController = navController,
        startDestination = startDestination,
        enterTransition = forwardEnter,
        exitTransition = forwardExit,
        popEnterTransition = backEnter,
        popExitTransition = backExit,
    ) {
        composable(Routes.HOME) {
            val runner = runners.find { it.hostname == currentHost } ?: runners.firstOrNull()
            if (runner == null) {
                // Last runner removed - back to the runner list (the root).
                LaunchedEffect(Unit) { goRunners(navController) }
                return@composable
            }
            val context = LocalContext.current
            val vm: HomeViewModel = viewModel(
                factory = viewModelFactory { initializer { HomeViewModel(ScheduleRepository(context)) } },
            )
            HomeScreen(
                vm = vm,
                runner = runner,
                runners = runners,
                lastProvider = lastProvider,
                onSwitchRunner = { r -> scope.launch { prefs.setCurrentRunner(r.hostname) } },
                onManageRunners = { goRunners(navController) },
                onUsage = { navController.navigate(Routes.usage(runner.hostname)) },
                onSchedule = { navController.navigate(Routes.schedule(runner.hostname)) },
                onOpenProject = { pid, tab -> navController.navigate(Routes.project(runner.hostname, pid, tab)) },
                onOpenChat = { pid, sid -> navController.navigate(Routes.chat(runner.hostname, pid, sid)) },
                onPickFolder = { navController.navigate(Routes.folderPicker(runner.hostname)) },
            )
        }

        composable(Routes.RUNNERS) {
            val canGoBack = navController.previousBackStackEntry != null
            RunnerListScreen(
                repository = runnersRepository,
                onRunnerSelected = { runner ->
                    scope.launch { prefs.setCurrentRunner(runner.hostname) }
                    goHome(navController)
                },
                onUsage = { runner -> navController.navigate(Routes.usage(runner.hostname)) },
                onSchedule = { runner -> navController.navigate(Routes.schedule(runner.hostname)) },
                onBack = if (canGoBack) ({ navController.popBackStack() }) else null,
            )
        }

        composable(
            route = Routes.USAGE,
            arguments = listOf(navArgument("host") { type = NavType.StringType }),
        ) { entry ->
            val runner = runnerFor(entry) ?: return@composable UnknownRunnerPlaceholder()
            val vm: UsageViewModel = viewModel(factory = viewModelFactory { initializer { UsageViewModel(runner) } })
            val costs by remember(runner.hostname) { prefs.usageCosts(runner.hostname) }
                .collectAsState(initial = AppPrefsRepository.UsageCosts())
            UsageScreen(
                vm = vm,
                title = runner.label,
                costs = costs,
                onCostsChange = { c -> scope.launch { prefs.setUsageCosts(runner.hostname, c) } },
                onBack = { navController.popBackStack() },
            )
        }

        composable(
            route = Routes.SCHEDULE,
            arguments = listOf(navArgument("host") { type = NavType.StringType }),
        ) { entry ->
            val runner = runnerFor(entry) ?: return@composable UnknownRunnerPlaceholder()
            val context = LocalContext.current
            val vm: ScheduleViewModel = viewModel(
                factory = viewModelFactory { initializer { ScheduleViewModel(runner, ScheduleRepository(context)) } },
            )
            val changed by entry.savedStateHandle.getStateFlow(SCHEDULE_CHANGED, false).collectAsState()
            LaunchedEffect(changed) {
                if (changed) {
                    entry.savedStateHandle[SCHEDULE_CHANGED] = false
                    vm.refresh()
                }
            }
            ScheduleScreen(
                vm = vm,
                title = runner.label,
                onBack = { navController.popBackStack() },
                onOpenEditor = { bookingId, date ->
                    navController.navigate(Routes.scheduleEdit(runner.hostname, bookingId, date))
                },
            )
        }

        composable(
            route = Routes.SCHEDULE_EDIT,
            arguments = listOf(
                navArgument("host") { type = NavType.StringType },
                navArgument("bookingId") { type = NavType.StringType; nullable = true; defaultValue = null },
                navArgument("date") { type = NavType.StringType; nullable = true; defaultValue = null },
            ),
        ) { entry ->
            val runner = runnerFor(entry) ?: return@composable UnknownRunnerPlaceholder()
            val context = LocalContext.current
            val bookingId = entry.arguments?.getString("bookingId")
            val date = entry.arguments?.getString("date")
            val vm: BookingEditorViewModel = viewModel(
                factory = viewModelFactory {
                    initializer { BookingEditorViewModel(runner, ScheduleRepository(context), bookingId, date) }
                },
            )
            BookingEditorScreen(
                vm = vm,
                onBack = { navController.popBackStack() },
                onDone = {
                    // Tell the grid to re-fetch (status line + weeks outside the cache).
                    navController.previousBackStackEntry?.savedStateHandle?.set(SCHEDULE_CHANGED, true)
                    navController.popBackStack()
                },
            )
        }

        composable(
            route = Routes.FOLDER_PICKER,
            arguments = listOf(navArgument("host") { type = NavType.StringType }),
        ) { entry ->
            val runner = runnerFor(entry) ?: return@composable UnknownRunnerPlaceholder()
            FolderPickerScreen(
                runner = runner,
                onRegistered = { project ->
                    // Picking a folder IS the create action - go straight into the new project.
                    navController.navigate(Routes.project(runner.hostname, project.id)) {
                        popUpTo(Routes.HOME)
                    }
                },
                onBack = { navController.popBackStack() },
            )
        }

        composable(
            route = Routes.PROJECT,
            arguments = listOf(
                navArgument("host") { type = NavType.StringType },
                navArgument("projectId") { type = NavType.StringType },
                navArgument("tab") { type = NavType.StringType; defaultValue = ProjectTab.CHATS },
            ),
        ) { entry ->
            val runner = runnerFor(entry) ?: return@composable UnknownRunnerPlaceholder()
            val projectId = entry.arguments?.getString("projectId").orEmpty()
            KeepCurrentRunner(runner.hostname, currentHost, prefs)
            val vm: ProjectViewModel = viewModel(factory = viewModelFactory { initializer { ProjectViewModel(runner, projectId) } })
            val filesVm: FileBrowserViewModel = viewModel(factory = viewModelFactory { initializer { FileBrowserViewModel(runner, projectId) } })
            val containersVm: ContainersViewModel = viewModel(factory = viewModelFactory { initializer { ContainersViewModel(runner, projectId) } })
            val gitVm: GitViewModel = viewModel(factory = viewModelFactory { initializer { GitViewModel(runner, projectId) } })
            ProjectScreen(
                vm = vm,
                filesVm = filesVm,
                containersVm = containersVm,
                gitVm = gitVm,
                initialTab = entry.arguments?.getString("tab") ?: ProjectTab.CHATS,
                lastProvider = lastProvider,
                onProviderUsed = { p -> scope.launch { prefs.setLastProvider(p) } },
                onBack = { if (!navController.popBackStack()) goHome(navController) },
                onOpenChat = { sid -> navController.navigate(Routes.chat(runner.hostname, projectId, sid)) },
                onMissing = { goHome(navController) },
            )
        }

        composable(
            route = Routes.CHAT,
            arguments = listOf(
                navArgument("host") { type = NavType.StringType },
                navArgument("projectId") { type = NavType.StringType },
                navArgument("sessionId") { type = NavType.StringType },
            ),
            deepLinks = listOf(navDeepLink { uriPattern = Routes.CHAT_DEEP_LINK }),
        ) { entry ->
            val runner = runnerFor(entry) ?: return@composable UnknownRunnerPlaceholder()
            KeepCurrentRunner(runner.hostname, currentHost, prefs)
            val projectId = entry.arguments?.getString("projectId").orEmpty()
            val sessionId = entry.arguments?.getString("sessionId").orEmpty()
            val vm: ChatViewModel = viewModel(factory = viewModelFactory { initializer { ChatViewModel(runner, projectId, sessionId) } })
            ChatScreen(
                vm = vm,
                onBack = { if (!navController.popBackStack()) goHome(navController) },
                // Pushed on top of the chat, so Back returns here.
                onOpenFiles = { navController.navigate(Routes.project(runner.hostname, projectId, ProjectTab.FILES)) },
            )
        }
    }
}

/** Back to Home, reusing it if it's on the stack (keeps its ViewModel), else right above the runner list. */
private fun goHome(navController: NavHostController) {
    if (!navController.popBackStack(Routes.HOME, inclusive = false)) {
        navController.navigate(Routes.HOME) {
            popUpTo(Routes.RUNNERS)
        }
    }
}

/** Back to the runner list - always the root of the back stack. */
private fun goRunners(navController: NavHostController) {
    if (!navController.popBackStack(Routes.RUNNERS, inclusive = false)) {
        navController.navigate(Routes.RUNNERS) { popUpTo(navController.graph.id) { inclusive = true } }
    }
}

/** Entering a runner's project/chat (incl. via restore or a push) makes it Home's runner. */
@Composable
private fun KeepCurrentRunner(host: String, currentHost: String?, prefs: AppPrefsRepository) {
    LaunchedEffect(host) { if (currentHost != host) prefs.setCurrentRunner(host) }
}

@Composable
private fun UnknownRunnerPlaceholder() {
    Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        Text("Unknown runner (it may have been removed).")
    }
}

/** savedStateHandle key: the booking editor saved/deleted something, the grid should refresh. */
private const val SCHEDULE_CHANGED = "schedule_changed"
