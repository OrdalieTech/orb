package tech.ordalie.orb.ui

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

// Threads on a device read like a file tree: device → folder → thread. Home lists them all;
// these screens are the way down into one machine.

/** A path the way its owner reads it: home as ~. */
private fun tidy(path: String) = path.replace(Regex("^/(Users|home)/[^/]+"), "~")

/** What starting Orb on a device is doing, on whichever screen started it. */
@Composable
fun Launching(c: Ctx) = AnimatedVisibility(c.rt.launching.isNotEmpty()) {
    val text = c.rt.launching
    Row(Modifier.fillMaxWidth().press { if (!text.startsWith("starting")) c.rt.launching = "" }.padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Dot(if (text.startsWith("starting")) p.mute else Ink.Rupture, pulse = text.startsWith("starting")); Spacer(Modifier.width(10.dp))
        T(text, Modifier.weight(1f), size = 13.sp, color = if (text.startsWith("starting")) p.mute else Ink.Rupture, lines = 3)
    }
}

/** One machine: its folders, most recently worked in first, and any other folder by path. */
@Composable
fun ColumnScope.DeviceScreen(c: Ctx, peerId: String) {
    val peer = c.rt.bridge.peers.firstOrNull { it.id == peerId } ?: return
    val threads = c.rt.bridge.threads[peer.id].orEmpty()
    val folders = threads.groupBy { it.cwd }.map { (cwd, ts) -> Triple(cwd, ts.size, ts.maxOf { it.modified }) }.sortedByDescending { it.third }
    var path by remember { mutableStateOf("") }
    Header(peer.name, sub = listOfNotNull("${folders.size} folders · ${threads.size} threads", peer.version.ifEmpty { null }?.let { "orb $it" }).joinToString(" · "), back = c.nav::back)
    Column(Modifier.padding(horizontal = Margin)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.weight(1f).clip(CircleShape).border(1.dp, p.fg, CircleShape).padding(horizontal = 18.dp, vertical = 12.dp)) {
                BasicTextField(path, { path = it }, Modifier.fillMaxWidth(), textStyle = mono(15.sp, p.fg), cursorBrush = SolidColor(p.fg), singleLine = true)
                if (path.isEmpty()) T("another folder · ~/code/project", size = 15.sp, color = p.meta)
            }
            Spacer(Modifier.width(8.dp))
            Btn("open", inverted = true) { if (path.isNotBlank()) c.nav.go(Screen.Folder(peer.id, path.trim())) }
        }
        Launching(c)
        Row(Modifier.padding(top = 12.dp)) { Btn("providers") { c.nav.go(Screen.Providers(peer.id)) } }
        T("folders", Modifier.padding(top = 18.dp, bottom = 4.dp), label = true)
    }
    LazyColumn(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Margin)) {
        items(folders, key = { it.first }) { (cwd, n, last) ->
            SessionRow(cwd.substringAfterLast('/').ifEmpty { "/" }, "$n", ago(last), live = peer.instances.any { it.cwd == cwd && it.busy }, asks = false, remote = true) {
                c.nav.go(Screen.Folder(peer.id, cwd))
            }
        }
    }
}

/** One folder on a machine: start a thread here, or open one of its threads. */
@Composable
fun ColumnScope.FolderScreen(c: Ctx, peerId: String, cwd: String) {
    val peer = c.rt.bridge.peers.firstOrNull { it.id == peerId } ?: return
    val threads = c.rt.bridge.threads[peer.id].orEmpty().filter { it.cwd == cwd }.sortedByDescending { it.modified }
    Header(cwd.substringAfterLast('/').ifEmpty { cwd }, sub = peer.name + " · " + tidy(cwd), back = c.nav::back)
    Column(Modifier.padding(horizontal = Margin)) {
        Btn("new thread here", inverted = true, modifier = Modifier.fillMaxWidth()) { c.start(peer, cwd = cwd) }
        Launching(c)
        T(if (threads.isEmpty()) "no threads here yet" else "threads", Modifier.padding(top = 18.dp, bottom = 4.dp), label = true, color = if (threads.isEmpty()) p.meta else p.fg)
    }
    LazyColumn(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Margin)) {
        items(threads, key = { it.id }) { t ->
            val running = peer.instances.firstOrNull { it.session == t.id }
            val s = running?.let { c.rt.opened(it.id) }
            SessionRow(t.title, if (running != null) "open" else "", ago(t.modified), s?.busy ?: running?.busy == true, s?.ask != null, remote = true, rename = { c.renameThread(peer, t) }) { c.openThread(peer, t) }
        }
    }
}
