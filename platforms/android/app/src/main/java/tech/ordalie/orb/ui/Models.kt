package tech.ordalie.orb.ui

import androidx.compose.animation.AnimatedVisibilityScope
import androidx.compose.animation.togetherWith
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideOutVertically
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.core.spring
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.gestures.snapping.rememberSnapFlingBehavior
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import tech.ordalie.orb.core.Session
import kotlin.math.abs

/** A model deck row: a provider heading, or a model under it. */
private sealed interface Card { val key: String }
private data class Heading(val provider: String, val count: Int) : Card { override val key get() = "h:$provider" }
private data class Model(val id: String) : Card { override val key get() = id }

/**
 * The model rolodex: a panel from the side whose drum tilts rows away from the centre band.
 * Models are grouped by provider; search narrows the drum; + provider adds one through Orb's models.json.
 */
@Composable
fun AnimatedVisibilityScope.ModelDeck(session: Session, c: Ctx, dismiss: () -> Unit) = Box(
    Modifier.fillMaxSize().background(Color(0x66000000)).press(onClick = dismiss), contentAlignment = Alignment.CenterEnd,
) {
    var query by remember { mutableStateOf("") }
    var searching by remember { mutableStateOf(false) }
    val keyboard = LocalSoftwareKeyboardController.current
    LaunchedEffect(Unit) { keyboard?.hide() }
    val all = session.models()
    val cards = remember(all, query) {
        all.filter { query.isBlank() || it.contains(query.trim(), ignoreCase = true) }.groupBy { it.substringBefore('/') }
            .flatMap { (provider, ids) -> listOf(Heading(provider, ids.size)) + ids.map(::Model) }
    }
    Column(
        Modifier.animateEnterExit(enter = slideInHorizontally(spring(dampingRatio = 0.86f, stiffness = 380f)) { it }, exit = slideOutHorizontally(tween(220)) { it })
            .fillMaxHeight().fillMaxWidth(0.86f).clip(RoundedCornerShape(topStart = Radius.Card, bottomStart = Radius.Card)).background(p.bg)
            .border(1.dp, p.fg.copy(alpha = 0.85f), RoundedCornerShape(topStart = Radius.Card, bottomStart = Radius.Card)).statusBarsPadding().navigationBarsPadding().imePadding().press {},
    ) {
        T("model", Modifier.padding(start = Margin, top = 18.dp), label = true, color = p.meta)
        Stretch(session.model.substringAfter('/').uppercase().ifEmpty { "NONE" }, 40.dp, squeeze = 0.72f, modifier = Modifier.padding(horizontal = Margin, vertical = 6.dp))
        T(session.where + " · " + all.size + " models", Modifier.padding(start = Margin, bottom = 10.dp), size = Size.Label, color = p.meta)
        if (session.levels.isNotEmpty()) Gauge(session.levels, session.thinking, session::useThinking)
        Rule()
        Drum(cards, session.model, Modifier.weight(1f)) { id -> session.useModel(id); if (!session.remote) c.rt.orb.model = id; dismiss() }
        Rule()
        Row(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 12.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            if (searching) Box(Modifier.weight(1f).clip(CircleShape).border(1.dp, p.fg, CircleShape).padding(horizontal = 16.dp, vertical = 10.dp)) {
                BasicTextField(query, { query = it }, Modifier.fillMaxWidth(), textStyle = mono(15.sp, p.fg), cursorBrush = SolidColor(p.fg), singleLine = true)
                if (query.isEmpty()) T("gemini, claude, gpt…", color = p.meta, size = 15.sp)
            } else {
                Box(Modifier.press { searching = true }.padding(8.dp)) { Magnifier() }
                Spacer(Modifier.weight(1f))
            }
            if (searching) Box(Modifier.press { searching = false; query = "" }.padding(8.dp)) { T("×", size = 20.sp, color = p.mute) }
            else Btn("+ provider") { dismiss(); c.nav.go(Screen.Providers) }
        }
    }
}

/** The drum: rows snap to a centre band and turn away from it like cards on a rolodex. */
@Composable
private fun Drum(cards: List<Card>, selected: String, modifier: Modifier, pick: (String) -> Unit) = BoxWithConstraints(modifier.fillMaxWidth()) {
    val list = rememberLazyListState()
    val rowHeight = 56.dp
    val pad = (maxHeight - rowHeight) / 2
    LaunchedEffect(cards) { cards.indexOfFirst { it is Model && it.id == selected }.takeIf { it >= 0 }?.let { list.scrollToItem(it) } }
    // The centre band: two hairlines the chosen row settles between.
    Column(Modifier.fillMaxWidth().padding(top = pad).padding(horizontal = 14.dp)) {
        Rule(color = p.fg.copy(alpha = 0.35f)); Spacer(Modifier.height(rowHeight - 2.dp)); Rule(color = p.fg.copy(alpha = 0.35f))
    }
    val camera = with(LocalDensity.current) { 14.dp.toPx() }
    LazyColumn(Modifier.fillMaxSize(), state = list, contentPadding = PaddingValues(vertical = pad), flingBehavior = rememberSnapFlingBehavior(list)) {
        itemsIndexed(cards, key = { _, card -> card.key }) { index, card ->
            Box(
                Modifier.fillMaxWidth().height(rowHeight).graphicsLayer {
                    // Read in the draw phase: the tilt follows the scroll without recomposing.
                    val info = list.layoutInfo
                    val item = info.visibleItemsInfo.firstOrNull { it.index == index } ?: return@graphicsLayer
                    val centre = (info.viewportStartOffset + info.viewportEndOffset) / 2f
                    val d = ((item.offset + item.size / 2f - centre) / (info.viewportEndOffset - info.viewportStartOffset) * 2f).coerceIn(-1f, 1f)
                    rotationX = -d * 62f; alpha = 1f - abs(d) * 0.75f
                    scaleX = 1f - abs(d) * 0.12f; scaleY = scaleX; cameraDistance = camera * density
                }.then(if (card is Model) Modifier.press { pick(card.id) } else Modifier),
                contentAlignment = Alignment.CenterStart,
            ) {
                when (card) {
                    is Heading -> Row(Modifier.padding(horizontal = Margin), verticalAlignment = Alignment.CenterVertically) {
                        Stretch(card.provider.uppercase(), 26.dp, p.meta, squeeze = 0.8f); Spacer(Modifier.width(10.dp)); T(card.count.toString(), size = Size.Label, color = p.meta)
                    }
                    is Model -> Row(Modifier.padding(horizontal = Margin), verticalAlignment = Alignment.CenterVertically) {
                        val on = card.id == selected
                        T(card.id.substringAfter('/'), Modifier.weight(1f), size = 18.sp, bold = on, lines = 1)
                        if (on) Dot(p.fg)
                    }
                }
            }
        }
    }
}

@Composable
private fun Magnifier() {
    val ink = p.fg
    Canvas(Modifier.size(22.dp)) {
        val r = size.minDimension * 0.34f
        val c = Offset(r + 2f, r + 2f)
        drawCircle(ink, r, c, style = Stroke(1.8.dp.toPx()))
        drawLine(ink, Offset(c.x + r * 0.72f, c.y + r * 0.72f), Offset(size.width - 2f, size.height - 2f), 1.8.dp.toPx(), StrokeCap.Round)
    }
}

/** Adds an OpenAI-, Anthropic- or Google-shaped endpoint to Orb's models.json. */
@Composable
fun ColumnScope.ProviderScreen(c: Ctx) {
    val scope = rememberCoroutineScope()
    var name by remember { mutableStateOf("") }
    var api by remember { mutableStateOf("openai-completions") }
    var url by remember { mutableStateOf("https://") }
    var key by remember { mutableStateOf("") }
    var models by remember { mutableStateOf("") }
    var status by remember { mutableStateOf("") }
    Header("provider", sub = "an endpoint Orb can call · written to models.json", back = c.nav::back, big = true)
    Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = Margin), verticalArrangement = Arrangement.spacedBy(14.dp)) {
        Field("name", name, "my-provider") { name = it.lowercase().filter { ch -> ch.isLetterOrDigit() || ch == '-' } }
        Column {
            T("api", label = true)
            Row(Modifier.padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                listOf("openai-completions" to "openai", "anthropic-messages" to "anthropic", "google-generative-ai" to "google").forEach { (shape, label) ->
                    Box(Modifier.press { api = shape }) { Chip(label, if (api == shape) ChipKind.Inverted else ChipKind.Outline) }
                }
            }
        }
        Field("base url", url, "https://api.example.com/v1") { url = it.trim() }
        Field("api key", key, "sk-…", secret = true) { key = it.trim() }
        Field("models", models, "model-a, model-b") { models = it }
        if (status.isNotEmpty()) T(status, color = if (status.startsWith("added")) p.mute else Ink.Rupture)
    }
    Row(Modifier.padding(Margin), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        Btn("add · restart core", inverted = true) {
            val ids = models.split(',', ' ').map(String::trim).filter(String::isNotEmpty)
            if (name.isEmpty() || ids.isEmpty() || !url.startsWith("http")) { status = "name, url and at least one model are needed"; return@Btn }
            scope.launch {
                status = withContext(Dispatchers.IO) { c.rt.orb.addProvider(name, api, url, key, ids) } ?: "added $name · ${ids.size} models"
                if (status.startsWith("added")) { c.rt.restart(); c.nav.back() }
            }
        }
    }
}

@Composable
private fun Field(label: String, value: String, hint: String, secret: Boolean = false, set: (String) -> Unit) = Column {
    T(label, label = true)
    Box(Modifier.padding(top = 8.dp).fillMaxWidth().clip(CircleShape).border(1.dp, if (value.isEmpty()) p.rule else p.fg, CircleShape).padding(horizontal = 18.dp, vertical = 12.dp)) {
        BasicTextField(value, set, Modifier.fillMaxWidth(), textStyle = mono(15.sp, p.fg), cursorBrush = SolidColor(p.fg), singleLine = true,
            visualTransformation = if (secret) androidx.compose.ui.text.input.PasswordVisualTransformation('·') else androidx.compose.ui.text.input.VisualTransformation.None)
        if (value.isEmpty()) T(hint, color = p.meta, size = 15.sp)
    }
}

/** Reasoning as a gauge: one segment per level the model takes, lit up to the chosen one. */
@Composable
fun Gauge(levels: List<String>, level: String, pick: (String) -> Unit) = Column(Modifier.fillMaxWidth().padding(start = Margin, end = Margin, bottom = 14.dp)) {
    val at = levels.indexOf(level)
    Row(verticalAlignment = Alignment.Bottom) {
        T("reasoning", Modifier.weight(1f).padding(bottom = 4.dp), label = true, color = p.meta)
        AnimatedContent(level.ifEmpty { "—" }, transitionSpec = { (slideInVertically { it } + fadeIn()) togetherWith (slideOutVertically { -it } + fadeOut()) }, label = "level") {
            Stretch(it.uppercase(), 26.dp, squeeze = 0.8f)
        }
    }
    Row(Modifier.fillMaxWidth().padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(5.dp)) {
        levels.forEachIndexed { i, l ->
            val lit by animateColorAsState(if (i <= at && l != "off") p.fg else Color.Transparent, tween(160 + 40 * i), label = "seg")
            Column(Modifier.weight(1f).press { pick(l) }, horizontalAlignment = Alignment.CenterHorizontally) {
                Box(Modifier.fillMaxWidth().height(26.dp).clip(CircleShape).background(lit).border(1.dp, if (i == at) p.fg else p.rule, CircleShape))
                T(l, Modifier.padding(top = 5.dp), size = 10.sp, color = if (i == at) p.fg else p.meta, lines = 1)
            }
        }
    }
}
