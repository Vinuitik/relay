package com.relay.app.ui.screens

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.Card
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.relay.app.data.AppPrefsRepository.UsageCosts
import com.relay.app.model.UsageLimit
import com.relay.app.model.UsageReport
import com.relay.app.model.UsageStats
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.components.RefreshableBox
import com.relay.app.ui.components.SkeletonRows
import com.relay.app.ui.theme.statusColors
import java.util.Locale
import kotlin.math.roundToInt

private val DAY_CHOICES = listOf(7, 14, 30, 90)
private val WEEKDAYS = listOf("Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun")

/**
 * "Would this machine actually sleep, and would a wake device pay for itself?" - the runner keeps
 * the machine on, records activity per minute, and simulates idle-suspend (GET /v1/usage). This
 * screen shows the simulation per idle limit, when activity clusters, and £/year from [costs].
 */
@Composable
fun UsageScreen(
    vm: UsageViewModel,
    title: String,
    costs: UsageCosts,
    onCostsChange: (UsageCosts) -> Unit,
    onBack: () -> Unit,
) {
    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Usage · $title") },
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
                    MeasuredCard(report)
                    SimulationCard(report, costs)
                    HeatmapCard(report)
                    TurnsCard(report.turns, report.replyLatency)
                    CostsCard(costs, onCostsChange)
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
private fun MeasuredCard(r: UsageReport) {
    val days = r.observedMinutes / 1440.0
    SectionCard("Measured") {
        Text(
            "Active ${pct(r.activeMinutes, r.observedMinutes)} of the time",
            style = MaterialTheme.typography.headlineSmall,
        )
        Supporting(
            "Agent turns ${hours(r.bySource["turn"])} · App open ${hours(r.bySource["app"])} · " +
                "Keyboard/mouse ${hours(r.bySource["local"])}",
        )
        Supporting("Recorded ${fmt1(days)} days · runner up ${fmt0(r.uptimePct)}% of that span")
        if (days < 14) {
            Text(
                "Collect 2-3 weeks before deciding - a few days can't show a weekly pattern.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.statusColors.warning.fg,
            )
        }
    }
}

@Composable
private fun SimulationCard(r: UsageReport, costs: UsageCosts) {
    SectionCard("If it slept when idle") {
        Row(Modifier.fillMaxWidth()) {
            listOf("Idle limit", "Awake", "Wakes/day", "Saves/yr").forEachIndexed { i, h ->
                Text(
                    h,
                    Modifier.weight(1f),
                    style = MaterialTheme.typography.labelMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    textAlign = if (i == 0) TextAlign.Start else TextAlign.End,
                )
            }
        }
        HorizontalDivider()
        r.limits.forEach { l ->
            val s = savings(l, costs)
            Row(Modifier.fillMaxWidth()) {
                Text("${l.idleMinutes} min", Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium)
                Text("${fmt0(l.awakePct)}%", Modifier.weight(1f), textAlign = TextAlign.End)
                Text(
                    "${fmt1(l.wakeupsPerDay)} (${fmt1(l.remoteWakeupsPerDay)})",
                    Modifier.weight(1f),
                    textAlign = TextAlign.End,
                )
                Text(money(s.savedPerYear), Modifier.weight(1f), textAlign = TextAlign.End)
            }
        }
        Supporting("Wakes/day (remote): remote = no keyboard/mouse that minute, so only a wake device could have woken it. Each wake costs ~20-60 s of waiting.")
        r.limits.firstOrNull { it.idleMinutes == 15 }?.let { Verdict(it, costs) }
    }
}

/** One-line answer for the 15-minute limit: does a wake device pay back? */
@Composable
private fun Verdict(l: UsageLimit, costs: UsageCosts) {
    val s = savings(l, costs)
    val text = when {
        l.remoteWakeups == 0 && l.wakeups > 0 ->
            "At 15 min: every wake-up came with local input - no wake device needed."
        s.paybackMonths == null ->
            "At 15 min: saves ${money(s.savedPerYear)}/yr, less than a ${fmtW(costs.deviceWatts)} wake device costs to run. Not worth it."
        else ->
            "At 15 min: nets ${money(s.netPerYear)}/yr after the device's own draw - a ${money(costs.deviceCost.toDouble())} wake device pays back in ${fmt0(s.paybackMonths ?: 0.0)} months."
    }
    Text(text, style = MaterialTheme.typography.bodyMedium, fontWeight = FontWeight.Medium)
}

@Composable
private fun HeatmapCard(r: UsageReport) {
    val primary = MaterialTheme.colorScheme.primary
    val empty = MaterialTheme.colorScheme.outlineVariant
    val label = MaterialTheme.typography.labelSmall
    val muted = MaterialTheme.colorScheme.onSurfaceVariant
    SectionCard("When you use it") {
        WEEKDAYS.forEachIndexed { wd, name ->
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(name, Modifier.width(32.dp), style = label, color = muted)
                val active = r.heatmapActive.getOrNull(wd).orEmpty()
                val observed = r.heatmapObserved.getOrNull(wd).orEmpty()
                val busiest = (0 until 24).maxByOrNull { h -> ratio(active.getOrNull(h), observed.getOrNull(h)) }
                Canvas(
                    Modifier.weight(1f).height(14.dp).semantics {
                        contentDescription = "$name: busiest around ${busiest ?: 0}:00"
                    },
                ) {
                    val gap = 2.dp.toPx()
                    val w = (size.width - gap * 23) / 24
                    for (h in 0 until 24) {
                        val obs = observed.getOrNull(h) ?: 0
                        val x = h * (w + gap)
                        val radius = CornerRadius(2.dp.toPx())
                        if (obs == 0) {
                            drawRoundRect(empty, Offset(x, 0f), Size(w, size.height), radius, style = Stroke(1.dp.toPx()))
                        } else {
                            val a = ratio(active.getOrNull(h), obs)
                            drawRoundRect(primary.copy(alpha = 0.08f + 0.92f * a), Offset(x, 0f), Size(w, size.height), radius)
                        }
                    }
                }
            }
        }
        Row {
            Spacer(Modifier.width(32.dp))
            listOf("0", "6", "12", "18", "24").forEachIndexed { i, h ->
                Text(
                    h,
                    Modifier.weight(if (i == 4) 0.5f else 1f),
                    style = label,
                    color = muted,
                    textAlign = if (i == 4) TextAlign.End else TextAlign.Start,
                )
            }
        }
        Supporting("Share of each hour with any activity, runner's local time. Outlined = no data yet.")
    }
}

@Composable
private fun TurnsCard(turns: UsageStats, latency: UsageStats) {
    SectionCard("Agent turns") {
        if (turns.count == 0) {
            Supporting("No agent turns recorded yet.")
        } else {
            Text("${turns.count} turns · ${dur(turns.totalSec)} total", style = MaterialTheme.typography.bodyLarge)
            Supporting("Turn length: median ${dur(turns.medianSec)} · 90% under ${dur(turns.p90Sec)}")
        }
        if (latency.count > 0) {
            Supporting("You reply after: median ${dur(latency.medianSec)} · 90% within ${dur(latency.p90Sec)}")
        }
    }
}

@Composable
private fun CostsCard(costs: UsageCosts, onChange: (UsageCosts) -> Unit) {
    SectionCard("Assumptions") {
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            NumberField("Idle W (this PC)", costs.idleWatts, Modifier.weight(1f)) { onChange(costs.copy(idleWatts = it)) }
            NumberField("Asleep W", costs.sleepWatts, Modifier.weight(1f)) { onChange(costs.copy(sleepWatts = it)) }
        }
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            NumberField("£ per kWh", costs.pricePerKwh, Modifier.weight(1f)) { onChange(costs.copy(pricePerKwh = it)) }
            NumberField("Device £", costs.deviceCost, Modifier.weight(1f)) { onChange(costs.copy(deviceCost = it)) }
            NumberField("Device W", costs.deviceWatts, Modifier.weight(1f)) { onChange(costs.copy(deviceWatts = it)) }
        }
        Supporting("Idle W is per machine: ~5 W for the Dell with the screen off; a desktop idles at 40-60 W.")
    }
}

/** Saves on every valid number; keeps the user's raw text while they type. */
@Composable
private fun NumberField(label: String, value: Float, modifier: Modifier, onValue: (Float) -> Unit) {
    var text by remember(value) { mutableStateOf(trimFloat(value)) }
    OutlinedTextField(
        value = text,
        onValueChange = { t ->
            text = t
            t.toFloatOrNull()?.takeIf { it >= 0f }?.let(onValue)
        },
        label = { Text(label) },
        singleLine = true,
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Decimal),
        modifier = modifier,
    )
}

private fun ratio(active: Int?, observed: Int?): Float =
    if (observed == null || observed == 0) 0f else (active ?: 0).toFloat() / observed

private fun pct(n: Int, d: Int) = if (d == 0) "0%" else "${(100.0 * n / d).roundToInt()}%"
private fun hours(minutes: Int?) = "${fmt1((minutes ?: 0) / 60.0)}h"
private fun fmt0(x: Double) = x.roundToInt().toString()
private fun fmt1(x: Double) = String.format(Locale.UK, "%.1f", x)
private fun fmtW(w: Float) = "${trimFloat(w)} W"
private fun money(x: Double) = if (x < 0) "-£" + String.format(Locale.UK, "%.2f", -x) else "£" + String.format(Locale.UK, "%.2f", x)
private fun trimFloat(f: Float) = if (f == f.toInt().toFloat()) f.toInt().toString() else f.toString()

/** 42s / 12m / 1h 5m. */
private fun dur(sec: Double): String {
    val s = sec.roundToInt()
    return when {
        s < 60 -> "${s}s"
        s < 3600 -> "${s / 60}m"
        else -> "${s / 3600}h ${(s % 3600) / 60}m"
    }
}
