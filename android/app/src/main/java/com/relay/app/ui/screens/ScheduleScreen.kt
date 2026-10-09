package com.relay.app.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.ChevronLeft
import androidx.compose.material.icons.filled.ChevronRight
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Repeat
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.produceState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.clipRect
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.max
import com.relay.app.model.Occurrence
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.components.RefreshableBox
import com.relay.app.ui.theme.FullShape
import kotlinx.coroutines.delay
import java.time.LocalDate
import java.time.LocalTime
import java.time.ZoneId
import java.time.format.TextStyle
import java.util.Locale

private val GUTTER = 32.dp
/** Hours visible without scrolling (06:00-24:00); 00-06 is above, reached by scrolling up. */
private const val VISIBLE_HOURS = 18
private const val FIRST_VISIBLE_HOUR = 6
private val MIN_HOUR_HEIGHT = 28.dp

/**
 * Outlook-style week of the runner's sleep/wake bookings (read-only). Each occurrence is its awake
 * block in the accent colour with the sleeps inside drawn as a dim hatched band. Taps open the
 * booking editor: an occurrence → (bookingId, date), an empty slot / day header → (date), the FAB →
 * (today). Days and "now" are in the runner's zone.
 */
@Composable
fun ScheduleScreen(
    vm: ScheduleViewModel,
    title: String,
    onBack: () -> Unit,
    onOpenEditor: (bookingId: String?, date: String) -> Unit,
) {
    val zone = vm.zone
    val today = LocalDate.now(zone)
    val status = scheduleStatusLine(vm.schedule, vm.cache, offline = vm.refreshError != null, zone = zone)
    var menuOpen by remember { mutableStateOf(false) }
    var remindersOpen by remember { mutableStateOf(false) }
    if (remindersOpen) ReminderSettingsDialog(onDismiss = { remindersOpen = false })
    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Schedule · $title", maxLines = 1, overflow = TextOverflow.Ellipsis) },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                actions = {
                    IconButton(onClick = { vm.refresh() }) {
                        Icon(Icons.Default.Refresh, contentDescription = "Refresh")
                    }
                    Box {
                        IconButton(onClick = { menuOpen = true }) {
                            Icon(Icons.Default.MoreVert, contentDescription = "More")
                        }
                        DropdownMenu(expanded = menuOpen, onDismissRequest = { menuOpen = false }) {
                            DropdownMenuItem(
                                text = { Text("Reminders") },
                                onClick = { menuOpen = false; remindersOpen = true },
                            )
                        }
                    }
                },
            )
        },
        floatingActionButton = {
            ExtendedFloatingActionButton(
                onClick = { onOpenEditor(null, today.toString()) },
                icon = { Icon(Icons.Default.Add, contentDescription = null) },
                text = { Text("Book") },
            )
        },
        bottomBar = {
            if (status != null) {
                Surface(color = MaterialTheme.colorScheme.surfaceContainer) {
                    Text(
                        status,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp),
                    )
                }
            }
        },
    ) { padding ->
        RefreshableBox(refreshing = vm.refreshing, onRefresh = vm::refresh, modifier = Modifier.padding(padding)) {
            val occurrences = vm.weekOccurrences
            // Never had data for this runner and can't reach it: nothing to draw a grid for.
            if (vm.cache == null && vm.refreshError != null && occurrences.isEmpty() && !vm.weekLoading) {
                FullScreenError(vm.refreshError!!, onRetry = { vm.refresh() })
                return@RefreshableBox
            }
            val monday = vm.monday
            val days = (0L until 7L).map { monday.plusDays(it) }
            Column(Modifier.fillMaxSize()) {
                WeekHeader(
                    monday = monday,
                    isCurrentWeek = vm.weekOffset == 0,
                    onPrevious = vm::previousWeek,
                    onNext = vm::nextWeek,
                    onToday = vm::thisWeek,
                )
                DayHeaderRow(days, today, onDay = { d -> onOpenEditor(null, d.toString()) })
                HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant)
                Box(Modifier.weight(1f).fillMaxWidth()) {
                    WeekGrid(
                        days = days,
                        today = today,
                        zone = zone,
                        byDate = occurrences.groupBy { it.date },
                        onSlot = { d -> onOpenEditor(null, d.toString()) },
                        onOccurrence = { o -> onOpenEditor(o.bookingId, o.date) },
                    )
                    val weekError = vm.weekError
                    when {
                        weekError != null -> InlineMessage(
                            "Couldn't load this week. $weekError",
                            Modifier.align(Alignment.TopCenter),
                        )
                        occurrences.isEmpty() && !vm.refreshing && !vm.weekLoading -> InlineMessage(
                            "No bookings this week · tap a day to book",
                            Modifier.align(Alignment.Center),
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun WeekHeader(
    monday: LocalDate,
    isCurrentWeek: Boolean,
    onPrevious: () -> Unit,
    onNext: () -> Unit,
    onToday: () -> Unit,
) {
    Box(Modifier.fillMaxWidth().padding(horizontal = 4.dp)) {
        Row(Modifier.align(Alignment.Center), verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = onPrevious) { Icon(Icons.Default.ChevronLeft, contentDescription = "Previous week") }
            Text(weekLabel(monday), style = MaterialTheme.typography.titleMedium)
            IconButton(onClick = onNext) { Icon(Icons.Default.ChevronRight, contentDescription = "Next week") }
        }
        if (!isCurrentWeek) {
            TextButton(onClick = onToday, modifier = Modifier.align(Alignment.CenterEnd)) { Text("Today") }
        }
    }
}

@Composable
private fun DayHeaderRow(days: List<LocalDate>, today: LocalDate, onDay: (LocalDate) -> Unit) {
    Row(Modifier.fillMaxWidth().padding(bottom = 4.dp)) {
        Spacer(Modifier.width(GUTTER))
        days.forEach { d ->
            val isToday = d == today
            val name = d.dayOfWeek.getDisplayName(TextStyle.SHORT, Locale.ENGLISH)
            Column(
                Modifier.weight(1f).clickable { onDay(d) }.padding(vertical = 4.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Text(
                    name,
                    style = MaterialTheme.typography.labelSmall,
                    color = if (isToday) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Box(
                    Modifier.size(28.dp).clip(FullShape)
                        .background(if (isToday) MaterialTheme.colorScheme.primary else Color.Transparent),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        d.dayOfMonth.toString(),
                        style = MaterialTheme.typography.titleSmall,
                        color = if (isToday) MaterialTheme.colorScheme.onPrimary else MaterialTheme.colorScheme.onSurface,
                    )
                }
            }
        }
    }
}

@Composable
private fun WeekGrid(
    days: List<LocalDate>,
    today: LocalDate,
    zone: ZoneId,
    byDate: Map<String, List<Occurrence>>,
    onSlot: (LocalDate) -> Unit,
    onOccurrence: (Occurrence) -> Unit,
) {
    BoxWithConstraints(Modifier.fillMaxSize()) {
        val hourHeight: Dp = max(maxHeight / VISIBLE_HOURS, MIN_HOUR_HEIGHT)
        val initialScroll = with(LocalDensity.current) { (hourHeight * FIRST_VISIBLE_HOUR).roundToPx() }
        val scroll = rememberScrollState(initialScroll)
        val line = MaterialTheme.colorScheme.outlineVariant
        val nowMinute by produceState(minuteNow(zone), zone) {
            while (true) {
                value = minuteNow(zone)
                delay(30_000)
            }
        }
        Row(
            Modifier.fillMaxWidth().verticalScroll(scroll).height(hourHeight * 24)
                .drawBehind {
                    val gutter = GUTTER.toPx()
                    val hour = hourHeight.toPx()
                    for (h in 1 until 24) {
                        drawLine(line, Offset(gutter, h * hour), Offset(size.width, h * hour), strokeWidth = 1f)
                    }
                    val col = (size.width - gutter) / 7
                    for (i in 0 until 7) {
                        drawLine(line, Offset(gutter + i * col, 0f), Offset(gutter + i * col, size.height), strokeWidth = 1f)
                    }
                },
        ) {
            HourGutter(hourHeight)
            days.forEach { d ->
                DayColumn(
                    date = d,
                    isToday = d == today,
                    occurrences = byDate[d.toString()].orEmpty(),
                    hourHeight = hourHeight,
                    nowMinute = nowMinute,
                    onSlot = onSlot,
                    onOccurrence = onOccurrence,
                    modifier = Modifier.weight(1f).fillMaxHeight(),
                )
            }
        }
    }
}

private fun minuteNow(zone: ZoneId): Int = LocalTime.now(zone).let { it.hour * 60 + it.minute }

@Composable
private fun HourGutter(hourHeight: Dp) {
    Box(Modifier.width(GUTTER).fillMaxHeight()) {
        for (h in 1 until 24) {
            Text(
                "%02d".format(h),
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                textAlign = TextAlign.Center,
                modifier = Modifier.width(GUTTER).offset(y = hourHeight * h - 8.dp),
            )
        }
    }
}

@Composable
private fun DayColumn(
    date: LocalDate,
    isToday: Boolean,
    occurrences: List<Occurrence>,
    hourHeight: Dp,
    nowMinute: Int,
    onSlot: (LocalDate) -> Unit,
    onOccurrence: (Occurrence) -> Unit,
    modifier: Modifier,
) {
    val todayTint = MaterialTheme.colorScheme.primary.copy(alpha = 0.05f)
    Box(
        modifier
            .then(if (isToday) Modifier.background(todayTint) else Modifier)
            .pointerInput(date) { detectTapGestures { onSlot(date) } },
    ) {
        occurrences.forEach { o -> OccurrenceBlock(o, hourHeight, onClick = { onOccurrence(o) }) }
        if (isToday) {
            val y = hourHeight * minuteToY(nowMinute, 1f)
            val now = MaterialTheme.colorScheme.tertiary
            Box(Modifier.fillMaxWidth().offset(y = y - 1.dp).height(2.dp).background(now))
            Box(Modifier.offset(x = (-3).dp, y = y - 4.dp).size(8.dp).clip(FullShape).background(now))
        }
    }
}

@Composable
private fun OccurrenceBlock(o: Occurrence, hourHeight: Dp, onClick: () -> Unit) {
    val segments = blockSegments(o.start, o.end, o.sleeps)
    if (segments.isEmpty()) return
    val start = segments.first().from
    val end = segments.last().to
    val top = hourHeight * minuteToY(start, 1f)
    val height = hourHeight * minuteToY(end - start, 1f)
    fun h(seg: BlockSegment): Dp = hourHeight * minuteToY(seg.to - seg.from, 1f)
    fun y(minute: Int): Dp = hourHeight * minuteToY(minute - start, 1f)

    val primary = MaterialTheme.colorScheme.primary
    val onPrimary = MaterialTheme.colorScheme.onPrimary
    val surface = MaterialTheme.colorScheme.surface
    val label = MaterialTheme.typography.labelSmall
    val describe = buildString {
        append(o.title.ifEmpty { "Booking" }).append(", awake ${o.start} to ${o.end}")
        o.sleeps.forEach { append(", sleep ${it.from} to ${it.to}") }
        if (o.recurring) append(", repeats")
        if (o.edited) append(", edited")
    }

    BoxWithConstraints(
        Modifier
            .fillMaxWidth()
            .offset(y = top)
            .height(height)
            .padding(horizontal = 1.dp)
            .clip(MaterialTheme.shapes.extraSmall)
            .background(primary)
            .drawBehind {
                val stripe = 6.dp.toPx()
                segments.filter { it.sleep }.forEach { s ->
                    val y0 = y(s.from).toPx()
                    val y1 = y0 + h(s).toPx()
                    drawRect(surface, Offset(0f, y0), androidx.compose.ui.geometry.Size(size.width, y1 - y0))
                    drawRect(primary.copy(alpha = 0.22f), Offset(0f, y0), androidx.compose.ui.geometry.Size(size.width, y1 - y0))
                    clipRect(0f, y0, size.width, y1) {
                        var x = -(y1 - y0)
                        while (x < size.width) {
                            drawLine(
                                primary.copy(alpha = 0.45f),
                                Offset(x, y1),
                                Offset(x + (y1 - y0), y0),
                                strokeWidth = 1.dp.toPx(),
                            )
                            x += stripe
                        }
                    }
                }
            }
            .clickable(onClick = onClick)
            .semantics { contentDescription = describe },
    ) {
        val wide = maxWidth >= 80.dp
        val first = segments.first()
        val last = segments.last()
        // Start time (+ end on wide columns) at the top of the first awake slice, if it fits.
        if (!first.sleep && h(first) >= 16.dp) {
            Column(Modifier.padding(horizontal = 3.dp, vertical = 1.dp)) {
                Text(
                    if (wide) "${o.start}–${o.end}" else o.start,
                    style = label,
                    color = onPrimary,
                    fontWeight = FontWeight.Medium,
                    maxLines = 1,
                    overflow = TextOverflow.Clip,
                )
                if (o.title.isNotEmpty() && h(first) >= 34.dp) {
                    Text(o.title, style = label, color = onPrimary, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
        }
        segments.filter { it.sleep && h(it) >= 18.dp }.forEach { s ->
            Text(
                "sleep",
                style = label,
                color = primary,
                textAlign = TextAlign.Center,
                maxLines = 1,
                modifier = Modifier.fillMaxWidth().offset(y = y(s.from) + h(s) / 2 - 8.dp),
            )
        }
        // Markers bottom-right (a 44dp column has no room for them beside the time).
        if (o.recurring || o.edited) {
            val tint = if (last.sleep) primary else onPrimary
            Row(
                Modifier.align(Alignment.BottomEnd).padding(2.dp),
                horizontalArrangement = Arrangement.spacedBy(2.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                if (o.edited) Box(Modifier.size(5.dp).clip(FullShape).background(tint))
                if (o.recurring) Icon(Icons.Default.Repeat, contentDescription = null, tint = tint, modifier = Modifier.size(10.dp))
            }
        }
    }
}

@Composable
private fun InlineMessage(text: String, modifier: Modifier) {
    Text(
        text,
        style = MaterialTheme.typography.bodySmall,
        color = MaterialTheme.colorScheme.onSurfaceVariant,
        textAlign = TextAlign.Center,
        modifier = modifier
            .padding(16.dp)
            .clip(MaterialTheme.shapes.small)
            .background(MaterialTheme.colorScheme.surfaceContainerHigh)
            .padding(horizontal = 12.dp, vertical = 8.dp),
    )
}
