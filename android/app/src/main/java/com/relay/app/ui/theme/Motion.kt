package com.relay.app.ui.theme

import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.Easing

// DESIGN.md "Motion". Tokens only. UI motion < 300ms; ease-out on enter/exit; exits faster than enters.
object RelayMotion {
    /** Enter/exit and most UI motion. */
    val EaseOut: Easing = CubicBezierEasing(0.23f, 1f, 0.32f, 1f)

    /** Things that move while on screen. */
    val EaseInOut: Easing = CubicBezierEasing(0.77f, 0f, 0.175f, 1f)

    const val DurationShort = 150
    const val DurationMedium = 220
    const val DurationScreen = 280
}
