package tech.ordalie.orb.ui

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.widget.Toast
import androidx.browser.customtabs.CustomTabColorSchemeParams
import androidx.browser.customtabs.CustomTabsIntent
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.BasicText
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import tech.ordalie.orb.MainActivity
import tech.ordalie.orb.core.Login
import tech.ordalie.orb.core.Method
import tech.ordalie.orb.core.Orb
import tech.ordalie.orb.core.Provider
import tech.ordalie.orb.core.providers

/** The last listing, so the screen opens full and refreshes in place. */
private var known by mutableStateOf(emptyList<Provider>())

private suspend fun Ctx.reload() { known = withContext(Dispatchers.IO) { rt.orb.providers() } }

/** Opens a page in a Custom Tab tinted like the app, or the browser when there is none. */
fun Context.browse(url: String, tint: androidx.compose.ui.graphics.Color) = runCatching {
    CustomTabsIntent.Builder().setShowTitle(true).setShareState(CustomTabsIntent.SHARE_STATE_OFF)
        .setDefaultColorSchemeParams(CustomTabColorSchemeParams.Builder().setToolbarColor(tint.toArgb()).build()).build()
        .launchUrl(this, Uri.parse(url))
}.getOrElse { startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) }

private fun Context.copy(text: String, what: String) {
    getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText("orb", text))
    Toast.makeText(this, "$what copied", Toast.LENGTH_SHORT).show()
}

@Composable
fun ColumnScope.ProvidersScreen(c: Ctx) {
    var query by remember { mutableStateOf("") }
    LaunchedEffect(Unit) { c.reload() }
    val shown = known.filter { query.isBlank() || it.name.contains(query.trim(), true) || it.id.contains(query.trim(), true) }
    // Endpoints added through models.json have no sign-in; they show with what they unlocked.
    val custom = c.rt.local?.models().orEmpty().groupBy { it.substringBefore('/') }.filterKeys { id -> known.none { it.id == id } }
    Header("providers", sub = if (known.isEmpty()) "reading Orb's providers…" else "${known.count { it.ready } + custom.size} ready · ${known.size} to choose from", back = c.nav::back, big = true)
    Search(query, "anthropic, openai, groq…") { query = it }
    LazyColumn(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Margin)) {
        fun section(name: String, rows: List<Provider>) {
            if (rows.isEmpty()) return
            item(key = "s:$name") { T(name, Modifier.padding(top = 22.dp, bottom = 4.dp).animateItem(), label = true, color = p.meta) }
            items(rows, key = { it.id }) { pr -> ProviderRow(pr, Modifier.animateItem()) { c.nav.go(Screen.Vendor(pr.id)) } }
        }
        section("ready", shown.filter { it.ready })
        section("your subscription", shown.filter { !it.ready && it.methods.any(Method::account) })
        section("api key", shown.filter { !it.ready && it.methods.none(Method::account) })
        if (custom.isNotEmpty()) item(key = "custom") {
            Column(Modifier.animateItem()) {
                T("endpoints", Modifier.padding(top = 22.dp, bottom = 4.dp), label = true, color = p.meta)
                custom.forEach { (id, models) -> Line(id, "models.json", "${models.size} models", true) {} }
            }
        }
        item(key = "add") {
            Row(Modifier.fillMaxWidth().press { c.nav.go(Screen.Provider) }.padding(vertical = 22.dp)) {
                T("+ endpoint", label = true); Spacer(Modifier.weight(1f)); T("any OpenAI, Anthropic or Google-shaped API", size = Size.Label, color = p.meta)
            }
        }
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
        Column(Modifier.weight(1f)) { T(name, size = 17.sp, bold = on, lines = 1); T(sub, size = Size.Label, color = p.meta, lines = 1) }
        T(state, size = 13.sp, color = if (on) p.fg else p.mute)
    }
    Rule()
}

@Composable
private fun Search(value: String, hint: String, set: (String) -> Unit) =
    Box(Modifier.padding(horizontal = Margin).padding(bottom = 4.dp).fillMaxWidth().clip(CircleShape).border(1.dp, if (value.isEmpty()) p.rule else p.fg, CircleShape).padding(horizontal = 18.dp, vertical = 11.dp)) {
        BasicTextField(value, set, Modifier.fillMaxWidth(), textStyle = mono(15.sp, p.fg), cursorBrush = SolidColor(p.fg), singleLine = true)
        if (value.isEmpty()) T(hint, color = p.meta, size = 15.sp)
    }

/** One provider: its sign-in methods exactly as /login offers them, the flow running, and what it unlocked. */
@Composable
fun ColumnScope.VendorScreen(c: Ctx, id: String) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val tint = p.bg
    val pr = known.firstOrNull { it.id == id } ?: Provider(id, id, emptyList(), 0, null, "")
    var flow by remember { mutableStateOf<Login?>(null) }
    var note by remember { mutableStateOf("") }
    LaunchedEffect(Unit) { if (known.none { it.id == id }) c.reload() }
    DisposableEffect(Unit) { onDispose { flow?.takeIf { it.state != "done" }?.cancel() } }
    // Signed in: the core restarts with the credential and the listing says what it unlocked.
    LaunchedEffect(flow?.state) { if (flow?.state == "done") { c.rt.restart(); c.reload(); note = "" } }
    fun start(m: Method) {
        note = ""
        val app = context.applicationContext
        flow = Login(scope, c.rt.orb, m) { ok ->
            // The browser is in front: bring the app back the moment Orb has the credential.
            if (ok) app.startActivity(Intent(app, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP))
        }
    }
    Header(pr.name, sub = pr.methods.joinToString(" · ") { it.about }, back = c.nav::back, big = true)
    Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = Margin).animateContentSize(), verticalArrangement = Arrangement.spacedBy(18.dp)) {
        Row(horizontalArrangement = Arrangement.spacedBy(32.dp)) {
            Reading("status", if (pr.ready) "ready" else "off"); Reading("models", pr.models.toString())
            if (pr.ready) Reading("through", pr.holds)
        }
        val f = flow
        AnimatedContent(f?.state?.takeUnless { it == "failed" } ?: "", transitionSpec = { (fadeIn(tween(260)) + slideInVertically(tween(300)) { it / 6 }) togetherWith fadeOut(tween(160)) }, label = "flow") { state ->
            if (state.isEmpty() || f == null) Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                if (f?.state == "failed") T(f.detail.ifEmpty { "sign-in failed" }, color = Ink.Rupture)
                pr.methods.forEach { m -> MethodCard(m, pr.ready) { start(m) } }
                if (pr.ready) SignOut(c, pr) { note = it; scope.launch { c.rt.restart(); c.reload() } }
            } else Flow(f, state, tint) { flow = null }
        }
        if (note.isNotEmpty()) T(note, color = p.mute)
        Spacer(Modifier.height(20.dp))
    }
}

@Composable
private fun MethodCard(m: Method, ready: Boolean, go: () -> Unit) = Card(Modifier.fillMaxWidth().press(onClick = go)) {
    T(if (m.account) "account" else "api key", label = true, color = p.meta)
    T(m.label, Modifier.padding(top = 6.dp), size = 18.sp, bold = true)
    T(m.about, Modifier.padding(top = 2.dp), size = 13.sp, color = p.meta)
    Row(Modifier.padding(top = 14.dp)) { Btn(if (m.account) (if (ready) "sign in again" else "sign in") else (if (ready) "replace key" else "add key"), inverted = !ready, onClick = go) }
}

/** Removes the credential where it lives: Orb's store (an account or a key) or the app's own environment. */
@Composable
private fun SignOut(c: Ctx, pr: Provider, done: (String) -> Unit) {
    val scope = rememberCoroutineScope()
    val env = Orb.PROVIDERS.firstOrNull { (e, id) -> id == pr.id && pr.source.contains(e) }?.first
    val stored = pr.status == "oauth" || pr.source == "stored"
    if (!stored && env == null) return T("Configured outside the app (${pr.source}); change it there.", size = 13.sp, color = p.meta)
    Row { Btn(if (pr.status == "oauth") "sign out" else "remove key") {
        scope.launch {
            if (stored) withContext(Dispatchers.IO) { c.rt.orb.run("logout", pr.id) } else c.rt.orb.setKey(env!!, "")
            done(if (pr.status == "oauth") "signed out of ${pr.name}" else "key removed")
        }
    } }
}

/** A sign-in in progress: a browser page, a device code, or a question, as the TUI shows them. */
@Composable
private fun Flow(f: Login, state: String, tint: androidx.compose.ui.graphics.Color, close: () -> Unit) = Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
    val context = LocalContext.current
    when (state) {
        "starting" -> Row(verticalAlignment = Alignment.CenterVertically) { Dot(p.mute, pulse = true); Spacer(Modifier.width(10.dp)); T(f.detail.ifEmpty { "starting…" }, color = p.mute) }
        "browser" -> {
            LaunchedEffect(f.url) { f.url?.let { context.browse(it, tint) } }
            Stretch("BROWSER", 96.dp, squeeze = 0.62f)
            T("Finish on the page that opened. It redirects to Orb on this phone, which brings you back here.", color = p.mute)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) { Btn("open again", inverted = true) { f.url?.let { context.browse(it, tint) } }; Btn("cancel") { f.cancel(); close() } }
            f.prompt?.takeIf { it.kind == "manual_code" }?.let { pr ->
                var paste by remember { mutableStateOf(false) }
                if (!paste) Box(Modifier.press { paste = true }) { T("signing in from another device? paste the code or redirect URL", size = 13.sp, color = p.meta) }
                else Answer(pr.message, pr.placeholder, secret = false) { f.answer(it) }
            }
        }
        "code" -> {
            val code = f.code.orEmpty()
            // The code rides the clipboard to the page, which opens by itself.
            LaunchedEffect(code) { context.copy(code, "code"); f.url?.let { context.browse(it, tint) } }
            T("enter this code", label = true, color = p.meta)
            Box(Modifier.press { context.copy(code, "code") }) { Stretch(code, 92.dp, squeeze = 0.66f) }
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
            Stretch("SIGNED IN", 96.dp, squeeze = 0.62f)
            T("The core restarted with the new credential; its models are in the model deck.", color = p.mute)
            Btn("done", inverted = true, onClick = close)
        }
    }
}

@Composable
private fun Answer(question: String, hint: String, secret: Boolean, send: (String) -> Unit) = Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
    var text by remember(question) { mutableStateOf("") }
    var shown by remember { mutableStateOf(false) }
    T(question, size = 14.sp, color = p.mute)
    Row(Modifier.fillMaxWidth().clip(CircleShape).border(1.dp, if (text.isEmpty()) p.rule else p.fg, CircleShape).padding(start = 18.dp, end = 6.dp, top = 6.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.weight(1f)) {
            BasicTextField(text, { text = it }, Modifier.fillMaxWidth(), textStyle = mono(15.sp, p.fg), cursorBrush = SolidColor(p.fg), singleLine = true,
                visualTransformation = if (secret && !shown) PasswordVisualTransformation('·') else VisualTransformation.None,
                keyboardOptions = KeyboardOptions(keyboardType = if (secret) KeyboardType.Password else KeyboardType.Uri))
            if (text.isEmpty()) T(hint.ifEmpty { if (secret) "paste it here" else "" }, size = 15.sp, color = p.meta, lines = 1)
        }
        if (secret) Box(Modifier.press { shown = !shown }.padding(horizontal = 8.dp)) { T(if (shown) "hide" else "show", size = Size.Label, color = p.meta) }
        Btn("save", inverted = true) { if (text.isNotBlank()) send(text.trim()) }
    }
    if (secret) BasicText("Orb stores it in its own credential store on this phone, as /login does.", style = mono(12.sp, p.meta))
}
