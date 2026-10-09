package com.relay.app.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Remove
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.DatePicker
import androidx.compose.material3.DatePickerDialog
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TimePicker
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.rememberDatePickerState
import androidx.compose.material3.rememberTimePickerState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.drawscope.clipRect
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.relay.app.ui.components.FullScreenError
import com.relay.app.ui.theme.FullShape
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneOffset
import java.time.format.TextStyle
import java.util.Locale

/** Which question the scope dialog ("This day only" / "The whole series") is asking. */
private enum class ScopeAsk { SAVE, DELETE }

/**
 * Booking editor (`r/{host}/schedule/edit`). Title · date · awake block · sleeps · live preview ·
 * repeat · ends, validated live against the server's rules ([validateForm]). Save is in the top
 * bar; Delete is red at the bottom and confirmed. On an occurrence of a repeating booking both ask
 * "This day only" / "The whole series". Calls [onDone] after a successful save/delete.
 */
@Composable
fun BookingEditorScreen(vm: BookingEditorViewModel, onBack: () -> Unit, onDone: () -> Unit) {
    val snackbar = remember { SnackbarHostState() }
    val scroll = rememberScrollState()
    var scopeAsk by remember { mutableStateOf<ScopeAsk?>(null) }
    var confirmDelete by remember { mutableStateOf(false) }

    LaunchedEffect(vm.done) { if (vm.done) onDone() }
    LaunchedEffect(vm.snackbar) {
        vm.snackbar?.let { snackbar.showSnackbar(it); vm.snackbarShown() }
    }
    LaunchedEffect(vm.serverError) { if (vm.serverError != null) scroll.animateScrollTo(0) }

    val errors = vm.errors
    val ready = !vm.loading && vm.loadError == null
    val title = when (vm.mode) {
        EditorMode.NEW -> "New booking"
        EditorMode.OCCURRENCE -> if (vm.isSeries) "Edit day" else "Edit booking"
        EditorMode.SERIES -> if (vm.isSeries) "Edit series" else "Edit booking"
    }

    Scaffold(
        topBar = {
            Column {
                TopAppBar(
                    title = { Text(title) },
                    navigationIcon = {
                        IconButton(onClick = onBack) {
                            Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                        }
                    },
                    actions = {
                        TextButton(
                            onClick = { if (vm.asksScope) scopeAsk = ScopeAsk.SAVE else vm.save(thisDayOnly = false) },
                            enabled = ready && !vm.saving && errors.isValid,
                        ) { Text("Save") }
                    },
                )
                Box(Modifier.fillMaxWidth().height(4.dp)) {
                    if (vm.saving || vm.loading) LinearProgressIndicator(Modifier.fillMaxWidth())
                }
            }
        },
        snackbarHost = { SnackbarHost(snackbar) },
    ) { padding ->
        Box(Modifier.padding(padding).fillMaxSize()) {
            when {
                vm.loadError != null -> FullScreenError(vm.loadError!!, onRetry = vm::load)
                vm.loading -> Unit
                else -> EditorForm(
                    vm = vm,
                    errors = errors,
                    modifier = Modifier.verticalScroll(scroll),
                    onDelete = { if (vm.asksScope) scopeAsk = ScopeAsk.DELETE else confirmDelete = true },
                )
            }
        }
    }

    scopeAsk?.let { ask ->
        ScopeDialog(
            ask = ask,
            dayOnlyBlocked = ask == ScopeAsk.SAVE && vm.seriesOnlyChanges,
            dayLabel = vm.occurrenceDate?.let(::longDate).orEmpty(),
            onDismiss = { scopeAsk = null },
            onPick = { dayOnly ->
                scopeAsk = null
                if (ask == ScopeAsk.SAVE) vm.save(dayOnly) else vm.delete(dayOnly)
            },
        )
    }
    if (confirmDelete) {
        AlertDialog(
            onDismissRequest = { confirmDelete = false },
            title = { Text(if (vm.isSeries) "Delete the whole series?" else "Delete this booking?") },
            text = { Text("The server stays awake on these days unless another booking covers them.") },
            confirmButton = {
                TextButton(
                    onClick = { confirmDelete = false; vm.delete(thisDayOnly = false) },
                    colors = ButtonDefaults.textButtonColors(contentColor = MaterialTheme.colorScheme.error),
                ) { Text("Delete") }
            },
            dismissButton = { TextButton(onClick = { confirmDelete = false }) { Text("Cancel") } },
        )
    }
}

@Composable
private fun ScopeDialog(
    ask: ScopeAsk,
    dayOnlyBlocked: Boolean,
    dayLabel: String,
    onDismiss: () -> Unit,
    onPick: (dayOnly: Boolean) -> Unit,
) {
    val destructive = if (ask == ScopeAsk.DELETE) {
        ButtonDefaults.textButtonColors(contentColor = MaterialTheme.colorScheme.error)
    } else {
        ButtonDefaults.textButtonColors()
    }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(if (ask == ScopeAsk.SAVE) "Save changes to" else "Delete") },
        text = {
            Text(
                when {
                    dayOnlyBlocked -> "Title and repeat changes can only apply to the whole series."
                    ask == ScopeAsk.SAVE -> "Change only $dayLabel, or every day of this series?"
                    else -> "Remove only $dayLabel, or the whole series?"
                },
            )
        },
        confirmButton = {
            Column(horizontalAlignment = Alignment.End) {
                TextButton(onClick = { onPick(true) }, enabled = !dayOnlyBlocked, colors = destructive) {
                    Text("This day only")
                }
                TextButton(onClick = { onPick(false) }, colors = destructive) { Text("The whole series") }
                TextButton(onClick = onDismiss) { Text("Cancel") }
            }
        },
    )
}

@Composable
private fun EditorForm(
    vm: BookingEditorViewModel,
    errors: FormErrors,
    modifier: Modifier,
    onDelete: () -> Unit,
) {
    val form = vm.form
    var pickTime by remember { mutableStateOf<TimeTarget?>(null) }
    var pickDate by remember { mutableStateOf<DateTarget?>(null) }

    Column(modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp)) {
        vm.serverError?.let { msg ->
            Surface(
                color = MaterialTheme.colorScheme.errorContainer,
                shape = MaterialTheme.shapes.small,
                modifier = Modifier.fillMaxWidth().padding(bottom = 12.dp),
            ) {
                Text(
                    msg,
                    color = MaterialTheme.colorScheme.onErrorContainer,
                    style = MaterialTheme.typography.bodyMedium,
                    modifier = Modifier.padding(12.dp),
                )
            }
        }

        OutlinedTextField(
            value = form.title,
            onValueChange = { t -> vm.update { it.copy(title = t) } },
            label = { Text("Title (optional)") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )

        Section("Date")
        val occurrence = vm.occurrenceDate
        if (vm.mode == EditorMode.OCCURRENCE && occurrence != null) {
            Text(longDate(occurrence), style = MaterialTheme.typography.bodyLarge)
            if (vm.isSeries) {
                Text(
                    "One day of a series that starts ${longDate(form.date)}",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        } else {
            ValueButton(longDate(form.date), "Date") { pickDate = DateTarget.START }
        }

        Section("Awake")
        Row(verticalAlignment = Alignment.CenterVertically) {
            ValueButton(formatMinutes(form.start), "Awake from") { pickTime = TimeTarget.Start }
            Text("→", Modifier.padding(horizontal = 8.dp))
            ValueButton(formatMinutes(form.end), "Awake until") { pickTime = TimeTarget.End }
        }
        FieldError(errors.end)

        Section("Sleeps")
        if (form.sleeps.isEmpty()) {
            Text(
                "No sleeps - the server stays awake the whole block.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        form.sleeps.forEachIndexed { i, s ->
            Row(verticalAlignment = Alignment.CenterVertically) {
                ValueButton(formatMinutes(s.from), "Sleep at") { pickTime = TimeTarget.SleepFrom(i) }
                Text("→ wake", Modifier.padding(horizontal = 8.dp))
                ValueButton(formatMinutes(s.to), "Wake at") { pickTime = TimeTarget.SleepTo(i) }
                Spacer(Modifier.weight(1f))
                IconButton(onClick = { vm.update { f -> f.copy(sleeps = f.sleeps.filterIndexed { j, _ -> j != i }) } }) {
                    Icon(Icons.Default.Close, contentDescription = "Remove sleep ${formatMinutes(s.from)}")
                }
            }
            FieldError(errors.sleeps[i])
        }
        val proposal = proposeSleep(form.start, form.end, form.sleeps)
        TextButton(onClick = vm::addSleep, enabled = proposal != null) {
            Icon(Icons.Default.Add, contentDescription = null, modifier = Modifier.size(18.dp))
            Spacer(Modifier.width(8.dp))
            Text(if (proposal != null || form.start >= form.end) "Add sleep" else "No room for another sleep")
        }
        warningTime(form.sleeps)?.let {
            Text(
                "Warning goes out at $it",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }

        Spacer(Modifier.height(12.dp))
        DayPreview(form)

        RepeatSection(
            form = form,
            seriesHint = vm.mode == EditorMode.OCCURRENCE && vm.isSeries,
            error = errors.repeat,
            onChange = vm::update,
            onPickUntil = { pickDate = DateTarget.UNTIL },
        )

        if (vm.bookingId != null) {
            Spacer(Modifier.height(24.dp))
            HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant)
            Spacer(Modifier.height(16.dp))
            OutlinedButton(
                onClick = onDelete,
                enabled = !vm.saving,
                colors = ButtonDefaults.outlinedButtonColors(contentColor = MaterialTheme.colorScheme.error),
                modifier = Modifier.fillMaxWidth(),
            ) { Text("Delete") }
        }
        Spacer(Modifier.height(32.dp))
    }

    pickTime?.let { target ->
        val initial = when (target) {
            TimeTarget.Start -> form.start
            TimeTarget.End -> form.end
            is TimeTarget.SleepFrom -> form.sleeps.getOrNull(target.index)?.from ?: form.start
            is TimeTarget.SleepTo -> form.sleeps.getOrNull(target.index)?.to ?: form.end
        }
        TimePickerDialog(
            title = when (target) {
                TimeTarget.Start -> "Awake from"
                TimeTarget.End -> "Awake until"
                is TimeTarget.SleepFrom -> "Sleep at"
                is TimeTarget.SleepTo -> "Wake at"
            },
            initialMinutes = initial,
            onDismiss = { pickTime = null },
            onConfirm = { m ->
                pickTime = null
                vm.update { f ->
                    fun sleepAt(i: Int, change: (SleepSlot) -> SleepSlot) =
                        f.sleeps.mapIndexed { j, s -> if (j == i) change(s) else s }
                    when (target) {
                        TimeTarget.Start -> f.copy(start = m)
                        TimeTarget.End -> f.copy(end = m)
                        is TimeTarget.SleepFrom -> f.copy(sleeps = sleepAt(target.index) { it.copy(from = m) })
                        is TimeTarget.SleepTo -> f.copy(sleeps = sleepAt(target.index) { it.copy(to = m) })
                    }
                }
            },
        )
    }
    pickDate?.let { target ->
        val initial = when (target) {
            DateTarget.START -> form.date
            DateTarget.UNTIL -> form.until ?: form.date.plusMonths(1)
        }
        DatePickerDialogFor(
            initial = initial,
            onDismiss = { pickDate = null },
            onConfirm = { d ->
                pickDate = null
                vm.update { f ->
                    when (target) {
                        // Weekly still on "the date's weekday" follows the new date.
                        DateTarget.START -> f.copy(
                            date = d,
                            weekdays = if (f.weekdays == setOf(f.date.dayOfWeek.value)) setOf(d.dayOfWeek.value) else f.weekdays,
                        )
                        DateTarget.UNTIL -> f.copy(until = d, ends = RepeatEnd.ON_DATE)
                    }
                }
            },
        )
    }
}

private sealed interface TimeTarget {
    object Start : TimeTarget
    object End : TimeTarget
    data class SleepFrom(val index: Int) : TimeTarget
    data class SleepTo(val index: Int) : TimeTarget
}

private enum class DateTarget { START, UNTIL }

@Composable
private fun RepeatSection(
    form: BookingForm,
    seriesHint: Boolean,
    error: String?,
    onChange: ((BookingForm) -> BookingForm) -> Unit,
    onPickUntil: () -> Unit,
) {
    Section(if (seriesHint) "Repeat (whole series)" else "Repeat")
    Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        RepeatFreq.entries.forEach { f ->
            FilterChip(
                selected = form.freq == f,
                onClick = { onChange { it.copy(freq = f) } },
                label = { Text(f.name.lowercase().replaceFirstChar { it.uppercase() }) },
            )
        }
    }
    if (form.freq == RepeatFreq.NEVER) {
        FieldError(error)
        return
    }

    if (form.freq == RepeatFreq.WEEKLY) {
        Spacer(Modifier.height(8.dp))
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            listOf("M", "T", "W", "T", "F", "S", "S").forEachIndexed { i, letter ->
                val day = i + 1
                val on = day in form.weekdays
                val name = java.time.DayOfWeek.of(day).getDisplayName(TextStyle.FULL, Locale.ENGLISH)
                Box(
                    Modifier.weight(1f).height(40.dp).clip(FullShape)
                        .background(if (on) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.surfaceContainerHigh)
                        .selectable(selected = on, role = Role.Checkbox) {
                            onChange { f -> f.copy(weekdays = if (on) f.weekdays - day else f.weekdays + day) }
                        }
                        .semantics { contentDescription = name },
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        letter,
                        style = MaterialTheme.typography.labelLarge,
                        color = if (on) MaterialTheme.colorScheme.onPrimary else MaterialTheme.colorScheme.onSurface,
                    )
                }
            }
        }
    }

    Row(verticalAlignment = Alignment.CenterVertically) {
        Text("Every", style = MaterialTheme.typography.bodyLarge)
        Stepper(form.interval, min = 1, onChange = { n -> onChange { it.copy(interval = n) } })
        Text(intervalUnit(form.freq, form.interval), style = MaterialTheme.typography.bodyLarge)
    }
    repeatHint(form)?.let {
        Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }

    Section("Ends")
    EndOption(form.ends == RepeatEnd.NEVER, onSelect = { onChange { it.copy(ends = RepeatEnd.NEVER) } }) {
        Text("Never")
    }
    EndOption(form.ends == RepeatEnd.ON_DATE, onSelect = {
        if (form.until == null) onPickUntil() else onChange { it.copy(ends = RepeatEnd.ON_DATE) }
    }) {
        Text("On")
        Spacer(Modifier.width(8.dp))
        ValueButton(form.until?.let(::longDate) ?: "pick a date", "End date", onClick = onPickUntil)
    }
    EndOption(form.ends == RepeatEnd.AFTER_COUNT, onSelect = { onChange { it.copy(ends = RepeatEnd.AFTER_COUNT) } }) {
        Text("After")
        Stepper(form.count, min = 1, onChange = { n -> onChange { it.copy(count = n, ends = RepeatEnd.AFTER_COUNT) } })
        Text(if (form.count == 1) "time" else "times")
    }
    FieldError(error)
}

private fun repeatHint(form: BookingForm): String? = when (form.freq) {
    RepeatFreq.MONTHLY -> "On day ${form.date.dayOfMonth}" +
        if (form.date.dayOfMonth > 28) " (shorter months: their last day)" else ""
    RepeatFreq.YEARLY -> "On ${form.date.dayOfMonth} ${form.date.month.getDisplayName(TextStyle.SHORT, Locale.ENGLISH)}" +
        if (form.date.monthValue == 2 && form.date.dayOfMonth == 29) " (28 Feb in other years)" else ""
    else -> null
}

@Composable
private fun EndOption(selected: Boolean, onSelect: () -> Unit, content: @Composable () -> Unit) {
    Row(
        Modifier.fillMaxWidth().selectable(selected = selected, role = Role.RadioButton, onClick = onSelect),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        RadioButton(selected = selected, onClick = null, modifier = Modifier.padding(12.dp))
        content()
    }
}

@Composable
private fun Stepper(value: Int, min: Int, onChange: (Int) -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        IconButton(onClick = { onChange(value - 1) }, enabled = value > min) {
            Icon(Icons.Default.Remove, contentDescription = "Fewer")
        }
        Text(
            value.toString(),
            style = MaterialTheme.typography.titleMedium,
            textAlign = TextAlign.Center,
            modifier = Modifier.width(32.dp),
        )
        IconButton(onClick = { onChange(value + 1) }, enabled = value < 999) {
            Icon(Icons.Default.Add, contentDescription = "More")
        }
    }
}

@Composable
private fun Section(text: String) {
    Text(
        text,
        style = MaterialTheme.typography.titleSmall,
        color = MaterialTheme.colorScheme.primary,
        modifier = Modifier.padding(top = 24.dp, bottom = 8.dp),
    )
}

/** A tappable value ("08:00", "Tue 14 Oct") that opens a picker. */
@Composable
private fun ValueButton(text: String, label: String, onClick: () -> Unit) {
    Surface(
        onClick = onClick,
        shape = MaterialTheme.shapes.small,
        color = MaterialTheme.colorScheme.surfaceContainerHigh,
        modifier = Modifier.semantics { contentDescription = "$label $text" },
    ) {
        Text(
            text,
            style = MaterialTheme.typography.titleMedium,
            fontWeight = FontWeight.Medium,
            modifier = Modifier.padding(horizontal = 12.dp, vertical = 12.dp),
        )
    }
}

@Composable
private fun FieldError(text: String?) {
    if (text == null) return
    Text(
        text,
        style = MaterialTheme.typography.bodySmall,
        color = MaterialTheme.colorScheme.error,
        modifier = Modifier.padding(top = 4.dp),
    )
}

/** 0-24 h bar: awake block in primary, sleeps as dim hatched bands, hour ticks every 6 h. */
@Composable
private fun DayPreview(form: BookingForm) {
    val segments = blockSegments(formatMinutes(form.start), formatMinutes(form.end), form.toGaps())
    val primary = MaterialTheme.colorScheme.primary
    val surface = MaterialTheme.colorScheme.surface
    val track = MaterialTheme.colorScheme.surfaceContainerHigh
    val describe = if (segments.isEmpty()) "No valid awake block" else buildString {
        append("Awake ${formatMinutes(form.start)} to ${formatMinutes(form.end)}")
        form.sleeps.forEach { append(", sleep ${formatMinutes(it.from)} to ${formatMinutes(it.to)}") }
    }
    Column(Modifier.fillMaxWidth().semantics { contentDescription = describe }) {
        Box(
            Modifier.fillMaxWidth().height(24.dp).clip(MaterialTheme.shapes.extraSmall).background(track)
                .drawBehind {
                    fun x(m: Int) = m / 1440f * size.width
                    segments.forEach { s ->
                        val x0 = x(s.from)
                        val w = x(s.to) - x0
                        if (!s.sleep) {
                            drawRect(primary, Offset(x0, 0f), Size(w, size.height))
                        } else {
                            drawRect(surface, Offset(x0, 0f), Size(w, size.height))
                            drawRect(primary.copy(alpha = 0.22f), Offset(x0, 0f), Size(w, size.height))
                            clipRect(x0, 0f, x0 + w, size.height) {
                                val stripe = 6.dp.toPx()
                                var sx = x0 - size.height
                                while (sx < x0 + w) {
                                    drawLine(
                                        primary.copy(alpha = 0.45f),
                                        Offset(sx, size.height),
                                        Offset(sx + size.height, 0f),
                                        strokeWidth = 1.dp.toPx(),
                                    )
                                    sx += stripe
                                }
                            }
                        }
                    }
                },
        )
        BoxWithConstraints(Modifier.fillMaxWidth().height(16.dp)) {
            val w = maxWidth
            listOf(0, 6, 12, 18, 24).forEach { h ->
                Text(
                    "%02d".format(h),
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.width(24.dp).offset(x = (w * (h / 24f) - 12.dp).coerceIn(0.dp, w - 24.dp)),
                )
            }
        }
    }
}

/** Material3 time picker in an AlertDialog (M3 1.2 has no TimePickerDialog). 24 h. */
@Composable
internal fun TimePickerDialog(title: String, initialMinutes: Int, onDismiss: () -> Unit, onConfirm: (Int) -> Unit) {
    val state = rememberTimePickerState(initialHour = initialMinutes / 60, initialMinute = initialMinutes % 60, is24Hour = true)
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(title) },
        text = { TimePicker(state = state) },
        confirmButton = { TextButton(onClick = { onConfirm(state.hour * 60 + state.minute) }) { Text("OK") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun DatePickerDialogFor(initial: LocalDate, onDismiss: () -> Unit, onConfirm: (LocalDate) -> Unit) {
    // DatePicker works in UTC-midnight millis.
    val state = rememberDatePickerState(initialSelectedDateMillis = initial.atStartOfDay(ZoneOffset.UTC).toInstant().toEpochMilli())
    DatePickerDialog(
        onDismissRequest = onDismiss,
        confirmButton = {
            TextButton(
                onClick = {
                    state.selectedDateMillis?.let { onConfirm(Instant.ofEpochMilli(it).atZone(ZoneOffset.UTC).toLocalDate()) }
                        ?: onDismiss()
                },
            ) { Text("OK") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    ) { DatePicker(state = state) }
}

/** "Tue 14 Oct 2026". */
private fun longDate(d: LocalDate): String =
    "${d.dayOfWeek.getDisplayName(TextStyle.SHORT, Locale.ENGLISH)} ${d.dayOfMonth} " +
        "${d.month.getDisplayName(TextStyle.SHORT, Locale.ENGLISH)} ${d.year}"
