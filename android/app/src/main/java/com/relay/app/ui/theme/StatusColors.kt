package com.relay.app.ui.theme

import androidx.compose.material3.ColorScheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color

/** What a lamp / chip is saying. Colour is never the only signal - pair with a word or icon. */
enum class RelayStatus { Success, Warning, Busy, Idle, Error }

/** One status hue: [fg] for lamp/icon/text on surface, [chipBg]/[chipOn] for a filled chip. */
@Immutable
data class StatusColor(val fg: Color, val chipBg: Color, val chipOn: Color)

@Immutable
data class RelayStatusColors(
    val success: StatusColor,
    val warning: StatusColor,
    val busy: StatusColor,
    val idle: StatusColor,
    val error: StatusColor,
) {
    operator fun get(status: RelayStatus): StatusColor = when (status) {
        RelayStatus.Success -> success
        RelayStatus.Warning -> warning
        RelayStatus.Busy -> busy
        RelayStatus.Idle -> idle
        RelayStatus.Error -> error
    }
}

private fun schemeDerived(
    scheme: ColorScheme,
    success: StatusColor,
    warning: StatusColor,
) = RelayStatusColors(
    success = success,
    warning = warning,
    busy = StatusColor(scheme.primary, scheme.primaryContainer, scheme.onPrimaryContainer),
    idle = StatusColor(scheme.onSurfaceVariant, scheme.surfaceContainerHighest, scheme.onSurfaceVariant),
    error = StatusColor(scheme.error, scheme.errorContainer, scheme.onErrorContainer),
)

internal val LightStatusColors = schemeDerived(
    LightColors,
    success = StatusColor(Color(0xFF006C50), Color(0xFF7FF9CB), Color(0xFF002116)),
    warning = StatusColor(Color(0xFF895200), Color(0xFFFFDCBC), Color(0xFF2C1700)),
)

internal val DarkStatusColors = schemeDerived(
    DarkColors,
    success = StatusColor(Color(0xFF61DCB0), Color(0xFF00513C), Color(0xFF7FF9CB)),
    warning = StatusColor(Color(0xFFFFB86A), Color(0xFF683D00), Color(0xFFFFDCBC)),
)

val LocalRelayStatusColors = staticCompositionLocalOf { DarkStatusColors }

/** `MaterialTheme.statusColors.warning.fg`, or `MaterialTheme.statusColors[RelayStatus.Busy]`. */
val MaterialTheme.statusColors: RelayStatusColors
    @Composable @ReadOnlyComposable get() = LocalRelayStatusColors.current
