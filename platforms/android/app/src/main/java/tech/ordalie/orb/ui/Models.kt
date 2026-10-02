package tech.ordalie.orb.ui

import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.layout.heightIn
import androidx.compose.ui.platform.LocalConfiguration
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

/**
 * The model sheet rises from the bottom: the model and its reasoning on top, every model below,
 * grouped by provider and narrowed by search. Choices are remembered for the sessions that follow.
 */
@Composable
fun AnimatedVisibilityScope.ModelSheet(session: Session, c: Ctx, dismiss: () -> Unit) = Box(
    Modifier.fillMaxSize().background(Color(0x66000000)).press(onClick = dismiss), contentAlignment = Alignment.BottomCenter,
) {
    var query by remember { mutableStateOf("") }
    val keyboard = LocalSoftwareKeyboardController.current
    LaunchedEffect(Unit) { keyboard?.hide() }
    val groups = session.models().filter { query.isBlank() || it.contains(query.trim(), ignoreCase = true) }.groupBy { it.substringBefore('/') }
    Column(
        Modifier.animateEnterExit(enter = slideInVertically(spring(dampingRatio = 0.86f, stiffness = 420f)) { it }, exit = slideOutVertically(tween(220)) { it })
            .fillMaxWidth().padding(10.dp).heightIn(max = LocalConfiguration.current.screenHeightDp.dp * 0.8f)
            .clip(RoundedCornerShape(Radius.Card)).background(p.bg).border(1.dp, p.fg, RoundedCornerShape(Radius.Card))
            .navigationBarsPadding().imePadding().press {},
    ) {
        Box(Modifier.fillMaxWidth().padding(top = 10.dp), contentAlignment = Alignment.Center) { Box(Modifier.width(36.dp).height(3.dp).clip(CircleShape).background(p.rule)) }
        Row(Modifier.fillMaxWidth().padding(start = Margin, end = Margin, top = 10.dp), verticalAlignment = Alignment.Bottom) {
            Column(Modifier.weight(1f)) {
                T("model", label = true, color = p.meta)
                T(session.model.substringAfter('/').ifEmpty { "none" }, size = 19.sp, bold = true, lines = 1)
            }
            T(listOf(session.model.substringBefore('/'), session.where).filter(String::isNotEmpty).joinToString(" · "), Modifier.padding(start = 12.dp, bottom = 3.dp), size = Size.Label, color = p.meta, lines = 1)
        }
        Row(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 8.dp).clip(CircleShape).border(1.dp, p.rule, CircleShape).padding(horizontal = 14.dp, vertical = 9.dp), verticalAlignment = Alignment.CenterVertically) {
            Magnifier(); Spacer(Modifier.width(10.dp))
            Box(Modifier.weight(1f)) {
                BasicTextField(query, { query = it }, Modifier.fillMaxWidth(), textStyle = mono(15.sp, p.fg), cursorBrush = SolidColor(p.fg), singleLine = true)
                if (query.isEmpty()) T("search models", color = p.meta, size = 15.sp)
            }
            if (query.isNotEmpty()) Box(Modifier.press { query = "" }.padding(start = 8.dp)) { T("×", size = 18.sp, color = p.mute) }
        }
        LazyColumn(Modifier.weight(1f, fill = false), state = rememberLazyListState(groups.values.flatten().indexOf(session.model).coerceAtLeast(0))) {
            groups.forEach { (provider, ids) ->
                item(key = "h:$provider") { T("$provider · ${ids.size}", Modifier.padding(start = Margin, top = 12.dp, bottom = 2.dp), label = true, color = p.meta) }
                items(ids, key = { it }) { id ->
                    val on = id == session.model
                    Row(Modifier.fillMaxWidth().press { session.useModel(id); dismiss() }.padding(horizontal = Margin, vertical = 11.dp), verticalAlignment = Alignment.CenterVertically) {
                        T(id.substringAfter('/'), Modifier.weight(1f), size = 16.sp, bold = on, lines = 1)
                        if (on) Dot(p.fg)
                    }
                }
            }
            item(key = "add") {
                Row(Modifier.fillMaxWidth().press { dismiss(); c.nav.go(Screen.Providers()) }.padding(horizontal = Margin, vertical = 16.dp)) { T("+ provider", color = p.mute) }
            }
        }
        // Reasoning is what changes most often: it sits last, where the thumb already is.
        if (session.levels.isNotEmpty()) { Rule(); Reasoning(session.levels, session.thinking, session::useThinking) }
    }
}

/** Reasoning as one segmented control: the levels this model takes, the chosen one said in words. */
@Composable
private fun Reasoning(levels: List<String>, level: String, pick: (String) -> Unit) = Column(Modifier.padding(start = Margin, end = Margin, top = 12.dp, bottom = 16.dp)) {
    Row {
        T("reasoning", Modifier.weight(1f), label = true, color = p.meta)
        T(REASONING[level] ?: level, size = Size.Label, color = p.mute)
    }
    Row(Modifier.padding(top = 8.dp).fillMaxWidth().clip(CircleShape).border(1.dp, p.fg, CircleShape)) {
        levels.forEach { l ->
            val on = l == level
            val fill by animateColorAsState(if (on) p.fg else Color.Transparent, tween(160), label = "level")
            Box(Modifier.weight(1f).press { pick(l) }.background(fill).padding(vertical = 10.dp), contentAlignment = Alignment.Center) {
                T(l.replace("minimal", "min").replace("medium", "med"), size = 12.sp, bold = on, color = if (on) p.bg else p.fg, lines = 1)
            }
        }
    }
}

private val REASONING = mapOf("off" to "no thinking", "minimal" to "barely", "low" to "light", "medium" to "balanced", "high" to "thorough", "xhigh" to "very thorough", "max" to "as long as it takes")

@Composable
private fun Magnifier() {
    val ink = p.fg
    Canvas(Modifier.size(17.dp)) {
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
    Header("provider", sub = "an endpoint Orb can call · written to models.json", back = c.nav::back)
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
