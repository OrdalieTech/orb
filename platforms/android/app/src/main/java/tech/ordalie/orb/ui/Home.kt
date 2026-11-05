package tech.ordalie.orb.ui

import androidx.compose.foundation.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.*
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.*
import kotlinx.coroutines.*
import tech.ordalie.orb.core.*

@Composable
fun ColumnScope.Home(c: Ctx) {
    val v = c.v
    Notice(c)
    var device by rememberSaveable { mutableStateOf("") }
    Magi(c, device) { device = if (device == it) "" else it }
    // What runs, as the desktop's sidebar says it under the same cards: "2 working · …", "needs you · …".
    T(v.state.summary, Modifier.padding(horizontal = Margin + 2.dp, vertical = 2.dp), size = 13.sp, color = if (v.state.summary.startsWith("needs")) Ink.Rupture else p.meta, lines = 1)
    // Every thread on every machine in one list, newest first: where it lives is a detail of the row.
    // A machine's card narrows the list to it (and its folders open from here). New conversations
    // start from the prompt box, which also chooses where.
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
            // An open one says its state as its tab does, its age once at rest; a re-sort slides it into place.
            SessionRow(named(e.title), m?.name.takeIf { device.isEmpty() }.orEmpty(), v.tab(e.tab)?.let(v::word).orEmpty().ifEmpty { if (e.unstored) "open" else ago(e.modified) },
                e.live, e.asks, e.open, Modifier.animateItem(), hue = v.hue(e.machine), rename = { c.threadMenu(e) }) { c.open(e.key) }
        }
        // Until the view sends Home (its Bridge starting), a loader rather than an empty list.
        if (v.home.machines.isEmpty()) item(key = "loading") {
            Row(Modifier.padding(vertical = 22.dp), verticalAlignment = Alignment.CenterVertically) { Dot(p.mute, pulse = true); Spacer(Modifier.width(10.dp)); T("reading conversations…", color = p.mute) }
        } else if (v.home.machines.size < 2) item(key = "pair") {
            Row(Modifier.fillMaxWidth().press { c.nav.go(Screen.Bridge) }.padding(vertical = 22.dp)) { T("No paired devices", Modifier.weight(1f), color = p.mute); T("Pair ›", weight = Medium) }
        }
        item { Spacer(Modifier.height(12.dp)) }
    }
    TitleCard()
    // Sent here from the overview's new-session card: the box takes the keyboard once the page settles.
    val focus = remember { FocusRequester() }
    val keyboard = LocalSoftwareKeyboardController.current
    LaunchedEffect(c.nav.compose) {
        if (!c.nav.compose) return@LaunchedEffect
        delay(400); runCatching { focus.requestFocus() }; keyboard?.show(); c.nav.compose = false
    }
    PromptBox(null, c, c.palette(null), placeholder = "What should we work on?", focus = focus) { c.send(null, it) }
}

/** An Evangelion title card over the prompt box, as on the desktop: a rule, the title stretched tall. */
@Composable
private fun TitleCard() = Column(Modifier.fillMaxWidth().padding(start = Margin + 4.dp, end = Margin, top = 6.dp, bottom = 10.dp)) {
    Rule()
    Row(Modifier.padding(top = 10.dp), verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        Stretch("NEW SESSION", 36.dp, p.fg)
        Box(Modifier.padding(bottom = 2.dp).size(7.dp).background(Ink.Rupture))
    }
}

/**
 * Every machine of the Bridge put to the vote, as NERV's MAGI put each decision to their three
 * computers: this phone first, up and on a current Orb, then each paired one, connected. Each says
 * 承認 (approved) or, in red, 否決 (refused), and how many Orbs run there. A card narrows the list
 * to its machine, and back.
 */
@Composable
private fun Magi(c: Ctx, device: String, pick: (String) -> Unit) {
    val v = c.v
    val machines = v.home.machines.sortedByDescending { it.self }.ifEmpty { listOf(Machine(v.state.self, "", true, false, "", false, 0, emptyList(), emptyList())) }
    // A development build is the newest there is, whatever its number.
    val current = v.state.latest.isEmpty() || !Release.newer(v.state.latest, c.rt.version) || c.rt.version.endsWith("-dev")
    val fit = machines.size <= 3
    Row(Modifier.fillMaxWidth().then(if (fit) Modifier else Modifier.horizontalScroll(rememberScrollState())).padding(start = Margin, end = Margin, top = 10.dp, bottom = 4.dp), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        machines.forEach { m ->
            val up = if (m.self) v.state.up else m.connected
            val yes = up && (!m.self || current)
            val ink = if (yes) p.mute else Ink.Rupture
            val on = device == m.id
            Column(Modifier.then(if (fit) Modifier.weight(1f) else Modifier.width(128.dp)).clip(RoundedCornerShape(4.dp)).background(if (on) p.raised else Color.Transparent)
                .border(1.dp, if (on) p.fg else p.rule.copy(alpha = 0.8f), RoundedCornerShape(4.dp)).press { pick(m.id) }.padding(horizontal = 8.dp, vertical = 6.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    Where(v.hue(m.id))
                    T(if (m.self) "this phone" else m.name, label = true, color = if (on) p.fg else p.mute, lines = 1)
                }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    T(if (!up) (if (m.self) "starting…" else "offline") else if (!yes) "update" else "${m.running.size} running", Modifier.weight(1f), size = 12.sp, color = ink, lines = 1)
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
    // Dismissed, an update waits for the next release to offer itself again.
    val prefs = remember { c.context.getSharedPreferences("orb", android.content.Context.MODE_PRIVATE) }
    var skipped by remember { mutableStateOf(prefs.getString("skip", "")) }
    val next = rt.view.state.latest.takeIf { it.isNotEmpty() && it != skipped && Release.newer(it, rt.version) }
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
        if (next != null && linux.state.isEmpty()) Box(Modifier.press { prefs.edit().putString("skip", next).apply(); skipped = next }.padding(start = 10.dp, top = 4.dp, bottom = 4.dp)) { T("×", size = 18.sp, color = p.meta) }
    }
}

/** One line per session: its title, then where it lives and its condition in words — live, asking,
 *  or how long ago. No rules between rows: the space and the weight of the open one are enough. */
@Composable
fun SessionRow(title: String, meta: String, age: String, live: Boolean, asks: Boolean, current: Boolean = false, modifier: Modifier = Modifier, hue: Int = 0, rename: (() -> Unit)? = null, open: () -> Unit) =
    Row(modifier.fillMaxWidth().press(onLong = rename, onClick = open).padding(vertical = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        Where(hue, live, asks)
        T(title, Modifier.weight(1f), size = 15.sp, weight = if (current || live) Strong else Regular, lines = 1)
        T(listOf(meta, if (asks) "asks" else if (live) "working" else age).filter(String::isNotEmpty).joinToString(" · "), size = 13.sp, color = if (asks) Ink.Rupture else p.meta, lines = 1)
    }

/** Where a session runs, as a square: the ink for this phone, a paired device's own hue, red while
 *  it asks you something; it pulses while it works. Tabs, rows and the prompt box share it. */
@Composable
fun Where(hue: Int, live: Boolean = false, asks: Boolean = false, phone: Color = p.mute) =
    Dot(if (asks) Ink.Rupture else hue(hue, phone), 7.dp, pulse = live, square = true)
