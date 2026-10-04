package tech.ordalie.orb.ui

import androidx.activity.compose.BackHandler
import androidx.compose.animation.*
import androidx.compose.animation.core.*
import androidx.compose.foundation.*
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.foundation.text.*
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.SnapshotStateList
import androidx.compose.ui.*
import androidx.compose.ui.focus.*
import androidx.compose.ui.graphics.*
import androidx.compose.ui.platform.*
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.*
import androidx.compose.ui.unit.*
import tech.ordalie.orb.Runtime
import tech.ordalie.orb.core.Session

sealed interface Screen {
    data object Home : Screen
    data class Chat(val session: Session) : Screen
    data object Bridge : Screen
    data object Invite : Screen
    data class Join(val text: String = "") : Screen
    /** The providers of a machine on Bridge, this phone included. */
    data class Providers(val peer: String) : Screen
    data class Vendor(val id: String, val peer: String) : Screen
    data object Plugins : Screen
    data class Device(val peer: String) : Screen
    data class Folder(val peer: String, val cwd: String) : Screen
    /** The terminal where [on] runs: this phone's Linux, or its machine over Bridge. */
    data class Terminal(val on: Session? = null) : Screen
}

class Nav {
    val stack = mutableStateListOf<Screen>(Screen.Home)
    var forward by mutableStateOf(true) // which way the last move went, so screens slide the right way
    fun go(s: Screen) { forward = true; stack += s }
    fun back() { if (stack.size > 1) { forward = false; stack.removeAt(stack.lastIndex) } }
    fun home() { forward = false; while (stack.size > 1) stack.removeAt(stack.lastIndex) }
    /** Shows a session in place of the one on screen: tabs switch, they do not stack. */
    fun show(s: Session, forward: Boolean = true) {
        this.forward = forward
        if (stack.last() is Screen.Chat) stack[stack.lastIndex] = Screen.Chat(s) else stack += Screen.Chat(s)
    }
}

/** A name to change, rising from the bottom with the current one ready to edit. */
class Rename(val title: String, val delete: (() -> Unit)? = null, val apply: (String) -> Unit)

/** A choice list rising from the bottom — places, menus, confirmations all use it. */
class Picker(val title: String, val options: List<String>, val selected: String = "", val pick: (String) -> Unit)

@Composable
fun App(rt: Runtime, cites: SnapshotStateList<String>, onCite: () -> Unit, shared: MutableState<String?>) {
    val nav = remember { Nav() }
    var picker by remember { mutableStateOf<Picker?>(null) }
    var deck by remember { mutableStateOf<Session?>(null) }
    var renaming by remember { mutableStateOf<Rename?>(null) }
    // Shared text is either a Bridge invitation or something to cite.
    LaunchedEffect(shared.value) {
        val text = shared.value ?: return@LaunchedEffect
        shared.value = null
        if (text.contains("invitation_id") || text.contains(tech.ordalie.orb.core.Bridge.PREFIX)) nav.go(Screen.Join(text))
        else java.io.File(rt.orb.cwd, "cites/shared-${System.currentTimeMillis() / 1000}.txt").apply { parentFile?.mkdirs(); writeText(text); cites += "cites/$name" }
    }
    BackHandler(nav.stack.size > 1 || picker != null || deck != null || renaming != null) {
        if (renaming != null) renaming = null else if (picker != null) picker = null else if (deck != null) deck = null else nav.back()
    }
    val ctx = Ctx(rt, nav, cites, onCite, LocalContext.current, { deck = it }, { renaming = it }) { picker = it }
    Box(Modifier.fillMaxSize().background(p.bg)) {
        val top = nav.stack.last()
        Column(Modifier.fillMaxSize()) {
            // Home and every conversation share one bar: switching sessions never leaves it.
            AnimatedVisibility(top is Screen.Home || top is Screen.Chat, enter = fadeIn() + expandVertically(), exit = fadeOut() + shrinkVertically()) {
                TopBar(ctx, (top as? Screen.Chat)?.session)
            }
            AnimatedContent(top, Modifier.weight(1f), transitionSpec = {
                val d = if (nav.forward) 1 else -1
                (slideInHorizontally(tween(300, easing = FastOutSlowInEasing)) { d * it / 4 } + fadeIn(tween(200, 40))) togetherWith
                    (slideOutHorizontally(tween(300, easing = FastOutSlowInEasing)) { -d * it / 8 } + fadeOut(tween(120)))
            }, label = "screen") { s ->
                val boxed = s is Screen.Home || s is Screen.Chat // the bar above and the prompt box below handle the edges
                Column(Modifier.fillMaxSize().then(if (boxed) Modifier else Modifier.statusBarsPadding().navigationBarsPadding()).imePadding()) {
                    when (s) {
                        Screen.Home -> Home(ctx)
                        is Screen.Chat -> Chat(ctx, s.session)
                        Screen.Bridge -> BridgeScreen(ctx)
                        Screen.Invite -> InviteScreen(ctx)
                        is Screen.Join -> JoinScreen(ctx, s.text)
                        is Screen.Providers -> ProvidersScreen(ctx, s.peer)
                        is Screen.Vendor -> VendorScreen(ctx, s.id, s.peer)
                        Screen.Plugins -> PluginsScreen(ctx)
                        is Screen.Device -> DeviceScreen(ctx, s.peer)
                        is Screen.Folder -> FolderScreen(ctx, s.peer, s.cwd)
                        is Screen.Terminal -> TerminalScreen(ctx, s.on)
                    }
                }
            }
        }
        // Interrupts belong to their owner and stop the world wherever you are.
        val asking = (top as? Screen.Chat)?.session ?: rt.sessions.firstOrNull { it.ask != null }
        Overlay(asking?.ask, rise = true) { Interrupt(it) { v -> asking?.answer(v) } }
        Overlay(rt.bridge.claim, rise = true) { PairRequest(it, ctx) }
        Overlay(picker) { pk -> PickerSheet(pk) { picker = null } }
        Overlay(deck) { s -> ModelSheet(s, ctx) { deck = null } }
        Overlay(renaming) { r -> RenameSheet(r) { renaming = null } }
    }
}

/**
 * The wordmark, which is Home; then, once more than one session is open — this phone's and those
 * followed on paired devices — a tab for each, else the open conversation's name; the menu. A tab's
 * square says where it runs, as in the prompt box: red when it asks, pulsing while it works.
 */
@Composable
private fun TopBar(c: Ctx, open: Session?) = Column(Modifier.statusBarsPadding()) {
    val tabs = c.rt.sessions
    Row(Modifier.fillMaxWidth().height(46.dp).padding(start = 10.dp, end = 2.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.press { c.nav.home() }.padding(horizontal = 6.dp, vertical = 8.dp)) { Stretch("ORB", 18.dp, if (open == null) p.fg else p.meta) }
        Row(Modifier.weight(1f).fillMaxHeight().horizontalScroll(rememberScrollState()).padding(start = 10.dp)) {
            if (tabs.size > 1) tabs.forEach { s ->
                Tab(s, s == open, onLong = { c.tabMenu(s) }) { c.nav.show(s, forward = open == null || tabs.indexOf(s) > tabs.indexOf(open)) }
            } else if (open != null) Box(Modifier.fillMaxHeight().press(onLong = { c.tabMenu(open) }) {}.padding(horizontal = 8.dp), contentAlignment = Alignment.CenterStart) {
                T(open.title.ifEmpty { "New session" }, size = 15.sp, weight = Strong, lines = 1)
            }
        }
        MenuMark(c::menu)
    }
    Rule()
}

@Composable
private fun Tab(s: Session, on: Boolean, onLong: () -> Unit, open: () -> Unit) =
    Column(Modifier.fillMaxHeight().width(IntrinsicSize.Max).press(onLong = onLong, onClick = open).padding(horizontal = 8.dp)) {
        Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
            Where(s.remote, s.busy, s.ask != null, if (on) p.fg else p.mute)
            Spacer(Modifier.width(7.dp))
            T(s.title.ifEmpty { "new session" }, Modifier.widthIn(max = 150.dp), size = 14.sp, weight = if (on) Strong else Regular, color = if (on) p.fg else if (s.online) p.mute else p.meta, lines = 1)
        }
        Box(Modifier.fillMaxWidth().height(2.dp).background(if (on) p.fg else Color.Transparent))
    }

/** Keeps the last value on screen while it animates away; interrupts rise, sheets fade their scrim. */
@Composable
private fun <V : Any> Overlay(value: V?, rise: Boolean = false, content: @Composable AnimatedVisibilityScope.(V) -> Unit) {
    var last by remember { mutableStateOf(value) }
    if (value != null) last = value
    AnimatedVisibility(value != null,
        enter = if (rise) slideInVertically(tween(360, easing = FastOutSlowInEasing)) { it / 3 } + fadeIn(tween(200)) else fadeIn(tween(180)),
        exit = if (rise) slideOutVertically(tween(260)) { it / 4 } + fadeOut(tween(200)) else fadeOut(tween(220))) { last?.let { content(it) } }
}

@Composable
fun AnimatedVisibilityScope.PickerSheet(pk: Picker, dismiss: () -> Unit) = Sheet(dismiss) {
    // A choice needs the whole sheet: the keyboard goes away when one opens.
    val keyboard = LocalSoftwareKeyboardController.current
    LaunchedEffect(Unit) { keyboard?.hide() }
    T(pk.title, Modifier.padding(start = Margin, end = Margin, top = 18.dp, bottom = 6.dp), label = true, color = p.meta, lines = 1)
    LazyColumn(Modifier.heightIn(max = 440.dp), state = rememberLazyListState((pk.options.indexOf(pk.selected) - 2).coerceAtLeast(0))) {
        itemsIndexed(pk.options) { n, o ->
            val sel = o == pk.selected
            Row(Modifier.animateEnterExit(enter = fadeIn(tween(240, 60 + n * 25)) + slideInVertically(tween(300, 40 + n * 25)) { it / 2 }).fillMaxWidth().press { pk.pick(o); dismiss() }.padding(horizontal = Margin, vertical = 12.dp), verticalAlignment = Alignment.CenterVertically) {
                T(o, Modifier.weight(1f), size = 16.sp, weight = if (sel) Strong else Regular, lines = 1)
                if (sel) Dot(p.fg)
            }
        }
    }
    Spacer(Modifier.height(10.dp))
}

/** A long-pressed row's name, in a field over the list; done saves it, outside or back leaves it. */
@Composable
fun AnimatedVisibilityScope.RenameSheet(r: Rename, dismiss: () -> Unit) = Sheet(dismiss) {
    var value by remember { mutableStateOf(TextFieldValue(r.title, TextRange(0, r.title.length))) }
    val focus = remember { FocusRequester() }
    LaunchedEffect(Unit) { focus.requestFocus() }
    fun done() { value.text.trim().takeIf { it.isNotEmpty() && it != r.title }?.let(r.apply); dismiss() }
    T("rename", Modifier.padding(start = Margin, top = 18.dp), label = true, color = p.meta)
    Row(Modifier.padding(start = Margin, end = 12.dp, top = 10.dp, bottom = 16.dp), verticalAlignment = Alignment.CenterVertically) {
        BasicTextField(value, { value = it }, Modifier.weight(1f).focusRequester(focus), textStyle = type(18.sp, p.fg, Medium), singleLine = true, cursorBrush = SolidColor(p.fg),
            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done), keyboardActions = KeyboardActions(onDone = { done() }))
        r.delete?.let { Spacer(Modifier.width(8.dp)); Btn("delete", color = Ink.Rupture) { it(); dismiss() } }
        Spacer(Modifier.width(8.dp)); Btn("save", inverted = true) { done() }
    }
}
