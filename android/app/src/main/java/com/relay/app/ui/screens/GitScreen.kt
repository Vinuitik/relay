package com.relay.app.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.ArrowDownward
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.relay.app.model.GitBranch
import com.relay.app.model.GitCommit
import com.relay.app.model.GitDiff
import com.relay.app.model.GitFile
import com.relay.app.model.GitStatus
import com.relay.app.model.KnownRunner
import com.relay.app.network.GitCommitRequest
import com.relay.app.network.GitPathsRequest
import com.relay.app.network.GitSwitchRequest
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.ui.components.EmptyState
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.components.RefreshableBox
import com.relay.app.ui.components.SignInSheet
import com.relay.app.ui.components.SkeletonRows
import com.relay.app.ui.theme.codeSmall
import com.relay.app.ui.theme.statusColors
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import org.json.JSONObject
import retrofit2.HttpException
import java.time.Instant
import java.time.ZoneOffset

/**
 * Git tab state (shared/API.md "Git"), scoped to the Project back-stack entry like the other tabs.
 * Local actions (stage/commit/switch) answer with fresh status; push/pull/fetch run in the
 * background on the runner, so [pollWhileVisible] polls fast while one is in flight.
 */
class GitViewModel(val runner: KnownRunner, private val projectId: String) : ViewModel() {
    private val api = RelayApiClient.forRunner(runner)

    var status by mutableStateOf<GitStatus?>(null)
        private set
    var log by mutableStateOf<List<GitCommit>>(emptyList())
        private set
    var branches by mutableStateOf<List<GitBranch>?>(null)
        private set
    var loadError by mutableStateOf<String?>(null)
        private set
    /** A local action git refused (its own message), shown under the branch card until the next action. */
    var actionError by mutableStateOf<String?>(null)
        private set
    var busy by mutableStateOf(false)
        private set
    var refreshing by mutableStateOf(false)
        private set
    var message by mutableStateOf("")

    /** Open diff (null = list view). */
    var diff by mutableStateOf<GitDiff?>(null)
        private set
    var diffLoading by mutableStateOf(false)
        private set

    val operating: Boolean get() = !status?.operation.isNullOrEmpty() && status?.operation != "local"

    private var lastLogHead: String? = null

    suspend fun refresh() {
        try {
            val s = api.gitStatus(projectId)
            val opJustEnded = operating && s.operation.isEmpty()
            status = s
            loadError = null
            // Log only changes with HEAD; refetch on first load, after an op, or when ahead/behind moved.
            val head = "${s.branch}:${s.ahead}:${s.behind}"
            if (s.isRepo && (head != lastLogHead || opJustEnded)) {
                lastLogHead = head
                log = runCatching { api.gitLog(projectId) }.getOrDefault(log)
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            loadError = friendlyErrorMessage(e, runner)
        }
    }

    /** Runs only while the tab is composed (caller's LaunchedEffect). */
    suspend fun pollWhileVisible() {
        while (true) {
            refresh()
            delay(if (operating) 1_500 else 10_000)
        }
    }

    /** Pull-to-refresh: re-read status and also fetch, so behind counts are real. */
    fun pullToRefresh(): Job = viewModelScope.launch {
        refreshing = true
        refresh()
        if (status?.isRepo == true && status?.upstream != null && !operating) fetch()
        refreshing = false
    }

    fun loadBranches() = viewModelScope.launch {
        branches = null
        branches = runCatching { api.gitBranches(projectId) }.getOrElse { emptyList() }
    }

    fun stage(f: GitFile) = local { api.gitStage(projectId, GitPathsRequest(listOf(f.path))) }
    fun unstage(f: GitFile) = local { api.gitUnstage(projectId, GitPathsRequest(listOf(f.path))) }
    fun stageAll() = local { api.gitStage(projectId, GitPathsRequest(all = true)) }
    fun unstageAll() = local { api.gitUnstage(projectId, GitPathsRequest(all = true)) }

    /** Commits what's staged; with nothing staged, stages everything first (the button says so). */
    fun commit() {
        val msg = message.trim()
        if (msg.isEmpty()) return
        val stageFirst = status?.files?.none { it.staged } == true
        local {
            if (stageFirst) api.gitStage(projectId, GitPathsRequest(all = true))
            api.gitCommit(projectId, GitCommitRequest(msg)).also { message = "" }
        }
    }

    fun switchTo(b: GitBranch) = local { api.gitSwitch(projectId, GitSwitchRequest(b.name, remote = b.remote)) }
    fun createBranch(name: String) = local { api.gitSwitch(projectId, GitSwitchRequest(name.trim(), create = true)) }

    fun push() = network { api.gitPush(projectId) }
    fun pull() = network { api.gitPull(projectId) }
    fun fetch() = network { api.gitFetch(projectId) }

    /** Re-runs the last network op - after signing in from the "needs sign-in" card. */
    fun retryLast() = when (status?.lastOp) {
        "pull" -> pull()
        "fetch" -> fetch()
        else -> push()
    }

    fun openDiff(f: GitFile, staged: Boolean) = viewModelScope.launch {
        diffLoading = true
        diff = GitDiff(f.path, staged, "")
        diff = try {
            api.gitDiff(projectId, f.path, staged)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            GitDiff(f.path, staged, "Couldn't load the diff: ${explain(e)}")
        }
        diffLoading = false
    }

    fun closeDiff() { diff = null }

    private fun local(call: suspend () -> GitStatus) {
        if (busy) return
        busy = true
        actionError = null
        viewModelScope.launch {
            try {
                status = call()
                lastLogHead = null
                refresh()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                actionError = explain(e)
                refresh()
            } finally {
                busy = false
            }
        }
    }

    private fun network(call: suspend () -> GitStatus) {
        actionError = null
        viewModelScope.launch {
            try {
                status = call()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                actionError = explain(e)
            }
        }
    }

    /** git's own message for refusals (`{error}` body), else the generic connection wording. */
    private fun explain(e: Exception): String {
        if (e is HttpException && e.code() in 400..499) {
            val reason = runCatching {
                JSONObject(e.response()?.errorBody()?.string().orEmpty()).optString("error")
            }.getOrNull()
            if (!reason.isNullOrBlank()) return reason
        }
        return friendlyErrorMessage(e, runner)
    }
}

/**
 * Git tab body: branch card (branch picker, ↑/↓, Fetch/Pull/Push, last result) → commit box →
 * Staged / Changes lists (checkbox = stage/unstage, tap = diff) → recent commits. A diff replaces
 * the list in place; Back closes it.
 */
@Composable
fun GitContent(vm: GitViewModel) {
    LaunchedEffect(vm) { vm.pollWhileVisible() }

    val st = vm.status
    if (st == null) {
        val err = vm.loadError
        if (err != null) FullScreenError(err, onRetry = { vm.pullToRefresh() }) else SkeletonRows(4)
        return
    }
    if (!st.isRepo) {
        EmptyState("This project isn't a git repository.", actionLabel = "Refresh", onAction = { vm.pullToRefresh() })
        return
    }
    vm.diff?.let { d ->
        DiffView(d, loading = vm.diffLoading, onClose = vm::closeDiff)
        return
    }

    val staged = st.files.filter { it.staged }
    val unstaged = st.files.filter { it.unstaged }
    var signInFor by remember { mutableStateOf<String?>(null) }

    RefreshableBox(refreshing = vm.refreshing, onRefresh = vm::pullToRefresh) {
        LazyColumn(Modifier.fillMaxSize(), contentPadding = androidx.compose.foundation.layout.PaddingValues(bottom = 24.dp)) {
            item(key = "branch") { BranchCard(vm, st, onSignIn = { signInFor = it }) }
            item(key = "commit") { CommitBox(vm, stagedCount = staged.size, changedCount = st.files.size) }
            if (st.files.isEmpty()) {
                item(key = "clean") {
                    Text(
                        "Nothing to commit - working tree clean.",
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(16.dp),
                    )
                }
            }
            if (staged.isNotEmpty()) {
                item(key = "stagedHeader") {
                    SectionHeader("Staged (${staged.size})", "Unstage all", enabled = !vm.busy, onAction = vm::unstageAll)
                }
                items(staged, key = { "s:" + it.path }) { f ->
                    FileRow(f, checked = true, letter = f.index, enabled = !vm.busy, onToggle = { vm.unstage(f) }, onOpen = { vm.openDiff(f, staged = true) })
                }
            }
            if (unstaged.isNotEmpty()) {
                item(key = "changesHeader") {
                    SectionHeader("Changes (${unstaged.size})", "Stage all", enabled = !vm.busy, onAction = vm::stageAll)
                }
                items(unstaged, key = { "u:" + it.path }) { f ->
                    FileRow(f, checked = false, letter = if (f.untracked) "U" else f.worktree, enabled = !vm.busy, onToggle = { vm.stage(f) }, onOpen = { vm.openDiff(f, staged = false) })
                }
            }
            if (st.truncated) {
                item(key = "truncated") {
                    Text(
                        "Only the first ${st.files.size} changed files are shown.",
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                    )
                }
            }
            if (vm.log.isNotEmpty()) {
                item(key = "logHeader") { SectionHeader("Recent commits", null, enabled = false, onAction = {}) }
                items(vm.log.withIndex().toList(), key = { "c:" + it.value.hash }) { (i, c) ->
                    CommitRow(c, unpushed = st.upstream != null && i < st.ahead)
                }
            }
        }
    }

    signInFor?.let { provider ->
        SignInSheet(
            runner = vm.runner,
            provider = provider,
            providerName = providerName(provider),
            doneHint = "retrying",
            onDismiss = { signInFor = null },
            onSignedIn = {
                signInFor = null
                vm.retryLast()
            },
        )
    }
}

private fun providerName(id: String) = when (id) {
    "github" -> "GitHub"
    "gcloud" -> "Google Cloud"
    "claude" -> "Claude"
    else -> id
}

@Composable
private fun BranchCard(vm: GitViewModel, st: GitStatus, onSignIn: (String) -> Unit) {
    var menuOpen by remember { mutableStateOf(false) }
    var newBranchOpen by remember { mutableStateOf(false) }
    val operating = vm.operating

    Card(
        modifier = Modifier.fillMaxWidth().padding(16.dp).animateContentSize(),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainer),
    ) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Box {
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    modifier = Modifier.clickable(enabled = !vm.busy && !operating) {
                        menuOpen = true
                        vm.loadBranches()
                    },
                ) {
                    Text(
                        if (st.detached) "Detached HEAD" else st.branch,
                        style = MaterialTheme.typography.titleLarge,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f, fill = false),
                    )
                    Icon(Icons.Filled.ArrowDropDown, contentDescription = "Switch branch")
                }
                BranchMenu(
                    expanded = menuOpen,
                    branches = vm.branches,
                    onDismiss = { menuOpen = false },
                    onPick = { menuOpen = false; vm.switchTo(it) },
                    onNew = { menuOpen = false; newBranchOpen = true },
                )
            }
            Text(
                when {
                    st.upstream == null && st.remoteUrl == null -> "No remote"
                    st.upstream == null -> "Not published yet"
                    else -> "${st.upstream} · ↑${st.ahead} ↓${st.behind}"
                },
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.fillMaxWidth()) {
                OutlinedButton(onClick = vm::fetch, enabled = !operating && st.remoteUrl != null) {
                    Icon(Icons.Filled.Refresh, contentDescription = null, modifier = Modifier.size(18.dp))
                    Text("Fetch", modifier = Modifier.padding(start = 6.dp))
                }
                OutlinedButton(onClick = vm::pull, enabled = !operating && st.upstream != null) {
                    Icon(Icons.Filled.ArrowDownward, contentDescription = null, modifier = Modifier.size(18.dp))
                    Text(if (st.behind > 0) "Pull ${st.behind}" else "Pull", modifier = Modifier.padding(start = 6.dp))
                }
                Button(onClick = vm::push, enabled = !operating && !st.detached && st.remoteUrl != null) {
                    Icon(Icons.Filled.ArrowUpward, contentDescription = null, modifier = Modifier.size(18.dp))
                    Text(
                        when {
                            st.upstream == null -> "Publish"
                            st.ahead > 0 -> "Push ${st.ahead}"
                            else -> "Push"
                        },
                        modifier = Modifier.padding(start = 6.dp),
                    )
                }
            }
            Box(Modifier.fillMaxWidth().height(4.dp)) {
                if (operating) LinearProgressIndicator(Modifier.fillMaxWidth())
            }
            OpResult(vm, st, onSignIn)
        }
    }

    if (newBranchOpen) NewBranchDialog(onDismiss = { newBranchOpen = false }, onCreate = { newBranchOpen = false; vm.createBranch(it) })
}

/** "Pushing…", or the last op's result: success line, git's error, and the sign-in fix if any. */
@Composable
private fun OpResult(vm: GitViewModel, st: GitStatus, onSignIn: (String) -> Unit) {
    val success = MaterialTheme.statusColors.success
    val opLabel = when (st.operation) {
        "pushing" -> "Pushing…"
        "pulling" -> "Pulling…"
        "fetching" -> "Fetching…"
        else -> null
    }
    when {
        opLabel != null -> Text(opLabel, style = MaterialTheme.typography.bodyMedium)
        vm.actionError != null -> ErrorText(vm.actionError!!)
        !st.lastError.isNullOrBlank() -> {
            val title = when (st.lastOp) { "pull" -> "Pull failed"; "fetch" -> "Fetch failed"; else -> "Push failed" }
            if (st.authFailed) {
                ErrorText(
                    "$title: ${if (st.needsAuth != null) "the runner isn't signed in to ${providerName(st.needsAuth)}." else "the runner has no working credentials for this remote (set up an ssh key or token on that PC)."}",
                )
                st.needsAuth?.let { p ->
                    FilledTonalButton(onClick = { onSignIn(p) }, modifier = Modifier.fillMaxWidth()) {
                        Text("Sign in to ${providerName(p)} and retry")
                    }
                }
                Text(st.lastError, style = MaterialTheme.typography.codeSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            } else {
                ErrorText("$title: ${st.lastError}")
            }
        }
        st.lastOp != null -> Text(
            when (st.lastOp) { "pull" -> "Pulled."; "fetch" -> "Fetched."; else -> "Pushed." },
            style = MaterialTheme.typography.bodyMedium,
            color = success.fg,
        )
    }
}

@Composable
private fun ErrorText(text: String) {
    Text(text, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.error)
}

@Composable
private fun BranchMenu(
    expanded: Boolean,
    branches: List<GitBranch>?,
    onDismiss: () -> Unit,
    onPick: (GitBranch) -> Unit,
    onNew: () -> Unit,
) {
    DropdownMenu(expanded = expanded, onDismissRequest = onDismiss) {
        DropdownMenuItem(text = { Text("New branch…") }, onClick = onNew)
        HorizontalDivider()
        when {
            branches == null -> DropdownMenuItem(
                text = { CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp) },
                onClick = {},
                enabled = false,
            )
            else -> branches.forEach { b ->
                DropdownMenuItem(
                    text = {
                        Text(
                            b.name + if (b.current) "  ✓" else "",
                            fontWeight = if (b.current) FontWeight.SemiBold else null,
                            color = if (b.remote) MaterialTheme.colorScheme.onSurfaceVariant else Color.Unspecified,
                        )
                    },
                    enabled = !b.current,
                    onClick = { onPick(b) },
                )
            }
        }
    }
}

@Composable
private fun NewBranchDialog(onDismiss: () -> Unit, onCreate: (String) -> Unit) {
    var name by remember { mutableStateOf("") }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("New branch") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("From the current commit. Your uncommitted changes come along.", style = MaterialTheme.typography.bodyMedium)
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it.replace(' ', '-') },
                    singleLine = true,
                    label = { Text("Name") },
                    textStyle = MaterialTheme.typography.codeSmall,
                    keyboardOptions = KeyboardOptions(autoCorrect = false, capitalization = KeyboardCapitalization.None),
                )
            }
        },
        confirmButton = { TextButton(onClick = { onCreate(name) }, enabled = name.isNotBlank()) { Text("Create") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun CommitBox(vm: GitViewModel, stagedCount: Int, changedCount: Int) {
    Column(Modifier.padding(horizontal = 16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        OutlinedTextField(
            value = vm.message,
            onValueChange = { vm.message = it },
            modifier = Modifier.fillMaxWidth(),
            placeholder = { Text("Commit message") },
            maxLines = 4,
            enabled = !vm.busy,
        )
        Button(
            onClick = vm::commit,
            enabled = !vm.busy && vm.message.isNotBlank() && changedCount > 0,
            modifier = Modifier.fillMaxWidth(),
        ) {
            if (vm.busy) {
                CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp, color = MaterialTheme.colorScheme.onPrimary)
            } else {
                Text(
                    when {
                        stagedCount > 0 -> "Commit $stagedCount staged"
                        changedCount > 0 -> "Stage all & commit"
                        else -> "Commit"
                    },
                )
            }
        }
    }
}

@Composable
private fun SectionHeader(title: String, action: String?, enabled: Boolean, onAction: () -> Unit) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(start = 16.dp, end = 8.dp, top = 16.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(title, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
        if (action != null) TextButton(onClick = onAction, enabled = enabled) { Text(action) }
    }
}

@Composable
private fun FileRow(f: GitFile, checked: Boolean, letter: String, enabled: Boolean, onToggle: () -> Unit, onOpen: () -> Unit) {
    val name = f.path.substringAfterLast('/')
    val dir = f.path.substringBeforeLast('/', "")
    Row(
        modifier = Modifier.fillMaxWidth().clickable(onClick = onOpen).padding(end = 16.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Checkbox(checked = checked, onCheckedChange = { onToggle() }, enabled = enabled && !f.conflicted)
        Column(Modifier.weight(1f)) {
            Text(name, style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
            val sub = listOfNotNull(dir.takeIf { it.isNotEmpty() }, f.origPath?.let { "from $it" }, "conflict".takeIf { f.conflicted })
            if (sub.isNotEmpty()) {
                Text(
                    sub.joinToString(" · "),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }
        StatusLetter(if (f.conflicted) "!" else letter)
    }
}

@Composable
private fun StatusLetter(letter: String) {
    val c = MaterialTheme.statusColors
    val color = when (letter) {
        "A", "U", "?" -> c.success.fg
        "D" -> c.error.fg
        "!" -> c.error.fg
        else -> c.warning.fg
    }
    Text(
        letter,
        style = MaterialTheme.typography.codeSmall,
        fontWeight = FontWeight.Bold,
        color = color,
        modifier = Modifier.width(16.dp),
    )
}

@Composable
private fun CommitRow(c: GitCommit, unpushed: Boolean) {
    Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(c.subject, style = MaterialTheme.typography.bodyMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
            Text(
                "${c.short} · ${c.author} · ${com.relay.app.ui.components.relativeTime(Instant.ofEpochSecond(c.time).atOffset(ZoneOffset.UTC).toString())}",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
            )
        }
        if (unpushed) {
            Text(
                "not pushed",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.statusColors.warning.fg,
                modifier = Modifier.padding(start = 8.dp),
            )
        }
    }
}

/** One file's patch: + lines green, - lines red, hunk headers dim. Back closes. */
@Composable
private fun DiffView(d: GitDiff, loading: Boolean, onClose: () -> Unit) {
    BackHandler(onBack = onClose)
    val c = MaterialTheme.statusColors
    // Drop the diff/index/---/+++ header: the path is already in the title bar.
    val lines = remember(d.diff) {
        val all = d.diff.lines()
        val firstHunk = all.indexOfFirst { it.startsWith("@@") }
        if (firstHunk > 0) all.drop(firstHunk) else all
    }
    Column(Modifier.fillMaxSize()) {
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth().padding(end = 16.dp)) {
            IconButton(onClick = onClose) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Close diff") }
            Column(Modifier.weight(1f)) {
                Text(d.path, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(
                    if (d.staged) "Staged changes" else "Unstaged changes",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
        HorizontalDivider()
        when {
            loading -> SkeletonRows(4)
            d.diff.isBlank() -> EmptyState("No changes to show.")
            else -> LazyColumn(Modifier.fillMaxSize()) {
                items(lines.size) { i ->
                    val ln = lines[i]
                    val (bg, fg) = when {
                        ln.startsWith("+") -> c.success.chipBg to c.success.chipOn
                        ln.startsWith("-") -> c.error.chipBg to c.error.chipOn
                        ln.startsWith("@@") -> Color.Transparent to MaterialTheme.colorScheme.primary
                        else -> Color.Transparent to MaterialTheme.colorScheme.onSurface
                    }
                    Text(
                        ln.ifEmpty { " " },
                        style = MaterialTheme.typography.codeSmall,
                        color = fg,
                        modifier = Modifier.fillMaxWidth().background(bg).padding(horizontal = 8.dp, vertical = 1.dp),
                    )
                }
                if (d.truncated) {
                    item {
                        Text(
                            "Diff truncated - too large to show in full.",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            modifier = Modifier.padding(16.dp),
                        )
                    }
                }
                item { Spacer(Modifier.height(24.dp)) }
            }
        }
    }
}
