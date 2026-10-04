package tech.ordalie.orb.ui

import androidx.compose.foundation.*
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.*
import kotlinx.coroutines.*
import tech.ordalie.orb.core.Plugin

/** The app manages Bridge itself, and these read a computer's Claude Code, Codex or footer: none belongs on a phone. */
private val MANAGED = setOf("bridge", "bridge-agent-calls", "claude-sessions", "codex-sessions", "provider-usage")

/** Orb's bundled plugins. Each one only ever renders into a slot or raises an interrupt. */
@Composable
fun ColumnScope.PluginsScreen(c: Ctx) {
    val scope = rememberCoroutineScope()
    var list by remember { mutableStateOf<List<Plugin>>(emptyList()) }
    var dirty by remember { mutableStateOf(false) }
    var mode by remember { mutableStateOf(c.rt.orb.permissions) }
    LaunchedEffect(Unit) { list = withContext(Dispatchers.IO) { c.rt.orb.plugins() }.filter { it.name !in MANAGED } }
    Header("Plugins", sub = if (list.isEmpty()) "reading" else "${list.count { it.on }} of ${list.size} on", back = c.nav::back)
    LazyColumn(Modifier.weight(1f).fillMaxWidth()) {
        items(list, key = { it.name }) { pl ->
            Row(
                Modifier.fillMaxWidth().press {
                    scope.launch {
                        if (withContext(Dispatchers.IO) { c.rt.orb.plugin(pl.name, !pl.on) }) {
                            list = list.map { if (it.name == pl.name) it.copy(on = !it.on) else it }; dirty = true
                        }
                    }
                }.padding(horizontal = 20.dp, vertical = 16.dp),
                verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(16.dp),
            ) {
                Box(Modifier.size(22.dp).border(1.5.dp, p.fg).background(if (pl.on) p.fg else Color.Transparent), contentAlignment = Alignment.Center) {
                    if (pl.on) T("x", size = 14.sp, bold = true, color = p.bg)
                }
                Column(Modifier.weight(1f)) {
                    T(pl.name, size = 17.sp, weight = if (pl.on) Strong else Regular, color = if (pl.on) p.fg else p.mute)
                    T(pl.about, size = 13.sp, color = p.meta)
                }
                if (pl.name == "permissions" && pl.on) Box(Modifier.press {
                    scope.launch { withContext(Dispatchers.IO) { c.rt.orb.permissions = if (mode == "auto") "enforce" else "auto" }; mode = c.rt.orb.permissions; dirty = true }
                }) { Chip(mode.uppercase(), if (mode == "enforce") ChipKind.Inverted else ChipKind.Outline) }
            }
            Rule(Modifier.padding(horizontal = 20.dp))
        }
    }
    Column(Modifier.padding(20.dp)) {
        if (dirty) Btn("apply · restart Orbs", inverted = true) { c.rt.scope.launch { c.rt.bridge.restart() }; c.nav.back() }
        else T("Toggles write Orb's own settings, the same as orb plugins enable.", size = Size.Label, color = p.meta)
    }
}
