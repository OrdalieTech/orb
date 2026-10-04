package tech.ordalie.orb.ui

import androidx.compose.animation.*
import androidx.compose.animation.core.*
import androidx.compose.foundation.*
import androidx.compose.foundation.gestures.detectVerticalDragGestures
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.SnapshotStateList
import androidx.compose.ui.*
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.*
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.*
import androidx.compose.ui.text.*
import androidx.compose.ui.text.input.*
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.*
import tech.ordalie.orb.core.*

/** Tokens stay plain text on the wire; the box only draws them as chips while you write. */
private class Tokens(val fg: Color, val bg: Color, val meta: Color) : VisualTransformation {
    override fun filter(text: AnnotatedString) = TransformedText(buildAnnotatedString {
        append(text)
        TOKEN.findAll(text).forEach { m ->
            val style = when (m.value[0]) {
                '/' -> SpanStyle(color = bg, background = fg, fontWeight = Strong)
                '@' -> SpanStyle(fontWeight = Medium, textDecoration = TextDecoration.Underline)
                else -> SpanStyle(color = meta, fontWeight = Medium)
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
    Column(
        modifier.fillMaxWidth().navigationBarsPadding().padding(start = 8.dp, end = 8.dp, bottom = 8.dp)
            .clip(Pane).background(p.raised).border(1.dp, p.rule, Pane).animateContentSize(spring(stiffness = 500f)),
    ) {
        // The top edge is a handle, unmarked until used: drag it to give the draft more room.
        Box(Modifier.fillMaxWidth().padding(top = 6.dp, bottom = 2.dp).pointerInput(Unit) {
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
                    T("/" + cmd.name, size = 15.sp, weight = Strong, lines = 1)
                    Spacer(Modifier.width(12.dp))
                    T(cmd.hint, Modifier.weight(1f), size = 13.sp, color = p.meta, lines = 1)
                }
            }
        }
        Box(Modifier.fillMaxWidth().heightIn(min = ((if (matches.isEmpty()) 52 else 40) + extra).dp, max = (240 + extra).dp).padding(horizontal = 14.dp, vertical = 6.dp)) {
            BasicTextField(
                value, { value = it }, Modifier.fillMaxWidth(), textStyle = type(16.sp, p.fg), cursorBrush = SolidColor(p.fg),
                visualTransformation = tokens,
                decorationBox = { inner -> if (value.text.isEmpty()) T(if (busy) "steer or queue a message" else placeholder, size = 16.sp, color = p.meta); inner() },
            )
        }
        Row(
            Modifier.fillMaxWidth().padding(start = 14.dp, end = 8.dp, top = 2.dp, bottom = 8.dp),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Row(Modifier.press(onClick = onWhere).padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                Where(session?.remote == true, phone = p.fg); Spacer(Modifier.width(7.dp))
                // The model matters more here: a long machine name gives way to it.
                T((session?.where?.takeIf { session.remote } ?: "phone").let { if (it.length > 14) it.take(13) + "…" else it } + " ▾", size = 14.sp, weight = Strong, lines = 1)
            }
            Row(Modifier.weight(1f).press(onClick = onModel).padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                T((session?.model?.substringAfter('/')?.ifEmpty { null } ?: "model") + " ▾", Modifier.weight(1f, fill = false), size = 14.sp, weight = Medium, color = p.mute, lines = 1)
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
