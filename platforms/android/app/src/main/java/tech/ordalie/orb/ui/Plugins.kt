package tech.ordalie.orb.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import tech.ordalie.orb.core.Plugin

/** The app manages Bridge itself; these stay out of the list. */
private val MANAGED = setOf("bridge", "claude-sessions", "provider-usage")

/** Orb's bundled plugins. Each one only ever renders into a slot or raises an interrupt. */
@Composable
fun ColumnScope.PluginsScreen(c: Ctx) {
    val scope = rememberCoroutineScope()
    var list by remember { mutableStateOf<List<Plugin>>(emptyList()) }
    var dirty by remember { mutableStateOf(false) }
    var mode by remember { mutableStateOf(c.rt.orb.permissions) }
    LaunchedEffect(Unit) { list = withContext(Dispatchers.IO) { c.rt.orb.plugins() }.filter { it.name !in MANAGED } }
    Header("plugins", sub = if (list.isEmpty()) "reading" else "${list.count { it.on }} of ${list.size} on", back = c.nav::back)
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
                    T(pl.name, size = 17.sp, bold = pl.on, color = if (pl.on) p.fg else p.mute)
                    T(pl.about, size = Size.Label, color = p.meta)
                }
                if (pl.name == "permissions" && pl.on) Box(Modifier.press {
                    scope.launch { withContext(Dispatchers.IO) { c.rt.orb.permissions = if (mode == "auto") "enforce" else "auto" }; mode = c.rt.orb.permissions; dirty = true }
                }) { Chip(mode.uppercase(), if (mode == "enforce") ChipKind.Inverted else ChipKind.Outline) }
            }
            Rule(Modifier.padding(horizontal = 20.dp))
        }
    }
    Column(Modifier.padding(20.dp)) {
        if (dirty) Btn("apply · restart core", inverted = true) { c.rt.restart(); c.nav.back() }
        else T("Toggles write Orb's own settings, the same as orb plugins enable.", size = Size.Label, color = p.meta)
    }
}
