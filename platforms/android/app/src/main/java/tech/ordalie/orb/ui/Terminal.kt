package tech.ordalie.orb.ui

import android.view.*
import android.view.inputmethod.InputMethodManager
import androidx.compose.foundation.*
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.unit.*
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.res.ResourcesCompat
import com.termux.terminal.*
import com.termux.view.*
import java.util.Properties
import tech.ordalie.orb.R
import tech.ordalie.orb.core.*

/**
 * The terminal where a conversation runs, through `orb bridge shell` on its machine: this phone's
 * is Orb's Linux, the shell its agent's bash tool runs; a paired machine's is its owner's shell.
 * Each lives as long as the app does, so leaving the screen never ends what runs in it.
 */
private object Shell : TerminalSessionClient, TerminalViewClient {
    private val sessions = HashMap<Pair<String, String>, TerminalSession>() // by machine and folder
    var session: TerminalSession? = null // the one on screen
    var view: TerminalView? = null
    var ctrl by mutableStateOf(false)
    var alt by mutableStateOf(false)
    var size = 0 // pixels; 13sp until pinched

    fun session(orb: Orb, peer: String, cwd: String): TerminalSession = sessions[peer to cwd]?.takeIf { it.isRunning } ?: run {
        TerminalSession(orb.binary, orb.cwd.path, arrayOf("liborb.so", "bridge", "shell", peer, cwd), (System.getenv() + orb.env() + ("TERM" to "xterm-256color")).map { (k, v) -> "$k=$v" }.toTypedArray(), 5000, this)
            .also { sessions[peer to cwd] = it }
    }.also { session = it }

    private fun keyboard(show: Boolean) {
        val v = view ?: return
        val imm = v.context.getSystemService(InputMethodManager::class.java)
        if (show) { v.requestFocus(); imm.showSoftInput(v, 0) } else imm.hideSoftInputFromWindow(v.windowToken, 0)
    }

    // TerminalSessionClient: what the shell does, shown or handed to Android.
    override fun onTextChanged(s: TerminalSession) { view?.onScreenUpdated() }
    override fun onTitleChanged(s: TerminalSession) {}
    override fun onSessionFinished(s: TerminalSession) { sessions.values.remove(s) }
    override fun onCopyTextToClipboard(s: TerminalSession, text: String?) {
        view?.context?.copy(text.orEmpty())
    }
    override fun onPasteTextFromClipboard(s: TerminalSession?) {
        view?.let { v -> v.mEmulator?.paste(v.context.paste()) }
    }
    override fun onBell(s: TerminalSession) {}
    override fun onColorsChanged(s: TerminalSession) { view?.invalidate() }
    override fun onTerminalCursorStateChange(state: Boolean) {}
    override fun getTerminalCursorStyle(): Int? = null

    // TerminalViewClient: touch and keys; Ctrl and Alt also come from the row of keys, once each.
    override fun onScale(scale: Float): Float {
        if (scale in 0.9f..1.1f) return scale
        size = (size + if (scale > 1f) 2 else -2).coerceIn(16, 96)
        view?.setTextSize(size)
        return 1f
    }
    override fun onSingleTapUp(e: MotionEvent) = keyboard(true)
    override fun shouldBackButtonBeMappedToEscape() = false
    override fun shouldEnforceCharBasedInput() = true
    override fun shouldUseCtrlSpaceWorkaround() = false
    override fun isTerminalViewSelected() = true
    override fun copyModeChanged(copyMode: Boolean) {}
    override fun onKeyDown(keyCode: Int, e: KeyEvent, s: TerminalSession) = false
    override fun onKeyUp(keyCode: Int, e: KeyEvent) = false
    override fun onLongPress(event: MotionEvent) = false
    override fun readControlKey() = ctrl.also { ctrl = false }
    override fun readAltKey() = alt.also { alt = false }
    override fun readShiftKey() = false
    override fun readFnKey() = false
    override fun onCodePoint(codePoint: Int, ctrlDown: Boolean, s: TerminalSession) = false
    override fun onEmulatorSet() {}
    override fun logError(tag: String?, message: String?) {}
    override fun logWarn(tag: String?, message: String?) {}
    override fun logInfo(tag: String?, message: String?) {}
    override fun logDebug(tag: String?, message: String?) {}
    override fun logVerbose(tag: String?, message: String?) {}
    override fun logStackTraceWithMessage(tag: String?, message: String?, e: Exception?) {}
    override fun logStackTrace(tag: String?, e: Exception?) {}
}

@Composable
fun ColumnScope.TerminalScreen(c: Ctx, on: Tab?) {
    val linux = c.rt.orb.linux
    val peer = on?.peer ?: c.v.state.self
    val cwd = on?.cwd?.ifEmpty { null } ?: c.rt.orb.cwd.path
    Header("Terminal", sub = (on?.where ?: "this phone") + " · " + cwd.replace(c.rt.orb.linux.home.path, "~").replace(Regex("^/(Users|home)/[^/]+"), "~"), back = c.nav::back)
    if (peer == c.v.state.self && !linux.ready) {
        T(linux.state.ifEmpty { "Linux is not set up yet: it installs by itself when Orb starts." }, Modifier.padding(horizontal = Margin), color = p.mute)
        return
    }
    val bg = DarkPalette.bg.toArgb()
    Box(Modifier.weight(1f).fillMaxWidth().background(DarkPalette.bg).padding(horizontal = 6.dp)) {
        AndroidView(factory = { context ->
            TerminalColors.COLOR_SCHEME.updateWith(Properties().apply {
                setProperty("background", hex(bg)); setProperty("foreground", hex(DarkPalette.fg.toArgb())); setProperty("cursor", hex(Ink.Rupture.toArgb()))
            })
            TerminalView(context, null).apply {
                setBackgroundColor(bg)
                setTerminalViewClient(Shell)
                if (Shell.size == 0) Shell.size = android.util.TypedValue.applyDimension(android.util.TypedValue.COMPLEX_UNIT_SP, 13f, resources.displayMetrics).toInt()
                setTextSize(Shell.size)
                ResourcesCompat.getFont(context, R.font.ubuntu_sans_mono)?.let(::setTypeface)
                isFocusable = true; isFocusableInTouchMode = true
                Shell.view = this
                attachSession(Shell.session(c.rt.orb, peer, cwd))
                post { requestFocus(); context.getSystemService(InputMethodManager::class.java).showSoftInput(this, 0) }
            }
        }, modifier = Modifier.fillMaxWidth(), update = { Shell.view = it })
    }
    DisposableEffect(Unit) { onDispose { Shell.view = null } }
    Keys()
}

private fun hex(argb: Int) = "#%06X".format(argb and 0xFFFFFF)

/** What a phone keyboard lacks: Esc, Tab, Ctrl, Alt and the arrows. */
@Composable
private fun Keys() {
    Row(Modifier.fillMaxWidth().background(DarkPalette.raised).horizontalScroll(rememberScrollState()).padding(horizontal = 8.dp, vertical = 6.dp),
        horizontalArrangement = Arrangement.spacedBy(4.dp), verticalAlignment = Alignment.CenterVertically) {
        Key("esc") { Shell.session?.write("\u001b") }
        Key("tab") { Shell.session?.write("\t") }
        Key("ctrl", Shell.ctrl) { Shell.ctrl = !Shell.ctrl }
        Key("alt", Shell.alt) { Shell.alt = !Shell.alt }
        Key("←") { Shell.view?.handleKeyCode(KeyEvent.KEYCODE_DPAD_LEFT, 0) }
        Key("↓") { Shell.view?.handleKeyCode(KeyEvent.KEYCODE_DPAD_DOWN, 0) }
        Key("↑") { Shell.view?.handleKeyCode(KeyEvent.KEYCODE_DPAD_UP, 0) }
        Key("→") { Shell.view?.handleKeyCode(KeyEvent.KEYCODE_DPAD_RIGHT, 0) }
        for (k in listOf("-", "/", "|", "~")) Key(k) { Shell.session?.write(k) }
        Key("paste") { Shell.session?.let(Shell::onPasteTextFromClipboard) }
    }
}

@Composable
private fun Key(label: String, on: Boolean = false, press: () -> Unit) =
    Box(Modifier.background(if (on) DarkPalette.fg else DarkPalette.raised).press(onClick = press).padding(horizontal = 12.dp, vertical = 8.dp)) {
        T(label, size = 15.sp, color = if (on) DarkPalette.bg else DarkPalette.fg)
    }

private fun TerminalSession.write(text: String) = text.toByteArray().let { write(it, 0, it.size) }
