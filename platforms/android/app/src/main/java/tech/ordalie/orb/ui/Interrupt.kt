package tech.ordalie.orb.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.runtime.Composable
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch
import tech.ordalie.orb.core.Ask
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue

/** A plugin stopping the world: the only place red fills space. The word names who is asking. */
@Composable
fun Interrupt(a: Ask, answer: (String?) -> Unit) {
    val word = when (a.owner) { "permissions" -> "APPROVAL"; "bridge" -> "PAIR"; else -> "QUESTION" }
    var text by remember(a.id) { mutableStateOf("") }
    val ink = Ink.Charcoal
    Column(Modifier.fillMaxSize().background(Ink.Rupture).press {}.statusBarsPadding().navigationBarsPadding().imePadding()) {
        Row(Modifier.fillMaxWidth().padding(horizontal = Margin, vertical = 16.dp)) { T(a.owner, Modifier.weight(1f), label = true, color = ink); T("waiting on you", size = Size.Label, color = ink) }
        Rule(color = ink)
        Stretch(word, 168.dp, ink, squeeze = if (word.length > 5) 0.6f else 1f, modifier = Modifier.padding(horizontal = Margin, vertical = 22.dp))
        // Orb's prompts put the request on the first line and its evidence below.
        val lines = a.title.lines()
        T(lines.first(), Modifier.padding(horizontal = Margin), size = Size.Title, color = ink)
        val detail = (lines.drop(1) + a.message).filter(String::isNotBlank).joinToString("\n")
        if (detail.isNotEmpty()) T(detail, Modifier.padding(horizontal = Margin, vertical = 10.dp), size = 14.sp, color = ink.copy(alpha = 0.8f))
        Spacer(Modifier.weight(1f))
        Rule(color = ink)
        a.choices.forEachIndexed { i, choice ->
            val first = i == 0
            // Orb's own prompts lead with their key ("y approve once"); others get one.
            val keyed = Regex("^([a-z]) (.+)").find(choice)
            val key = keyed?.groupValues?.get(1) ?: when (choice) { "allow" -> "y"; "deny" -> "n"; else -> (i + 1).toString() }
            Row(Modifier.fillMaxWidth().background(if (first) ink else Color.Transparent).press { answer(choice) }.padding(horizontal = 20.dp, vertical = 16.dp)) {
                T(key, Modifier.width(36.dp), bold = true, color = if (first) Ink.Rupture else ink)
                T(keyed?.groupValues?.get(2) ?: choice, color = if (first) Ink.Rupture else ink)
            }
        }
        if (a.free) Row(Modifier.fillMaxWidth().padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.weight(1f).border(1.dp, ink, CircleShape).padding(horizontal = 18.dp, vertical = 12.dp)) {
                BasicTextField(text, { text = it }, Modifier.fillMaxWidth(), textStyle = type(color = ink), cursorBrush = SolidColor(ink))
                if (text.isEmpty()) T("type an answer", color = ink.copy(alpha = 0.6f))
            }
            Spacer(Modifier.width(8.dp))
            Btn("send", inverted = true, color = ink, on = Ink.Rupture) { answer(text) }
        }
        Rule(color = ink)
        Box(Modifier.fillMaxWidth().press { answer(null) }.padding(16.dp)) { T("back  ·  dismiss", size = Size.Label, color = ink) }
        Spacer(Modifier.height(4.dp))
    }
}

@Composable
fun PairRequest(claim: org.json.JSONObject, c: Ctx) {
    val scope = rememberCoroutineScope()
    val claimant = claim.optString("claimant")
    Interrupt(Ask("pair:$claimant", "bridge", "Pair with ${claimant.substringAfterLast(':').take(8)}?",
        "Check the other device shows this fingerprint:\n$claimant\nAllowing lets it read and drive this phone's conversations, run what they run, and start Orb here.", listOf("allow", "deny"))) { v ->
        scope.launch { if (v == "allow") c.rt.bridge.approve(claim) else c.rt.bridge.forget(claimant) }
    }
}
