package com.relay.app.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
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
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp

/**
 * Minimal markdown for agent replies: headings, bullet/numbered lists, quotes, rules,
 * fenced code blocks, and inline **bold** / *italic* / `code` / [links](url) / ~~strike~~.
 * Tables and nested lists render as plain text. Hand-rolled to avoid a markdown dependency.
 */
@Composable
fun MarkdownText(text: String, color: Color, modifier: Modifier = Modifier) {
    val blocks = remember(text) { parseBlocks(text) }
    val codeBg = MaterialTheme.colorScheme.surface.copy(alpha = 0.5f)
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
                        .background(codeBg, RoundedCornerShape(6.dp))
                        .horizontalScroll(rememberScrollState())
                        .padding(8.dp),
                ) {
                    Text(
                        text = block.text,
                        color = color,
                        fontFamily = FontFamily.Monospace,
                        style = MaterialTheme.typography.bodySmall,
                        softWrap = false,
                    )
                }
                Block.Rule -> HorizontalDivider(color = color.copy(alpha = 0.3f))
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
}

private val headingRe = Regex("^(#{1,6})\\s+(.*)$")
private val bulletRe = Regex("^(\\s*)[-*+]\\s+(.*)$")
private val numberedRe = Regex("^(\\s*)(\\d+[.)])\\s+(.*)$")
private val ruleRe = Regex("^\\s*([-*_])(\\s*\\1){2,}\\s*$")

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
