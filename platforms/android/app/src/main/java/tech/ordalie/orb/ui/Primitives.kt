package tech.ordalie.orb.ui

import android.content.*
import android.graphics.BitmapFactory
import android.util.LruCache
import android.widget.Toast
import androidx.compose.animation.*
import androidx.compose.animation.core.*
import androidx.compose.foundation.*
import androidx.compose.foundation.interaction.*
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.*
import androidx.compose.foundation.text.*
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.draw.*
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.*
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.input.*
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.*
import androidx.compose.ui.graphics.drawscope.*
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.res.painterResource
import tech.ordalie.orb.R
import kotlin.math.roundToInt
import kotlinx.coroutines.*

/** A hairline separates regions — turns, table rows, the prompt — never words. */
@Composable
fun Rule(modifier: Modifier = Modifier, color: Color = p.rule) = Box(modifier.fillMaxWidth().height(1.dp).background(color))

enum class ChipKind { Inverted, Outline, Quiet }

@Composable
fun Chip(label: String, kind: ChipKind = ChipKind.Outline, modifier: Modifier = Modifier, caps: Boolean = true) {
    val (bg, fg, edge) = when (kind) {
        ChipKind.Inverted -> Triple(p.fg, p.bg, p.fg)
        ChipKind.Outline -> Triple(Color.Transparent, p.fg, p.fg)
        ChipKind.Quiet -> Triple(Color.Transparent, p.mute, p.rule)
    }
    Box(modifier.clip(Soft).background(bg).border(1.dp, edge, Soft).padding(horizontal = 8.dp, vertical = 2.dp)) {
        if (caps) T(label, label = true, color = fg) else T(label, size = 13.sp, color = fg, weight = Medium)
    }
}

/** Taps give way under the finger and confirm with a tick. */
@Composable
fun Modifier.press(enabled: Boolean = true, onLong: (() -> Unit)? = null, onClick: () -> Unit): Modifier {
    val source = remember { MutableInteractionSource() }
    val pressed by source.collectIsPressedAsState()
    val haptic = LocalHapticFeedback.current
    val s by animateFloatAsState(if (pressed) 0.97f else 1f, spring(stiffness = 900f), label = "press")
    return this.scale(s).alpha(if (pressed) 0.75f else 1f).combinedClickable(
        source, indication = null, enabled = enabled,
        onLongClick = onLong?.let { { haptic.performHapticFeedback(HapticFeedbackType.LongPress); it() } },
    ) { haptic.performHapticFeedback(HapticFeedbackType.TextHandleMove); onClick() }
}

@Composable
fun Btn(label: String, inverted: Boolean = false, modifier: Modifier = Modifier, color: Color = p.fg, on: Color = p.bg, shape: Shape = Soft, onClick: () -> Unit) =
    Box(modifier.press(onClick = onClick).clip(shape).background(if (inverted) color else Color.Transparent).border(1.dp, color, shape).padding(horizontal = 14.dp, vertical = 8.dp), contentAlignment = Alignment.Center) {
        T(label, size = 13.sp, weight = Strong, color = if (inverted) on else color)
    }

/** The one text field: a round outline that firms up once it holds something. */
@Composable
fun Field(value: String, hint: String, modifier: Modifier = Modifier, secret: Boolean = false, set: (String) -> Unit) =
    Box(modifier.clip(Soft).border(1.dp, if (value.isEmpty()) p.rule else p.fg, Soft).padding(horizontal = 12.dp, vertical = 9.dp)) {
        BasicTextField(value, set, Modifier.fillMaxWidth(), textStyle = type(15.sp, p.fg), cursorBrush = SolidColor(p.fg), singleLine = true,
            visualTransformation = if (secret) PasswordVisualTransformation('·') else VisualTransformation.None,
            keyboardOptions = KeyboardOptions(keyboardType = if (secret) KeyboardType.Password else KeyboardType.Uri))
        if (value.isEmpty()) T(hint, size = 15.sp, color = p.meta, lines = 1)
    }

/** A card rising from the bottom over a dimmed screen, its corners following the screen's; a tap outside closes it. */
@Composable
fun AnimatedVisibilityScope.Sheet(dismiss: () -> Unit, modifier: Modifier = Modifier, content: @Composable ColumnScope.() -> Unit) =
    Box(Modifier.fillMaxSize().background(Color(0x66000000)).press(onClick = dismiss), contentAlignment = Alignment.BottomCenter) {
        val shape = bezel(8.dp)
        Column(
            Modifier.animateEnterExit(enter = slideInVertically(spring(dampingRatio = 0.86f, stiffness = 420f)) { it }, exit = slideOutVertically(tween(220)) { it })
                .fillMaxWidth().padding(8.dp).then(modifier).clip(shape).background(p.bg).border(1.dp, p.rule, shape)
                .navigationBarsPadding().imePadding().press {},
            content = content,
        )
    }

@Composable
fun Dot(color: Color = Ink.Rupture, size: Dp = 8.dp, pulse: Boolean = false, square: Boolean = false) {
    val on = if (pulse) blink(700) else null
    Box(Modifier.size(size).graphicsLayer { alpha = if (on?.value == false) 0.25f else 1f }.background(color, if (square) RoundedCornerShape(0) else CircleShape))
}

@Composable
fun Caret(color: Color = p.fg, width: Dp = 8.dp, height: Dp = 16.dp) {
    val on = blink(530)
    Box(Modifier.width(width).height(height).graphicsLayer { alpha = if (on.value) 1f else 0f }.background(color))
}

/** Flips every [ms], every blink in step and read as it draws: a frame per flip, not sixty a second. */
@Composable
private fun blink(ms: Long): State<Boolean> {
    val on = remember { mutableStateOf(true) }
    LaunchedEffect(ms) { while (true) { delay(ms - System.currentTimeMillis() % ms); on.value = System.currentTimeMillis() / ms % 2 == 0L } }
    return on
}

/** The maker's mark, small and faint where the app waits on you, as the TUI's empty chat shows it. */
@Composable
fun Ordalie(size: Dp = 26.dp, color: Color = p.rule) = Image(painterResource(R.drawable.ic_ordalie), null, Modifier.size(size), colorFilter = ColorFilter.tint(color))

/** Three hairlines: the menu. */
@Composable
fun MenuMark(onClick: () -> Unit) = Box(Modifier.press(onClick = onClick).semantics { contentDescription = "menu" }.padding(10.dp)) {
    val ink = p.fg
    Canvas(Modifier.size(16.dp, 11.dp)) {
        listOf(0f, 0.5f, 1f).forEach { f -> drawLine(ink, Offset(0f, size.height * f), Offset(size.width, size.height * f), 1.4.dp.toPx(), StrokeCap.Round) }
    }
}

/**
 * The EVA title card: Ubuntu Sans Mono Bold stretched ×1.9 tall, squeezed as needed. Reserved for
 * events and identities. The capitals are the font's own outlines, stretched as vectors and filled
 * at the screen's resolution: text scaled as a layer would stretch its pixels. Letters are set by their ink, a fixed gap apart, as Matisse is:
 * the mono's equal advances would leave an I or an S floating in its cell.
 */
@Composable
fun Stretch(text: String, height: Dp, color: Color = p.fg, squeeze: Float = 1f, modifier: Modifier = Modifier) {
    val context = LocalContext.current
    val face = remember {
        android.graphics.Paint(android.graphics.Paint.ANTI_ALIAS_FLAG).apply {
            typeface = context.resources.getFont(R.font.ubuntu_sans_mono)
            fontVariationSettings = "'wght' 700"
            textSize = 100f // one em
        }
    }
    val caps = remember { android.graphics.Rect().also { face.getTextBounds("H", 0, 1, it) }.height().toFloat() }
    val (path, width) = remember(text) {
        val all = android.graphics.Path()
        val ink = android.graphics.RectF()
        var x = 0f
        text.forEachIndexed { i, ch ->
            if (ch == ' ') { x += 30f; return@forEachIndexed }
            if (i > 0 && text[i - 1] != ' ') x += 3.5f
            val glyph = android.graphics.Path().also { face.getTextPath(ch.toString(), 0, 1, 0f, 0f, it) }
            glyph.computeBounds(ink, true)
            all.addPath(glyph, x - ink.left, 0f)
            x += ink.width()
        }
        all to x
    }
    val sy = with(LocalDensity.current) { height.toPx() } / caps
    val fill = remember(color) { android.graphics.Paint(android.graphics.Paint.ANTI_ALIAS_FLAG).apply { this.color = color.toArgb() } }
    Canvas(modifier.size(with(LocalDensity.current) { (width * sy / 1.9f * squeeze).toDp() }, height)) {
        drawIntoCanvas {
            it.nativeCanvas.save(); it.nativeCanvas.translate(0f, size.height); it.nativeCanvas.scale(size.width / width, sy)
            it.nativeCanvas.drawPath(path, fill); it.nativeCanvas.restore()
        }
    }
}

/**
 * One line per action — a tool call, a thought, a group of them: a mark, the verb, what it acted
 * on, what came of it. Live actions get the dot; ones that open show › or ⌄. Every action aligns.
 */
@Composable
fun ActionLine(verb: String, target: String, result: String, live: Boolean = false, failed: Boolean = false, open: Boolean? = null) = Column(Modifier.fillMaxWidth().padding(vertical = 3.dp)) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.width(16.dp)) { if (live) Dot(pulse = true, size = 6.dp) else if (open != null) T(if (open) "⌄" else "›", size = 14.sp, color = p.meta) }
        T(verb, Modifier.width(72.dp), size = 14.sp, weight = Medium, color = if (live) p.fg else p.mute, lines = 1)
        T(target, Modifier.weight(1f), size = 14.sp, color = if (live) p.fg else p.mute, lines = 1)
        if (!failed) { Spacer(Modifier.width(8.dp)); T(result, Modifier.fillMaxWidth(0.3f), size = 13.sp, color = p.meta, lines = 1) }
    }
    // Why it failed gets a line of its own, readable without opening it.
    if (failed && result.isNotEmpty()) T(result, Modifier.padding(start = 88.dp), size = 13.sp, color = Ink.Rupture, lines = 2)
}


/** A labelled region of a screen: a rule, its name, its state at right. */
@Composable
fun Slot(name: String, state: String = "", modifier: Modifier = Modifier, body: @Composable () -> Unit) = Column(modifier) {
    Rule()
    Row(Modifier.fillMaxWidth().padding(top = 12.dp, bottom = 8.dp)) { T(name, Modifier.weight(1f), label = true, color = p.mute); T(state, size = Size.Label, color = p.meta) }
    body()
}

/** The bar of every screen below Home and its conversations: back, the name, what it shows, an action at right. */
@Composable
fun Header(title: String, sub: String = "", back: () -> Unit, right: @Composable RowScope.() -> Unit = {}) =
    Row(Modifier.fillMaxWidth().padding(start = 4.dp, end = 12.dp, top = 4.dp, bottom = 8.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
        Box(Modifier.press(onClick = back).padding(horizontal = 10.dp, vertical = 2.dp)) { T("‹", size = 28.sp, color = p.fg) }
        Column(Modifier.weight(1f)) {
            T(title, size = Size.Title, weight = Strong, lines = 1)
            if (sub.isNotEmpty()) T(sub, size = 13.sp, color = p.meta, lines = 1)
        }
        right()
    }

/** Decoded pictures by reference and size, within an eighth of the heap; nothing is written anywhere. */
private val pictures = object : LruCache<String, ImageBitmap>((java.lang.Runtime.getRuntime().maxMemory() / 8).toInt()) {
    override fun sizeOf(key: String, value: ImageBitmap) = value.width * value.height * 4
}

/** An image [load]ed at [px], already fitted by its Orb, when it first comes on screen; decoded off the main thread. */
@Composable
fun Picture(ref: String, px: Int, load: suspend (Int) -> String?, modifier: Modifier = Modifier) {
    val key = "$ref:$px"
    val image by produceState(pictures.get(key), key) {
        if (value == null) value = withContext(Dispatchers.Default) {
            load(px)?.let { runCatching { java.util.Base64.getDecoder().decode(it) }.getOrNull() }?.let { BitmapFactory.decodeByteArray(it, 0, it.size)?.asImageBitmap() }
        }?.also { pictures.put(key, it) }
    }
    image?.let { Image(it, null, modifier, contentScale = ContentScale.Fit) } ?: Box(modifier.size(96.dp).background(p.raised, Soft))
}

fun Context.copy(text: String, what: String = "copied") {
    getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText("orb", text))
    Toast.makeText(this, what, Toast.LENGTH_SHORT).show()
}
fun Context.paste(): String = getSystemService(ClipboardManager::class.java).primaryClip?.getItemAt(0)?.coerceToText(this)?.toString().orEmpty()

// The NERV look, after Evangelion's interfaces, as on the desktop: spent where it says something
// true (a takeover, a gauge, a vote), never on every row.

/** A band of warning stripes, [ink] slanting over [ground], as an emergency is framed in Evangelion. */
@Composable
fun Hazard(height: Dp, ground: Color, ink: Color) = Canvas(Modifier.fillMaxWidth().height(height).background(ground)) {
    val w = 6.dp.toPx()
    var x = -size.height
    while (x < size.width) {
        drawPath(Path().apply { moveTo(x, size.height); lineTo(x + size.height, 0f); lineTo(x + size.height + w, 0f); lineTo(x + w, size.height); close() }, ink)
        x += 2 * w
    }
}

/** A reading in ten segments, as Evangelion shows a synchronisation rate: lit up to [value], red past [alarm]. */
@Composable
fun Gauge(value: Double, alarm: Double) = Row(horizontalArrangement = Arrangement.spacedBy(2.dp)) {
    val lit = (value * 10).roundToInt()
    repeat(10) { i ->
        val ink by animateColorAsState(when { i >= lit -> p.rule.copy(alpha = 0.6f); value > alarm -> Ink.Rupture; else -> p.mute }, tween(120, i * 25), label = "segment")
        Box(Modifier.size(3.dp, 10.dp).background(ink))
    }
}

/** 0 to 1 over 260 ms, easing out, from the first composition: what wipes in plays it once. */
@Composable
fun reveal(key: Any?, delay: Int = 0): Float {
    val v = remember(key) { Animatable(0f) }
    LaunchedEffect(key) { v.animateTo(1f, tween(260, delay, CubicBezierEasing(0.23f, 1f, 0.32f, 1f))) }
    return v.value
}
