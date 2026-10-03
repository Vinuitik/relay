package com.relay.app.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.relay.app.ui.theme.FullShape
import com.relay.app.ui.theme.RelayStatus
import com.relay.app.ui.theme.statusColors

/**
 * Signal-box lamp: a [size] disc with a 1.5dp bezel. [lit] = filled, dark = bezel only, so the
 * state reads without colour. Colour is the status `fg`.
 */
@Composable
fun StatusLamp(
    status: RelayStatus,
    modifier: Modifier = Modifier,
    size: Dp = 10.dp,
    lit: Boolean = true,
) {
    Lamp(MaterialTheme.statusColors[status].fg, lit, size, modifier)
}

@Composable
private fun Lamp(color: Color, lit: Boolean, size: Dp, modifier: Modifier = Modifier) {
    Box(
        modifier
            .size(size)
            .border(1.5.dp, color, CircleShape)
            .then(if (lit) Modifier.background(color, CircleShape) else Modifier),
    )
}

/**
 * Fully rounded status pill in the status chip colours (chipBg / chipOn), labelMedium text.
 * Leads with [icon] if given, otherwise a lit lamp.
 */
@Composable
fun StatusChip(
    status: RelayStatus,
    label: String,
    modifier: Modifier = Modifier,
    icon: ImageVector? = null,
) {
    val colors = MaterialTheme.statusColors[status]
    Surface(
        modifier = modifier,
        shape = FullShape,
        color = colors.chipBg,
        contentColor = colors.chipOn,
    ) {
        Row(
            modifier = Modifier.padding(horizontal = 12.dp, vertical = 4.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (icon != null) {
                Icon(icon, contentDescription = null, modifier = Modifier.size(16.dp))
            } else {
                Lamp(colors.chipOn, lit = true, size = 10.dp)
            }
            Text(label, style = MaterialTheme.typography.labelMedium)
        }
    }
}
