package tech.ordalie.orb.ui

import androidx.compose.foundation.*
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.text.BasicText
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.*
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.*
import tech.ordalie.orb.core.*

/** Styled runs as text: bold, italic, code on a raised ground, struck, links underlined. */
fun spans(spans: List<Span>, code: Color, codeBg: Color): AnnotatedString = buildAnnotatedString {
    spans.forEach { s ->
        withStyle(SpanStyle(
            fontWeight = if (s.bold) Strong else null, fontStyle = if (s.italic) FontStyle.Italic else null,
            color = if (s.code) code else Color.Unspecified, background = if (s.code) codeBg else Color.Unspecified,
            textDecoration = when { s.strike -> TextDecoration.LineThrough; s.href.isNotEmpty() -> TextDecoration.Underline; else -> null },
        )) { append(s.text) }
    }
}

/** One block of what Orb wrote, parsed by the view: a paragraph, heading, list item, quote, code, drawing, table or rule. */
@Composable
fun Markdown(b: Block, size: Float = SIZE, ink: Color = p.fg) {
    val body = type(size.sp, ink)
    val indent = Modifier.padding(start = (b.depth * 18).dp)
    when (b.type) {
        "p" -> BasicText(spans(b.spans, p.mute, p.raised), indent, body)
        "h" -> BasicText(spans(b.spans, p.mute, p.raised), indent.padding(top = 4.dp), type(if (b.level <= 2) (size + 3).sp else (size + 1).sp, ink, Strong))
        "li" -> Row(indent) {
            T(b.mark, Modifier.width(if (b.mark == "·") (size + 1).dp else (size * 1.8f).dp), size = size.sp, color = p.meta, weight = Medium)
            BasicText(spans(b.spans, p.mute, p.raised), Modifier.weight(1f), body)
        }
        // A quote holds blocks of its own, its bar as tall as they are.
        "quote" -> {
            val rule = p.rule
            Column(indent.drawBehind { drawRect(rule, size = this.size.copy(width = 2.dp.toPx())) }.padding(start = 14.dp), verticalArrangement = Arrangement.spacedBy((size * 0.6f).dp)) {
                b.blocks.forEach { Markdown(it, size, p.mute) }
            }
        }
        "code", "art" -> Code(b, size, indent)
        "table" -> Grid(b.rows, size, indent)
        "rule" -> Spacer(Modifier.height(4.dp))
    }
}

/** Code wraps, so a long line reads without sideways scrolling, which swipes pages here; a drawing scrolls, its lines flush. */
@Composable
private fun Code(b: Block, size: Float, modifier: Modifier) = Column(modifier.fillMaxWidth().background(p.raised, Pane).border(1.dp, p.rule, Pane).padding(12.dp)) {
    if (b.lang.isNotEmpty()) T(b.lang, Modifier.padding(bottom = 6.dp), label = true, color = p.meta)
    if (b.type == "art") Box(Modifier.horizontalScroll(rememberScrollState())) { BasicText(b.text, style = type((size - 3).sp, p.fg).copy(lineHeight = 1.1.em, letterSpacing = 0.sp), softWrap = false) }
    else BasicText(b.text, style = type((size - 3).sp, p.fg))
}

/** A table as wide as its cells (each column up to 28 characters, wrapping beyond); a wide one scrolls sideways. */
@Composable
private fun Grid(rows: List<List<List<Span>>>, size: Float, modifier: Modifier) {
    if (rows.isEmpty()) return
    val cols = rows.maxOf { it.size }
    val widths = (0 until cols).map { c -> ((rows.maxOf { r -> r.getOrNull(c)?.sumOf { it.text.length } ?: 0 }.coerceIn(3, 28)) * (size - 1) * 0.58f + 16).dp }
    Column(modifier.horizontalScroll(rememberScrollState()).border(1.dp, p.rule, Soft)) {
        rows.forEachIndexed { r, row ->
            if (r > 0) Box(Modifier.width(widths.fold(0.dp) { a, w -> a + w }).height(1.dp).background(p.rule))
            Row(Modifier.background(if (r == 0) p.raised else Color.Transparent)) {
                for (c in 0 until cols) BasicText(spans(row.getOrNull(c).orEmpty(), p.mute, p.raised), Modifier.width(widths[c]).padding(horizontal = 8.dp, vertical = 6.dp),
                    type((size - 1).sp, p.fg, if (r == 0) Strong else Regular))
            }
        }
    }
}
