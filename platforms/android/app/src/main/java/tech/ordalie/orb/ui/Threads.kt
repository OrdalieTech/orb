package tech.ordalie.orb.ui

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.unit.*

// Threads on a device read like a file tree: device → folder → thread. Home lists them all;
// these screens are the way down into one machine.

/** A path the way its owner reads it: home as ~. */
private fun tidy(path: String) = path.replace(Regex("^/(Users|home)/[^/]+"), "~")

/** What starting Orb on a device is doing, on whichever screen started it. */
@Composable
fun Launching(c: Ctx) = AnimatedVisibility(c.v.state.launching.isNotEmpty()) {
    val text = c.v.state.launching
    Row(Modifier.fillMaxWidth().padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Dot(if (text.startsWith("starting")) p.mute else Ink.Rupture, pulse = text.startsWith("starting")); Spacer(Modifier.width(10.dp))
        T(text, Modifier.weight(1f), size = 13.sp, color = if (text.startsWith("starting")) p.mute else Ink.Rupture, lines = 3)
    }
}

/** One machine: its folders, most recently worked in first, and any other folder by path. */
@Composable
fun ColumnScope.DeviceScreen(c: Ctx, peerId: String) {
    val peer = c.v.machine(peerId) ?: return
    val folders = peer.folders
    var path by remember { mutableStateOf("") }
    Header(peer.name, sub = listOfNotNull("${folders.size} folders · ${folders.sumOf { it.threads }} threads", peer.version.ifEmpty { null }?.let { "orb $it" }).joinToString(" · "), back = c.nav::back) {
        Btn("providers") { c.nav.go(Screen.Providers(peer.id)) }
    }
    Column(Modifier.padding(horizontal = Margin)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Field(path, "another folder · ~/code/project", Modifier.weight(1f)) { path = it }
            Spacer(Modifier.width(8.dp))
            Btn("open", inverted = true) { if (path.isNotBlank()) c.nav.go(Screen.Folder(peer.id, path.trim())) }
        }
        Launching(c)
        T("folders", Modifier.padding(top = 18.dp, bottom = 4.dp), label = true)
    }
    LazyColumn(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Margin)) {
        items(folders, key = { it.cwd }) { f ->
            SessionRow(f.cwd.substringAfterLast('/').ifEmpty { "/" }, "${f.threads}", ago(f.modified), live = f.live, asks = false, hue = c.v.hue(peerId)) {
                c.nav.go(Screen.Folder(peer.id, f.cwd))
            }
        }
    }
}

/** One folder on a machine: start a thread here, or open one of its threads. */
@Composable
fun ColumnScope.FolderScreen(c: Ctx, peerId: String, cwd: String) {
    val peer = c.v.machine(peerId) ?: return
    val threads = c.v.home.entries.filter { it.machine == peerId && it.cwd == cwd && !it.unstored }
    Header(cwd.substringAfterLast('/').ifEmpty { cwd }, sub = peer.name + " · " + tidy(cwd), back = c.nav::back)
    Column(Modifier.padding(horizontal = Margin)) {
        Btn("new thread here", inverted = true, modifier = Modifier.fillMaxWidth()) { c.start(peer.id, cwd) }
        Launching(c)
        T(if (threads.isEmpty()) "no threads here yet" else "threads", Modifier.padding(top = 18.dp, bottom = 4.dp), label = true, color = if (threads.isEmpty()) p.meta else p.fg)
    }
    LazyColumn(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Margin)) {
        items(threads, key = { it.key }) { e ->
            SessionRow(e.title, if (e.open) "open" else "", ago(e.modified), e.live, e.asks, hue = c.v.hue(peerId), rename = { c.renameThread(e) }) { c.open(e.key) }
        }
    }
}
