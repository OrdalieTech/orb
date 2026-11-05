@file:OptIn(androidx.compose.animation.ExperimentalSharedTransitionApi::class)

package tech.ordalie.orb.ui

import androidx.activity.compose.BackHandler
import androidx.compose.animation.*
import androidx.compose.animation.core.*
import androidx.compose.foundation.*
import androidx.compose.foundation.gestures.detectDragGesturesAfterLongPress
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.foundation.pager.*
import androidx.compose.foundation.text.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.snapshots.SnapshotStateList
import androidx.compose.ui.*
import androidx.compose.ui.focus.*
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.graphics.*
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.*
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.*
import androidx.compose.ui.unit.*
import kotlin.math.abs
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.first
import tech.ordalie.orb.Runtime
import tech.ordalie.orb.core.Tab

sealed interface Screen {
    /** Home and the open conversations, side by side ([Nav.pager]). */
    data object Home : Screen
    data object Bridge : Screen
    data object Invite : Screen
    data class Join(val text: String = "") : Screen
    /** The providers of a machine on Bridge, this phone included. */
    data class Providers(val peer: String) : Screen
    data class Vendor(val id: String, val peer: String) : Screen
    data class Plugins(val peer: String) : Screen
    data class Device(val peer: String) : Screen
    data class Folder(val peer: String, val cwd: String) : Screen
    /** The terminal where [on] runs: this phone's Linux, or its machine over Bridge. */
    data class Terminal(val on: Tab? = null) : Screen
}

class Nav(private val rt: Runtime, private val scope: CoroutineScope) {
    val stack = mutableStateListOf<Screen>(Screen.Home)
    var forward by mutableStateOf(true) // which way the last move went, so screens slide the right way
    /** Home is the first page, then a page per open conversation, in tab order: a swipe moves between them. */
    val pager = PagerState { 1 + rt.view.state.tabs.size }
    /** The conversation on screen, or the one a swipe is nearest; null on Home. */
    val open: Tab? get() = rt.view.state.tabs.getOrNull(pager.currentPage - 1)
    /** A message the view refused, for the prompt box it came from to take back: its tab ("" for Home) and text. */
    var unsent by mutableStateOf<Pair<String, String>?>(null)
    /** Home's prompt box takes the keyboard when it next shows (the overview's new-session card). */
    var compose by mutableStateOf(false)
    /** The page on screen was opened from the overview, which Back returns to. */
    var fromOverview by mutableStateOf(false)
    /** The conversation just closed, by name, and what opens it again, while its undo shows. */
    var closed by mutableStateOf<Pair<String, () -> Unit>?>(null)
    fun go(s: Screen) { forward = true; stack += s }
    fun back() { if (stack.size > 1) { forward = false; stack.removeAt(stack.lastIndex) } }
    fun home() = show(null)
    /** Slides to a conversation by its tab id, Home for null, closing any screen above them; a tab
     *  just opened slides in once the view lists it. */
    fun show(tab: String?) {
        forward = false
        fromOverview = false
        while (stack.size > 1) stack.removeAt(stack.lastIndex)
        scope.launch {
            val page = if (tab == null) 0 else withTimeoutOrNull(5000) { snapshotFlow { rt.view.state.tabs.indexOfFirst { it.id == tab } }.first { it >= 0 } }?.plus(1) ?: return@launch
            // A page just added is laid out a frame later; scrolled to before, the pager stops short of it.
            withFrameNanos {}
            pager.animateScrollToPage(page)
            if (pager.currentPage != page) pager.scrollToPage(page)
        }
    }
}

/** A name to change, rising from the bottom with the current one ready to edit. */
class Rename(val title: String, val apply: (String) -> Unit)

/** A choice list rising from the bottom — places, menus, confirmations all use it. */
class Picker(val title: String, val options: List<String>, val selected: String = "", val pick: (String) -> Unit)

@Composable
fun App(rt: Runtime, cites: SnapshotStateList<String>, onCite: () -> Unit, shared: MutableState<String?>) {
    val scope = rememberCoroutineScope()
    val nav = remember { Nav(rt, scope) }
    var picker by remember { mutableStateOf<Picker?>(null) }
    var menu by remember { mutableStateOf(false) }
    var deck by remember { mutableStateOf<Tab?>(null) } // the model sheet's conversation, or with no id a machine's default
    var renaming by remember { mutableStateOf<Rename?>(null) }
    var viewing by remember { mutableStateOf<Pair<String, String>?>(null) }
    var overview by rememberSaveable { mutableStateOf(false) } // every conversation at once, in place of the deck
    // Shared text is either a Bridge invitation or something to cite.
    LaunchedEffect(shared.value) {
        val text = shared.value ?: return@LaunchedEffect
        shared.value = null
        if (text.contains("invitation_id") || text.contains("orb-bridge:v1:")) nav.go(Screen.Join(text))
        else java.io.File(rt.orb.cwd, "cites/shared-${System.currentTimeMillis() / 1000}.txt").apply { parentFile?.mkdirs(); writeText(text); cites += "cites/$name" }
    }
    BackHandler(nav.stack.size > 1 || nav.pager.currentPage > 0 || picker != null || menu || deck != null || renaming != null || viewing != null || overview) {
        if (viewing != null) viewing = null else if (renaming != null) renaming = null else if (picker != null) picker = null else if (menu) menu = false else if (deck != null) deck = null
        else if (nav.stack.size > 1) nav.back() else if (overview) overview = false else if (nav.fromOverview) overview = true else nav.home()
    }
    // A notification asked for a conversation: show its tab.
    LaunchedEffect(rt.show) { rt.show?.let { nav.show(it); rt.show = null } }
    // What a new conversation starts on, before one runs: this phone's models and its default,
    // read again once the sheet closes, a screen (Providers) gives way, or another page shows (a
    // conversation's model change becomes the default).
    LaunchedEffect(rt.view.state.up, deck, nav.stack.size, nav.open?.id) {
        if (rt.view.state.up && deck == null) rt.view.readDefault(rt.view.state.self)
    }
    val fresh = rt.view.defaults[rt.view.state.self]
    val ctx = Ctx(rt, nav, cites, onCite, LocalContext.current, fresh, { deck = it }, { renaming = it }, { picker = it }) { ref, tab -> viewing = ref to tab }
    // The view follows the conversation a page shows at full pace, and all of them in the overview.
    LaunchedEffect(nav) {
        snapshotFlow {
            if (nav.stack.last() !is Screen.Home) emptyList() else if (overview) rt.view.state.tabs.map { it.id } else listOfNotNull(nav.open?.id)
        }.collect { rt.view.show(it) }
    }
    Box(Modifier.fillMaxSize().background(p.bg)) {
        val top = nav.stack.last()
        Column(Modifier.fillMaxSize()) {
            // Home and every conversation share one bar: switching sessions never leaves it.
            AnimatedVisibility(top is Screen.Home, enter = fadeIn() + expandVertically(), exit = fadeOut() + shrinkVertically()) { TopBar(ctx, overview, { menu = true }) { overview = !overview } }
            AnimatedContent(top, Modifier.weight(1f), transitionSpec = {
                val d = if (nav.forward) 1 else -1
                (slideInHorizontally(tween(300, easing = FastOutSlowInEasing)) { d * it / 4 } + fadeIn(tween(200, 40))) togetherWith
                    (slideOutHorizontally(tween(300, easing = FastOutSlowInEasing)) { -d * it / 8 } + fadeOut(tween(120)))
            }, label = "screen") { s ->
                // Home's bar above and its prompt boxes below handle the edges.
                Column(Modifier.fillMaxSize().then(if (s is Screen.Home) Modifier else Modifier.statusBarsPadding().navigationBarsPadding()).imePadding()) {
                    when (s) {
                        // The overview and the deck trade places as a zoom: a card grows into its page, a page shrinks into its card.
                        Screen.Home -> SharedTransitionLayout {
                            AnimatedContent(overview, transitionSpec = { fadeIn(tween(220, 60)) togetherWith fadeOut(tween(160)) }, label = "overview") { on ->
                                CompositionLocalProvider(LocalMorph provides Morph(this@SharedTransitionLayout, this)) { if (on) Overview(ctx) { overview = false } else Deck(ctx) }
                            }
                        }
                        Screen.Bridge -> BridgeScreen(ctx)
                        Screen.Invite -> InviteScreen(ctx)
                        is Screen.Join -> JoinScreen(ctx, s.text)
                        is Screen.Providers -> ProvidersScreen(ctx, s.peer)
                        is Screen.Vendor -> VendorScreen(ctx, s.id, s.peer)
                        is Screen.Plugins -> PluginsScreen(ctx, s.peer)
                        is Screen.Device -> DeviceScreen(ctx, s.peer)
                        is Screen.Folder -> FolderScreen(ctx, s.peer, s.cwd)
                        is Screen.Terminal -> TerminalScreen(ctx, s.on)
                    }
                }
            }
        }
        // A conversation just closed can come back for a few seconds, as a mail app's undo.
        LaunchedEffect(nav.closed) { if (nav.closed != null) { delay(8000); nav.closed = null } }
        Overlay(nav.closed) { (name, undo) ->
            Box(Modifier.fillMaxWidth().statusBarsPadding().padding(top = 52.dp), contentAlignment = Alignment.TopCenter) {
                Row(Modifier.clip(Soft).background(p.fg).press { nav.closed = null; undo() }.padding(horizontal = 14.dp, vertical = 9.dp), verticalAlignment = Alignment.CenterVertically) {
                    T("closed · " + name, Modifier.widthIn(max = 220.dp), size = 14.sp, color = p.bg, lines = 1)
                    T("  undo", size = 14.sp, weight = Strong, color = p.bg)
                }
            }
        }
        // Interrupts belong to their owner and stop the world wherever you are.
        val tabs = rt.view.state.tabs
        val asking = nav.open?.takeIf { top is Screen.Home } ?: tabs.firstOrNull { it.ask != null }
        Overlay(asking?.ask, rise = true) { Interrupt(it) { v -> asking?.let { t -> rt.view.send("answer", "tab" to t.id, "value" to v) } } }
        Overlay(rt.view.state.claim, rise = true) { PairRequest(it, ctx) }
        Overlay(picker) { pk -> PickerSheet(pk) { picker = null } }
        Overlay(true.takeIf { menu }) { MenuSheet(ctx, nav.open) { menu = false } }
        Overlay(deck?.let { if (it.id.isEmpty()) it else rt.view.tab(it.id) }) { t -> ModelSheet(t, ctx) { deck = null } }
        Overlay(renaming) { r -> RenameSheet(r) { renaming = null } }
        Overlay(viewing) { (ref, tab) -> Viewer(ref, tab, ctx) { viewing = null } }
    }
}

/** Four squares, the overview: filled while it shows. */
@Composable
private fun GridMark(on: Boolean, onClick: () -> Unit) = Box(Modifier.press(onClick = onClick).padding(10.dp)) {
    val ink = p.fg
    val fill by animateFloatAsState(if (on) 1f else 0f, tween(200), label = "grid")
    Canvas(Modifier.size(13.dp)) {
        val s = size.width * 0.42f
        val line = 1.2.dp.toPx()
        listOf(Offset.Zero, Offset(size.width - s, 0f), Offset(0f, size.height - s), Offset(size.width - s, size.height - s)).forEach { o ->
            drawRect(ink, o + Offset(line / 2, line / 2), androidx.compose.ui.geometry.Size(s - line, s - line), style = Stroke(line))
            drawRect(ink.copy(alpha = fill), o, androidx.compose.ui.geometry.Size(s, s))
        }
    }
}

/** An image full screen, fetched at screen size: pinch to zoom, drag to look around, a tap closes it. */
@Composable
private fun Viewer(ref: String, tab: String, c: Ctx, close: () -> Unit) {
    var zoom by remember { mutableFloatStateOf(1f) }
    var pan by remember { mutableStateOf(Offset.Zero) }
    Box(Modifier.fillMaxSize().background(Color.Black).pointerInput(Unit) {
        detectTransformGestures { _, move, scale, _ -> zoom = (zoom * scale).coerceIn(1f, 8f); pan = if (zoom == 1f) Offset.Zero else pan + move }
    }.press(onClick = close), contentAlignment = Alignment.Center) {
        Picture(ref, 2048, { c.image(tab, ref, it) }, Modifier.fillMaxSize().graphicsLayer { scaleX = zoom; scaleY = zoom; translationX = pan.x; translationY = pan.y })
    }
}

/** Home, then each open conversation, a swipe apart; a hairline shows between two pages as they move. */
@Composable
private fun Deck(c: Ctx) = HorizontalPager(c.nav.pager, Modifier.fillMaxSize().background(p.rule), pageSpacing = 1.dp,
    key = { if (it == 0) "home" else c.rt.view.state.tabs.getOrNull(it - 1)?.id ?: it }) { page ->
    val t = c.rt.view.state.tabs.getOrNull(page - 1)
    Column(Modifier.fillMaxSize().then(if (t != null) Modifier.morph("tab-${t.id}") else Modifier).background(p.bg)) { if (t == null) Home(c) else Chat(c, t) }
}

/**
 * The wordmark, which is Home; a tab for each open session — this phone's and those followed on
 * paired devices; the menu. A tab's square says where it runs, as in the prompt box: red when it
 * asks, pulsing while it works. Its underline follows a swipe.
 */
@Composable
private fun TopBar(c: Ctx, overview: Boolean, menu: () -> Unit, toggle: () -> Unit) = Column(Modifier.statusBarsPadding()) {
    val pager = c.nav.pager
    Row(Modifier.fillMaxWidth().height(46.dp).padding(start = 10.dp, end = 2.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.press { if (overview) toggle(); c.nav.home() }.padding(horizontal = 6.dp, vertical = 8.dp)) { Stretch("ORB", 18.dp, if (pager.currentPage == 0 && !overview) p.fg else p.meta) }
        // A tab held then dragged along the bar takes another place, its neighbours making room as it
        // passes them; held and let go, it opens its menu.
        val ids = c.rt.view.state.tabs.map { it.id }
        var order by remember { mutableStateOf(ids) }
        var dragged by remember { mutableStateOf<String?>(null) }
        var dx by remember { mutableFloatStateOf(0f) }
        val widths = remember { mutableStateMapOf<String, Int>() }
        val haptic = LocalHapticFeedback.current
        LaunchedEffect(ids) { if (dragged == null) order = ids }
        // The tab on screen scrolls into the strip whole, however it came to show: a send, a swipe, a notification.
        val strip = rememberLazyListState()
        val current = c.nav.open?.id
        // Checked again as the tab widens (its × shows once it is on screen).
        LaunchedEffect(current, order) {
            val at = order.indexOf(current).takeIf { it >= 0 } ?: return@LaunchedEffect
            snapshotFlow { strip.layoutInfo.let { i -> if (i.totalItemsCount > at) i.visibleItemsInfo.firstOrNull { it.index == at }?.size ?: 0 else null } }
                .filterNotNull().distinctUntilChanged().collect {
                    val info = strip.layoutInfo
                    val seen = info.visibleItemsInfo.firstOrNull { it.index == at }
                    if (seen == null || seen.offset < info.viewportStartOffset || seen.offset + seen.size > info.viewportEndOffset) strip.animateScrollToItem(at)
                }
        }
        val ground = p.bg
        // Tabs cut by the strip's end fade into it, rather than leaving a stray letter by the icons.
        LazyRow(Modifier.weight(1f).fillMaxHeight().drawWithContent {
            drawContent()
            val fade = 28.dp.toPx()
            if (strip.canScrollForward) drawRect(Brush.horizontalGradient(listOf(Color.Transparent, ground), startX = size.width - fade, endX = size.width))
            if (strip.canScrollBackward) drawRect(Brush.horizontalGradient(listOf(ground, Color.Transparent), startX = 0f, endX = fade))
        }, state = strip, contentPadding = PaddingValues(start = 10.dp)) {
            items(order, key = { it }) { id ->
                val t = c.rt.view.state.tabs.firstOrNull { it.id == id } ?: return@items
                val page = ids.indexOf(id) + 1
                val drag = Modifier.animateItem(fadeInSpec = null, fadeOutSpec = null, placementSpec = if (dragged == id) null else spring(stiffness = Spring.StiffnessMediumLow))
                    .onSizeChanged { widths[id] = it.width }.zIndex(if (dragged == id) 1f else 0f)
                    .graphicsLayer { translationX = if (dragged == id) dx else 0f }
                // Inside the tab's tap, which would otherwise take the touch before a hold is seen.
                val hold = Modifier.pointerInput(id) {
                    var net = 0f // how far the finger went, whatever the swaps: a hold that stays put opens the menu
                    detectDragGesturesAfterLongPress(
                        onDragStart = { dragged = id; dx = 0f; net = 0f; haptic.performHapticFeedback(HapticFeedbackType.LongPress) },
                        onDrag = { change, d ->
                            change.consume()
                            dx += d.x
                            net += d.x
                            val at = order.indexOf(id)
                            val next = order.getOrNull(at + 1)?.let { it to (widths[it] ?: 0) }
                            val prev = order.getOrNull(at - 1)?.let { it to (widths[it] ?: 0) }
                            if (next != null && dx > next.second / 2f) {
                                order = order.toMutableList().apply { removeAt(at); add(at + 1, id) }
                                dx -= next.second
                            } else if (prev != null && dx < -prev.second / 2f) {
                                order = order.toMutableList().apply { removeAt(at); add(at - 1, id) }
                                dx += prev.second
                            }
                        },
                        // Read live, never captured: this gesture outlives compositions, and the order with them.
                        onDragCancel = { order = c.rt.view.state.tabs.map { it.id }; dragged = null; dx = 0f },
                        onDragEnd = {
                            if (abs(net) < viewConfiguration.touchSlop) c.tabMenu(t) else c.v.send("move", "tab" to id, "to" to order.indexOf(id))
                            dragged = null
                            dx = 0f
                        },
                    )
                }
                TabMark(t, c.v.word(t), c.v.hue(t.peer), pager.currentPage == page, { (1f - abs(pager.getOffsetDistanceInPages(page))).coerceIn(0f, 1f) }, drag, hold, { c.close(t) }) { c.nav.show(id) }
            }
        }
        GridMark(overview, toggle)
        MenuMark(menu)
    }
    Rule()
}

/** [near] is how close the pager is to this tab's page, read at draw time so a swipe redraws only the line. */
@Composable
private fun TabMark(s: Tab, word: String, hue: Int, on: Boolean, near: () -> Float, modifier: Modifier, hold: Modifier, close: () -> Unit, open: () -> Unit) {
    Column(modifier.fillMaxHeight().width(IntrinsicSize.Max).press(onClick = open).then(hold).padding(horizontal = 8.dp)) {
        Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
            Where(hue, s.busy, s.ask != null, if (on) p.fg else p.mute)
            Spacer(Modifier.width(7.dp))
            T(s.name, Modifier.widthIn(max = 150.dp), size = 14.sp, weight = if (on || s.unread > 0) Strong else Regular, color = if (on) p.fg else if (s.online) p.mute else p.meta, lines = 1)
            // Answers that came while it was out of sight, until it shows.
            if (s.unread > 0) T("${s.unread}", Modifier.padding(start = 6.dp), size = Size.Label, weight = Strong, color = p.fg)
            // A run stopped short says so until the next one (on screen, its page does); working and asking show in its square.
            else if (word == "stopped" && !on) T(word, Modifier.padding(start = 6.dp), size = Size.Label, color = p.meta)
            // The tab on screen closes from its own ×; any tab from its menu, held down.
            if (on) Box(Modifier.press(onClick = close).padding(start = 8.dp, end = 2.dp, top = 6.dp, bottom = 6.dp)) { T("×", size = 16.sp, color = p.meta) }
        }
        Box(Modifier.fillMaxWidth().height(2.dp).graphicsLayer { alpha = near() }.background(p.fg))
    }
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
    // Closed before its choice runs, so a choice may open the next sheet (a confirmation).
    LazyColumn(Modifier.heightIn(max = 440.dp), state = rememberLazyListState((pk.options.indexOf(pk.selected) - 2).coerceAtLeast(0))) {
        itemsIndexed(pk.options) { n, o ->
            val sel = o == pk.selected
            Row(Modifier.animateEnterExit(enter = fadeIn(tween(240, 60 + n * 25)) + slideInVertically(tween(300, 40 + n * 25)) { it / 2 }).fillMaxWidth().press { dismiss(); pk.pick(o) }.padding(horizontal = Margin, vertical = 12.dp), verticalAlignment = Alignment.CenterVertically) {
                T(o, Modifier.weight(1f), size = 16.sp, weight = if (sel) Strong else Regular, lines = 1)
                if (sel) Dot(p.fg)
            }
        }
    }
    Spacer(Modifier.height(10.dp))
}

/**
 * The menu, sorted by what each thing acts on: the conversation on screen, the machine it runs on
 * (this phone on Home), and the Bridge that links them.
 */
@Composable
fun AnimatedVisibilityScope.MenuSheet(c: Ctx, t: Tab?, dismiss: () -> Unit) = Sheet(dismiss) {
    val v = c.v
    val peer = t?.peer ?: v.state.self
    @Composable fun Section(name: String) = T(name, Modifier.padding(start = Margin, end = Margin, top = 18.dp, bottom = 2.dp), label = true, color = p.meta, lines = 1)
    @Composable fun Item(name: String, meta: String = "", act: () -> Unit) =
        Row(Modifier.fillMaxWidth().press { dismiss(); act() }.padding(horizontal = Margin, vertical = 11.dp), verticalAlignment = Alignment.CenterVertically) {
            T(name, Modifier.weight(1f), size = 16.sp, lines = 1)
            T(meta, Modifier.padding(start = 16.dp).widthIn(max = 220.dp), size = 13.sp, color = p.meta, lines = 1)
        }
    if (t != null) {
        Row(Modifier.padding(start = Margin, end = Margin, top = 20.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            Where(v.hue(t.peer), t.busy, t.ask != null)
            Spacer(Modifier.width(9.dp))
            Column {
                T(t.name, size = 17.sp, weight = Strong, lines = 1)
                T(listOf(t.model.substringAfter('/'), t.where).filter(String::isNotEmpty).joinToString(" · "), size = 13.sp, color = p.meta, lines = 1)
            }
        }
        Item("rename") { c.rename(Rename(t.title) { name -> v.send("rename", "tab" to t.id, "name" to name) }) }
        Item("terminal", tidy(t.cwd)) { c.nav.go(Screen.Terminal(t)) }
        Item("close conversation") { c.close(t) }
        Rule(Modifier.padding(top = 6.dp))
    }
    Section(if (peer == v.state.self) "this phone" else v.machine(peer)?.name ?: "its machine")
    Item("providers") { c.nav.go(Screen.Providers(peer)) }
    Item("plugins") { c.nav.go(Screen.Plugins(peer)) }
    if (t == null) Item("terminal") { c.nav.go(Screen.Terminal()) }
    Rule(Modifier.padding(top = 6.dp))
    Section("bridge")
    Item("devices", v.state.summary) { c.nav.go(Screen.Bridge) }
    Row(Modifier.padding(start = Margin, top = 8.dp, bottom = 14.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(7.dp)) {
        Ordalie(11.dp, p.meta)
        T("orb ${c.rt.version}", size = 12.sp, color = p.meta)
    }
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
        Spacer(Modifier.width(8.dp)); Btn("save", inverted = true) { done() }
    }
}
