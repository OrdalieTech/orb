package tech.ordalie.orb.ui

import androidx.compose.foundation.*
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.text.BasicText
import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalContext
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import tech.ordalie.orb.runtime
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.*
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.*

/** Markdown blocks a model actually writes. Anything else stays a paragraph. */
private sealed interface Block
private data class Para(val text: String) : Block
private data class Head(val text: String, val level: Int) : Block
private data class Item(val mark: String, val text: String, val depth: Int) : Block
private data class Quote(val text: String) : Block
private data class Code(val lang: String, val text: String) : Block
private data class Table(val rows: List<List<String>>) : Block // the first row is the header
private data object Break : Block

private val ITEM = Regex("""^(\s*)([-*+]|\d+[.)])\s+(.*)$""")
private val RULER = Regex("""^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$""")
private fun cells(line: String) = line.trim().removePrefix("|").removeSuffix("|").split('|').map(String::trim)

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
            t.startsWith("|") && i + 1 < lines.size && RULER.matches(lines[i + 1].trim()) -> {
                flush()
                val rows = mutableListOf(cells(t))
                i += 2
                while (i < lines.size && lines[i].trimStart().startsWith("|")) rows += cells(lines[i++])
                add(Table(rows))
                i--
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
            text.startsWith("**", i) && text.indexOf("**", i + 2) > i + 1 -> { val e = text.indexOf("**", i + 2); withStyle(SpanStyle(fontWeight = Strong)) { append(text, i + 2, e) }; i = e + 2 }
            text[i] == '`' && text.indexOf('`', i + 1) > i -> { val e = text.indexOf('`', i + 1); withStyle(SpanStyle(color = code, background = codeBg)) { append(" "); append(text, i + 1, e); append(" ") }; i = e + 1 }
            text[i] == '[' && text.indexOf("](", i) > i && text.indexOf(')', text.indexOf("](", i)) > 0 -> {
                val mid = text.indexOf("](", i); val end = text.indexOf(')', mid)
                withStyle(SpanStyle(textDecoration = TextDecoration.Underline)) { append(text, i + 1, mid) }; i = end + 1
            }
            text[i] == '*' && i + 1 < text.length && text[i + 1] != ' ' && text.indexOf('*', i + 1) > i -> { val e = text.indexOf('*', i + 1); withStyle(SpanStyle(color = code, fontStyle = FontStyle.Italic)) { append(text, i + 1, e) }; i = e + 1 }
            else -> { append(text[i]); i++ }
        }
    }
}

/** Mermaid art by source, drawn by this phone's orb (`orb mermaid`), the TUI's renderer; null when not a diagram. */
private val drawings = object : LinkedHashMap<String, String?>(16, 0.75f, true) {
    override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, String?>) = size > 32
}

/** A Mermaid block as the TUI draws it, once its message is done; the code until then, or when it does not parse. */
@Composable
private fun Diagram(source: String, done: Boolean, size: Float) {
    val orb = LocalContext.current.runtime.orb
    val art by produceState(drawings[source], source, done) {
        if (done && !drawings.containsKey(source)) value = withContext(Dispatchers.IO) { orb.run("mermaid", stdin = source).let { (code, out) -> out.trimEnd().takeIf { code == 0 } } }.also { drawings[source] = it }
    }
    CodeBox(if (art == null) "mermaid" else "", art ?: source, size, art != null)
}

@Composable
private fun CodeBox(lang: String, text: String, size: Float, drawing: Boolean = false) = Column(Modifier.fillMaxWidth().background(p.raised, Pane).border(1.dp, p.rule, Pane).padding(12.dp)) {
    if (lang.isNotEmpty()) T(lang, Modifier.padding(bottom = 6.dp), label = true, color = p.meta)
    // Box-drawing lines join only when lines sit flush.
    Box(Modifier.horizontalScroll(rememberScrollState())) { BasicText(text, style = type((size - 3).sp, p.fg).let { if (drawing) it.copy(lineHeight = 1.1.em, letterSpacing = 0.sp) else it }, softWrap = false) }
}

/** A table as wide as its cells (each column up to 28 characters, wrapping beyond); a wide one scrolls sideways. */
@Composable
private fun Grid(rows: List<List<String>>, size: Float) {
    val cols = rows.maxOf { it.size }
    val widths = (0 until cols).map { c -> ((rows.maxOf { it.getOrNull(c)?.length ?: 0 }.coerceIn(3, 28)) * (size - 1) * 0.58f + 16).dp }
    Column(Modifier.horizontalScroll(rememberScrollState()).border(1.dp, p.rule, Soft)) {
        rows.forEachIndexed { r, row ->
            if (r > 0) Box(Modifier.width(widths.fold(0.dp) { a, w -> a + w }).height(1.dp).background(p.rule))
            Row(Modifier.background(if (r == 0) p.raised else Color.Transparent)) {
                for (c in 0 until cols) BasicText(inline(row.getOrNull(c).orEmpty(), p.mute, p.raised), Modifier.width(widths[c]).padding(horizontal = 8.dp, vertical = 6.dp),
                    type((size - 1).sp, p.fg, if (r == 0) Strong else Regular))
            }
        }
    }
}

/** [done] once the message is complete: diagrams are drawn then, not at every streamed token. */
@Composable
fun Markdown(text: String, modifier: Modifier = Modifier, size: Float = 15f, done: Boolean = true) = Column(modifier, verticalArrangement = Arrangement.spacedBy((size * 0.6f).dp)) {
    val body = type(size.sp, p.fg)
    blocks(text).forEach { b ->
        when (b) {
            is Para -> BasicText(inline(b.text, p.mute, p.raised), style = body)
            is Head -> BasicText(inline(b.text, p.mute, p.raised), Modifier.padding(top = 4.dp), type(if (b.level <= 2) (size + 3).sp else (size + 1).sp, p.fg, Strong))
            is Item -> Row(Modifier.padding(start = (b.depth * 18).dp)) {
                T(b.mark, Modifier.width(if (b.mark == "·") (size + 1).dp else (size * 1.8f).dp), size = size.sp, color = p.meta, weight = Medium)
                BasicText(inline(b.text, p.mute, p.raised), Modifier.weight(1f), body)
            }
            is Quote -> Row { Box(Modifier.width(2.dp).height(22.dp).background(p.rule)); BasicText(inline(b.text, p.mute, p.raised), Modifier.padding(start = 12.dp), body.copy(color = p.mute)) }
            is Code -> if (b.lang == "mermaid") Diagram(b.text, done, size) else CodeBox(b.lang, b.text, size)
            is Table -> Grid(b.rows, size)
            Break -> Spacer(Modifier.height(4.dp))
        }
    }
}
