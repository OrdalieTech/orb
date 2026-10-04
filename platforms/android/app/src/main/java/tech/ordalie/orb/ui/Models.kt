package tech.ordalie.orb.ui

import androidx.compose.animation.*
import androidx.compose.animation.core.tween
import androidx.compose.foundation.*
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.*
import androidx.compose.ui.unit.*
import tech.ordalie.orb.core.Session

/**
 * The model sheet rises from the bottom: the model and its reasoning on top, every model below,
 * grouped by provider and narrowed by search. Choices are remembered for the sessions that follow.
 */
@Composable
fun AnimatedVisibilityScope.ModelSheet(session: Session, c: Ctx, dismiss: () -> Unit) = Sheet(dismiss, Modifier.heightIn(max = LocalConfiguration.current.screenHeightDp.dp * 0.8f)) {
    var query by remember { mutableStateOf("") }
    val keyboard = LocalSoftwareKeyboardController.current
    LaunchedEffect(Unit) { keyboard?.hide() }
    val groups = session.models().filter { query.isBlank() || it.contains(query.trim(), ignoreCase = true) }.groupBy { it.substringBefore('/') }
    Row(Modifier.fillMaxWidth().padding(start = Margin, end = Margin, top = 18.dp), verticalAlignment = Alignment.Bottom) {
        Column(Modifier.weight(1f)) {
            T("model", label = true, color = p.meta)
            T(session.model.substringAfter('/').ifEmpty { "none" }, size = 19.sp, weight = Strong, lines = 1)
        }
        T(listOf(session.model.substringBefore('/'), session.where).filter(String::isNotEmpty).joinToString(" · "), Modifier.padding(start = 12.dp, bottom = 3.dp), size = 13.sp, color = p.meta, lines = 1)
    }
    Field(query, "Search models", Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 10.dp)) { query = it }
    LazyColumn(Modifier.weight(1f, fill = false), state = rememberLazyListState(groups.values.flatten().indexOf(session.model).coerceAtLeast(0))) {
        groups.forEach { (provider, ids) ->
            item(key = "h:$provider") { T("$provider · ${ids.size}", Modifier.padding(start = Margin, top = 12.dp, bottom = 2.dp), label = true, color = p.meta) }
            items(ids, key = { it }) { id ->
                val on = id == session.model
                Row(Modifier.fillMaxWidth().press { session.useModel(id); dismiss() }.padding(horizontal = Margin, vertical = 11.dp), verticalAlignment = Alignment.CenterVertically) {
                    T(id.substringAfter('/'), Modifier.weight(1f), size = 16.sp, weight = if (on) Strong else Regular, lines = 1)
                    if (on) Dot(p.fg)
                }
            }
        }
        item(key = "add") {
            Row(Modifier.fillMaxWidth().press { dismiss(); c.nav.go(Screen.Providers(session.peer)) }.padding(horizontal = Margin, vertical = 16.dp)) { T("+ Provider", weight = Medium, color = p.mute) }
        }
    }
    // Reasoning is what changes most often: it sits last, where the thumb already is.
    if (session.levels.isNotEmpty()) { Rule(); Reasoning(session.levels, session.thinking, session::useThinking) }
}

/** Reasoning as one segmented control: the levels this model takes, the chosen one said in words. */
@Composable
private fun Reasoning(levels: List<String>, level: String, pick: (String) -> Unit) = Column(Modifier.padding(start = Margin, end = Margin, top = 12.dp, bottom = 16.dp)) {
    Row {
        T("reasoning", Modifier.weight(1f), label = true, color = p.meta)
        T(REASONING[level] ?: level, size = 13.sp, color = p.mute)
    }
    Row(Modifier.padding(top = 8.dp).fillMaxWidth().clip(Soft).border(1.dp, p.fg, Soft)) {
        levels.forEach { l ->
            val on = l == level
            val fill by animateColorAsState(if (on) p.fg else Color.Transparent, tween(160), label = "level")
            Box(Modifier.weight(1f).press { pick(l) }.background(fill).padding(vertical = 10.dp), contentAlignment = Alignment.Center) {
                T(l.replace("minimal", "min").replace("medium", "med"), size = 13.sp, weight = if (on) Strong else Medium, color = if (on) p.bg else p.fg, lines = 1)
            }
        }
    }
}

private val REASONING = mapOf("off" to "no thinking", "minimal" to "barely", "low" to "light", "medium" to "balanced", "high" to "thorough", "xhigh" to "very thorough", "max" to "as long as it takes")
