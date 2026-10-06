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
import androidx.compose.ui.text.input.*
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.*
import kotlin.math.roundToInt
import kotlinx.coroutines.*
import tech.ordalie.orb.core.Tool

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
    val a = if (pulse) rememberInfiniteTransition("dot").animateFloat(1f, 0.25f, infiniteRepeatable(tween(700), RepeatMode.Reverse), "a").value else 1f
    Box(Modifier.size(size).alpha(a).background(color, if (square) RoundedCornerShape(0) else CircleShape))
}

@Composable
fun Caret(color: Color = p.fg, width: Dp = 8.dp, height: Dp = 16.dp) {
    val on = rememberInfiniteTransition("caret").animateFloat(1f, 0f, infiniteRepeatable(tween(530, delayMillis = 400), RepeatMode.Reverse), "c").value
    Box(Modifier.width(width).height(height).alpha(if (on > 0.5f) 1f else 0f).background(color))
}

/** Three hairlines: the menu. */
@Composable
fun MenuMark(onClick: () -> Unit) = Box(Modifier.press(onClick = onClick).padding(10.dp)) {
    val ink = p.fg
    Canvas(Modifier.size(16.dp, 11.dp)) {
        listOf(0f, 0.5f, 1f).forEach { f -> drawLine(ink, Offset(0f, size.height * f), Offset(size.width, size.height * f), 1.4.dp.toPx(), StrokeCap.Round) }
    }
}

/** The EVA title card: Ubuntu Sans Mono Bold stretched ×1.9 tall, squeezed as needed. Reserved for events and identities. */
@Composable
fun Stretch(text: String, height: Dp, color: Color = p.fg, squeeze: Float = 1f, modifier: Modifier = Modifier) {
    val size = (height.value / 1.9f / 0.7f).sp // cap height of Ubuntu Sans Mono ≈ 0.7em
    BasicText(
        text, modifier
            .layout { m, c ->
                val placeable = m.measure(c.copy(maxWidth = Int.MAX_VALUE))
                val w = (placeable.width * squeeze).roundToInt().coerceAtMost(c.maxWidth)
                // The line box scales about its own centre, so centring it centres the stretched caps.
                layout(w, height.roundToPx()) { placeable.place(0, ((height.toPx() - placeable.height) / 2f).roundToInt()) }
            }
            .graphicsLayer { scaleY = 1.9f; scaleX = squeeze; transformOrigin = TransformOrigin(0f, 0.5f) },
        type(size, color, androidx.compose.ui.text.font.FontWeight.Bold).copy(lineHeight = 1.em, letterSpacing = (-0.02).em), maxLines = 1, softWrap = false, overflow = TextOverflow.Clip,
    )
}

/**
 * One line per action — a tool call, a thought, a group of them: a mark, the verb, what it acted
 * on, what came of it. Live actions get the dot; ones that open show › or ⌄. Every action aligns.
 */
@Composable
fun ActionLine(verb: String, target: String, result: String, live: Boolean = false, failed: Boolean = false, open: Boolean? = null) =
    Row(Modifier.fillMaxWidth().padding(vertical = 3.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.width(16.dp)) { if (live) Dot(pulse = true, size = 6.dp) else if (open != null) T(if (open) "⌄" else "›", size = 14.sp, color = p.meta) }
        T(verb, Modifier.width(72.dp), size = 14.sp, weight = Medium, color = if (live) p.fg else p.mute, lines = 1)
        T(target, Modifier.weight(1f), size = 14.sp, color = if (live) p.fg else p.mute, lines = 1)
        Spacer(Modifier.width(8.dp))
        T(result, Modifier.fillMaxWidth(0.3f), size = 13.sp, color = if (failed) Ink.Rupture else p.meta, lines = 1)
    }

@Composable
fun ToolLine(t: Tool, open: Boolean? = null) = ActionLine(t.verb, t.target, t.result, t.live, t.failed, open)

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
