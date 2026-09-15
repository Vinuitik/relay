package com.relay.app.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.relay.app.data.db.RelayDatabase
import com.relay.app.data.db.UptimeIntervalEntity
import java.time.Duration
import java.time.Instant
import java.time.ZoneOffset
import java.time.temporal.WeekFields

/**
 * "How much was this system awake" — reads entirely from the app's own Room-persisted uptime
 * history (see [com.relay.app.data.db.UptimeIntervalEntity], synced in by
 * [com.relay.app.data.UptimeSyncWorker]), NOT live from any runner - the whole point is this view
 * still works for a runner that's currently asleep, and belongs to the app rather than to any one
 * device (per the user's own framing of this feature).
 *
 * Week bucketing is intentionally simple: an interval is attributed wholly to the ISO week its
 * *start* falls in, even if it actually crosses into the next week. Splitting a duration exactly
 * across a week boundary would be more correct but is overkill for a personal dashboard - "off by
 * a few minutes at a week boundary" is an accepted tradeoff, not a bug to chase.
 */
@Composable
fun DashboardScreen(onBack: () -> Unit) {
    val context = LocalContext.current
    val dao = remember(context) { RelayDatabase.get(context).uptimeDao() }
    val intervals by dao.observeAll().collectAsState(initial = emptyList())

    val weeks = remember(intervals) { aggregateByWeek(intervals) }
    val thisWeekByRunner = remember(intervals) { aggregateCurrentWeekByRunner(intervals) }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Dashboard") },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
    ) { padding ->
        if (intervals.isEmpty()) {
            Box(modifier = Modifier.padding(padding).fillMaxSize()) {
                Text(
                    "No uptime data synced yet — this fills in once a known runner has been reachable at least once.",
                    modifier = Modifier.align(Alignment.Center).padding(24.dp),
                )
            }
            return@Scaffold
        }

        LazyColumn(modifier = Modifier.padding(padding).fillMaxSize()) {
            item {
                Text(
                    "This week",
                    style = MaterialTheme.typography.titleMedium,
                    modifier = Modifier.padding(start = 16.dp, top = 16.dp, end = 16.dp),
                )
            }
            item {
                Text(
                    formatDuration(weeks.firstOrNull()?.totalSeconds ?: 0),
                    style = MaterialTheme.typography.headlineLarge,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
                )
            }
            if (thisWeekByRunner.isNotEmpty()) {
                item {
                    Text(
                        "By runner",
                        style = MaterialTheme.typography.titleSmall,
                        modifier = Modifier.padding(start = 16.dp, top = 12.dp, end = 16.dp),
                    )
                }
                items(thisWeekByRunner, key = { it.first }) { (hostname, seconds) ->
                    ListItem(
                        headlineContent = { Text(hostname) },
                        trailingContent = { Text(formatDuration(seconds)) },
                    )
                }
            }
            item {
                Text(
                    "Past weeks",
                    style = MaterialTheme.typography.titleMedium,
                    modifier = Modifier.padding(start = 16.dp, top = 20.dp, end = 16.dp),
                )
            }
            items(weeks.drop(1), key = { it.weekLabel }) { week ->
                ListItem(
                    headlineContent = { Text(week.weekLabel) },
                    trailingContent = { Text(formatDuration(week.totalSeconds)) },
                )
                HorizontalDivider()
            }
        }
    }
}

data class WeekUptime(val weekLabel: String, val totalSeconds: Long)

private val weekFields = WeekFields.ISO

private fun weekLabelFor(instant: Instant): String {
    val zdt = instant.atZone(ZoneOffset.UTC)
    val year = zdt.get(weekFields.weekBasedYear())
    val week = zdt.get(weekFields.weekOfWeekBasedYear())
    return "$year-W${week.toString().padStart(2, '0')}"
}

private fun intervalSeconds(interval: UptimeIntervalEntity, now: Instant): Long {
    val start = runCatching { Instant.parse(interval.start) }.getOrNull() ?: return 0
    val end = interval.end?.let { runCatching { Instant.parse(it) }.getOrNull() } ?: now
    return Duration.between(start, end).seconds.coerceAtLeast(0)
}

fun aggregateByWeek(intervals: List<UptimeIntervalEntity>): List<WeekUptime> {
    val now = Instant.now()
    val byWeek = linkedMapOf<String, Long>()
    for (interval in intervals) {
        val start = runCatching { Instant.parse(interval.start) }.getOrNull() ?: continue
        val label = weekLabelFor(start)
        byWeek[label] = (byWeek[label] ?: 0L) + intervalSeconds(interval, now)
    }
    return byWeek.entries.sortedByDescending { it.key }.map { WeekUptime(it.key, it.value) }
}

fun aggregateCurrentWeekByRunner(intervals: List<UptimeIntervalEntity>): List<Pair<String, Long>> {
    val now = Instant.now()
    val currentWeek = weekLabelFor(now)
    val byRunner = linkedMapOf<String, Long>()
    for (interval in intervals) {
        val start = runCatching { Instant.parse(interval.start) }.getOrNull() ?: continue
        if (weekLabelFor(start) != currentWeek) continue
        byRunner[interval.runnerHostname] = (byRunner[interval.runnerHostname] ?: 0L) + intervalSeconds(interval, now)
    }
    return byRunner.entries.sortedByDescending { it.value }.map { it.key to it.value }
}

fun formatDuration(totalSeconds: Long): String {
    val hours = totalSeconds / 3600
    val minutes = (totalSeconds % 3600) / 60
    return if (hours > 0) "${hours}h ${minutes}m" else "${minutes}m"
}
