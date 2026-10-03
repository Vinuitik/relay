package com.relay.app.ui.theme

import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Shapes
import androidx.compose.ui.unit.dp

// DESIGN.md "Shape & spacing". Use MaterialTheme.shapes.* - no inline RoundedCornerShape elsewhere.
val RelayShapes = Shapes(
    extraSmall = RoundedCornerShape(4.dp),
    small = RoundedCornerShape(8.dp),
    medium = RoundedCornerShape(12.dp), // cards, bubbles
    large = RoundedCornerShape(16.dp),
    extraLarge = RoundedCornerShape(28.dp), // sheets, dialogs
)

/** Fully rounded (buttons, chips). M3 `Shapes` has no `full` slot in this version. */
val FullShape = RoundedCornerShape(percent = 50)
