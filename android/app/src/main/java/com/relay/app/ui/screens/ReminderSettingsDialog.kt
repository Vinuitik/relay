package com.relay.app.ui.screens

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.selection.toggleable
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.dp
import com.relay.app.reminders.ReminderLogic
import com.relay.app.reminders.ReminderPrefs
import com.relay.app.reminders.ReminderSettings
import kotlinx.coroutines.launch
import java.time.LocalTime

/**
 * Schedule ⋮ → Reminders. Every change is saved immediately through [ReminderPrefs] setters
 * (which re-schedule the alarms), so the dialog only has "Done". Times are phone-local.
 */
@Composable
fun ReminderSettingsDialog(onDismiss: () -> Unit) {
    val context = LocalContext.current
    val prefs = remember { ReminderPrefs(context) }
    val settings by prefs.settings.collectAsState(initial = ReminderSettings())
    val scope = rememberCoroutineScope()
    var picking by remember { mutableStateOf<String?>(null) } // "morning" | "evening"

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Reminders") },
        text = {
            Column {
                Row(
                    Modifier.fillMaxWidth().toggleable(
                        value = settings.enabled,
                        role = Role.Switch,
                        onValueChange = { on -> scope.launch { prefs.setEnabled(on) } },
                    ).padding(vertical = 8.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text("Schedule reminders", style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f))
                    Spacer(Modifier.width(8.dp))
                    Switch(checked = settings.enabled, onCheckedChange = null)
                }
                Text(
                    "On booked days only: morning = turn the server on, evening = power it down.",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                TimeRow("Morning", settings.morning, settings.enabled) { picking = "morning" }
                TimeRow("Evening", settings.evening, settings.enabled) { picking = "evening" }
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text("Done") } },
    )

    picking?.let { which ->
        val current = if (which == "morning") settings.morning else settings.evening
        TimePickerDialog(
            title = if (which == "morning") "Morning reminder" else "Evening reminder",
            initialMinutes = current.hour * 60 + current.minute,
            onDismiss = { picking = null },
            onConfirm = { m ->
                picking = null
                val t = LocalTime.of(m / 60, m % 60)
                scope.launch { if (which == "morning") prefs.setMorning(t) else prefs.setEvening(t) }
            },
        )
    }
}

@Composable
private fun TimeRow(label: String, time: LocalTime, enabled: Boolean, onClick: () -> Unit) {
    Row(Modifier.fillMaxWidth().padding(top = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(label, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f))
        TextButton(onClick = onClick, enabled = enabled) {
            Text(ReminderLogic.formatTime(time), style = MaterialTheme.typography.titleMedium)
        }
    }
}
