package tech.ordalie.orb.ui

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.spring
import androidx.compose.animation.core.tween
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicText
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.flow.first
import tech.ordalie.orb.core.Item
import tech.ordalie.orb.core.Note
import tech.ordalie.orb.core.RemoteSession
import tech.ordalie.orb.core.Said
import tech.ordalie.orb.core.Session
import tech.ordalie.orb.core.Tool
import tech.ordalie.orb.core.You
import kotlin.math.roundToInt

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
    Strip(s) { c.nav.go(Screen.Terminal) }
    AnimatedVisibility(!s.remote && c.rt.acting, enter = expandVertically(spring(stiffness = 400f)) + fadeIn(), exit = shrinkVertically() + fadeOut()) { PatternBlue(s::abort) }
    // A peer's session follows its transcript only while shown here.
    if (s is RemoteSession) DisposableEffect(s) { s.watched = true; onDispose { s.watched = false } }
    val blocks = blocks(s.transcript.items)
    val list = rememberLazyListState()
    val tail = (s.transcript.items.lastOrNull() as? Said)?.text?.length ?: 0
    // Open on the latest turn, then follow new output while the reader stays at the bottom.
    LaunchedEffect(s) { snapshotFlow { list.layoutInfo.totalItemsCount }.first { it > 0 }.let { list.scrollToItem(it - 1, Int.MAX_VALUE) } }
    var follow by remember { mutableStateOf(true) }
    LaunchedEffect(list) { snapshotFlow { list.isScrollInProgress }.collect { if (!it) follow = !list.canScrollForward } }
    LaunchedEffect(s.transcript.items.size, tail) {
        val n = list.layoutInfo.totalItemsCount
        if (follow && n > 0) list.animateScrollToItem(n - 1, Int.MAX_VALUE)
    }
    Box(Modifier.weight(1f).fillMaxWidth()) {
        val ghost by animateFloatAsState(if (blocks.isEmpty()) 1f else 0f, tween(400), label = "standby")
        if (ghost > 0f) Box(Modifier.fillMaxSize().alpha(ghost), contentAlignment = Alignment.Center) { T("standby", label = true, color = p.meta) }
        LazyColumn(Modifier.fillMaxSize(), state = list) {
            itemsIndexed(blocks, key = { _, b -> b.first().key }) { i, b ->
                Column(Modifier.animateItem(fadeInSpec = tween(280), fadeOutSpec = tween(160))) { Block(b, first = i == 0) }
            }
            item { Spacer(Modifier.height(12.dp)) }
        }
    }
    AnimatedVisibility(s.status.isNotBlank(), enter = expandVertically() + fadeIn(), exit = shrinkVertically() + fadeOut()) {
        T(s.status, Modifier.padding(horizontal = Margin + 6.dp, vertical = 6.dp), size = 13.sp, color = p.meta, lines = 1)
    }
    PromptBox(s, c.cites, c.onCite, { c.chooseWhere { c.nav.show(it) } }, { c.chooseModel(s) }, c.palette(s)) { if (!c.command(s, it)) s.prompt(it) }
}

/** Under the bar, what matters while reading: the device when it is not this phone, its state, how full its context is, what it cost; the phone's terminal at right. */
@Composable
private fun Strip(s: Session, terminal: () -> Unit) =
    Row(Modifier.fillMaxWidth().padding(start = Margin, end = 10.dp).height(32.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
        val state = when { s.busy -> "working"; !s.online -> "offline"; else -> "" }
        T(listOfNotNull(s.where.takeIf { s.remote }, state.ifEmpty { null }).joinToString(" · "), Modifier.weight(1f), size = 13.sp, weight = Medium, color = if (s.busy) p.fg else p.meta, lines = 1)
        if (s.context > 0f) T("${(s.context * 100).roundToInt()}% context", size = 13.sp, color = if (s.context > 0.8f) Ink.Rupture else p.meta)
        if (s.cost > 0.0) T("$" + "%.2f".format(java.util.Locale.ROOT, s.cost), size = 13.sp, color = p.meta)
        if (!s.remote) Box(Modifier.press(onClick = terminal).padding(horizontal = 6.dp, vertical = 4.dp)) { T(">_", size = 15.sp, weight = Strong) }
    }

/** The person is labelled; Orb just speaks, full width, its tools inline. */
@Composable
private fun Block(items: List<Item>, first: Boolean) {
    val you = items.singleOrNull() as? You
    if (you != null) Row(Modifier.fillMaxWidth().padding(start = Margin, end = Margin, top = if (first) 16.dp else 28.dp, bottom = 8.dp)) {
        Box(Modifier.width(54.dp).padding(top = 2.dp)) { if (you.via != null) Chip("peer", ChipKind.Blue) else Chip("you", ChipKind.Inverted) }
        BasicText(tokens(you.text, p.fg, p.bg), Modifier.weight(1f).copyable(you.text), type(SIZE.sp, p.fg, Medium))
    } else Column(Modifier.fillMaxWidth().padding(start = Margin, end = Margin, top = 4.dp, bottom = 8.dp).animateContentSize(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        runs(items).forEach { run ->
            when (val one = run.singleOrNull()) {
                null -> Worked(run)
                is Act.Thought -> Thought(one.said)
                is Act.Call -> ToolView(one.tool)
                is Act.Prose -> Said(one.said)
                is Act.Aside -> Folded(one.note.text, if (one.note.alarm) Ink.Rupture else p.meta)
            }
        }
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
    run.filter { open || it is Act.Call && it.tool.live }.forEach {
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
    if (open) Panel { BasicText(s.thinking.trim(), Modifier.copyable(s.thinking), type((SIZE - 2).sp, p.mute)) }
}

@Composable
private fun Said(s: Said) = Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
    if (s.text.isNotBlank()) Markdown(s.text, Modifier.copyable(s.text), SIZE)
    if (s.live) Caret(Ink.Rupture, (SIZE * 0.55f).dp, (SIZE * 1.1f).dp)
}

/** A tool line opens into what it was given and what it returned. */
@Composable
private fun ToolView(t: Tool) {
    var open by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth().animateContentSize()) {
        Box(Modifier.press { open = !open }) { ToolLine(t, open) }
        if (open) Panel {
            if (t.args.isNotBlank()) { T("input", label = true, color = p.meta); BasicText(t.args, Modifier.copyable(t.args), type(12.sp, p.mute)) }
            T("output", label = true, color = p.meta)
            BasicText(t.output.ifBlank { if (t.live) "running…" else "no output" }, Modifier.copyable(t.output), type(12.sp, if (t.failed) Ink.Rupture else p.fg))
        }
    }
}

/** What an action line opens onto: a raised, outlined surface under it. */
@Composable
private fun Panel(content: @Composable ColumnScope.() -> Unit) = Column(
    Modifier.padding(start = 16.dp, top = 6.dp).fillMaxWidth().background(p.raised, RoundedCornerShape(16.dp)).border(1.dp, p.rule, RoundedCornerShape(16.dp)).padding(14.dp),
    verticalArrangement = Arrangement.spacedBy(8.dp), content = content,
)

/** Long errors fold to three lines; a tap opens them. */
@Composable
private fun Folded(text: String, color: Color) {
    var open by remember { mutableStateOf(false) }
    Box(Modifier.press { open = !open }.animateContentSize()) { T(text, size = 14.sp, color = color, lines = if (open) Int.MAX_VALUE else 3) }
}

/** Long press copies, with a tick and a word — the only confirmation a copy needs. */
@Composable
private fun Modifier.copyable(text: String): Modifier {
    val context = LocalContext.current
    val haptic = LocalHapticFeedback.current
    return pointerInput(text) {
        detectTapGestures(onLongPress = { haptic.performHapticFeedback(HapticFeedbackType.LongPress); context.copy(text) })
    }
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
