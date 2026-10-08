package tech.ordalie.orb.ui

import androidx.compose.animation.*
import androidx.compose.animation.core.*
import androidx.compose.foundation.*
import androidx.compose.foundation.interaction.DragInteraction
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.foundation.text.BasicText
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.*
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.*
import kotlin.math.roundToInt
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import org.json.JSONObject
import tech.ordalie.orb.core.*

/** Conversation text, in sp. */
const val SIZE = 15f

/** A turn is one speaker's run: what the person said, or everything Orb said and did until the next one. */
private fun turns(rows: List<Row>): List<List<Row>> = buildList {
    var run = mutableListOf<Row>()
    for (r in rows) {
        if (r.kind == "you") { if (run.isNotEmpty()) add(run); add(listOf(r)); run = mutableListOf() } else run += r
    }
    if (run.isNotEmpty()) add(run)
}

@Composable
fun ColumnScope.Chat(c: Ctx, t: Tab) {
    val v = c.v
    Strip(t, { c.usage(t) }) { c.nav.go(Screen.Terminal(t)) }
    AnimatedVisibility(!t.remote && v.state.acting, enter = expandVertically(spring(stiffness = 400f)) + fadeIn(), exit = shrinkVertically() + fadeOut()) { PatternBlue { v.send("abort", "tab" to t.id) } }
    val turns = turns(v.rows[t.id].orEmpty())
    // Anchored at the top, the list never moves on its own: what streams in follows only a reader at
    // the bottom. Scrolling away leaves them where they read until they come back down, or send.
    val list = rememberLazyListState()
    var follow by remember(t.id) { mutableStateOf(true) }
    LaunchedEffect(list) { list.interactionSource.interactions.collect { if (it is DragInteraction.Start) follow = false } }
    LaunchedEffect(list) { snapshotFlow { list.isScrollInProgress }.collect { if (!it) follow = !list.canScrollForward } }
    LaunchedEffect(list) {
        snapshotFlow { list.canScrollForward to list.layoutInfo.totalItemsCount }.collect { (more, n) -> if (follow && more && n > 0) list.requestScrollToItem(n - 1, Int.MAX_VALUE) }
    }
    Box(Modifier.weight(1f).fillMaxWidth()) {
        val ghost by animateFloatAsState(if (turns.isEmpty()) 1f else 0f, tween(400), label = "standby")
        if (ghost > 0f) Box(Modifier.fillMaxSize().alpha(ghost), contentAlignment = Alignment.Center) { T(if (t.loaded) "ready" else "standby", label = true, color = p.meta) }
        // Items never slide: a streamed line would set every row around it moving.
        LazyColumn(Modifier.fillMaxSize(), list) {
            if (t.earlier) item(key = "earlier") {
                // Older messages load above; the ones read so far stay where they are.
                val shown = turns.size
                Box(Modifier.fillMaxWidth().press {
                    v.send("earlier", "tab" to t.id)
                    c.rt.scope.launch { snapshotFlow { turns(v.rows[t.id].orEmpty()).size }.first { it != shown }.let { list.scrollToItem((it - shown).coerceAtLeast(0)) } }
                }.padding(vertical = 12.dp), contentAlignment = Alignment.Center) {
                    T("earlier messages", size = 13.sp, weight = Medium, color = p.meta)
                }
            }
            itemsIndexed(turns, key = { _, b -> b.first().key }) { i, b ->
                // A held finger selects words, in what anyone said, with the system's copy and share.
                SelectionContainer(Modifier.animateItem(fadeInSpec = tween(280), placementSpec = null, fadeOutSpec = tween(160))) { Turn(b, first = i == 0, t, c) }
            }
            // Room under the last message, where the caret waits while Orb writes.
            item(key = "end") { Box(Modifier.fillMaxWidth().height(40.dp).padding(start = Margin, top = 6.dp)) { if (t.streaming) Caret(Ink.Rupture, (SIZE * 0.55f).dp, (SIZE * 1.1f).dp) } }
        }
    }
    AnimatedVisibility(t.status.isNotBlank(), enter = expandVertically() + fadeIn(), exit = shrinkVertically() + fadeOut()) {
        T(t.status, Modifier.padding(horizontal = Margin + 6.dp, vertical = 6.dp), size = 13.sp, color = p.meta, lines = 1)
    }
    PromptBox(t, c, c.palette(t)) { follow = true; c.send(t, it) }
}

/**
 * Under the bar, what matters while reading: the device when it is not this phone, its state, how
 * full its context is, its plan's window nearest the limit (all of them a tap away), what it cost;
 * at right, the terminal where it runs.
 */
@Composable
private fun Strip(t: Tab, usage: () -> Unit, terminal: () -> Unit) =
    Row(Modifier.fillMaxWidth().padding(start = Margin, end = 10.dp).height(28.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
        val state = when { t.busy -> "working"; !t.online -> "offline"; else -> "" }
        T(listOfNotNull(t.where.takeIf { t.remote }, state.ifEmpty { null }).joinToString(" · "), Modifier.weight(1f), size = 13.sp, weight = Medium, color = if (t.busy) p.fg else p.meta, lines = 1)
        if (t.context > 0) T("${(t.context * 100).roundToInt()}% context", size = 13.sp, color = if (t.context > 0.8) Ink.Rupture else p.meta)
        t.usage?.windows?.minByOrNull { it.left }?.let { w ->
            T("${w.name} ${w.left.roundToInt()}% left", Modifier.press(onClick = usage), size = 13.sp, color = if (w.left < 15) Ink.Rupture else p.meta, lines = 1)
        }
        if (t.cost > 0.0) T("$" + "%.2f".format(java.util.Locale.ROOT, t.cost), size = 13.sp, color = p.meta)
        Box(Modifier.press(onClick = terminal).padding(horizontal = 6.dp, vertical = 4.dp)) { T(">_", size = 15.sp, weight = Strong) }
    }

/** What the person said sits at right on a soft ground (a peer's says it came by Bridge); Orb
 *  just speaks, full width, its actions inline. */
@Composable
private fun Turn(rows: List<Row>, first: Boolean, t: Tab, c: Ctx) {
    val you = rows.singleOrNull()?.takeIf { it.kind == "you" }
    if (you != null) Column(Modifier.fillMaxWidth().padding(start = 48.dp, end = Margin, top = if (first) 12.dp else 28.dp, bottom = 16.dp), horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(6.dp)) {
        if (you.via.isNotEmpty()) T("from a peer", size = Size.Label, color = Ink.Blue)
        Pictures(you.images, t, c)
        if (you.text.isNotBlank()) BasicText(tokens(you.text, p.fg, p.bg), Modifier.background(p.fg.copy(alpha = 0.07f), Pane).padding(horizontal = 12.dp, vertical = 8.dp), type(SIZE.sp, p.fg))
    } else Column(Modifier.fillMaxWidth().padding(start = Margin, end = Margin, top = 4.dp, bottom = 8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        rows.forEach { r ->
            when (r.kind) {
                "md" -> r.block?.let { Markdown(it) }
                "run" -> if (r.actions.size == 1) ActionView(r.actions[0], t, c) else Worked(r, t, c)
                "note" -> Folded(r.text, if (r.alarm) Ink.Rupture else p.meta)
            }
            // What the tools showed the model stays in view, even while their run is folded.
            Pictures(r.images.takeIf { r.kind == "run" }.orEmpty(), t, c)
        }
    }
}

/** A row of thumbnails, fetched small from the Orb; a tap opens one full screen. */
@Composable
private fun Pictures(images: List<String>, t: Tab, c: Ctx) {
    if (images.isNotEmpty()) Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        images.forEach { ref -> Picture(ref, 384, { c.image(t.id, ref, it) }, Modifier.height(112.dp).clip(Soft).press { c.view(ref, t.id) }) }
    }
}

/** A run of actions folds into one line, as the TUI folds exploration, naming what runs now; it opens to the actions. */
@Composable
private fun Worked(r: Row, t: Tab, c: Ctx) = Column(Modifier.fillMaxWidth().animateContentSize()) {
    var open by remember { mutableStateOf(false) }
    val now = r.now.takeIf { !open && it.isNotBlank() }
    Box(Modifier.press { open = !open }) { ActionLine(if (r.live) "working" else "worked", listOfNotNull(r.what, now).joinToString(" · "), if (r.failed > 0) "${r.failed} failed" else "", live = r.live, failed = r.failed > 0, open = open) }
    if (open) r.actions.forEach { Box(Modifier.padding(start = 16.dp)) { ActionView(it, t, c) } }
}

/** A thought or a tool call: one line that opens into its whole text, or what it was given and returned. */
@Composable
private fun ActionView(a: Action, t: Tab, c: Ctx) = Column(Modifier.fillMaxWidth().animateContentSize()) {
    var open by remember { mutableStateOf(false) }
    var detail by remember { mutableStateOf<JSONObject?>(null) }
    LaunchedEffect(open, a.result, a.live) { if (open) detail = c.v.ask("detail", "tab" to t.id, "key" to a.key).obj }
    Box(Modifier.press { open = !open }) { ActionLine(a.verb, a.target, a.result, a.live, a.failed, open) }
    if (open) Panel {
        val d = detail
        if (a.verb == "thought") BasicText(d?.optString("text").orEmpty(), style = type((SIZE - 2).sp, p.mute))
        else {
            d?.optString("args")?.takeIf { it.isNotBlank() }?.let { T("input", label = true, color = p.meta); BasicText(it, style = type(12.sp, p.mute)) }
            T("output", label = true, color = p.meta)
            BasicText(d?.optString("output") ?: "…", style = type(12.sp, if (a.failed) Ink.Rupture else p.fg))
        }
    }
}

/** What an action line opens onto: a raised, outlined surface under it. */
@Composable
private fun Panel(content: @Composable ColumnScope.() -> Unit) = Column(
    Modifier.padding(start = 16.dp, top = 4.dp).fillMaxWidth().background(p.raised, Pane).border(1.dp, p.rule, Pane).padding(12.dp),
    verticalArrangement = Arrangement.spacedBy(8.dp), content = content,
)

/** Long errors fold to three lines; a tap opens them. */
@Composable
private fun Folded(text: String, color: Color) {
    var open by remember { mutableStateOf(false) }
    Box(Modifier.press { open = !open }.animateContentSize()) { T(text, size = 14.sp, color = color, lines = if (open) Int.MAX_VALUE else 3) }
}

/** Cited files, skills and references stay recognisable after sending. */
private fun tokens(text: String, fg: Color, bg: Color): AnnotatedString = buildAnnotatedString {
    append(text)
    Regex("""(?<=^|\s)(/skill:[\w.-]+|@[^\s]+|#[\w-]+)""").findAll(text).forEach { m ->
        addStyle(if (m.value[0] == '/') SpanStyle(color = bg, background = fg) else SpanStyle(textDecoration = TextDecoration.Underline), m.range.first, m.range.last + 1)
    }
}

/** Another Orb is driving this phone. Blue fills space here and nowhere else. */
@Composable
fun PatternBlue(stop: () -> Unit) = Column(Modifier.fillMaxWidth().background(Ink.Blue).padding(horizontal = Margin, vertical = 14.dp)) {
    Stretch("PATTERN BLUE", 30.dp, Ink.Texte)
    Row(Modifier.fillMaxWidth().padding(top = 10.dp), verticalAlignment = Alignment.CenterVertically) {
        T("A peer is prompting this phone", Modifier.weight(1f), color = Ink.Texte, weight = Medium)
        Btn("stop", inverted = true, color = Ink.Texte, on = Ink.Blue, onClick = stop)
    }
}
