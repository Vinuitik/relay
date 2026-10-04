package com.relay.app.ui.screens

import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.scaleIn
import androidx.compose.animation.shrinkVertically
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.gestures.scrollBy
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.ArrowDownward
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Stop
import androidx.compose.material.icons.outlined.ErrorOutline
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.RadioButton
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import com.relay.app.model.ConfigOption
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledIconButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.SnackbarResult
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.relay.app.model.Message
import com.relay.app.model.PendingPermission
import com.relay.app.model.Session
import com.relay.app.ui.components.ClaudeSignInSheet
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.components.SkeletonRows
import com.relay.app.ui.theme.FullShape
import com.relay.app.ui.theme.RelayMotion
import com.relay.app.ui.theme.RelayStatus
import com.relay.app.ui.theme.codeSmall
import com.relay.app.ui.theme.statusColors
import kotlinx.coroutines.launch

private fun isTool(m: Message) = m.role == "tool"

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ChatScreen(
    vm: ChatViewModel,
    onBack: () -> Unit,
    onOpenFiles: () -> Unit,
) {
    val session = vm.session
    val haptics = LocalHapticFeedback.current
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    var signInOpen by remember { mutableStateOf(false) }

    LaunchedEffect(vm) {
        vm.events.collect { ev ->
            val result = snackbar.showSnackbar(ev.message, actionLabel = ev.retry?.let { "Retry" }, withDismissAction = ev.retry == null)
            if (result == SnackbarResult.ActionPerformed) ev.retry?.invoke()
        }
    }

    val state = session?.state
    val working = state == "busy" || state == "waiting"

    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        topBar = {
            TopAppBar(
                title = { ChatTitle(session, vm.projectName) },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                actions = {
                    if (session != null && !session.modes.isNullOrEmpty()) ModePicker(session, vm::setMode)
                    ChatOverflow(onOpenFiles)
                },
            )
        },
    ) { padding ->
        Column(modifier = Modifier.padding(padding).fillMaxSize()) {
            Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
                when {
                    session != null -> Transcript(vm, session, onSignIn = { signInOpen = true })
                    vm.loadError != null -> FullScreenError(vm.loadError.orEmpty(), onRetry = vm::retryLoad)
                    else -> SkeletonRows(count = 4)
                }
            }

            // Fixed 4dp slot: the bar fades, the layout never jumps.
            val busyAlpha by animateFloatAsState(
                targetValue = if (state == "busy" || vm.sending) 1f else 0f,
                animationSpec = tween(200, easing = RelayMotion.EaseOut),
                label = "busyBar",
            )
            Box(Modifier.fillMaxWidth().height(4.dp)) {
                if (busyAlpha > 0f) LinearProgressIndicator(Modifier.fillMaxSize().alpha(busyAlpha))
            }

            StickyPermission(
                session = session,
                onChoose = { optionId ->
                    haptics.performHapticFeedback(HapticFeedbackType.LongPress)
                    vm.answerPermission(optionId)
                },
            )

            session?.configOptions?.filterNot { it.isMode }?.takeIf { it.isNotEmpty() }?.let { opts ->
                AgentConfigChip(opts, enabled = !working, onSelect = vm::setConfig)
            }

            Composer(
                vm = vm,
                working = working,
                onSend = {
                    haptics.performHapticFeedback(HapticFeedbackType.TextHandleMove)
                    vm.send()
                },
                onStop = {
                    haptics.performHapticFeedback(HapticFeedbackType.LongPress)
                    vm.cancelTurn()
                },
            )
        }
    }

    // Login-expired card → phone-side sign-in. The card stays in history; on success the sheet
    // closes and the Snackbar says what to do next.
    if (signInOpen) {
        ClaudeSignInSheet(
            runner = vm.runner,
            onDismiss = { signInOpen = false },
            onSignedIn = {
                signInOpen = false
                scope.launch { snackbar.showSnackbar("Signed in - send your message again", withDismissAction = true) }
            },
        )
    }
}

@Composable
private fun ChatTitle(session: Session?, projectName: String?) {
    val modeName = session?.modes?.find { it.id == session.mode }?.name ?: session?.mode
    val subtitle = listOfNotNull(projectName, modeName).filter { it.isNotBlank() }.joinToString(" · ")
    Column {
        Text(session?.displayTitle.orEmpty(), maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (subtitle.isNotEmpty()) {
            Text(
                subtitle,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
    }
}

@Composable
private fun ChatOverflow(onOpenFiles: () -> Unit) {
    var open by remember { mutableStateOf(false) }
    Box {
        IconButton(onClick = { open = true }) { Icon(Icons.Default.MoreVert, contentDescription = "More") }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            DropdownMenuItem(text = { Text("Files") }, onClick = { open = false; onOpenFiles() })
        }
    }
}

/**
 * The transcript. DESIGN.md Motion: items keyed by index; only items appended after the first
 * load slide 8dp up + fade in (180ms). Follow-bottom only while the user is at the bottom:
 * streamed growth sticks without animation, each new item gets one animated scroll; scrolled up
 * → "↓ New" pill instead.
 */
@Composable
private fun Transcript(vm: ChatViewModel, session: Session, onSignIn: () -> Unit) {
    val listState = rememberLazyListState()
    val scope = rememberCoroutineScope()
    val messages = session.messages
    val pending = vm.pendingText
    val count = messages.size + if (pending != null) 1 else 0

    // Items below this index were on screen at first composition - they never animate in.
    var animatedUpTo by remember { mutableIntStateOf(count) }
    var follow by remember { mutableStateOf(true) }
    var autoScrolling by remember { mutableStateOf(false) }
    var newBelow by remember { mutableStateOf(false) }

    // Only user-driven scrolls change `follow`; our own scrolls are ignored.
    LaunchedEffect(listState) {
        snapshotFlow { Triple(listState.isScrollInProgress, listState.canScrollForward, autoScrolling) }
            .collect { (scrolling, canForward, auto) ->
                if (auto) return@collect
                if (scrolling) follow = !canForward else if (!canForward) follow = true
                if (follow) newBelow = false
            }
    }

    LaunchedEffect(listState) {
        var lastCount = -1
        snapshotFlow {
            val n = vm.session?.messages?.size ?: 0
            val total = n + if (vm.pendingText != null) 1 else 0
            Triple(total, vm.session?.messages?.lastOrNull()?.text?.length ?: 0, listState.layoutInfo.viewportSize.height)
        }.collect { (total, _, _) ->
            if (total == 0) return@collect
            when {
                lastCount == -1 -> {
                    listState.scrollToItem(total - 1)
                    listState.scrollBy(STICK_DELTA)
                }
                total > lastCount -> if (follow) {
                    autoScrolling = true
                    try {
                        listState.animateScrollToItem(total - 1)
                    } finally {
                        autoScrolling = false
                    }
                } else {
                    newBelow = true
                }
                follow -> listState.scrollBy(STICK_DELTA) // streaming / viewport shrink: no animation
            }
            lastCount = total
        }
    }

    Box(Modifier.fillMaxSize()) {
        LazyColumn(
            state = listState,
            modifier = Modifier.fillMaxSize(),
            contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp),
        ) {
            items(count = count, key = { it }) { index ->
                val message = messages.getOrNull(index)
                    ?: Message(role = "user", text = pending.orEmpty(), at = "")
                val prev = messages.getOrNull(index - 1)
                // 4dp between consecutive tool rows, 8 between everything else.
                val top = when {
                    index == 0 -> 0.dp
                    prev != null && isTool(prev) && isTool(message) -> 4.dp
                    else -> 8.dp
                }
                val animate = remember { index >= animatedUpTo }
                LaunchedEffect(Unit) { if (index >= animatedUpTo) animatedUpTo = index + 1 }
                AppearOnce(animate, Modifier.padding(top = top)) {
                    when {
                        isTool(message) -> ToolRow(message)
                        message.kind == "quota" || message.kind == "auth" -> ProblemCard(message, onSignIn)
                        message.role == "user" -> MessageActions(message.text, onEdit = { vm.input = it }) {
                            UserBubble(message.text, optimistic = index >= messages.size)
                        }
                        else -> MessageActions(message.text, onEdit = null) {
                            MarkdownText(text = message.text, color = MaterialTheme.colorScheme.onSurface)
                        }
                    }
                }
            }
        }

        AnimatedVisibility(
            visible = newBelow,
            enter = fadeIn(tween(RelayMotion.DurationShort, easing = RelayMotion.EaseOut)),
            exit = fadeOut(tween(100, easing = RelayMotion.EaseOut)),
            modifier = Modifier.align(Alignment.BottomCenter).padding(bottom = 8.dp),
        ) {
            Surface(
                onClick = {
                    newBelow = false
                    follow = true
                    scope.launch {
                        autoScrolling = true
                        try {
                            listState.animateScrollToItem((count - 1).coerceAtLeast(0))
                            listState.scrollBy(STICK_DELTA)
                        } finally {
                            autoScrolling = false
                        }
                    }
                },
                shape = FullShape,
                color = MaterialTheme.colorScheme.secondaryContainer,
                contentColor = MaterialTheme.colorScheme.onSecondaryContainer,
                shadowElevation = 2.dp,
            ) {
                Row(
                    modifier = Modifier.padding(horizontal = 12.dp, vertical = 4.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    Icon(Icons.Default.ArrowDownward, contentDescription = null, modifier = Modifier.size(16.dp))
                    Text("New", style = MaterialTheme.typography.labelLarge)
                }
            }
        }
    }
}

/** Scrolls past the end of the last item; LazyList consumes only what's left, so it's a clamp. */
private const val STICK_DELTA = 100_000f

/** 8dp up + fade, 180ms EaseOut, once - only for items appended after the first load. */
@Composable
private fun AppearOnce(animate: Boolean, modifier: Modifier, content: @Composable () -> Unit) {
    val progress = remember { Animatable(if (animate) 0f else 1f) }
    LaunchedEffect(Unit) {
        if (progress.value < 1f) progress.animateTo(1f, tween(180, easing = RelayMotion.EaseOut))
    }
    val shift = with(LocalDensity.current) { 8.dp.toPx() }
    Box(
        modifier.graphicsLayer {
            alpha = progress.value
            translationY = (1f - progress.value) * shift
        },
    ) { content() }
}

/**
 * Long-press a message → Copy, and for your own messages "Edit & resend" (puts the text back in
 * the composer). One haptic on open.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun MessageActions(text: String, onEdit: ((String) -> Unit)?, content: @Composable () -> Unit) {
    var open by remember { mutableStateOf(false) }
    val clipboard = LocalClipboardManager.current
    val haptics = LocalHapticFeedback.current
    Box(
        Modifier.fillMaxWidth().combinedClickable(
            onClick = {},
            onLongClick = { haptics.performHapticFeedback(HapticFeedbackType.LongPress); open = true },
        ),
    ) {
        content()
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            DropdownMenuItem(
                text = { Text("Copy") },
                onClick = { clipboard.setText(AnnotatedString(text)); open = false },
            )
            if (onEdit != null) {
                DropdownMenuItem(
                    text = { Text("Edit & resend") },
                    onClick = { onEdit(text); open = false },
                )
            }
        }
    }
}

@Composable
private fun UserBubble(text: String, optimistic: Boolean) {
    val alpha by animateFloatAsState(
        targetValue = if (optimistic) 0.6f else 1f,
        animationSpec = tween(RelayMotion.DurationShort, easing = RelayMotion.EaseOut),
        label = "echo",
    )
    Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
        Text(
            text = text,
            color = MaterialTheme.colorScheme.onPrimaryContainer,
            modifier = Modifier
                .widthIn(max = 300.dp)
                .alpha(alpha)
                .background(MaterialTheme.colorScheme.primaryContainer, MaterialTheme.shapes.medium)
                .padding(horizontal = 12.dp, vertical = 8.dp),
        )
    }
}

/** One compact line per agent step: icon (done / failed / running) + title. */
@Composable
private fun ToolRow(message: Message) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Box(Modifier.size(16.dp), contentAlignment = Alignment.Center) {
            when (message.status) {
                "completed" -> Icon(
                    Icons.Default.Check,
                    contentDescription = "Done",
                    tint = MaterialTheme.statusColors[RelayStatus.Success].fg,
                    modifier = Modifier.size(16.dp),
                )
                "failed" -> Icon(
                    Icons.Outlined.ErrorOutline,
                    contentDescription = "Failed",
                    tint = MaterialTheme.colorScheme.error,
                    modifier = Modifier.size(16.dp),
                )
                else -> CircularProgressIndicator(
                    modifier = Modifier.size(12.dp),
                    strokeWidth = 1.5.dp,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
        Text(
            text = message.text.ifBlank { message.toolKind.orEmpty() },
            style = MaterialTheme.typography.codeSmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

/**
 * Signature detail 2: a card that holds the agent until you act, with a 4dp amber leading edge.
 * Only the permission and quota/login cards use it.
 */
@Composable
private fun InterlockCard(container: Color, content: Color, body: @Composable () -> Unit) {
    Surface(
        color = container,
        contentColor = content,
        shape = MaterialTheme.shapes.medium,
        modifier = Modifier.fillMaxWidth(),
    ) {
        Row(Modifier.height(IntrinsicSize.Min)) {
            Box(Modifier.width(4.dp).fillMaxHeight().background(MaterialTheme.statusColors[RelayStatus.Warning].fg))
            Column(Modifier.weight(1f).padding(16.dp)) { body() }
        }
    }
}

/**
 * Permission request, sticky above the composer (always in thumb reach, visible when scrolled
 * up). Enter: fade + slide up 1/6 + expand 220ms; exit: fade 120 + shrink 150. One haptic when
 * it arrives while you're watching (not when the screen opens with it already pending).
 */
@Composable
private fun StickyPermission(session: Session?, onChoose: (String) -> Unit) {
    val pp = session?.pendingPermission
    // Keep the last request around so the exit animation has something to draw.
    var shown by remember { mutableStateOf<PendingPermission?>(null) }
    if (pp != null) shown = pp
    val haptics = LocalHapticFeedback.current
    var loaded by remember { mutableStateOf(false) }
    LaunchedEffect(pp != null, session != null) {
        if (pp != null && loaded) haptics.performHapticFeedback(HapticFeedbackType.LongPress)
        if (session != null) loaded = true
    }
    AnimatedVisibility(
        visible = pp != null,
        enter = fadeIn(tween(RelayMotion.DurationMedium, easing = RelayMotion.EaseOut)) +
            slideInVertically(tween(RelayMotion.DurationMedium, easing = RelayMotion.EaseOut)) { it / 6 } +
            expandVertically(tween(RelayMotion.DurationMedium, easing = RelayMotion.EaseOut)),
        exit = fadeOut(tween(120, easing = RelayMotion.EaseOut)) +
            shrinkVertically(tween(RelayMotion.DurationShort, easing = RelayMotion.EaseOut)),
    ) {
        shown?.let { PermissionCard(it, onChoose, Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp)) }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun PermissionCard(pp: PendingPermission, onChoose: (String) -> Unit, modifier: Modifier = Modifier) {
    val warning = MaterialTheme.statusColors[RelayStatus.Warning]
    Box(modifier) {
        InterlockCard(container = warning.chipBg, content = warning.chipOn) {
            Text("Allow this?", style = MaterialTheme.typography.titleSmall)
            Text(
                text = pp.title,
                style = MaterialTheme.typography.codeSmall,
                modifier = Modifier.padding(top = 4.dp, bottom = 12.dp),
            )
            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                pp.options.forEach { option ->
                    if (option.kind.startsWith("allow")) {
                        Button(onClick = { onChoose(option.optionId) }) { Text(option.name) }
                    } else {
                        OutlinedButton(
                            onClick = { onChoose(option.optionId) },
                            colors = ButtonDefaults.outlinedButtonColors(contentColor = MaterialTheme.colorScheme.error),
                        ) { Text(option.name) }
                    }
                }
            }
        }
    }
}

/**
 * A provider problem the runner flagged (Message.kind): quota used up, or login expired.
 * Shown as an interlock card so it can't be mistaken for an agent reply. Login expired gets a
 * "Sign in again" button → [ClaudeSignInSheet] (via [onSignIn]).
 */
@Composable
private fun ProblemCard(message: Message, onSignIn: () -> Unit) {
    val auth = message.kind != "quota"
    val (title, hint) = if (auth) {
        "Claude login expired" to null
    } else {
        "Quota exhausted" to "Your Claude subscription limit is used up. Sessions work again once it resets."
    }
    InterlockCard(
        container = MaterialTheme.colorScheme.errorContainer,
        content = MaterialTheme.colorScheme.onErrorContainer,
    ) {
        Text(title, style = MaterialTheme.typography.titleSmall)
        if (hint != null) {
            Text(text = hint, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.padding(top = 4.dp))
        }
        // The provider's own wording carries the reset time ("resets 5pm").
        Text(text = message.text, style = MaterialTheme.typography.codeSmall, modifier = Modifier.padding(top = 8.dp))
        if (auth) {
            Button(onClick = onSignIn, modifier = Modifier.padding(top = 12.dp)) { Text("Sign in again") }
        }
    }
}

@Composable
private fun Composer(vm: ChatViewModel, working: Boolean, onSend: () -> Unit, onStop: () -> Unit) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(start = 16.dp, end = 4.dp, top = 8.dp, bottom = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        OutlinedTextField(
            value = vm.input,
            onValueChange = { vm.input = it },
            modifier = Modifier.weight(1f),
            placeholder = { Text(if (working) "Agent is working…" else "Message the agent") },
        )
        // Send ↔ Stop share one fixed 48dp slot: crossfade + scale 0.9→1, 150 in / 100 out.
        AnimatedContent(
            targetState = working,
            transitionSpec = {
                (fadeIn(tween(RelayMotion.DurationShort, easing = RelayMotion.EaseOut)) +
                    scaleIn(tween(RelayMotion.DurationShort, easing = RelayMotion.EaseOut), initialScale = 0.9f)) togetherWith
                    fadeOut(tween(100, easing = RelayMotion.EaseOut))
            },
            contentAlignment = Alignment.Center,
            modifier = Modifier.padding(start = 4.dp).size(48.dp),
            label = "sendStop",
        ) { isWorking ->
            if (isWorking) {
                // The brake for no-permission mode: stops this turn, keeps the session.
                FilledIconButton(
                    onClick = onStop,
                    colors = IconButtonDefaults.filledIconButtonColors(
                        containerColor = MaterialTheme.colorScheme.error,
                        contentColor = MaterialTheme.colorScheme.onError,
                    ),
                ) { Icon(Icons.Default.Stop, contentDescription = "Stop") }
            } else {
                IconButton(enabled = vm.input.isNotBlank() && !vm.sending, onClick = onSend) {
                    Icon(Icons.AutoMirrored.Filled.Send, contentDescription = "Send")
                }
            }
        }
    }
}

/**
 * "Sonnet · High" above the composer -> bottom sheet with every config option (model, effort,
 * fast) as a list of choices. Disabled while a turn runs - changing model mid-turn is confusing.
 */
@Composable
private fun AgentConfigChip(options: List<ConfigOption>, enabled: Boolean, onSelect: (String, String) -> Unit) {
    var open by remember { mutableStateOf(false) }
    val label = options.filter { it.category == "model" || it.category == "thought_level" }
        .joinToString(" · ") { it.currentName }
        .ifEmpty { options.first().currentName }
    TextButton(
        onClick = { open = true },
        enabled = enabled,
        modifier = Modifier.padding(start = 8.dp),
    ) {
        Text(label, style = MaterialTheme.typography.labelLarge)
        Icon(Icons.Default.ArrowDropDown, contentDescription = "Model and effort")
    }
    if (open) {
        ModalBottomSheet(onDismissRequest = { open = false }) {
            Column(
                Modifier.verticalScroll(rememberScrollState()).padding(start = 16.dp, end = 16.dp, bottom = 32.dp),
            ) {
                options.forEach { opt ->
                    Text(
                        opt.name,
                        style = MaterialTheme.typography.titleSmall,
                        modifier = Modifier.padding(top = 16.dp, bottom = 4.dp),
                    )
                    opt.options.forEach { choice ->
                        val value = choice.value ?: return@forEach
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            modifier = Modifier
                                .fillMaxWidth()
                                .heightIn(min = 48.dp)
                                .clickable {
                                    if (value != opt.current) onSelect(opt.id, value)
                                    open = false
                                },
                        ) {
                            RadioButton(selected = value == opt.current, onClick = null)
                            Column(Modifier.padding(start = 12.dp)) {
                                Text(choice.name)
                                choice.description?.let {
                                    Text(
                                        it,
                                        style = MaterialTheme.typography.bodySmall,
                                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    )
                                }
                            }
                        }
                    }
                }
                Text(
                    "Your choice also becomes this runner's default for new chats.",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(top = 16.dp),
                )
            }
        }
    }
}

@Composable
private fun ModePicker(session: Session, onSelect: (String) -> Unit) {
    var open by remember { mutableStateOf(false) }
    val current = session.modes?.find { it.id == session.mode }
    Box {
        TextButton(onClick = { open = true }) {
            Text(current?.name ?: session.mode.orEmpty())
            Icon(Icons.Default.ArrowDropDown, contentDescription = null)
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            session.modes.orEmpty().forEach { mode ->
                DropdownMenuItem(
                    text = {
                        Column {
                            Text(mode.name)
                            mode.description?.let { Text(it, style = MaterialTheme.typography.bodySmall) }
                        }
                    },
                    onClick = {
                        open = false
                        if (mode.id != session.mode) onSelect(mode.id)
                    },
                )
            }
        }
    }
}
