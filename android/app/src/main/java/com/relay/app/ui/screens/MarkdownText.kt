package com.relay.app.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.dp
import com.relay.app.ui.theme.codeMedium

/**
 * Minimal markdown for agent replies: headings, bullet/numbered lists, quotes, rules,
 * fenced code blocks, pipe tables, and inline **bold** / *italic* / `code` / [links](url) / ~~strike~~.
 * Nested lists render flat. Hand-rolled to avoid a markdown dependency.
 */
@Composable
fun MarkdownText(text: String, color: Color, modifier: Modifier = Modifier) {
    val blocks = remember(text) { parseBlocks(text) }
    // Agent text sits directly on surface (no bubble), so code needs its own tonal step.
    val codeBg = MaterialTheme.colorScheme.surfaceContainerHigh
    Column(modifier = modifier, verticalArrangement = Arrangement.spacedBy(6.dp)) {
        blocks.forEach { block ->
            when (block) {
                is Block.Heading -> Text(
                    text = inline(block.text, codeBg),
                    color = color,
                    style = when (block.level) {
                        1 -> MaterialTheme.typography.titleLarge
                        2 -> MaterialTheme.typography.titleMedium
                        else -> MaterialTheme.typography.titleSmall
                    },
                    fontWeight = FontWeight.Bold,
                )
                is Block.Paragraph -> Text(inline(block.text, codeBg), color = color)
                is Block.ListItem -> Row(modifier = Modifier.padding(start = (block.indent * 12).dp)) {
                    Text(block.marker, color = color, modifier = Modifier.padding(end = 6.dp))
                    Text(inline(block.text, codeBg), color = color)
                }
                is Block.Quote -> Row {
                    Box(
                        modifier = Modifier
                            .padding(end = 8.dp)
                            .background(color.copy(alpha = 0.4f))
                            .padding(horizontal = 1.5.dp, vertical = 10.dp),
                    )
                    Text(inline(block.text, codeBg), color = color.copy(alpha = 0.8f), fontStyle = FontStyle.Italic)
                }
                is Block.Code -> Box(
                    modifier = Modifier
                        .fillMaxWidth()
                        .background(codeBg, MaterialTheme.shapes.small)
                        .horizontalScroll(rememberScrollState())
                        .padding(8.dp),
                ) {
                    Text(
                        text = block.text,
                        color = color,
                        style = MaterialTheme.typography.codeMedium,
                        softWrap = false,
                    )
                }
                Block.Rule -> HorizontalDivider(color = color.copy(alpha = 0.3f))
                is Block.Table -> MarkdownTable(block, color, codeBg)
            }
        }
    }
}

private sealed interface Block {
    data class Heading(val level: Int, val text: String) : Block
    data class Paragraph(val text: String) : Block
    data class ListItem(val marker: String, val text: String, val indent: Int) : Block
    data class Quote(val text: String) : Block
    data class Code(val text: String) : Block
    data object Rule : Block
    data class Table(val header: List<String>, val rows: List<List<String>>) : Block
}

/**
 * Row-major grid: column widths come from measuring each column's widest cell, capped so
 * long prose wraps instead of producing one huge column. Scrolls sideways when wider than
 * the screen. To change the cap: [maxCellWidth].
 */
@Composable
private fun MarkdownTable(table: Block.Table, color: Color, codeBg: Color) {
    val measurer = rememberTextMeasurer()
    val style = MaterialTheme.typography.bodyMedium
    val density = LocalDensity.current
    val cellPad = 8.dp
    val widths = remember(table, style) {
        (table.header.indices).map { col ->
            val widest = (listOf(table.header) + table.rows).maxOf { row ->
                measurer.measure(inline(row[col], codeBg), style.copy(fontWeight = FontWeight.Bold)).size.width
            }
            with(density) { widest.toDp() }.coerceAtMost(maxCellWidth) + cellPad * 2
        }
    }
    val lineColor = color.copy(alpha = 0.25f)
    Column(
        modifier = Modifier
            .horizontalScroll(rememberScrollState())
            .background(codeBg.copy(alpha = 0.5f), MaterialTheme.shapes.small),
    ) {
        (listOf(table.header) + table.rows).forEachIndexed { r, row ->
            if (r > 0) HorizontalDivider(modifier = Modifier.width(widths.fold(0.dp) { a, b -> a + b }), color = lineColor)
            Row(modifier = Modifier.height(IntrinsicSize.Min)) {
                row.forEachIndexed { c, cell ->
                    Text(
                        text = inline(cell, codeBg),
                        color = color,
                        style = style,
                        fontWeight = if (r == 0) FontWeight.Bold else null,
                        modifier = Modifier.width(widths[c]).fillMaxHeight().padding(horizontal = cellPad, vertical = 6.dp),
                    )
                }
            }
        }
    }
}

private val maxCellWidth = 220.dp

private val headingRe = Regex("^(#{1,6})\\s+(.*)$")
private val bulletRe = Regex("^(\\s*)[-*+]\\s+(.*)$")
private val numberedRe = Regex("^(\\s*)(\\d+[.)])\\s+(.*)$")
private val ruleRe = Regex("^\\s*([-*_])(\\s*\\1){2,}\\s*$")
private val tableSepRe = Regex("^\\s*\\|?\\s*:?-+:?\\s*(\\|\\s*:?-+:?\\s*)*\\|?\\s*$")

/** `| a | b |` → [a, b]. Outer pipes optional; `\|` stays a literal pipe. */
private fun tableCells(line: String): List<String> {
    val t = line.trim().removePrefix("|").let { if (it.endsWith("|") && !it.endsWith("\\|")) it.dropLast(1) else it }
    return t.split(Regex("(?<!\\\\)\\|")).map { it.trim().replace("\\|", "|") }
}

private fun parseBlocks(text: String): List<Block> {
    val blocks = mutableListOf<Block>()
    val paragraph = mutableListOf<String>()
    fun flush() {
        if (paragraph.isNotEmpty()) blocks += Block.Paragraph(paragraph.joinToString("\n"))
        paragraph.clear()
    }

    val lines = text.lines()
    var i = 0
    while (i < lines.size) {
        val line = lines[i]
        val trimmed = line.trimStart()
        when {
            trimmed.startsWith("```") -> {
                flush()
                val code = mutableListOf<String>()
                i++
                while (i < lines.size && !lines[i].trimStart().startsWith("```")) code += lines[i++]
                blocks += Block.Code(code.joinToString("\n"))
            }
            line.isBlank() -> flush()
            // Table: a pipe row followed by a `---|---` separator; body runs until a pipe-less line.
            trimmed.contains('|') && i + 1 < lines.size && tableSepRe.matches(lines[i + 1]) &&
                lines[i + 1].contains('|') -> {
                flush()
                val header = tableCells(line)
                i += 2
                val rows = mutableListOf<List<String>>()
                while (i < lines.size && lines[i].isNotBlank() && lines[i].contains('|')) {
                    val cells = tableCells(lines[i++])
                    rows += List(header.size) { cells.getOrElse(it) { "" } }
                }
                i--
                blocks += Block.Table(header, rows)
            }
            ruleRe.matches(line) -> { flush(); blocks += Block.Rule }
            headingRe.matches(trimmed) -> {
                flush()
                val m = headingRe.find(trimmed)!!
                blocks += Block.Heading(m.groupValues[1].length, m.groupValues[2])
            }
            bulletRe.matches(line) -> {
                flush()
                val m = bulletRe.find(line)!!
                blocks += Block.ListItem("•", m.groupValues[2], m.groupValues[1].length / 2)
            }
            numberedRe.matches(line) -> {
                flush()
                val m = numberedRe.find(line)!!
                blocks += Block.ListItem(m.groupValues[2], m.groupValues[3], m.groupValues[1].length / 2)
            }
            trimmed.startsWith(">") -> {
                flush()
                blocks += Block.Quote(trimmed.removePrefix(">").trimStart())
            }
            else -> paragraph += line
        }
        i++
    }
    flush()
    return blocks
}

/** Inline spans. Unclosed markers are left as literal text. */
private fun inline(text: String, codeBg: Color): AnnotatedString = buildAnnotatedString {
    var i = 0
    while (i < text.length) {
        val rest = text.substring(i)
        fun closing(marker: String): Int {
            val end = text.indexOf(marker, i + marker.length)
            return if (end > i + marker.length) end else -1
        }
        when {
            rest.startsWith("`") && closing("`") != -1 -> {
                val end = closing("`")
                withStyle(SpanStyle(fontFamily = FontFamily.Monospace, background = codeBg)) {
                    append(text.substring(i + 1, end))
                }
                i = end + 1
            }
            rest.startsWith("**") && closing("**") != -1 -> {
                val end = closing("**")
                withStyle(SpanStyle(fontWeight = FontWeight.Bold)) { append(inline(text.substring(i + 2, end), codeBg)) }
                i = end + 2
            }
            rest.startsWith("~~") && closing("~~") != -1 -> {
                val end = closing("~~")
                withStyle(SpanStyle(textDecoration = TextDecoration.LineThrough)) {
                    append(inline(text.substring(i + 2, end), codeBg))
                }
                i = end + 2
            }
            // `_` is not treated as emphasis, so snake_case names stay intact.
            rest.startsWith("*") && rest.length > 1 && !rest[1].isWhitespace() && closing("*") != -1 -> {
                val end = closing("*")
                withStyle(SpanStyle(fontStyle = FontStyle.Italic)) { append(inline(text.substring(i + 1, end), codeBg)) }
                i = end + 1
            }
            rest.startsWith("[") -> {
                val m = Regex("^\\[([^\\]]+)]\\(([^)]+)\\)").find(rest)
                if (m != null) {
                    withStyle(SpanStyle(textDecoration = TextDecoration.Underline)) { append(m.groupValues[1]) }
                    i += m.value.length
                } else {
                    append('['); i++
                }
            }
            else -> { append(text[i]); i++ }
        }
    }
}
