package tech.ordalie.orb.ui

import androidx.compose.foundation.*
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.grid.*
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.*
import androidx.compose.ui.unit.*
import kotlin.math.roundToInt
import tech.ordalie.orb.core.*

/**
 * Every open conversation at once, each a live card: where it runs, its title, its state, and the
 * end of its transcript as it streams. A tap opens it, a hold its menu; the last card starts a
 * new one. Cards fill the width two by two on a phone, more on a wider screen.
 */
@Composable
fun Overview(c: Ctx, done: () -> Unit) {
    val tabs = c.v.state.tabs
    LazyVerticalGrid(GridCells.Adaptive(168.dp), Modifier.fillMaxSize().background(p.bg), contentPadding = PaddingValues(Margin - 4.dp, 12.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        items(tabs, key = { it.id }) { t -> Card(t, c, Modifier.animateItem()) { done(); c.nav.show(t.id) } }
        item(key = "new") {
            Box(Modifier.height(72.dp).border(1.dp, p.rule, Soft).press { done(); c.nav.home() }, contentAlignment = Alignment.Center) {
                T("+ new session", size = 14.sp, weight = Medium, color = p.mute)
            }
        }
    }
}

private val Tall = 216.dp

@Composable
private fun Card(t: Tab, c: Ctx, modifier: Modifier, open: () -> Unit) {
    val ink = if (t.ask != null) Ink.Rupture else p.rule
    Column(modifier.height(Tall).clip(Soft).background(p.raised).border(1.dp, ink, Soft).press(onLong = { c.tabMenu(t) }, onClick = open).padding(10.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Where(c.v.hue(t.peer), t.busy, t.ask != null, p.fg)
            Spacer(Modifier.width(7.dp))
            T(t.title.ifEmpty { "new session" }, Modifier.weight(1f), size = 13.sp, weight = Strong, lines = 1)
            if (t.unread > 0) T("${t.unread}", Modifier.padding(start = 6.dp), size = Size.Label, weight = Strong)
        }
        val state = when {
            t.ask != null -> "asks · " + t.ask.title.ifEmpty { "approval" }
            t.busy -> "working"
            !t.online -> "offline"
            else -> t.model.substringAfter('/')
        }
        T(listOf(t.where.takeIf { t.remote }.orEmpty(), state).filter(String::isNotEmpty).joinToString(" · "), Modifier.padding(top = 2.dp),
            size = Size.Label, color = if (t.ask != null) Ink.Rupture else p.meta, lines = 1)
        // The end of the transcript, its oldest lines fading under the card's head.
        val lines = tail(c.v.rows[t.id].orEmpty())
        val ground = p.raised
        Box(Modifier.weight(1f).fillMaxWidth().padding(top = 8.dp).clipToBounds().drawWithContent {
            drawContent()
            drawRect(Brush.verticalGradient(listOf(ground, Color.Transparent), endY = size.height * 0.35f))
        }, contentAlignment = Alignment.BottomStart) {
            T(lines.ifEmpty { if (t.loaded) "ready" else "standby" }, size = 11.sp, color = p.mute)
        }
        if (t.context > 0) Row(Modifier.padding(top = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            val (rule, fill) = p.rule to if (t.context > 0.8) Ink.Rupture else p.fg
            Canvas(Modifier.width(40.dp).height(3.dp)) {
                drawRect(rule)
                drawRect(fill, size = size.copy(width = size.width * t.context.toFloat()))
            }
            T("${(t.context * 100).roundToInt()}%", Modifier.padding(start = 6.dp), size = Size.Label, color = p.meta)
        }
    }
}

/** The last lines a conversation shows, as plain text: what you asked, what it wrote, what it did. */
private fun tail(rows: List<Row>): String = rows.takeLast(8).mapNotNull { r ->
    when (r.kind) {
        "you" -> "› " + r.text
        "md" -> r.block?.plain()
        "run" -> "· " + r.now.ifEmpty { r.what }
        "note" -> r.text
        else -> null
    }?.trim()?.takeIf(String::isNotEmpty)
}.joinToString("\n").lines().takeLast(14).joinToString("\n")

private fun Block.plain(): String =
    (listOf(text, spans.joinToString("") { it.text }) + blocks.map { it.plain() } + rows.map { r -> r.joinToString("  ") { cell -> cell.joinToString("") { it.text } } })
        .filter(String::isNotBlank).joinToString("\n")
