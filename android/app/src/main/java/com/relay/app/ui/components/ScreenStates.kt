package com.relay.app.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.CloudOff
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.pulltorefresh.PullToRefreshContainer
import androidx.compose.material3.pulltorefresh.rememberPullToRefreshState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.relay.app.ui.theme.RelayStatus
import kotlinx.coroutines.Job
import java.time.Duration
import java.time.Instant
import java.time.OffsetDateTime

/**
 * DESIGN.md "States": a list body that supports pull-to-refresh and shows a thin progress bar at
 * its top while a refresh runs with content already on screen. [onRefresh] returns the refresh
 * Job so the pull indicator stays until the work is actually done.
 */
@Composable
fun RefreshableBox(
    refreshing: Boolean,
    onRefresh: () -> Job,
    modifier: Modifier = Modifier,
    content: @Composable BoxScope.() -> Unit,
) {
    val pullState = rememberPullToRefreshState()
    if (pullState.isRefreshing) {
        LaunchedEffect(Unit) {
            onRefresh().join()
            pullState.endRefresh()
        }
    }
    Box(modifier.fillMaxSize().nestedScroll(pullState.nestedScrollConnection)) {
        content()
        // Thin bar only for refreshes the pull indicator isn't already showing.
        if (refreshing && !pullState.isRefreshing) {
            LinearProgressIndicator(Modifier.fillMaxWidth().height(2.dp).align(Alignment.TopCenter))
        }
        PullToRefreshContainer(pullState, Modifier.align(Alignment.TopCenter))
    }
}

/** First-load placeholder: a few grey rows instead of a spinner (no shimmer - DESIGN.md Motion). */
@Composable
fun SkeletonRows(count: Int = 6) {
    val block = MaterialTheme.colorScheme.surfaceContainerHighest
    Column {
        repeat(count) {
            ListItem(
                headlineContent = {
                    Box(Modifier.width(180.dp).height(14.dp).clip(MaterialTheme.shapes.extraSmall).background(block))
                },
                supportingContent = {
                    Box(
                        Modifier.padding(top = 8.dp).width(120.dp).height(10.dp)
                            .clip(MaterialTheme.shapes.extraSmall).background(block),
                    )
                },
            )
        }
    }
}

/**
 * Full-screen error, used only when there's no data to show: icon, plain cause, likely fix,
 * Retry. Scrollable so pull-to-refresh also works from here. No "Error:" prefix.
 */
@Composable
fun FullScreenError(message: String, onRetry: () -> Unit, modifier: Modifier = Modifier) {
    Column(
        modifier = modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Icon(
            Icons.Outlined.CloudOff,
            contentDescription = null,
            modifier = Modifier.size(48.dp),
            tint = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        Spacer(Modifier.height(16.dp))
        Text(message, style = MaterialTheme.typography.bodyMedium, textAlign = TextAlign.Center)
        Spacer(Modifier.height(8.dp))
        Text(
            "Is Tailscale on? Is the runner awake?",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            textAlign = TextAlign.Center,
        )
        Spacer(Modifier.height(16.dp))
        Button(onClick = onRetry) { Text("Retry") }
    }
}

/**
 * Centered empty state (DESIGN.md "States"): one line telling you what to do + the primary action
 * as a Button when [actionLabel]/[onAction] are given.
 */
@Composable
fun EmptyState(
    text: String,
    modifier: Modifier = Modifier,
    actionLabel: String? = null,
    onAction: (() -> Unit)? = null,
) {
    Column(
        modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(24.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(
            text,
            modifier = Modifier.fillMaxWidth().padding(top = 96.dp),
            textAlign = TextAlign.Center,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        if (actionLabel != null && onAction != null) {
            Spacer(Modifier.height(16.dp))
            Button(onClick = onAction) { Text(actionLabel) }
        }
    }
}

/** Session `state` → lamp/chip status. Amber (Warning) = waiting on you, nothing else. */
fun sessionStatus(state: String): RelayStatus = when (state) {
    "waiting" -> RelayStatus.Warning
    "busy" -> RelayStatus.Busy
    "finished" -> RelayStatus.Success
    "error" -> RelayStatus.Error
    else -> RelayStatus.Idle
}

@Composable
fun SessionStateChip(state: String, modifier: Modifier = Modifier) {
    StatusChip(status = sessionStatus(state), label = state, modifier = modifier)
}

/** "now" / "5m" / "3h" / "2d" / "4w" since an RFC3339 timestamp; "" if it doesn't parse. */
fun relativeTime(rfc3339: String?, now: Instant = Instant.now()): String {
    if (rfc3339.isNullOrBlank()) return ""
    val then = runCatching { OffsetDateTime.parse(rfc3339).toInstant() }.getOrNull() ?: return ""
    val d = Duration.between(then, now)
    return when {
        d.toMinutes() < 1 -> "now"
        d.toHours() < 1 -> "${d.toMinutes()}m"
        d.toDays() < 1 -> "${d.toHours()}h"
        d.toDays() < 14 -> "${d.toDays()}d"
        else -> "${d.toDays() / 7}w"
    }
}

/** Newest-first comparator key for RFC3339 timestamps (unparseable sorts last). */
fun epochOf(rfc3339: String?): Long =
    rfc3339?.let { runCatching { OffsetDateTime.parse(it).toInstant().toEpochMilli() }.getOrNull() } ?: 0L
