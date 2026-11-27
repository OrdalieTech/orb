package tech.ordalie.orb.ui

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.*
import tech.ordalie.orb.core.*

// Threads on a device read like a file tree: device → folder → thread. Home lists them all;
// these screens are the way down into one machine.

/** A path the way its owner reads it: home as ~. */
fun tidy(path: String) = path.replace(Regex("^/(Users|home)/[^/]+"), "~")

/** What starting Orb on a device is doing, on whichever screen started it; why it failed until tapped away. */
@Composable
fun Launching(c: Ctx) {
    val text = c.v.state.launching
    var gone by remember { mutableStateOf("") } // the failure tapped away; the next start shows again
    LaunchedEffect(text) { if (text != gone) gone = "" }
    val starting = text.startsWith("starting")
    AnimatedVisibility(text.isNotEmpty() && text != gone) {
        Row(Modifier.fillMaxWidth().press(enabled = !starting) { gone = text }.padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            Dot(if (starting) p.mute else Ink.Rupture, pulse = starting); Spacer(Modifier.width(10.dp))
            T(text, Modifier.weight(1f), size = 13.sp, color = if (starting) p.mute else Ink.Rupture, lines = 3)
            if (!starting) T("×", Modifier.padding(start = 10.dp), size = 16.sp, color = p.meta)
        }
    }
}

/** One machine: its folders, most recently worked in first, and any other folder by path. */
@Composable
fun ColumnScope.DeviceScreen(c: Ctx, peerId: String) {
    val peer = c.v.machine(peerId) ?: return
    val folders = peer.folders
    var path by remember { mutableStateOf("") }
    Header(peer.name, sub = listOfNotNull("${folders.size} folder" + (if (folders.size == 1) "" else "s") + " · ${folders.sumOf { it.threads }} threads", peer.version.ifEmpty { null }?.let { "orb $it" }).joinToString(" · "), back = c.nav::back, hue = c.v.hue(peerId)) {
        Btn("providers") { c.nav.go(Screen.Providers(peer.id)) }
        Btn("plugins") { c.nav.go(Screen.Plugins(peer.id)) }
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
    val folderName = cwd.substringAfterLast('/').ifEmpty { cwd }
    var showArchived by remember { mutableStateOf(false) }
    var archived by remember { mutableStateOf<List<Entry>>(emptyList()) }
    var loadingArchived by remember { mutableStateOf(false) }
    LaunchedEffect(showArchived, peerId, cwd) {
        if (showArchived) {
            loadingArchived = true
            c.v.archivedThreads(peerId) { entries, _ ->
                archived = entries.filter { it.cwd == cwd }
                loadingArchived = false
            }
        }
    }
    Header(folderName, sub = peer.name + " · " + tidy(cwd), back = c.nav::back, hue = c.v.hue(peerId))
    Column(Modifier.padding(horizontal = Margin)) {
        Launching(c)
        Row(
            Modifier.fillMaxWidth().padding(top = 12.dp, bottom = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            T(
                if (showArchived) {
                    if (loadingArchived) "loading archived…" else if (archived.isEmpty()) "no archived threads" else "archived"
                } else {
                    if (threads.isEmpty()) "no threads here yet" else "threads"
                },
                label = true,
                color = if ((showArchived && archived.isEmpty()) || (!showArchived && threads.isEmpty())) p.meta else p.fg,
            )
            Box(
                Modifier.clip(Pane)
                    .background(if (showArchived) p.raised else Color.Transparent)
                    .border(1.dp, if (showArchived) p.mute else p.rule, Pane)
                    .press { showArchived = !showArchived }
                    .padding(horizontal = 10.dp, vertical = 4.dp),
            ) {
                T("Archived", size = 12.sp, weight = if (showArchived) Strong else Medium, color = if (showArchived) p.fg else p.mute, lines = 1)
            }
        }
    }
    LazyColumn(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Margin)) {
        if (showArchived) {
            items(archived, key = { it.key }) { e ->
                SessionRow(
                    named(e.title),
                    "unarchive",
                    ago(e.modified),
                    live = false,
                    asks = false,
                    hue = c.v.hue(peerId),
                    rename = {
                        c.pick(Picker(named(e.title), listOf("unarchive")) {
                            c.v.archive(e.key, false) { err ->
                                if (err == null) archived = archived.filterNot { it.key == e.key }
                            }
                        })
                    },
                ) {
                    c.v.archive(e.key, false) { err ->
                        if (err == null) archived = archived.filterNot { it.key == e.key }
                    }
                }
            }
        } else {
            items(threads, key = { it.key }) { e ->
                SessionRow(e.title, if (e.open) "open" else "", ago(e.modified), e.live, e.asks, hue = c.v.hue(peerId), rename = { c.threadMenu(e) }) { c.open(e.key) }
            }
        }
    }
    PromptBox(
        session = null,
        c = c,
        commands = c.palette(null),
        placeholder = "Work in $folderName on ${peer.name}…",
        targetOverride = peer.id to cwd,
    ) { c.sendIn(peer.id, cwd, it) }
}
