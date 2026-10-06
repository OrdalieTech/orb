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
import tech.ordalie.orb.core.*

/** Conversation text, in sp. */
private const val SIZE = 15f

/** A block is one speaker's run: a YOU message, or everything Orb said and did until the next one. */
private fun blocks(items: List<Item>): List<List<Item>> = buildList {
    var run = mutableListOf<Item>()
    for (i in items) {
        if (i is You) { if (run.isNotEmpty()) add(run); add(listOf(i)); run = mutableListOf() } else run += i
    }
    if (run.isNotEmpty()) add(run)
}

@Composable
fun ColumnScope.Chat(c: Ctx, s: Session) {
    Strip(s, { c.usage(s) }) { c.nav.go(Screen.Terminal(s)) }
    AnimatedVisibility(!s.remote && c.rt.acting, enter = expandVertically(spring(stiffness = 400f)) + fadeIn(), exit = shrinkVertically() + fadeOut()) { PatternBlue(s::abort) }
    // A peer's session follows its transcript only while shown here.
    DisposableEffect(s) { s.watched = true; onDispose { s.watched = false } }
    val blocks = blocks(s.transcript.items)
    val streaming = (s.transcript.items.lastOrNull() as? Said)?.live == true
    // Anchored at the top, the list never moves on its own: what streams in follows only a reader at
    // the bottom. Scrolling away leaves them where they read until they come back down, or send.
    val list = rememberLazyListState()
    var follow by remember(s) { mutableStateOf(true) }
    LaunchedEffect(list) { list.interactionSource.interactions.collect { if (it is DragInteraction.Start) follow = false } }
    LaunchedEffect(list) { snapshotFlow { list.isScrollInProgress }.collect { if (!it) follow = !list.canScrollForward } }
    LaunchedEffect(list) {
        snapshotFlow { list.canScrollForward to list.layoutInfo.totalItemsCount }.collect { (more, n) -> if (follow && more && n > 0) list.requestScrollToItem(n - 1, Int.MAX_VALUE) }
    }
    Box(Modifier.weight(1f).fillMaxWidth()) {
        val ghost by animateFloatAsState(if (blocks.isEmpty()) 1f else 0f, tween(400), label = "standby")
        if (ghost > 0f) Box(Modifier.fillMaxSize().alpha(ghost), contentAlignment = Alignment.Center) { T(if (s.loaded) "ready" else "standby", label = true, color = p.meta) }
        // Items never slide: a streamed line would set every row around it moving.
        LazyColumn(Modifier.fillMaxSize(), list) {
            if (s.earlier) item(key = "earlier") {
                // Older messages load above; the ones read so far stay where they are.
                val shown = blocks.size
                Box(Modifier.fillMaxWidth().press { s.loadEarlier(); c.rt.scope.launch { snapshotFlow { blocks(s.transcript.items).size }.first { it != shown }.let { list.scrollToItem((it - shown).coerceAtLeast(0)) } } }.padding(vertical = 12.dp), contentAlignment = Alignment.Center) {
                    T("earlier messages", size = 13.sp, weight = Medium, color = p.meta)
                }
            }
            itemsIndexed(blocks, key = { _, b -> b.first().key }) { i, b ->
                // A held finger selects words, in what anyone said, with the system's copy and share.
                SelectionContainer(Modifier.animateItem(fadeInSpec = tween(280), placementSpec = null, fadeOutSpec = tween(160))) { Block(b, first = i == 0, s) { ref -> c.view(ref, s) } }
            }
            // Room under the last message, where the caret waits while Orb writes.
            item(key = "end") { Box(Modifier.fillMaxWidth().height(40.dp).padding(start = Margin, top = 6.dp)) { if (streaming) Caret(Ink.Rupture, (SIZE * 0.55f).dp, (SIZE * 1.1f).dp) } }
        }
    }
    AnimatedVisibility(s.status.isNotBlank(), enter = expandVertically() + fadeIn(), exit = shrinkVertically() + fadeOut()) {
        T(s.status, Modifier.padding(horizontal = Margin + 6.dp, vertical = 6.dp), size = 13.sp, color = p.meta, lines = 1)
    }
    PromptBox(s, c.cites, c.onCite, c::chooseWhere, { c.chooseModel(s) }, c.palette(s)) { follow = true; if (!c.command(s, it)) s.prompt(it) }
}

/**
 * Under the bar, what matters while reading: the device when it is not this phone, its state, how
 * full its context is, its plan's window nearest the limit (all of them a tap away), what it cost;
 * at right, the terminal where it runs.
 */
@Composable
private fun Strip(s: Session, usage: () -> Unit, terminal: () -> Unit) =
    Row(Modifier.fillMaxWidth().padding(start = Margin, end = 10.dp).height(28.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
        val state = when { s.busy -> "working"; !s.online -> "offline"; else -> "" }
        T(listOfNotNull(s.where.takeIf { s.remote }, state.ifEmpty { null }).joinToString(" · "), Modifier.weight(1f), size = 13.sp, weight = Medium, color = if (s.busy) p.fg else p.meta, lines = 1)
        if (s.context > 0f) T("${(s.context * 100).roundToInt()}% context", size = 13.sp, color = if (s.context > 0.8f) Ink.Rupture else p.meta)
        s.usage?.open()?.minByOrNull { it.left }?.let { w ->
            T("${w.name} ${w.left.roundToInt()}% left", Modifier.press(onClick = usage), size = 13.sp, color = if (w.left < 15) Ink.Rupture else p.meta, lines = 1)
        }
        if (s.cost > 0.0) T("$" + "%.2f".format(java.util.Locale.ROOT, s.cost), size = 13.sp, color = p.meta)
        Box(Modifier.press(onClick = terminal).padding(horizontal = 6.dp, vertical = 4.dp)) { T(">_", size = 15.sp, weight = Strong) }
    }

/** What the person said sits at right on a soft ground (a peer's says it came by Bridge); Orb
 *  just speaks, full width, its tools inline. */
@Composable
private fun Block(items: List<Item>, first: Boolean, s: Session, view: (String) -> Unit) {
    val you = items.singleOrNull() as? You
    if (you != null) Column(Modifier.fillMaxWidth().padding(start = 48.dp, end = Margin, top = if (first) 12.dp else 28.dp, bottom = 16.dp), horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(6.dp)) {
        if (you.via != null) T("from a peer", size = Size.Label, color = Ink.Blue)
        Pictures(you.images, s, view)
        if (you.text.isNotBlank()) BasicText(tokens(you.text, p.fg, p.bg), Modifier.background(p.fg.copy(alpha = 0.07f), Pane).padding(horizontal = 12.dp, vertical = 8.dp), type(SIZE.sp, p.fg))
    } else Column(Modifier.fillMaxWidth().padding(start = Margin, end = Margin, top = 4.dp, bottom = 8.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        runs(items).forEach { run ->
            when (val one = run.singleOrNull()) {
                null -> Worked(run)
                is Act.Thought -> Thought(one.said)
                is Act.Call -> ToolView(one.tool)
                is Act.Prose -> Said(one.said)
                is Act.Aside -> Folded(one.note.text, if (one.note.alarm) Ink.Rupture else p.meta)
            }
            // What the tools showed the model stays in view, even while their run is folded.
            Pictures(run.flatMap { (it as? Act.Call)?.tool?.images.orEmpty() }, s, view)
        }
    }
}

/** A row of thumbnails, fetched small from the Orb; a tap opens one full screen. */
@Composable
private fun Pictures(images: List<String>, s: Session, view: (String) -> Unit) {
    if (images.isNotEmpty()) Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        images.forEach { ref -> Picture(ref, 384, { s.image(ref, it) }, Modifier.height(112.dp).clip(Soft).press { view(ref) }) }
    }
}

/** What Orb did between two things it said: thoughts and tool calls are actions, the rest is not. */
private sealed interface Act {
    class Thought(val said: Said) : Act
    class Call(val tool: Tool) : Act
    class Prose(val said: Said) : Act
    class Aside(val note: Note) : Act
}

/** Consecutive actions form one run, which folds into a line; prose and notes stand alone. */
private fun runs(items: List<Item>): List<List<Act>> = buildList {
    var run = mutableListOf<Act>()
    fun close() { if (run.isNotEmpty()) add(run); run = mutableListOf() }
    for (i in items) when (i) {
        is Said -> {
            if (i.thinking.isNotBlank()) run += Act.Thought(i)
            if (i.text.isNotBlank() || (i.live && i.thinking.isBlank())) { close(); add(listOf(Act.Prose(i))) }
        }
        is Tool -> run += Act.Call(i)
        is Note -> { close(); add(listOf(Act.Aside(i))) }
        is You -> close()
    }
    close()
}

private val KIND = mapOf("bash" to "command", "read" to "read", "grep" to "search", "find" to "search", "glob" to "search", "ls" to "listing", "edit" to "edit", "write" to "edit")

/**
 * A run of actions folds into one line, as the TUI folds exploration: "worked · 2 thoughts ·
 * 3 commands", failures counted at its end. It opens to the actions; running ones stay in view.
 */
@Composable
private fun Worked(run: List<Act>) = Column(Modifier.fillMaxWidth().animateContentSize()) {
    var open by remember { mutableStateOf(false) }
    val live = run.any { it is Act.Call && it.tool.live || it is Act.Thought && it.said.live }
    val kinds = run.map { if (it is Act.Call) KIND[it.tool.verb] ?: it.tool.verb else "thought" }
    val what = kinds.groupingBy { it }.eachCount().entries.joinToString(" · ") { (k, n) ->
        "$n " + if (n == 1) k else if (k == "search") "searches" else k + "s"
    }
    val failed = run.count { it is Act.Call && it.tool.failed }
    Box(Modifier.press { open = !open }) { ActionLine(if (live) "working" else "worked", what, if (failed > 0) "$failed failed" else "", live = live, failed = failed > 0, open = open) }
    // Folded, only the latest running action shows: a run never jumps a line per tool.
    (if (open) run else listOfNotNull(run.lastOrNull { it is Act.Call && it.tool.live })).forEach {
        Box(Modifier.padding(start = 16.dp)) { if (it is Act.Call) ToolView(it.tool) else if (it is Act.Thought) Thought(it.said) }
    }
}

/** A thought is an action like the tools around it: same line, same columns, opens the same way. */
@Composable
private fun Thought(s: Said) = Column(Modifier.fillMaxWidth().animateContentSize()) {
    var open by remember { mutableStateOf(false) }
    Box(Modifier.press { open = !open }) {
        ActionLine("thought", s.thinking.trim().lineSequence().first().removePrefix("**").substringBefore("**"), "${s.thinking.length / 4} tok", live = s.live && s.text.isBlank(), open = open)
    }
    if (open) Panel { BasicText(s.thinking.trim(), style = type((SIZE - 2).sp, p.mute)) }
}

@Composable
private fun Said(s: Said) {
    if (s.text.isNotBlank()) Markdown(s.text, size = SIZE, done = !s.live)
}

/** A tool line opens into what it was given and what it returned. */
@Composable
private fun ToolView(t: Tool) {
    var open by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth().animateContentSize()) {
        Box(Modifier.press { open = !open }) { ToolLine(t, open) }
        if (open) Panel {
            if (t.args.isNotBlank()) { T("input", label = true, color = p.meta); BasicText(t.args, style = type(12.sp, p.mute)) }
            T("output", label = true, color = p.meta)
            BasicText(t.output.ifBlank { if (t.live) "running…" else "no output" }, style = type(12.sp, if (t.failed) Ink.Rupture else p.fg))
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
