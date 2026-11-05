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
import kotlin.math.roundToInt
import kotlinx.coroutines.launch
import tech.ordalie.orb.core.*

/**
 * The model sheet rises from the bottom: the model and its reasoning on top, every model below,
 * those in use first, then grouped by provider and narrowed by search. With no conversation (Home,
 * Providers) it chooses the default new conversations start on, on its machine.
 */
@Composable
fun AnimatedVisibilityScope.ModelSheet(session: Tab, c: Ctx, dismiss: () -> Unit) = Sheet(dismiss, Modifier.heightIn(max = LocalConfiguration.current.screenHeightDp.dp * 0.8f)) {
    var query by remember { mutableStateOf("") }
    val keyboard = LocalSoftwareKeyboardController.current
    LaunchedEffect(Unit) { keyboard?.hide() }
    // Asked as it opens: a conversation elsewhere may have changed it.
    LaunchedEffect(session.peer) { c.v.readDefault(session.peer) }
    val def = c.v.defaults[session.peer]
    val home = session.id.isEmpty()
    val s = if (home) def ?: session else session
    // Run past the sheet, which a pick closes.
    fun default(model: String, level: String = "") = c.rt.scope.launch { c.v.makeDefault(s.peer, model, level) }
    fun match(id: String) = query.isBlank() || id.contains(query.trim(), ignoreCase = true)
    val used = (listOfNotNull(def?.model) + c.v.state.tabs.filter { it.peer == s.peer }.map { it.model }).distinct().filter { it in s.models && match(it) }
    val groups = mapOf("in use" to used) + s.models.filter(::match).groupBy { it.substringBefore('/') }
    Row(Modifier.fillMaxWidth().padding(start = Margin, end = Margin, top = 18.dp), verticalAlignment = Alignment.Bottom) {
        Column(Modifier.weight(1f)) {
            T(if (!home) "model" else "default · " + if (s.peer == c.v.state.self) "this phone" else s.where, label = true, color = p.meta, lines = 1)
            T(s.model.substringAfter('/').ifEmpty { "none" }, size = 19.sp, weight = Strong, lines = 1)
        }
        if (!home && def != null && s.model.isNotEmpty() && s.model != def.model) Btn("make default", modifier = Modifier.padding(start = 12.dp)) { default(s.model, s.thinking) }
        else T(listOf(s.model.substringBefore('/'), if (home) "" else s.where).filter(String::isNotEmpty).joinToString(" · "), Modifier.padding(start = 12.dp, bottom = 3.dp), size = 13.sp, color = p.meta, lines = 1)
    }
    Field(query, "Search models", Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 10.dp)) { query = it }
    LazyColumn(Modifier.weight(1f, fill = false)) {
        groups.filterValues { it.isNotEmpty() }.forEach { (group, ids) ->
            item(key = "h:$group") { T(if (group == "in use") group else "$group · ${ids.size}", Modifier.padding(start = Margin, top = 12.dp, bottom = 2.dp), label = true, color = p.meta) }
            items(ids, key = { "$group:$it" }) { id ->
                val on = id == s.model
                Row(Modifier.fillMaxWidth().press {
                    if (home) default(id) else c.v.send("model", "tab" to s.id, "name" to id)
                    dismiss()
                }.padding(horizontal = Margin, vertical = 11.dp), verticalAlignment = Alignment.CenterVertically) {
                    T(if (group == "in use") id else id.substringAfter('/'), Modifier.weight(1f), size = 16.sp, weight = if (on) Strong else Regular, lines = 1)
                    if (id == def?.model) T("default", Modifier.padding(start = 10.dp, end = 10.dp), label = true, color = p.meta)
                    if (on) Dot(p.fg)
                }
            }
        }
        item(key = "add") {
            Row(Modifier.fillMaxWidth().press { dismiss(); c.nav.go(Screen.Providers(s.peer)) }.padding(horizontal = Margin, vertical = 16.dp)) { T("+ connect a provider", weight = Medium, color = p.mute) }
        }
    }
    Accounts(s, c)
    // Reasoning is what changes most often: it sits last, where the thumb already is.
    if (s.levels.isNotEmpty()) { Rule(); Reasoning(s.levels, s.thinking) { if (home) default(s.model, it) else c.v.send("thinking", "tab" to s.id, "name" to it) } }
}

/**
 * The accounts the model's provider has on the machine the session runs on, as that machine lists
 * them (its own, not this phone's): the one in use marked, the others a tap away with what their
 * plans have left.
 */
@Composable
private fun Accounts(session: Tab, c: Ctx) {
    val scope = rememberCoroutineScope()
    var list by remember { mutableStateOf(emptyList<Account>()) }
    var note by remember { mutableStateOf("") }
    LaunchedEffect(session.peer) { list = accounts(c.v.ask("accounts", "machine" to session.peer).array) }
    val mine = list.filter { it.provider == session.model.substringBefore('/') }
    if (mine.isEmpty()) return
    Rule()
    Column(Modifier.padding(start = Margin, end = Margin, top = 12.dp, bottom = 4.dp)) {
        T("account · ${session.where}", label = true, color = p.meta)
        mine.forEach { a ->
            Row(Modifier.fillMaxWidth().press(enabled = !a.active) {
                scope.launch {
                    note = c.v.ask("account", "machine" to session.peer, "provider" to a.provider, "account" to a.id).error.orEmpty()
                    list = accounts(c.v.ask("accounts", "machine" to session.peer).array)
                }
            }.padding(vertical = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Dot(if (a.active) p.fg else p.rule)
                T(a.name, Modifier.weight(1f), size = 15.sp, weight = if (a.active) Strong else Regular, lines = 1)
                a.windows.minByOrNull { it.left }?.let { w ->
                    T("${w.name} ${w.left.roundToInt()}% left", size = 13.sp, color = if (w.left < 15) Ink.Rupture else p.meta, lines = 1)
                }
            }
        }
        if (note.isNotEmpty()) T(note, size = 13.sp, color = Ink.Rupture)
    }
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
