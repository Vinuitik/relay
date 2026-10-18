package com.relay.app.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.Card
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.relay.app.model.TokenBucket
import com.relay.app.model.TokenSession
import com.relay.app.model.TokensReport
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.components.RefreshableBox
import com.relay.app.ui.components.SkeletonRows
import com.relay.app.ui.theme.statusColors
import java.util.Locale

private val DAY_CHOICES = listOf(1, 3, 7, 14, 30)

/** Plain-words reason + what to do, per runner flag (see runner/internal/tokens FlagXxx). */
private val FLAG_HELP = mapOf(
    "long-chain" to "Long chain: one prompt triggered dozens of calls. Ask for a short report instead of 'investigate and fix'.",
    "huge-context" to "Huge context: every call re-read 200k+ tokens. Start a fresh chat or /compact.",
    "opus-mechanical" to "Opus mostly running shell commands. A smaller model does that for much less.",
    "many-shell" to "Many shell calls, each re-reading the whole chat. Ask for batched commands.",
)

/**
 * "Where did my plan go?" - token use read by the runner from Claude's own transcripts
 * (GET /v1/tokens): per day, model, project, and the heaviest chats with plain-words flags.
 * Units are a weighted ESTIMATE (the plan's real accounting isn't public).
 */
@Composable
fun TokensScreen(vm: TokensViewModel, title: String, onBack: () -> Unit) {
    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Tokens · $title") },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
    ) { padding ->
        RefreshableBox(refreshing = vm.refreshing, onRefresh = vm::refresh, modifier = Modifier.padding(padding)) {
            val report = vm.report
            when {
                report == null && vm.error != null -> FullScreenError(vm.error!!, onRetry = { vm.refresh() })
                report == null -> SkeletonRows()
                else -> Column(
                    Modifier.verticalScroll(rememberScrollState()).padding(16.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp),
                ) {
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        DAY_CHOICES.forEach { d ->
                            FilterChip(selected = vm.days == d, onClick = { vm.selectDays(d) }, label = { Text("${d}d") })
                        }
                    }
                    TotalsCard(report)
                    DaysCard(report)
                    ShareCard("By model", report.byModel, report.totals.units)
                    ShareCard("By project", report.byProject, report.totals.units)
                    ChatsCard(report)
                    TipsCard()
                    Text(
                        report.estimate,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
        }
    }
}

@Composable
private fun SectionCard(title: String, content: @Composable () -> Unit) {
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(title, style = MaterialTheme.typography.titleSmall)
            content()
        }
    }
}

@Composable
private fun Supporting(text: String) {
    Text(text, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
}

@Composable
private fun TotalsCard(r: TokensReport) {
    val t = r.totals
    val heavyShare = if (t.units > 0) r.flagged.units / t.units * 100 else 0.0
    SectionCard("Spent · last ${r.days} day${if (r.days == 1) "" else "s"}") {
        Text("≈ ${compact(t.units)} units", style = MaterialTheme.typography.headlineSmall)
        Supporting("${t.calls} model calls · output ${compact(t.output.toDouble())} · cache written ${compact(t.cacheWrite.toDouble())}")
        Supporting("Cache re-read ${compact(t.cacheRead.toDouble())} - the same chat history read again on every call")
        if (t.units > 0) {
            Text(
                "${String.format(Locale.US, "%.0f", heavyShare)}% came from chats flagged below as heavy",
                style = MaterialTheme.typography.bodyMedium,
                color = if (heavyShare >= 50) MaterialTheme.statusColors.warning.fg else MaterialTheme.colorScheme.onSurface,
            )
        }
    }
}

@Composable
private fun DaysCard(r: TokensReport) {
    val peak = r.byDay.maxOfOrNull { it.units }?.takeIf { it > 0 } ?: 1.0
    SectionCard("Per day") {
        Row(
            Modifier.fillMaxWidth().height(96.dp),
            horizontalArrangement = Arrangement.spacedBy(3.dp),
            verticalAlignment = Alignment.Bottom,
        ) {
            r.byDay.forEachIndexed { i, d ->
                Box(Modifier.weight(1f).fillMaxHeight(), contentAlignment = Alignment.BottomCenter) {
                    Box(
                        Modifier
                            .fillMaxWidth()
                            .fillMaxHeight(((d.units / peak).toFloat()).coerceIn(0.02f, 1f))
                            .clip(RoundedCornerShape(topStart = 3.dp, topEnd = 3.dp))
                            .background(
                                if (i == r.byDay.lastIndex) MaterialTheme.colorScheme.primary
                                else MaterialTheme.colorScheme.primary.copy(alpha = 0.45f),
                            ),
                    )
                }
            }
        }
        val first = r.byDay.firstOrNull()
        val last = r.byDay.lastOrNull()
        if (first != null && last != null) {
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                Supporting(first.date.drop(5))
                Supporting("today ${compact(last.units)}")
            }
        }
    }
}

@Composable
private fun ShareCard(title: String, buckets: List<TokenBucket>, total: Double) {
    if (buckets.isEmpty()) return
    SectionCard(title) {
        buckets.take(6).forEach { b ->
            val frac = if (total > 0) (b.units / total).toFloat().coerceIn(0f, 1f) else 0f
            Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                    Text(b.name, style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                    Text("${compact(b.units)} · ${String.format(Locale.US, "%.0f", frac * 100)}%", style = MaterialTheme.typography.bodySmall)
                }
                ShareBar(frac)
                Supporting("${b.calls} calls" + if (b.sessions > 0) " · ${b.sessions} chat${if (b.sessions == 1) "" else "s"}" else "")
            }
        }
    }
}

@Composable
private fun ShareBar(frac: Float) {
    Box(
        Modifier.fillMaxWidth().height(6.dp).clip(RoundedCornerShape(3.dp))
            .background(MaterialTheme.colorScheme.surfaceVariant),
    ) {
        Box(
            Modifier.fillMaxWidth(frac.coerceAtLeast(0.01f)).fillMaxHeight()
                .clip(RoundedCornerShape(3.dp)).background(MaterialTheme.colorScheme.primary),
        )
    }
}

@Composable
private fun ChatsCard(r: TokensReport) {
    SectionCard("Heaviest chats") {
        if (r.sessions.isEmpty()) Supporting("No Claude activity in this window.")
        r.sessions.take(10).forEachIndexed { i, s ->
            if (i > 0) HorizontalDivider()
            ChatRow(s, r.totals.units)
        }
    }
}

@Composable
private fun ChatRow(s: TokenSession, total: Double) {
    val frac = if (total > 0) (s.units / total).toFloat().coerceIn(0f, 1f) else 0f
    Column(Modifier.padding(vertical = 4.dp), verticalArrangement = Arrangement.spacedBy(3.dp)) {
        Text(
            s.title.ifBlank { "(untitled chat)" },
            style = MaterialTheme.typography.bodyMedium.copy(fontWeight = FontWeight.Medium),
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
        )
        Supporting("${s.project} · ${modelShort(s.model)} · ${compact(s.units)} units (${String.format(Locale.US, "%.0f", frac * 100)}%)")
        ShareBar(frac)
        Supporting(
            "${s.calls} calls for ${s.prompts} prompt${if (s.prompts == 1) "" else "s"} · " +
                "${s.shellCalls} shell · biggest context ${compact(s.maxContext.toDouble())}" +
                if (s.subagentCalls > 0) " · ${s.subagentCalls} by subagents" else "",
        )
        s.flags.forEach { f ->
            Text(
                FLAG_HELP[f] ?: f,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.statusColors.warning.fg,
            )
        }
    }
}

@Composable
private fun TipsCard() {
    SectionCard("How to spend less") {
        Supporting("• Every tool call re-reads the whole chat, so cost grows with calls × chat length.")
        Supporting("• New chat per task, or /compact when it gets long.")
        Supporting("• Ask for one script instead of many small commands.")
        Supporting("• Use a smaller model for log reading and ssh; keep Opus for design.")
    }
}

/** 1234 -> "1.2k", 5_600_000 -> "5.6M". */
private fun compact(n: Double): String = when {
    n >= 1e9 -> String.format(Locale.US, "%.1fB", n / 1e9)
    n >= 1e6 -> String.format(Locale.US, "%.1fM", n / 1e6)
    n >= 1e3 -> String.format(Locale.US, "%.1fk", n / 1e3)
    else -> String.format(Locale.US, "%.0f", n)
}

/** "claude-opus-5-5" -> "opus-5-5"; unknown names pass through. */
private fun modelShort(m: String): String = m.removePrefix("claude-").ifBlank { "?" }
