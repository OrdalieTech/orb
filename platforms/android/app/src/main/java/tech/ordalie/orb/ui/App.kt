package tech.ordalie.orb.ui

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.widget.Toast
import androidx.compose.ui.platform.LocalContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import tech.ordalie.orb.core.Command
import tech.ordalie.orb.core.Release
import androidx.activity.compose.BackHandler
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.horizontalScroll
import androidx.compose.runtime.saveable.rememberSaveable
import tech.ordalie.orb.core.Thread
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.runtime.rememberCoroutineScope
import tech.ordalie.orb.core.Peer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.ime
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.AnimatedVisibilityScope
import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.spring
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.animation.slideOutVertically
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.runtime.snapshots.SnapshotStateList
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import tech.ordalie.orb.Runtime
import tech.ordalie.orb.core.Session
import tech.ordalie.orb.core.Tool

sealed interface Screen {
    data object Home : Screen
    data class Chat(val session: Session) : Screen
    data object Bridge : Screen
    data object Invite : Screen
    data class Join(val text: String = "", val auto: Boolean = false) : Screen
    data object Providers : Screen
    data class Vendor(val id: String) : Screen
    data object Plugins : Screen
    data object Provider : Screen
    data class Device(val peer: String) : Screen
    data class Folder(val peer: String, val cwd: String) : Screen
}

class Nav {
    val stack = mutableStateListOf<Screen>(Screen.Home)
    var forward by mutableStateOf(true) // which way the last move went, so screens slide the right way
    fun go(s: Screen) { forward = true; stack += s }
    fun back() { if (stack.size > 1) { forward = false; stack.removeAt(stack.lastIndex) } }
}

/** A choice list rising from the bottom — models, instances, the menu all use it. */
class Picker(val title: String, val options: List<String>, val selected: String = "", val pick: (String) -> Unit)

@Composable
fun App(rt: Runtime, cites: SnapshotStateList<String>, onCite: () -> Unit, shared: MutableState<String?>) {
    val nav = remember { Nav() }
    var picker by remember { mutableStateOf<Picker?>(null) }
    var deck by remember { mutableStateOf<Session?>(null) }
    // Shared text is either a Bridge invitation or something to cite.
    LaunchedEffect(shared.value) {
        val text = shared.value ?: return@LaunchedEffect
        shared.value = null
        if (text.contains("invitation_id") || text.contains(tech.ordalie.orb.core.Bridge.PREFIX)) nav.go(Screen.Join(text))
        else java.io.File(rt.orb.workspace, "cites/shared-${System.currentTimeMillis() / 1000}.txt").apply { parentFile?.mkdirs(); writeText(text); cites += "cites/$name" }
    }
    BackHandler(nav.stack.size > 1 || picker != null || deck != null) { if (picker != null) picker = null else if (deck != null) deck = null else nav.back() }
    val ctx = Ctx(rt, nav, cites, onCite, LocalContext.current, { deck = it }) { picker = it }
    Box(Modifier.fillMaxSize().background(p.bg)) {
        AnimatedContent(nav.stack.last(), transitionSpec = {
            val d = if (nav.forward) 1 else -1
            (slideInHorizontally(tween(320, easing = FastOutSlowInEasing)) { d * it / 3 } + fadeIn(tween(220, 60))) togetherWith
                (slideOutHorizontally(tween(320, easing = FastOutSlowInEasing)) { -d * it / 6 } + fadeOut(tween(140)))
        }, label = "screen") { s ->
            val boxed = s is Screen.Home || s is Screen.Chat // the prompt box handles the bottom edge itself
            Column(Modifier.fillMaxSize().statusBarsPadding().then(if (boxed) Modifier else Modifier.navigationBarsPadding()).imePadding()) {
                when (s) {
                    Screen.Home -> Home(ctx)
                    is Screen.Chat -> Chat(ctx, s.session)
                    Screen.Bridge -> BridgeScreen(ctx)
                    Screen.Invite -> InviteScreen(ctx)
                    is Screen.Join -> JoinScreen(ctx, s.text, s.auto)
                    Screen.Providers -> ProvidersScreen(ctx)
                    is Screen.Vendor -> VendorScreen(ctx, s.id)
                    Screen.Plugins -> PluginsScreen(ctx)
                    Screen.Provider -> ProviderScreen(ctx)
                    is Screen.Device -> DeviceScreen(ctx, s.peer)
                    is Screen.Folder -> FolderScreen(ctx, s.peer, s.cwd)
                }
            }
        }
        // Interrupts belong to their owner and stop the world wherever you are.
        val asking = (nav.stack.last() as? Screen.Chat)?.session ?: rt.local?.takeIf { it.ask != null }
        Rising(asking?.ask) { Interrupt(it) { v -> asking?.answer(v) } }
        Rising(rt.bridge.claim) { PairRequest(it.optString("claimant"), ctx) }
        Sheet(picker) { pk -> PickerSheet(pk) { picker = null } }
        Sheet(deck) { s -> ModelDeck(s, ctx) { deck = null } }
    }
}

/** What every screen needs, passed as one value. */
class Ctx(val rt: Runtime, val nav: Nav, val cites: SnapshotStateList<String>, val onCite: () -> Unit, val context: Context, val deck: (Session) -> Unit, val pick: (Picker) -> Unit) {
    /** The slash palette: the app's own commands, then whatever the core offers (extensions, templates, skills). */
    fun palette(s: Session?): List<Command> = BUILTINS.filter { s?.remote != true || it.name in REMOTE } + s?.commands.orEmpty()

    /** Runs a built-in command; false when the text is a prompt for the core. */
    fun command(s: Session?, text: String): Boolean {
        if (!text.startsWith("/")) return false
        val name = text.drop(1).substringBefore(' ')
        val arg = text.substringAfter(' ', "").trim()
        when (name) {
            "new" -> { s?.newSession(arg.ifEmpty { null }); s?.let { if (nav.stack.last() !is Screen.Chat) nav.go(Screen.Chat(it)) } }
            "compact" -> s?.compact(arg)
            "name" -> if (arg.isNotEmpty()) s?.rename(arg) else return false
            "model", "reasoning" -> chooseModel(s)
            "copy" -> rt.scope.launch {
                val text = s?.lastText() ?: return@launch
                context.getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText("orb", text))
                Toast.makeText(context, "copied the last answer", Toast.LENGTH_SHORT).show()
            }
            "sessions", "resume" -> { while (nav.stack.size > 1) nav.back() }
            "bridge", "pair" -> nav.go(Screen.Bridge)
            "login", "providers" -> nav.go(Screen.Providers)
            "plugins" -> nav.go(Screen.Plugins)
            "text" -> rt.orb.chatSize = when (arg) { "small" -> 13f; "large" -> 17f; "" , "medium" -> 15f; else -> arg.toFloatOrNull() ?: return false }
            else -> return false
        }
        return true
    }

    /** Where the next message goes: this phone, an Orb running on a device, or a folder of a device. */
    fun chooseWhere(then: (Session) -> Unit) {
        val running = rt.bridge.peers.flatMap { p -> p.instances.map { i -> p to i } }
        val hosts = rt.bridge.peers.filter { rt.bridge.threads.containsKey(it.id) }
        val labels = mutableListOf("this phone")
        running.forEach { (p, i) ->
            val what = rt.opened(i.id)?.title?.ifEmpty { null } ?: i.title.ifEmpty { null }
                ?: rt.bridge.threads[p.id]?.firstOrNull { it.id == i.session }?.title ?: "new thread"
            val label = listOfNotNull(p.name, i.cwd.substringAfterLast('/').ifEmpty { null }, what).joinToString(" · ")
            labels += generateSequence(label) { "$it ·" }.first { it !in labels } // two Orbs may share a folder
        }
        hosts.forEach { labels += "a folder on ${it.name}…" }
        pick(Picker("run on", labels) { choice ->
            val at = labels.indexOf(choice)
            when {
                at == 0 -> rt.local?.let(then)
                at <= running.size -> then(rt.open(running[at - 1].second))
                else -> nav.go(Screen.Device(hosts[at - 1 - running.size].id))
            }
        })
    }
    fun chooseModel(s: Session?) {
        val models = s?.models().orEmpty()
        if (models.isEmpty()) nav.go(Screen.Providers) else deck(s!!)
    }
    /** Opens a thread where it lives: its running Orb, or Orb started on it again. */
    fun openThread(peer: Peer, t: Thread) {
        peer.instances.firstOrNull { it.session == t.id }?.let { nav.go(Screen.Chat(rt.open(it))) } ?: start(peer, session = t.id)
    }

    /** Starts Orb on a device, in a folder or on a thread, and opens it; [Runtime.launching] says how it goes. */
    fun start(peer: Peer, cwd: String? = null, session: String? = null) = rt.scope.launch {
        rt.launching = "starting Orb on ${peer.name}…"
        rt.bridge.launch(peer.id, cwd, session).onSuccess { rt.launching = ""; nav.go(Screen.Chat(rt.open(it))) }.onFailure { rt.launching = it.message.orEmpty() }
    }

    fun menu() = pick(Picker("orb", listOf("providers", "bridge", "plugins")) { nav.go(when (it) { "bridge" -> Screen.Bridge; "plugins" -> Screen.Plugins; else -> Screen.Providers }) })
}

/** Keeps the last value on screen while it animates away. */
@Composable
private fun <V : Any> Rising(value: V?, content: @Composable (V) -> Unit) {
    var last by remember { mutableStateOf(value) }
    if (value != null) last = value
    AnimatedVisibility(value != null, enter = slideInVertically(tween(360, easing = FastOutSlowInEasing)) { it / 3 } + fadeIn(tween(200)), exit = slideOutVertically(tween(260)) { it / 4 } + fadeOut(tween(200))) {
        last?.let { content(it) }
    }
}

/** A bottom sheet: the scrim fades, the sheet rises and sinks. */
@Composable
private fun <V : Any> Sheet(value: V?, content: @Composable AnimatedVisibilityScope.(V) -> Unit) {
    var last by remember { mutableStateOf(value) }
    if (value != null) last = value
    AnimatedVisibility(value != null, enter = fadeIn(tween(180)), exit = fadeOut(tween(220))) { last?.let { content(it) } }
}

@Composable
fun AnimatedVisibilityScope.PickerSheet(pk: Picker, dismiss: () -> Unit) = Box(Modifier.fillMaxSize().background(Color(0x66000000)).press(onClick = dismiss), contentAlignment = Alignment.BottomCenter) {
    // A choice needs the whole sheet: the keyboard goes away when one opens.
    val keyboard = LocalSoftwareKeyboardController.current
    LaunchedEffect(Unit) { keyboard?.hide() }
    Column(Modifier.animateEnterExit(enter = slideInVertically(spring(dampingRatio = 0.86f, stiffness = 420f)) { it }, exit = slideOutVertically(tween(220)) { it }).fillMaxWidth().padding(10.dp).clip(RoundedCornerShape(Radius.Card)).background(p.bg).border(1.dp, p.fg, RoundedCornerShape(Radius.Card)).navigationBarsPadding().press {}) {
        Box(Modifier.fillMaxWidth().padding(top = 10.dp), contentAlignment = Alignment.Center) { Box(Modifier.width(36.dp).height(3.dp).clip(CircleShape).background(p.rule)) }
        T(pk.title, Modifier.padding(start = Margin, top = 14.dp, bottom = 8.dp), label = true, color = p.meta)
        LazyColumn(Modifier.heightIn(max = 440.dp), state = rememberLazyListState((pk.options.indexOf(pk.selected) - 2).coerceAtLeast(0))) {
            itemsIndexed(pk.options) { n, o ->
                val sel = o == pk.selected
                Row(Modifier.animateEnterExit(enter = fadeIn(tween(240, 60 + n * 25)) + slideInVertically(tween(300, 40 + n * 25)) { it / 2 }).fillMaxWidth().press { pk.pick(o); dismiss() }.padding(horizontal = Margin, vertical = 15.dp), verticalAlignment = Alignment.CenterVertically) {
                    T(o, Modifier.weight(1f), size = 17.sp, bold = sel, lines = 1)
                    if (sel) Dot(p.fg)
                }
                Rule(Modifier.padding(horizontal = Margin))
            }
        }
        Spacer(Modifier.height(12.dp))
    }
}

@Composable
fun ColumnScope.Home(c: Ctx) {
    val rt = c.rt
    val local = rt.local
    val peers = rt.bridge.peers
    // The list follows the conversation: it refreshes when Home shows and whenever a turn ends.
    LaunchedEffect(local?.busy, local?.id, peers.map { it.id to it.connected }) { if (local?.busy != true) rt.reload() }
    // Other devices work too: their threads refresh while Home is on screen.
    LaunchedEffect(Unit) { while (true) { delay(20_000); rt.reload() } }
    Header("Orb", sub = rt.orb.device + " · " + if (rt.bridge.up) "bridge on" else "bridge starting") { MenuMark(c::menu) }
    // A newer Orb is one tap away: the app downloads its release and hands it to Android's installer.
    rt.latest?.takeIf { Release.newer(it, rt.version) }?.let { next ->
        val scope = rememberCoroutineScope()
        var state by remember { mutableStateOf("") }
        Row(Modifier.fillMaxWidth().press(enabled = state.isEmpty() || state.startsWith("could")) {
            state = "downloading $next…"
            scope.launch { state = runCatching { Release.install(c.context, next) }.fold({ "" }, { "could not update · " + it.message }) }
        }.padding(start = Margin, end = Margin, bottom = 12.dp), verticalAlignment = Alignment.CenterVertically) {
            Dot(Ink.Rupture, pulse = state.startsWith("downloading")); Spacer(Modifier.width(10.dp))
            T(state.ifEmpty { "Orb $next is available · update" }, Modifier.weight(1f), size = 13.sp, color = if (state.startsWith("could")) Ink.Rupture else p.fg, lines = 2)
        }
    }
    // Readouts, one line: what is running, who is reachable, what it costs, how full the context is.
    // They fold away while the keyboard is up, so the prompt box keeps its room.
    val typing = WindowInsets.ime.getBottom(LocalDensity.current) > 0
    AnimatedVisibility(!typing) { Row(Modifier.padding(start = Margin, end = Margin, bottom = 12.dp).fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(26.dp)) {
        Readout("live", rt.sessions.count { it.busy }.toString().padStart(2, '0'), if (rt.sessions.any { it.ask != null }) Ink.Rupture else p.fg)
        Readout("peers", "${peers.count { it.connected }}/${peers.size}", if (rt.acting) Ink.Blue else p.fg)
        Readout("cost", "$" + "%.2f".format(java.util.Locale.US, local?.cost ?: 0.0))
        Spacer(Modifier.weight(1f))
        Ring(local?.context ?: 0f, "${((local?.context ?: 0f) * 100).toInt()}%", size = 50.dp)
    } }
    Rule(color = p.fg.copy(alpha = 0.5f))
    // Every thread on every device in one list, newest first: where it lives is a detail of the row.
    val now = System.currentTimeMillis()
    val entries = buildList {
        if (local != null && rt.history.none { it.id == local.id } && local.transcript.items.isNotEmpty())
            add(Entry("current", local.title.ifEmpty { "this conversation" }, "phone", now, local.busy, local.ask != null, current = true) { c.nav.go(Screen.Chat(local)) })
        rt.history.forEach { past ->
            val on = local != null && past.id == local.id
            add(Entry("p:" + past.id, past.title, "phone · ${past.messages} messages", past.modified, on && local!!.busy, on && local!!.ask != null, current = on) {
                local?.let { l -> l.switchTo(past.id); c.nav.go(Screen.Chat(l)) }
            })
        }
        peers.forEach { peer ->
            val threads = rt.bridge.threads[peer.id].orEmpty()
            threads.forEach { t ->
                val i = peer.instances.firstOrNull { it.session == t.id }
                val s = i?.let { rt.opened(it.id) }
                add(Entry("t:${peer.id}:${t.id}", t.title, listOf(peer.name, t.cwd.substringAfterLast('/'), "${t.messages} messages").joinToString(" · "),
                    if (i != null && (s?.busy ?: i.busy)) now else t.modified, s?.busy ?: i?.busy == true, s?.ask != null, remote = true, device = peer.id) { c.openThread(peer, t) })
            }
            // Open instances whose thread is not listed: unsaved yet, or a device that lists no threads.
            peer.instances.filter { i -> threads.none { it.id == i.session } }.forEach { i ->
                val s = rt.opened(i.id)
                val folder = i.cwd.substringAfterLast('/')
                add(Entry("i:" + i.id, s?.title?.ifEmpty { null } ?: i.title.ifEmpty { null } ?: folder.ifEmpty { i.alias }, listOf(peer.name, folder).filter(String::isNotEmpty).joinToString(" · "),
                    now, s?.busy ?: i.busy, s?.ask != null, remote = true, age = "open", device = peer.id) { c.nav.go(Screen.Chat(rt.open(i))) })
            }
        }
    }.sortedByDescending { it.modified }
    // The header stays put: starting a thread never depends on how far the list is scrolled.
    Row(Modifier.fillMaxWidth().padding(start = Margin, end = Margin, top = 18.dp, bottom = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        T("sessions", Modifier.weight(1f), label = true)
        Box(Modifier.press {
            val hosts = peers.filter { rt.bridge.threads.containsKey(it.id) }
            fun here() { local?.let { l -> if (l.transcript.items.isNotEmpty()) l.newSession(); c.nav.go(Screen.Chat(l)) } }
            if (hosts.isEmpty()) here() else c.pick(Picker("new thread on", listOf("this phone") + hosts.map { it.name }) { choice ->
                hosts.firstOrNull { it.name == choice }?.let { c.nav.go(Screen.Device(it.id)) } ?: here()
            })
        }.padding(4.dp)) { T("+ new", label = true) }
    }
    // Devices narrow the list: all of them, this phone, or one machine (whose folders open from here).
    var device by rememberSaveable { mutableStateOf("") }
    val devices = listOf("phone") + peers.filter { p -> p.instances.isNotEmpty() || rt.bridge.threads.containsKey(p.id) }.map { it.id }
    if (devices.size > 1) Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = Margin, vertical = 6.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        (listOf("") + devices).forEach { d ->
            val name = when (d) { "" -> "all"; "phone" -> "this phone"; else -> peers.firstOrNull { it.id == d }?.name ?: d }
            Box(Modifier.press { device = d }) { Chip(name, if (device == d) ChipKind.Inverted else ChipKind.Outline, caps = false) }
        }
    }
    val shown = if (device.isEmpty()) entries else entries.filter { it.device == device }
    val browsable = peers.firstOrNull { it.id == device }?.takeIf { rt.bridge.threads.containsKey(it.id) }
    // Newer threads arriving from other devices land above; a reader at the top stays at the top.
    val list = rememberLazyListState()
    var browsed by remember { mutableStateOf(false) } // the reader scrolled down on purpose
    LaunchedEffect(list) { snapshotFlow { list.isScrollInProgress }.collect { if (!it) browsed = list.firstVisibleItemIndex > 0 } }
    LaunchedEffect(shown.firstOrNull()?.key) { if (!browsed) list.scrollToItem(0) }
    LazyColumn(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Margin), state = list) {
        item(key = "status") { Launching(c) }
        browsable?.let { peer ->
            item(key = "folders") {
                Row(Modifier.fillMaxWidth().press { c.nav.go(Screen.Device(peer.id)) }.padding(vertical = 14.dp), verticalAlignment = Alignment.CenterVertically) {
                    T("folders on ${peer.name}", Modifier.weight(1f), bold = true); T("›", size = 20.sp, color = p.mute)
                }
                Rule()
            }
        }
        items(shown, key = { it.key }) { e ->
            SessionRow(e.title, e.meta, e.age ?: ago(e.modified), e.live, e.asks, e.current, Modifier.animateItem(), e.remote, e.open)
        }
        if (peers.isEmpty()) item(key = "pair") {
            Row(Modifier.fillMaxWidth().press { c.nav.go(Screen.Bridge) }.padding(vertical = 22.dp)) { T("no paired devices", Modifier.weight(1f), color = p.mute); T("pair ›") }
        }
        item { Spacer(Modifier.height(12.dp)) }
    }
    PromptBox(local, c.cites, c.onCite, { c.chooseWhere { c.nav.go(Screen.Chat(it)) } }, { c.chooseModel(local) }, c.palette(local), placeholder = "new session") { text ->
        local ?: return@PromptBox
        if (c.command(local, text)) return@PromptBox
        if (local.transcript.items.isNotEmpty()) local.newSession(text) else local.prompt(text)
        c.nav.go(Screen.Chat(local))
    }
}

@Composable
private fun Readout(label: String, value: String, color: Color = p.fg) = Column {
    Stretch(value, 30.dp, color, squeeze = 0.86f)
    T(label, size = Size.Label, color = p.meta)
}

fun ago(ms: Long): String {
    val m = (System.currentTimeMillis() - ms) / 60_000L
    return when { m < 1 -> "now"; m < 60 -> "${m}m"; m < 1440 -> "${m / 60}h"; else -> "${m / 1440}d" }
}

/** One row of the merged list: a thread on some device, however it opens. */
private class Entry(
    val key: String, val title: String, val meta: String, val modified: Long, val live: Boolean, val asks: Boolean,
    val current: Boolean = false, val remote: Boolean = false, val age: String? = null, val device: String = "phone", val open: () -> Unit,
)

/** A session row states its condition in words — live, asking, or how long ago — never as a switch. */
@Composable
fun SessionRow(title: String, meta: String, age: String, live: Boolean, asks: Boolean, current: Boolean = false, modifier: Modifier = Modifier, remote: Boolean = false, open: () -> Unit) = Column(modifier) {
    Row(Modifier.fillMaxWidth().press(onClick = open).padding(vertical = 13.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        Box(Modifier.width(3.dp).height(34.dp).background(if (current) p.fg else Color.Transparent))
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(3.dp)) {
            T(title, size = 16.sp, bold = current || live, lines = 1)
            T(meta, size = Size.Label, color = p.meta, lines = 1)
        }
        when {
            asks -> Chip("ask", ChipKind.Rupture)
            live -> Row(verticalAlignment = Alignment.CenterVertically) { Dot(if (remote) Ink.Blue else Ink.Rupture, pulse = true); Spacer(Modifier.width(6.dp)); T("live", label = true) }
            else -> T(age, size = Size.Label, color = p.meta)
        }
    }
    Rule()
}

/** The app's commands, named like the TUI's. Those in [NOW] run on tap; the rest take an argument. */
val BUILTINS = listOf(
    Command("new", "fresh session · optional first message"), Command("compact", "summarize to free context · optional focus"),
    Command("name", "name this session"), Command("model", "choose model and reasoning"), Command("reasoning", "reasoning level"),
    Command("copy", "copy the last answer"), Command("sessions", "all sessions"), Command("pair", "Bridge: scan or share a code"),
    Command("text", "chat text size · small, medium, large (or pinch)"), Command("login", "providers and keys"), Command("plugins", "turn plugins on and off"),
)
val NOW = setOf("model", "reasoning", "copy", "sessions", "pair", "login", "plugins")
private val REMOTE = setOf("new", "model", "reasoning", "copy", "sessions", "pair")
