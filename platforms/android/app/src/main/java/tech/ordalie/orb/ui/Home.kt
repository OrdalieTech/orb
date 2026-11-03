package tech.ordalie.orb.ui

import androidx.compose.foundation.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.*
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.*
import kotlinx.coroutines.*
import tech.ordalie.orb.core.*

@Composable
fun ColumnScope.Home(c: Ctx) {
    val v = c.v
    Notice(c)
    Magi(c)
    // Every thread on every machine in one list, newest first: where it lives is a detail of the row.
    // Machines narrow the list: all of them or one (whose folders open from here). New
    // conversations start from the prompt box, which also chooses where.
    var device by rememberSaveable { mutableStateOf("") }
    val devices = v.home.machines.filter { m -> v.home.entries.any { it.machine == m.id } }
    if (devices.size > 1) Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 10.dp, vertical = 6.dp), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
        (listOf<Machine?>(null) + devices).forEach { d ->
            val on = device == d?.id.orEmpty()
            Box(Modifier.press { device = d?.id.orEmpty() }.clip(Soft).background(if (on) p.fg else Color.Transparent).padding(horizontal = 10.dp, vertical = 5.dp)) {
                T(d?.name ?: "all", size = 14.sp, weight = if (on) Strong else Medium, color = if (on) p.bg else p.mute)
            }
        }
    }
    val shown = if (device.isEmpty()) v.home.entries else v.home.entries.filter { it.machine == device }
    val browsable = v.machine(device)?.takeIf { !it.self && it.launch }
    // Newer threads arriving from other machines land above; a reader at the top stays at the top.
    val list = rememberLazyListState()
    var browsed by remember { mutableStateOf(false) } // the reader scrolled down on purpose
    LaunchedEffect(list) { snapshotFlow { list.isScrollInProgress }.collect { if (!it) browsed = list.firstVisibleItemIndex > 0 } }
    LaunchedEffect(shown.firstOrNull()?.key) { if (!browsed) list.scrollToItem(0) }
    Box(Modifier.padding(horizontal = Margin)) { Launching(c) }
    LazyColumn(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Margin), state = list) {
        browsable?.let { m ->
            item(key = "folders") {
                Row(Modifier.fillMaxWidth().press { c.nav.go(Screen.Device(m.id)) }.padding(vertical = 14.dp), verticalAlignment = Alignment.CenterVertically) {
                    T("Folders on ${m.name}", Modifier.weight(1f), weight = Medium); T("›", size = 20.sp, color = p.mute)
                }
            }
        }
        items(shown, key = { it.key }) { e ->
            val m = v.machine(e.machine)
            SessionRow(e.title, m?.name.takeIf { device.isEmpty() }.orEmpty(), if (e.unstored) "open" else ago(e.modified), e.live, e.asks, e.open, hue = v.hue(e.machine),
                rename = { c.renameThread(e) }) { c.open(e.key) }
        }
        if (v.home.machines.size < 2) item(key = "pair") {
            Row(Modifier.fillMaxWidth().press { c.nav.go(Screen.Bridge) }.padding(vertical = 22.dp)) { T("No paired devices", Modifier.weight(1f), color = p.mute); T("Pair ›", weight = Medium) }
        }
        item { Spacer(Modifier.height(12.dp)) }
    }
    TitleCard(v.home.entries.size + 1, v.state.up)
    PromptBox(null, c, c.palette(null), placeholder = "What should we work on?") { c.send(null, it) }
}

/** An Evangelion title card over the prompt box, as on the desktop: the episode, its name in Japanese, the title stretched tall. */
@Composable
private fun TitleCard(episode: Int, up: Boolean) = Column(Modifier.fillMaxWidth().padding(start = Margin + 4.dp, end = Margin, top = 6.dp, bottom = 6.dp)) {
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        T(if (up) "第%03d話".format(episode) else "第———話", size = 12.sp, weight = Strong, color = Ink.Rupture)
        T("新規セッション · new session", label = true, color = p.meta, lines = 1)
        Box(Modifier.weight(1f).height(1.dp).background(p.rule))
    }
    Row(Modifier.padding(top = 10.dp), verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        Stretch("NEW SESSION", 40.dp, p.fg)
        Box(Modifier.padding(bottom = 6.dp).size(9.dp).background(Ink.Rupture))
    }
}

/**
 * This phone's state put to three votes, as NERV's MAGI put each decision to their three
 * computers: its Bridge is up, its paired machines are connected, its Orb is current. Each says
 * 承認 (approved) or, in red, 否決 (refused); Bridge's open its screen.
 */
@Composable
private fun Magi(c: Ctx) {
    val v = c.v
    val peers = v.home.machines.filter { !it.self }
    val connected = peers.count { it.connected }
    val current = v.state.latest.isEmpty() || !Release.newer(v.state.latest, c.rt.version)
    Row(Modifier.fillMaxWidth().padding(start = Margin, end = Margin, top = 10.dp, bottom = 4.dp), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        listOf(
            Triple("bridge", if (v.state.up) "up" else "…", v.state.up),
            Triple("peers", "$connected/${peers.size}", connected == peers.size),
            Triple("orb", c.rt.version, current),
        ).forEach { (name, value, yes) ->
            val ink = if (yes) p.mute else Ink.Rupture
            Column(Modifier.weight(1f).border(1.dp, p.rule.copy(alpha = 0.8f), RoundedCornerShape(4.dp)).press(enabled = name != "orb") { c.nav.go(Screen.Bridge) }.padding(horizontal = 8.dp, vertical = 6.dp)) {
                T(name, label = true, color = p.mute)
                Row(verticalAlignment = Alignment.CenterVertically) {
                    T(value, Modifier.weight(1f), size = 12.sp, color = ink, lines = 1)
                    T(if (yes) "承認" else "否決", size = Size.Label, weight = Strong, color = ink)
                }
            }
        }
    }
}

fun ago(ms: Long): String {
    val m = (System.currentTimeMillis() - ms) / 60_000L
    return when { m < 1 -> "now"; m < 60 -> "${m}m"; m < 1440 -> "${m / 60}h"; else -> "${m / 1440}d" }
}

/** At most one line above the list: the Linux setting up, a newer Orb, or the phone's files to allow. */
@Composable
private fun Notice(c: Ctx) {
    val rt = c.rt
    val linux = rt.orb.linux
    val scope = rememberCoroutineScope()
    var updating by remember { mutableStateOf("") }
    val next = rt.view.state.latest.takeIf { it.isNotEmpty() && Release.newer(it, rt.version) }
    val failed = linux.state.contains("failed") || updating.startsWith("could")
    val (text, act) = when {
        linux.state.isNotEmpty() -> linux.state to { if (failed) rt.setupLinux() }
        next != null -> updating.ifEmpty { "Orb $next is available · update" } to {
            if (updating.isEmpty() || updating.startsWith("could")) {
                updating = "downloading $next…"
                scope.launch { updating = runCatching { Release.install(c.context, next) }.fold({ "" }, { "could not update · " + it.message }) }
            }
        }
        linux.ready && !linux.storage && android.os.Build.VERSION.SDK_INT >= 30 -> "Let Orb use the phone's files · allow" to {
            c.context.startActivity(android.content.Intent(android.provider.Settings.ACTION_MANAGE_APP_ALL_FILES_ACCESS_PERMISSION, android.net.Uri.parse("package:" + c.context.packageName)))
        }
        else -> return
    }
    Row(Modifier.fillMaxWidth().press(onClick = act).padding(start = Margin, end = Margin, top = 12.dp, bottom = 2.dp), verticalAlignment = Alignment.CenterVertically) {
        Dot(if (failed || next != null) Ink.Rupture else p.mute, pulse = !failed && (linux.state.isNotEmpty() || updating.startsWith("downloading"))); Spacer(Modifier.width(10.dp))
        T(text, Modifier.weight(1f), size = 14.sp, weight = Medium, color = if (failed) Ink.Rupture else p.mute, lines = 2)
    }
}

/** One line per session: its title, then where it lives and its condition in words — live, asking,
 *  or how long ago. No rules between rows: the space and the weight of the open one are enough. */
@Composable
fun SessionRow(title: String, meta: String, age: String, live: Boolean, asks: Boolean, current: Boolean = false, modifier: Modifier = Modifier, hue: Int = 0, rename: (() -> Unit)? = null, open: () -> Unit) =
    Row(modifier.fillMaxWidth().press(onLong = rename, onClick = open).padding(vertical = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        Where(hue, live, asks)
        T(title, Modifier.weight(1f), size = 15.sp, weight = if (current || live) Strong else Regular, lines = 1)
        T(listOf(meta, if (asks) "asks" else if (live) "live" else age).filter(String::isNotEmpty).joinToString(" · "), size = 13.sp, color = if (asks) Ink.Rupture else p.meta, lines = 1)
    }

/** Where a session runs, as a square: the ink for this phone, a paired device's own hue, red while
 *  it asks you something; it pulses while it works. Tabs, rows and the prompt box share it. */
@Composable
fun Where(hue: Int, live: Boolean = false, asks: Boolean = false, phone: Color = p.mute) =
    Dot(if (asks) Ink.Rupture else hue(hue, phone), 7.dp, pulse = live, square = true)
