package tech.ordalie.orb.ui

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.spring
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicText
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.scale
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.TransformOrigin
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.layout.layout
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import tech.ordalie.orb.core.Tool
import kotlin.math.roundToInt

/** A hairline separates regions — turns, table rows, the prompt — never words. */
@Composable
fun Rule(modifier: Modifier = Modifier, color: Color = p.rule) = Box(modifier.fillMaxWidth().height(1.dp).background(color))

/** Something you hold: a softly rounded, outlined surface. */
@Composable
fun Card(modifier: Modifier = Modifier, padding: Dp = 18.dp, content: @Composable ColumnScope.() -> Unit) =
    Column(modifier.clip(RoundedCornerShape(Radius.Card)).border(1.dp, p.fg.copy(alpha = 0.85f), RoundedCornerShape(Radius.Card)).padding(padding), content = content)

enum class ChipKind { Inverted, Outline, Quiet, Blue, Rupture }

@Composable
fun Chip(label: String, kind: ChipKind = ChipKind.Outline, modifier: Modifier = Modifier, caps: Boolean = true) {
    val (bg, fg, edge) = when (kind) {
        ChipKind.Inverted -> Triple(p.fg, p.bg, p.fg)
        ChipKind.Outline -> Triple(Color.Transparent, p.fg, p.fg)
        ChipKind.Quiet -> Triple(Color.Transparent, p.mute, p.rule)
        ChipKind.Blue -> Triple(Ink.Blue, Ink.Texte, Ink.Blue)
        ChipKind.Rupture -> Triple(Ink.Rupture, Ink.Texte, Ink.Rupture)
    }
    Box(modifier.clip(CircleShape).background(bg).border(1.dp, edge, CircleShape).padding(horizontal = 10.dp, vertical = 3.dp)) { T(if (caps) label.uppercase() else label, size = if (caps) 11.sp else 13.sp, color = fg) }
}

/** Taps give way under the finger and confirm with a tick. */
@Composable
fun Modifier.press(enabled: Boolean = true, onClick: () -> Unit): Modifier {
    val source = remember { MutableInteractionSource() }
    val pressed by source.collectIsPressedAsState()
    val haptic = LocalHapticFeedback.current
    val s by animateFloatAsState(if (pressed) 0.97f else 1f, spring(stiffness = 900f), label = "press")
    return this.scale(s).alpha(if (pressed) 0.75f else 1f)
        .clickable(source, indication = null, enabled = enabled) { haptic.performHapticFeedback(HapticFeedbackType.TextHandleMove); onClick() }
}

@Composable
fun Btn(label: String, inverted: Boolean = false, modifier: Modifier = Modifier, color: Color = p.fg, on: Color = p.bg, onClick: () -> Unit) =
    Box(modifier.press(onClick = onClick).clip(CircleShape).background(if (inverted) color else Color.Transparent).border(1.dp, color, CircleShape).padding(horizontal = 20.dp, vertical = 11.dp), contentAlignment = Alignment.Center) {
        T(label.uppercase(), size = 13.sp, color = if (inverted) on else color)
    }

/** The ON/OFF pill from the instrument panel: filled, a knob that travels, its state in words. */
@Composable
fun Toggle(on: Boolean, labelOn: String = "on", labelOff: String = "off", accent: Color = p.fg, onClick: (() -> Unit)? = null) {
    val x by animateFloatAsState(if (on) 1f else 0f, spring(dampingRatio = 0.7f, stiffness = 500f), label = "knob")
    val fill by animateColorAsState(if (on) accent else p.mute, label = "fill")
    Box(
        Modifier.then(if (onClick != null) Modifier.press(onClick = onClick) else Modifier).size(72.dp, 34.dp).clip(CircleShape).background(fill),
        contentAlignment = Alignment.CenterStart,
    ) {
        T((if (on) labelOn else labelOff).uppercase(), Modifier.align(if (on) Alignment.CenterStart else Alignment.CenterEnd).padding(horizontal = 11.dp), size = 11.sp, color = p.bg)
        Box(Modifier.offset(x = (4 + 38 * x).dp).size(26.dp).clip(CircleShape).background(p.bg))
    }
}

/** A thin arc gauge with its value in the middle, like the 68% dial. */
@Composable
fun Ring(fraction: Float, label: String, size: Dp = 64.dp) = Box(Modifier.size(size), contentAlignment = Alignment.Center) {
    val track = p.rule
    val ink = p.fg
    Canvas(Modifier.size(size)) {
        val w = 3.dp.toPx()
        drawArc(track, 0f, 360f, false, style = Stroke(w))
        drawArc(ink, -90f, 360f * fraction.coerceIn(0f, 1f), false, style = Stroke(w, cap = StrokeCap.Round))
    }
    T(label, size = 14.sp)
}

@Composable
fun Dot(color: Color = Ink.Rupture, size: Dp = 8.dp, pulse: Boolean = false) {
    val a = if (pulse) rememberInfiniteTransition("dot").animateFloat(1f, 0.25f, infiniteRepeatable(tween(700), RepeatMode.Reverse), "a").value else 1f
    Box(Modifier.size(size).alpha(a).background(color, CircleShape))
}

@Composable
fun Caret(color: Color = p.fg, width: Dp = 8.dp, height: Dp = 16.dp) {
    val on = rememberInfiniteTransition("caret").animateFloat(1f, 0f, infiniteRepeatable(tween(530, delayMillis = 400), RepeatMode.Reverse), "c").value
    Box(Modifier.width(width).height(height).alpha(if (on > 0.5f) 1f else 0f).background(color))
}

/** Three hairlines: the menu. */
@Composable
fun MenuMark(onClick: () -> Unit) = Box(Modifier.press(onClick = onClick).padding(8.dp)) {
    val ink = p.fg
    Canvas(Modifier.size(28.dp, 18.dp)) {
        listOf(0f, 0.5f, 1f).forEach { f -> drawLine(ink, Offset(0f, size.height * f), Offset(size.width, size.height * f), 1.6.dp.toPx(), StrokeCap.Round) }
    }
}

/** The EVA title card: Ubuntu Mono Bold stretched ×1.9 tall, squeezed as needed. Reserved for events and identities. */
@Composable
fun Stretch(text: String, height: Dp, color: Color = p.fg, squeeze: Float = 1f, modifier: Modifier = Modifier) {
    val size = (height.value / 1.9f / 0.72f).sp // cap height of Ubuntu Mono ≈ 0.72em
    BasicText(
        text, modifier
            .layout { m, c ->
                val placeable = m.measure(c.copy(maxWidth = Int.MAX_VALUE))
                val w = (placeable.width * squeeze).roundToInt().coerceAtMost(c.maxWidth)
                // The line box scales about its own centre, so centring it centres the stretched caps.
                layout(w, height.roundToPx()) { placeable.place(0, ((height.toPx() - placeable.height) / 2f).roundToInt()) }
            }
            .graphicsLayer { scaleY = 1.9f; scaleX = squeeze; transformOrigin = TransformOrigin(0f, 0.5f) },
        mono(size, color, bold = true, spacing = (-0.04).em).copy(lineHeight = 1.em), maxLines = 1, softWrap = false, overflow = TextOverflow.Clip,
    )
}

/** One line per tool call: verb, target, result. Live calls get the dot. */
@Composable
fun ToolLine(t: Tool) = Row(Modifier.fillMaxWidth().padding(vertical = 3.dp), verticalAlignment = Alignment.CenterVertically) {
    Box(Modifier.width(14.dp)) { if (t.live) Dot(pulse = true) }
    T(t.verb, Modifier.width(58.dp), color = if (t.live) p.fg else p.meta, lines = 1, size = 15.sp)
    T(t.target, Modifier.weight(1f), color = if (t.live) p.fg else p.mute, lines = 1, size = 15.sp)
    Spacer(Modifier.width(8.dp))
    T(t.result, Modifier.fillMaxWidth(0.34f), color = if (t.failed) Ink.Rupture else p.meta, lines = 1, size = 13.sp)
}

/** A labelled value, the unit of every instrument card: small caps over a large number. */
@Composable
fun Reading(label: String, value: String, modifier: Modifier = Modifier, sub: String = "", big: Boolean = false) = Column(modifier) {
    T(label, label = true, color = p.fg)
    if (sub.isNotEmpty()) T(sub, size = Size.Label, color = p.meta)
    // Numbers read as condensed readouts; words stay at title size so they fit the card.
    if (value.any(Char::isLetter)) T(value, size = 19.sp, lines = 1)
    else Stretch(value, if (big) 52.dp else 36.dp, squeeze = 0.86f, modifier = Modifier.padding(top = 4.dp))
}

/** Where plugins live: a name, a state, a body. */
@Composable
fun Slot(name: String, state: String = "", modifier: Modifier = Modifier, body: @Composable () -> Unit) = Column(modifier) {
    Rule()
    Row(Modifier.fillMaxWidth().padding(top = 14.dp, bottom = 10.dp)) { T(name, Modifier.weight(1f), label = true); T(state, size = Size.Label, color = p.meta) }
    body()
}

/** Every screen opens with its name set large, like a product on a panel, and an action at right. */
@Composable
fun Header(title: String, sub: String = "", back: (() -> Unit)? = null, big: Boolean = back == null, right: @Composable RowScope.() -> Unit = {}) =
    Row(Modifier.fillMaxWidth().padding(start = Margin, end = 12.dp, top = 14.dp, bottom = 14.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        if (back != null) Box(Modifier.press(onClick = back).padding(end = 2.dp)) { T("‹", size = 34.sp, color = p.fg) }
        Column(Modifier.weight(1f)) {
            if (big) Stretch(title.uppercase(), if (back == null) 46.dp else 34.dp, squeeze = 0.9f, modifier = Modifier.padding(bottom = 6.dp))
            else T(title, size = Size.Title, lines = 1)
            if (sub.isNotEmpty()) T(sub, size = 13.sp, color = p.meta, lines = 1)
        }
        right()
    }
