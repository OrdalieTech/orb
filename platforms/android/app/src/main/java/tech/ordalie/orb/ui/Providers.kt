package tech.ordalie.orb.ui

import android.content.*
import android.net.Uri
import androidx.browser.customtabs.*
import androidx.compose.animation.*
import androidx.compose.animation.core.tween
import androidx.compose.foundation.*
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.foundation.shape.*
import androidx.compose.foundation.text.*
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.*
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.input.*
import androidx.compose.ui.unit.*
import kotlinx.coroutines.*
import tech.ordalie.orb.MainActivity
import tech.ordalie.orb.core.*

/** The last listing per machine ("" is this phone), so the screen opens full and refreshes in place. */
private val listings = mutableStateMapOf<String, List<Provider>>()

private fun known(peer: String) = listings[peer].orEmpty()

private suspend fun Ctx.reload(peer: String) {
    listings[peer] = rt.bridge.providers(peer)
}

/** Opens a page in a Custom Tab tinted like the app, or the browser when there is none. */
fun Context.browse(url: String, tint: androidx.compose.ui.graphics.Color) = runCatching {
    CustomTabsIntent.Builder().setShowTitle(true).setShareState(CustomTabsIntent.SHARE_STATE_OFF)
        .setDefaultColorSchemeParams(CustomTabColorSchemeParams.Builder().setToolbarColor(tint.toArgb()).build()).build()
        .launchUrl(this, Uri.parse(url))
}.getOrElse { startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) }

@Composable
fun ColumnScope.ProvidersScreen(c: Ctx, peer: String) {
    var query by remember { mutableStateOf("") }
    LaunchedEffect(peer) { c.reload(peer) }
    val known = known(peer)
    val shown = known.filter { query.isBlank() || it.name.contains(query.trim(), true) || it.id.contains(query.trim(), true) }
    val device = c.rt.bridge.peers.firstOrNull { it.id == peer }?.name ?: "that device"
    Header("Providers", sub = listOfNotNull(device, if (known.isEmpty()) "reading Orb's providers…" else "${known.count { it.ready }} ready · ${known.size} to choose from").joinToString(" · "), back = c.nav::back)
    Field(query, "Anthropic, OpenAI, Groq…", Modifier.padding(horizontal = Margin).padding(bottom = 4.dp).fillMaxWidth()) { query = it }
    LazyColumn(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Margin)) {
        fun section(name: String, rows: List<Provider>) {
            if (rows.isEmpty()) return
            item(key = "s:$name") { T(name, Modifier.padding(top = 22.dp, bottom = 4.dp).animateItem(), label = true, color = p.meta) }
            items(rows, key = { it.id }) { pr -> ProviderRow(pr, Modifier.animateItem()) { c.nav.go(Screen.Vendor(pr.id, peer)) } }
        }
        section("ready", shown.filter { it.ready })
        section("your subscription", shown.filter { !it.ready && it.methods.any(Method::account) })
        section("api key", shown.filter { !it.ready && it.methods.none(Method::account) })
    }
}

@Composable
private fun ProviderRow(pr: Provider, modifier: Modifier, open: () -> Unit) = Line(
    pr.name, if (pr.ready) pr.holds else pr.methods.joinToString(" · ") { if (it.account) "account" else "api key" },
    when { pr.ready -> "${pr.models} models"; pr.methods.any(Method::account) -> "sign in ›"; else -> "add key ›" }, pr.ready, modifier, open,
)

@Composable
private fun Line(name: String, sub: String, state: String, on: Boolean, modifier: Modifier = Modifier, open: () -> Unit) = Column(modifier) {
    Row(Modifier.fillMaxWidth().press(onClick = open).padding(vertical = 14.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        Dot(if (on) p.fg else p.rule, 8.dp)
        Column(Modifier.weight(1f)) { T(name, size = 17.sp, weight = if (on) Strong else Regular, lines = 1); T(sub, size = 13.sp, color = p.meta, lines = 1) }
        T(state, size = 14.sp, weight = Medium, color = if (on) p.fg else p.mute)
    }
    Rule()
}

/** One provider: its sign-in methods exactly as /login offers them, the flow running, and what it unlocked. */
@Composable
fun ColumnScope.VendorScreen(c: Ctx, id: String, peer: String) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val tint = p.bg
    val pr = known(peer).firstOrNull { it.id == id } ?: Provider(id, id, emptyList(), 0, null, "")
    var flow by remember { mutableStateOf<Login?>(null) }
    var note by remember { mutableStateOf("") }
    LaunchedEffect(Unit) { if (known(peer).none { it.id == id }) c.reload(peer) }
    DisposableEffect(Unit) { onDispose { flow?.takeIf { it.state != "done" }?.cancel() } }
    // Signed in: this phone's core restarts with the credential and the listing says what it unlocked.
    LaunchedEffect(flow?.state) { if (flow?.state == "done") { c.reload(peer); note = "" } }
    fun start(m: Method) {
        note = ""
        val app = context.applicationContext
        flow = Login(scope, SignIn(c.rt.bridge, peer, m), m) { ok ->
            // The browser is in front: bring the app back the moment Orb has the credential.
            if (ok) app.startActivity(Intent(app, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP))
        }
    }
    Header(pr.name, sub = pr.methods.joinToString(" · ") { it.about }, back = c.nav::back)
    Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = Margin).animateContentSize(), verticalArrangement = Arrangement.spacedBy(18.dp)) {
        T(if (pr.ready) "Ready through ${pr.holds} · ${pr.models} models" else "Not signed in", size = 15.sp, weight = Medium, color = if (pr.ready) p.fg else p.mute)
        val f = flow
        AnimatedContent(f?.state?.takeUnless { it == "failed" } ?: "", transitionSpec = { (fadeIn(tween(260)) + slideInVertically(tween(300)) { it / 6 }) togetherWith fadeOut(tween(160)) }, label = "flow") { state ->
            if (state.isEmpty() || f == null) Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                if (f?.state == "failed") T(f.detail.ifEmpty { "sign-in failed" }, color = Ink.Rupture)
                pr.methods.forEach { m -> MethodCard(m, pr.ready) { start(m) } }
                if (pr.ready && peer == c.rt.bridge.self) SignOut(c, pr) { note = it; scope.launch { c.reload(peer) } }
            } else Flow(f, state, tint, remote = peer != c.rt.bridge.self) { flow = null }
        }
        if (note.isNotEmpty()) T(note, color = p.mute)
        Spacer(Modifier.height(20.dp))
    }
}

@Composable
private fun MethodCard(m: Method, ready: Boolean, go: () -> Unit) = Column(
    Modifier.fillMaxWidth().press(onClick = go).clip(Pane).border(1.dp, p.fg, Pane).padding(18.dp),
) {
    T(if (m.account) "account" else "api key", label = true, color = p.meta)
    T(m.label, Modifier.padding(top = 6.dp), size = 18.sp, weight = Strong)
    T(m.about, Modifier.padding(top = 2.dp), size = 14.sp, color = p.meta)
    Row(Modifier.padding(top = 14.dp)) { Btn(if (m.account) (if (ready) "sign in again" else "sign in") else (if (ready) "replace key" else "add key"), inverted = !ready, onClick = go) }
}

/** Removes the credential from Orb's store, as `orb logout` does; one configured elsewhere is changed there. */
@Composable
private fun SignOut(c: Ctx, pr: Provider, done: (String) -> Unit) {
    val scope = rememberCoroutineScope()
    if (pr.status != "oauth" && pr.source != "stored") return T("Configured outside the app (${pr.source}); change it there.", size = 13.sp, color = p.meta)
    Row { Btn(if (pr.status == "oauth") "sign out" else "remove key") {
        scope.launch {
            withContext(Dispatchers.IO) { c.rt.orb.run("logout", pr.id) }
            done(if (pr.status == "oauth") "signed out of ${pr.name}" else "key removed")
        }
    } }
}

/** A sign-in in progress: a browser page, a device code, or a question, as the TUI shows them. */
@Composable
private fun Flow(f: Login, state: String, tint: androidx.compose.ui.graphics.Color, remote: Boolean, close: () -> Unit) = Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
    val context = LocalContext.current
    when (state) {
        "starting" -> Row(verticalAlignment = Alignment.CenterVertically) { Dot(p.mute, pulse = true); Spacer(Modifier.width(10.dp)); T(f.detail.ifEmpty { "starting…" }, color = p.mute) }
        "browser" -> {
            LaunchedEffect(f.url) { f.url?.let { context.browse(it, tint) } }
            T("Continue in the browser", size = Size.Title, weight = Strong)
            // A device signing in over Bridge listens on its own localhost: the code or final URL comes back by paste.
            T(if (remote) "Finish on the page that opened, then paste the code it shows or the final redirect URL here." else "Finish on the page that opened. It redirects to Orb on this phone, which brings you back here.", color = p.mute)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) { Btn("open again", inverted = true) { f.url?.let { context.browse(it, tint) } }; Btn("cancel") { f.cancel(); close() } }
            f.prompt?.takeIf { it.kind == "manual_code" }?.let { pr ->
                var paste by remember { mutableStateOf(remote) }
                if (!paste) Box(Modifier.press { paste = true }) { T("signing in from another device? paste the code or redirect URL", size = 13.sp, color = p.meta) }
                else Answer(pr.message, pr.placeholder, secret = false) { f.answer(it) }
            }
        }
        "code" -> {
            val code = f.code.orEmpty()
            // The code rides the clipboard to the page, which opens by itself.
            LaunchedEffect(code) { context.copy(code, "code copied"); f.url?.let { context.browse(it, tint) } }
            T("enter this code", label = true, color = p.meta)
            Box(Modifier.press { context.copy(code, "code copied") }) { T(code, size = 34.sp, weight = Strong) }
            T("It is on your clipboard. The page is " + f.url.orEmpty().removePrefix("https://"), size = 13.sp, color = p.mute)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) { Btn("open page", inverted = true) { f.url?.let { context.browse(it, tint) } }; Btn("cancel") { f.cancel(); close() } }
            if (f.detail.isNotEmpty()) T(f.detail, size = 13.sp, color = p.meta)
        }
        "asking" -> f.prompt?.let { pr ->
            if (pr.options.isNotEmpty()) {
                T(pr.message, color = p.mute)
                pr.options.forEachIndexed { i, (id, label) -> Btn(label, inverted = i == 0) { f.answer(id) } }
            } else Answer(pr.message, pr.placeholder, secret = pr.kind == "secret") { f.answer(it) }
            Btn("cancel") { f.cancel(); close() }
        }
        "done" -> {
            T("Signed in", size = Size.Title, weight = Strong)
            T(if (remote) "That device holds the new credential; its sessions can use the models now." else "The core restarted with the new credential; its models are in the model deck.", color = p.mute)
            Btn("done", inverted = true, onClick = close)
        }
    }
}

@Composable
private fun Answer(question: String, hint: String, secret: Boolean, send: (String) -> Unit) = Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
    var text by remember(question) { mutableStateOf("") }
    var shown by remember { mutableStateOf(false) }
    T(question, size = 14.sp, color = p.mute)
    Row(Modifier.fillMaxWidth().clip(Soft).border(1.dp, if (text.isEmpty()) p.rule else p.fg, Soft).padding(start = 18.dp, end = 6.dp, top = 6.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.weight(1f)) {
            BasicTextField(text, { text = it }, Modifier.fillMaxWidth(), textStyle = type(15.sp, p.fg), cursorBrush = SolidColor(p.fg), singleLine = true,
                visualTransformation = if (secret && !shown) PasswordVisualTransformation('·') else VisualTransformation.None,
                keyboardOptions = KeyboardOptions(keyboardType = if (secret) KeyboardType.Password else KeyboardType.Uri))
            if (text.isEmpty()) T(hint.ifEmpty { if (secret) "paste it here" else "" }, size = 15.sp, color = p.meta, lines = 1)
        }
        if (secret) Box(Modifier.press { shown = !shown }.padding(horizontal = 8.dp)) { T(if (shown) "hide" else "show", size = Size.Label, color = p.meta) }
        Btn("save", inverted = true) { if (text.isNotBlank()) send(text.trim()) }
    }
    if (secret) T("Orb stores it in its own credential store on this phone, as /login does.", size = 13.sp, color = p.meta)
}
