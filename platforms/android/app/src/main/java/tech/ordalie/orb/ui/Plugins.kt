package tech.ordalie.orb.ui

import androidx.compose.foundation.*
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.*
import kotlinx.coroutines.*
import tech.ordalie.orb.core.*

/** These read a computer's Claude Code or Codex: neither belongs on a phone. */
private val COMPUTER = setOf("claude-sessions", "codex-sessions")

/** Orb's bundled plugins on a machine, and their choices. Each one only ever renders into a slot or raises an interrupt. */
@Composable
fun ColumnScope.PluginsScreen(c: Ctx, peer: String) {
    val scope = rememberCoroutineScope()
    var list by remember { mutableStateOf<List<Plugin>>(emptyList()) }
    var note by remember { mutableStateOf("") }
    val phone = peer == c.v.state.self
    suspend fun load() {
        val r = c.v.ask("plugins", "machine" to peer)
        r.error?.let { note = it }
        list = plugins(r.array).filter { !phone || it.name !in COMPUTER }
    }
    // A plugin on or off, or one of its choices; the machine's Orbs opened here reopen with it.
    fun change(vararg args: Pair<String, Any?>) = scope.launch {
        note = c.v.ask("plugin", "machine" to peer, *args).error.orEmpty()
        load()
    }
    LaunchedEffect(peer) { load() }
    val device = c.v.machine(peer)?.name ?: "that device"
    Header("Plugins", sub = "$device · " + if (list.isEmpty()) "reading" else "${list.count { it.on }} of ${list.size} on", back = c.nav::back)
    LazyColumn(Modifier.weight(1f).fillMaxWidth()) {
        items(list, key = { it.name }) { pl ->
            Row(
                Modifier.fillMaxWidth().press { change("name" to pl.name, "on" to !pl.on) }.padding(horizontal = 20.dp, vertical = 16.dp),
                verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(16.dp),
            ) {
                Box(Modifier.size(22.dp).border(1.5.dp, p.fg).background(if (pl.on) p.fg else Color.Transparent), contentAlignment = Alignment.Center) {
                    if (pl.on) T("x", size = 14.sp, bold = true, color = p.bg)
                }
                Column(Modifier.weight(1f)) {
                    T(pl.name, size = 17.sp, weight = if (pl.on) Strong else Regular, color = if (pl.on) p.fg else p.mute)
                    T(pl.about, size = 13.sp, color = p.meta)
                }
            }
            if (pl.on) pl.choices.forEach { ch ->
                Row(Modifier.padding(start = 58.dp, end = 20.dp, bottom = 14.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    T(ch.key, size = 13.sp, color = p.meta)
                    ch.values.forEach { v ->
                        Chip(v, if (v == ch.value) ChipKind.Inverted else ChipKind.Outline, Modifier.press { if (v != ch.value) change("name" to pl.name, "key" to ch.key, "value" to v) })
                    }
                }
            }
            Rule(Modifier.padding(horizontal = 20.dp))
        }
    }
    Column(Modifier.padding(20.dp)) {
        T(note.ifEmpty { "An Orb reads its plugins when it starts: those open here reopen with a change at their next message." }, size = Size.Label, color = if (note.isEmpty()) p.meta else Ink.Rupture)
    }
}
