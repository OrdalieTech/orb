package tech.ordalie.orb.ui

import androidx.compose.animation.*
import androidx.compose.animation.core.*
import androidx.compose.foundation.*
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.grid.*
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.*
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.unit.*
import kotlin.math.abs
import kotlin.math.roundToInt
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import tech.ordalie.orb.core.*

/** The scopes a card and its conversation's page share, so that one grows out of the other. */
@OptIn(ExperimentalSharedTransitionApi::class)
class Morph(val shared: SharedTransitionScope, val scope: AnimatedVisibilityScope)

val LocalMorph = staticCompositionLocalOf<Morph?> { null }

/** Marks what shows conversation [key] in the overview and on its page: going from one to the other, it grows or shrinks between them. */
@OptIn(ExperimentalSharedTransitionApi::class)
@Composable
fun Modifier.morph(key: String): Modifier {
    val m = LocalMorph.current ?: return this
    return with(m.shared) { this@morph.sharedBounds(rememberSharedContentState(key), m.scope, boundsTransform = { _, _ -> spring(0.9f, 380f) }) }
}

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
        items(tabs, key = { it.id }) { t ->
            // Its page shows before the overview goes, so the card grows into it.
            val scope = rememberCoroutineScope()
            Card(t, c, Modifier.animateItem()) { scope.launch { c.nav.pager.scrollToPage(1 + c.v.state.tabs.indexOfFirst { it.id == t.id }); done() } }
        }
        item(key = "new") {
            Box(Modifier.height(72.dp).border(1.dp, p.rule, Soft).press { done(); c.nav.home(); c.nav.compose = true }, contentAlignment = Alignment.Center) {
                T("+ new session", size = 14.sp, weight = Medium, color = p.mute)
            }
        }
    }
}

private val Tall = 216.dp

@Composable
private fun Card(t: Tab, c: Ctx, modifier: Modifier, open: () -> Unit) {
    // Its border says its state at a glance: red while it asks, ink while it works, quiet at rest.
    val ink by animateColorAsState(if (t.ask != null) Ink.Rupture else if (t.busy) p.fg else p.rule, tween(240), label = "ink")
    // Swiped aside, it closes its conversation, as an app switcher's card does.
    val swipe = remember { Animatable(0f) }
    val scope = rememberCoroutineScope()
    var width by remember { mutableIntStateOf(1) }
    val away = Modifier.onSizeChanged { width = it.width }
        .graphicsLayer { translationX = swipe.value; alpha = 1f - 0.7f * (abs(swipe.value) / width).coerceIn(0f, 1f) }
        .pointerInput(t.id) {
            detectHorizontalDragGestures(
                onDragEnd = {
                    scope.launch {
                        if (abs(swipe.value) > width * 0.35f) { swipe.animateTo(if (swipe.value > 0) width * 1.2f else -width * 1.2f, tween(160)); c.close(t)
                            // Still here a moment later, its close did not take: it comes back.
                            delay(1500); swipe.animateTo(0f, spring(0.7f, 600f)) }
                        else swipe.animateTo(0f, spring(0.7f, 600f))
                    }
                },
                onDragCancel = { scope.launch { swipe.animateTo(0f, spring(0.7f, 600f)) } },
            ) { change, dx -> change.consume(); scope.launch { swipe.snapTo(swipe.value + dx) } }
        }
    Column(modifier.then(away).morph("tab-${t.id}").height(Tall).clip(Soft).background(p.raised).border(1.dp, ink, Soft).press(onLong = { c.tabMenu(t) }, onClick = open).padding(10.dp)) {
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
        // Where it runs, named as its prompt box names it, then its state; a new state rises into the last one's place.
        AnimatedContent(listOf(t.where.takeIf { t.remote } ?: "phone", state).filter(String::isNotEmpty).joinToString(" · "), Modifier.padding(top = 2.dp),
            transitionSpec = { (fadeIn(tween(200)) + slideInVertically(tween(200)) { it / 2 }) togetherWith fadeOut(tween(120)) }, label = "state") { line ->
            T(line, size = Size.Label, color = if (t.ask != null) Ink.Rupture else p.meta, lines = 1)
        }
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
            val rule = p.rule
            val fill by animateColorAsState(if (t.context > 0.8) Ink.Rupture else p.fg, tween(240), label = "fill")
            val full by animateFloatAsState(t.context.toFloat(), tween(400, easing = FastOutSlowInEasing), label = "context")
            Canvas(Modifier.width(40.dp).height(3.dp)) {
                drawRect(rule)
                drawRect(fill, size = size.copy(width = size.width * full))
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
