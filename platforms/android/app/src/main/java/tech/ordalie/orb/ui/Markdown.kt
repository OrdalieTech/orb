package tech.ordalie.orb.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicText
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/** Markdown blocks a model actually writes. Anything else stays a paragraph. */
private sealed interface Block
private data class Para(val text: String) : Block
private data class Head(val text: String, val level: Int) : Block
private data class Item(val mark: String, val text: String, val depth: Int) : Block
private data class Quote(val text: String) : Block
private data class Code(val lang: String, val text: String) : Block
private data object Break : Block

private val ITEM = Regex("""^(\s*)([-*+]|\d+[.)])\s+(.*)$""")

private fun blocks(md: String): List<Block> = buildList {
    val lines = md.lines()
    var i = 0
    val para = StringBuilder()
    fun flush() { if (para.isNotBlank()) add(Para(para.toString().trim())); para.clear() }
    while (i < lines.size) {
        val line = lines[i]
        val t = line.trimStart()
        when {
            t.startsWith("```") -> {
                flush()
                val lang = t.removePrefix("```").trim()
                val body = StringBuilder()
                i++
                while (i < lines.size && !lines[i].trimStart().startsWith("```")) { body.appendLine(lines[i]); i++ }
                add(Code(lang, body.toString().trimEnd('\n')))
            }
            t.startsWith("#") -> { flush(); add(Head(t.trimStart('#').trim(), t.takeWhile { it == '#' }.length)) }
            t.startsWith(">") -> { flush(); add(Quote(t.removePrefix(">").trim())) }
            ITEM.matches(line) -> { flush(); ITEM.find(line)!!.groupValues.let { add(Item(if (it[2][0].isDigit()) it[2] else "·", it[3], it[1].length / 2)) } }
            t == "---" || t == "***" -> { flush(); add(Break) }
            t.isEmpty() -> flush()
            else -> para.append(if (para.isEmpty()) line else "\n$line")
        }
        i++
    }
    flush()
}

/** **bold**, *italic*, `code`, [links](url) — inline only. */
fun inline(text: String, code: Color, codeBg: Color): AnnotatedString = buildAnnotatedString {
    var i = 0
    while (i < text.length) {
        when {
            text.startsWith("**", i) && text.indexOf("**", i + 2) > i + 1 -> { val e = text.indexOf("**", i + 2); withStyle(SpanStyle(fontWeight = FontWeight.Bold)) { append(text, i + 2, e) }; i = e + 2 }
            text[i] == '`' && text.indexOf('`', i + 1) > i -> { val e = text.indexOf('`', i + 1); withStyle(SpanStyle(color = code, background = codeBg)) { append(" "); append(text, i + 1, e); append(" ") }; i = e + 1 }
            text[i] == '[' && text.indexOf("](", i) > i && text.indexOf(')', text.indexOf("](", i)) > 0 -> {
                val mid = text.indexOf("](", i); val end = text.indexOf(')', mid)
                withStyle(SpanStyle(textDecoration = TextDecoration.Underline)) { append(text, i + 1, mid) }; i = end + 1
            }
            text[i] == '*' && i + 1 < text.length && text[i + 1] != ' ' && text.indexOf('*', i + 1) > i -> { val e = text.indexOf('*', i + 1); withStyle(SpanStyle(color = code)) { append(text, i + 1, e) }; i = e + 1 }
            else -> { append(text[i]); i++ }
        }
    }
}

@Composable
fun Markdown(text: String, modifier: Modifier = Modifier, size: Float = 15f) = Column(modifier, verticalArrangement = Arrangement.spacedBy((size * 0.6f).dp)) {
    val body = mono(size.sp, p.fg)
    blocks(text).forEach { b ->
        when (b) {
            is Para -> BasicText(inline(b.text, p.mute, p.raised), style = body)
            is Head -> BasicText(inline(b.text, p.mute, p.raised), Modifier.padding(top = 4.dp), mono(if (b.level <= 2) (size + 3).sp else (size + 1).sp, p.fg, bold = true))
            is Item -> Row(Modifier.padding(start = (b.depth * 18).dp)) {
                T(b.mark, Modifier.width(if (b.mark == "·") (size + 1).dp else (size * 1.8f).dp), size = size.sp, color = p.meta)
                BasicText(inline(b.text, p.mute, p.raised), Modifier.weight(1f), body)
            }
            is Quote -> Row { Box(Modifier.width(2.dp).height(22.dp).background(p.rule)); BasicText(inline(b.text, p.mute, p.raised), Modifier.padding(start = 12.dp), body.copy(color = p.mute)) }
            is Code -> Column(Modifier.fillMaxWidth().background(p.raised, RoundedCornerShape(16.dp)).border(1.dp, p.rule, RoundedCornerShape(16.dp)).padding(14.dp)) {
                if (b.lang.isNotEmpty()) T(b.lang, Modifier.padding(bottom = 6.dp), label = true, color = p.meta)
                Box(Modifier.horizontalScroll(rememberScrollState())) { BasicText(b.text, style = mono((size - 3).sp, p.fg).copy(lineHeight = (size + 2).sp), softWrap = false) }
            }
            Break -> Spacer(Modifier.height(4.dp))
        }
    }
}
