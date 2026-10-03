package com.relay.app.ui.theme

import androidx.compose.material3.Typography
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp
import com.relay.app.R

/** Titles + wordmark. Only SemiBold is bundled - other weights get synthesised. */
val BarlowSemiCondensed = FontFamily(
    Font(R.font.barlow_semi_condensed_semibold, FontWeight.SemiBold),
)

/** Code. Bundled (not downloadable) so chat doesn't flicker on first render. */
val JetBrainsMono = FontFamily(
    Font(R.font.jetbrains_mono_regular, FontWeight.Normal),
    Font(R.font.jetbrains_mono_medium, FontWeight.Medium),
)

private val Base = Typography()

// UI text stays Roboto (M3 defaults); only titles switch to Barlow. Mapping per DESIGN.md "Type".
val RelayTypography = Typography(
    titleLarge = Base.titleLarge.copy(
        fontFamily = BarlowSemiCondensed,
        fontWeight = FontWeight.SemiBold,
    ),
    titleMedium = Base.titleMedium.copy(
        fontFamily = BarlowSemiCondensed,
        fontWeight = FontWeight.SemiBold,
        fontSize = 18.sp,
    ),
    labelSmall = Base.labelSmall.copy(fontSize = 11.sp, lineHeight = 16.sp),
)

private val CodeSmall = TextStyle(
    fontFamily = JetBrainsMono,
    fontWeight = FontWeight.Normal,
    fontSize = 12.sp,
    lineHeight = 16.sp,
)

private val CodeMedium = TextStyle(
    fontFamily = JetBrainsMono,
    fontWeight = FontWeight.Normal,
    fontSize = 13.sp,
    lineHeight = 20.sp,
)

/** 12/16 mono - tool rows, file sizes/paths. `MaterialTheme.typography.codeSmall`. */
@Suppress("UnusedReceiverParameter")
val Typography.codeSmall: TextStyle get() = CodeSmall

/** 13/20 mono - code blocks, commands. `MaterialTheme.typography.codeMedium`. */
@Suppress("UnusedReceiverParameter")
val Typography.codeMedium: TextStyle get() = CodeMedium
