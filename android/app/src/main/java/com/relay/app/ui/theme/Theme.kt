package com.relay.app.ui.theme

import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider

@Composable
fun RelayTheme(
    // Always dark (user's choice, f49fceb). Pass isSystemInDarkTheme() to follow the phone; the
    // light scheme is fully defined. Dynamic (wallpaper) colour is deliberately off - status hues
    // must stay recognisable (DESIGN.md "Colour").
    darkTheme: Boolean = true,
    content: @Composable () -> Unit,
) {
    CompositionLocalProvider(
        LocalRelayStatusColors provides if (darkTheme) DarkStatusColors else LightStatusColors,
    ) {
        MaterialTheme(
            colorScheme = if (darkTheme) DarkColors else LightColors,
            typography = RelayTypography,
            shapes = RelayShapes,
            content = content,
        )
    }
}
