package tech.ordalie.orb.ui

import android.os.Build
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.spring
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.scaleIn
import androidx.compose.animation.togetherWith
import android.view.RoundedCorner
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.ime
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.ui.layout.layout
import androidx.compose.foundation.layout.WindowInsetsSides
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.only
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.unit.Dp
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.gestures.detectVerticalDragGestures
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.SnapshotStateList
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.OffsetMapping
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.input.TransformedText
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import tech.ordalie.orb.core.Command
import tech.ordalie.orb.core.Session
import androidx.compose.ui.text.TextRange

/** Tokens stay plain text on the wire; the box only draws them as chips while you write. */
private class Tokens(val fg: Color, val bg: Color, val meta: Color) : VisualTransformation {
    override fun filter(text: AnnotatedString) = TransformedText(buildAnnotatedString {
        append(text)
        TOKEN.findAll(text).forEach { m ->
            val style = when (m.value[0]) {
                '/' -> SpanStyle(color = bg, background = fg, fontWeight = FontWeight.Bold)
                '@' -> SpanStyle(fontWeight = FontWeight.Bold, textDecoration = TextDecoration.Underline)
                else -> SpanStyle(color = meta, fontWeight = FontWeight.Bold)
            }
            addStyle(style, m.range.first, m.range.last + 1)
        }
    }, OffsetMapping.Identity)
    companion object { val TOKEN = Regex("""^/[\w:.-]+|(?<=^|\s)(/skill:[\w.-]+|@[^\s]+|#[\w-]+)""") }
}

@Composable
fun PromptBox(
    session: Session?, cites: SnapshotStateList<String>, onCite: () -> Unit, onWhere: () -> Unit, onModel: () -> Unit, commands: List<Command> = emptyList(),
    placeholder: String = "What should we work on?", modifier: Modifier = Modifier, onSend: (String) -> Unit,
) {
    var value by remember { mutableStateOf(TextFieldValue("")) }
    var extra by remember { mutableFloatStateOf(0f) }
    val density = LocalDensity.current
    val busy = session?.busy == true
    val pal = p
    val tokens = remember(pal) { Tokens(pal.fg, pal.bg, pal.meta) }
    fun take(): String? {
        val body = value.text.trim()
        if (body.isEmpty() && cites.isEmpty()) return null
        val text = (cites.map { "@$it" } + body).filter(String::isNotEmpty).joinToString(" ")
        value = TextFieldValue(""); cites.clear()
        return text
    }
    // Resting, the box floats concentric with the screen's corners. With the keyboard up it becomes a
    // sheet: full width, sides and bottom bleeding past the edges, round only where it meets the content.
    val keyboard = WindowInsets.ime.getBottom(density) > 0
    val k by animateFloatAsState(if (keyboard) 1f else 0f, spring(dampingRatio = 0.9f, stiffness = 380f), label = "sheet")
    val rest = (screenCorner() - 10.dp).coerceAtLeast(22.dp)
    val shape = RoundedCornerShape(topStart = 24.dp, topEnd = 24.dp, bottomStart = rest * (1 - k), bottomEnd = rest * (1 - k))
    val bleed = with(density) { (2.dp * k).roundToPx() }
    Column(
        modifier.fillMaxWidth().padding(start = 10.dp * (1 - k), end = 10.dp * (1 - k), bottom = 10.dp * (1 - k))
            .layout { m, c ->
                val placeable = m.measure(c.copy(minWidth = c.maxWidth + 2 * bleed, maxWidth = c.maxWidth + 2 * bleed))
                layout(c.maxWidth, placeable.height - bleed) { placeable.place(-bleed, 0) }
            }
            .clip(shape).background(p.raised).border(1.dp, p.fg.copy(alpha = 0.85f), shape).animateContentSize(spring(stiffness = 500f)),
    ) {
        // The top edge is a handle, unmarked until used: drag it to give the draft more room.
        Box(Modifier.fillMaxWidth().padding(top = 8.dp, bottom = 4.dp).pointerInput(Unit) {
            detectVerticalDragGestures { _, dy -> extra = (extra - dy / density.density).coerceIn(0f, 320f) }
        }, contentAlignment = Alignment.Center) { Box(Modifier.size(36.dp, 3.dp).clip(RoundedCornerShape(2.dp)).background(if (extra > 0f) p.fg else Color.Transparent)) }
        if (cites.isNotEmpty()) Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 4.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            cites.toList().forEach { c -> Box(Modifier.press { cites.remove(c) }) { Chip("@ ${c.substringAfterLast('/')}  ×", caps = false) } }
            Box(Modifier.press(onClick = onCite)) { Chip("+ cite", ChipKind.Quiet) }
        }
        // Typing / opens the palette: the app's commands and the core's, filtered as you type.
        val query = value.text.takeIf { it.startsWith("/") && it.none(Char::isWhitespace) }?.drop(1)
        val matches = query?.let { q -> commands.filter { it.name.startsWith(q, true) } + commands.filter { !it.name.startsWith(q, true) && it.name.contains(q, true) } }.orEmpty()
        if (matches.isNotEmpty()) Column(Modifier.fillMaxWidth().heightIn(max = 150.dp).verticalScroll(rememberScrollState()).padding(top = 2.dp)) {
            matches.forEach { cmd ->
                Row(Modifier.fillMaxWidth().press {
                    if (cmd.name in NOW) { value = TextFieldValue(""); onSend("/" + cmd.name) }
                    else ("/" + cmd.name + " ").let { value = TextFieldValue(it, TextRange(it.length)) }
                }.padding(horizontal = 18.dp, vertical = 9.dp), verticalAlignment = Alignment.CenterVertically) {
                    T("/" + cmd.name, size = 15.sp, bold = true, lines = 1)
                    Spacer(Modifier.width(12.dp))
                    T(cmd.hint, Modifier.weight(1f), size = Size.Label, color = p.meta, lines = 1)
                }
            }
        }
        Box(Modifier.fillMaxWidth().heightIn(min = ((if (matches.isEmpty()) 72 else 48) + extra).dp, max = (240 + extra).dp).padding(horizontal = 18.dp, vertical = 10.dp)) {
            BasicTextField(
                value, { value = it }, Modifier.fillMaxWidth(), textStyle = mono(17.sp, p.fg), cursorBrush = SolidColor(p.fg),
                visualTransformation = tokens,
                decorationBox = { inner -> if (value.text.isEmpty()) T(if (busy) "steer or queue a message" else placeholder, size = 17.sp, color = p.meta); inner() },
            )
        }
        Row(
            Modifier.fillMaxWidth().padding(start = 18.dp, end = 12.dp, top = 4.dp, bottom = 10.dp).windowInsetsPadding(WindowInsets.navigationBars.only(WindowInsetsSides.Bottom)),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp),
        ) {
            Row(Modifier.press(onClick = onWhere).padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.size(8.dp).background(if (session?.remote == true) Ink.Blue else p.fg)); Spacer(Modifier.width(7.dp))
                T((session?.where ?: "this phone").uppercase() + " ▾", size = 13.sp, bold = true, lines = 1)
            }
            Row(Modifier.weight(1f).press(onClick = onModel).padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                T((session?.model?.substringAfter('/')?.ifEmpty { null } ?: "model") + " ▾", Modifier.weight(1f, fill = false), size = 13.sp, bold = true, color = p.mute, lines = 1)
                // Reasoning as a small meter: one bar per level the model takes, lit up to the current one.
                val levels = session?.levels.orEmpty()
                val at = levels.indexOf(session?.thinking)
                if (levels.isNotEmpty()) Row(Modifier.padding(start = 8.dp), horizontalArrangement = Arrangement.spacedBy(2.dp), verticalAlignment = Alignment.Bottom) {
                    levels.forEachIndexed { i, l -> Box(Modifier.size(3.dp, (5 + 2 * i).dp).background(if (i <= at && l != "off") p.fg else p.rule)) }
                }
            }
            if (cites.isEmpty()) Box(Modifier.press(onClick = onCite).padding(6.dp)) { T("@", size = 19.sp, color = p.mute) }
            val mode = when { busy && value.text.isBlank() -> 0; busy -> 1; else -> 2 }
            AnimatedContent(mode, transitionSpec = { (fadeIn(tween(180)) + scaleIn(tween(180), 0.9f)) togetherWith fadeOut(tween(120)) }, label = "act") { m ->
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    when (m) {
                        0 -> Btn("stop", color = Ink.Rupture) { session?.abort() }
                        1 -> { Btn("queue") { take()?.let { session?.prompt(it) } }; Btn("steer", inverted = true) { take()?.let { session?.steer(it) } } }
                        else -> Btn("send", inverted = true) { take()?.let(onSend) }
                    }
                }
            }
        }
    }
}

/** The display's own bottom corner radius, where the platform reports it. */
@Composable
fun screenCorner(): Dp {
    val view = LocalView.current
    val px = if (Build.VERSION.SDK_INT >= 31) view.rootWindowInsets?.getRoundedCorner(RoundedCorner.POSITION_BOTTOM_LEFT)?.radius ?: 0 else 0
    return with(LocalDensity.current) { px.toDp() }
}
