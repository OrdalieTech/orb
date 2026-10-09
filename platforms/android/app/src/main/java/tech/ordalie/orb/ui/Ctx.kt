package tech.ordalie.orb.ui

import android.content.Context
import androidx.compose.runtime.snapshots.SnapshotStateList
import kotlin.math.roundToInt
import kotlinx.coroutines.*
import tech.ordalie.orb.Runtime
import tech.ordalie.orb.core.*

/** What every screen needs, passed as one value, and the app's actions on conversations. */
class Ctx(
    val rt: Runtime, val nav: Nav, val cites: SnapshotStateList<String>, val onCite: () -> Unit, val context: Context,
    val deck: (String) -> Unit, val rename: (Rename) -> Unit, val pick: (Picker) -> Unit, val view: (ref: String, tab: String) -> Unit,
) {
    val v get() = rt.view

    /** The slash palette: the app's own commands, then the Orb's (extensions, templates, skills). */
    fun palette(t: Tab?): List<Command> = v.state.commands + t?.commands.orEmpty()

    /** Sends what the prompt box holds: the view runs commands and messages, and names what only the app does. */
    fun send(t: Tab?, text: String) = rt.scope.launch {
        val r = v.ask("send", "tab" to t?.id.orEmpty(), "text" to text)
        r.obj?.optString("tab")?.takeIf { it.isNotEmpty() }?.let(nav::show)
        r.obj?.optString("copy")?.takeIf { it.isNotEmpty() }?.let { context.copy(it, "copied the last answer") }
        when (r.obj?.optString("nav")) {
            "model" -> chooseModel(t)
            "home" -> nav.home()
            "pair" -> nav.go(Screen.Bridge)
            "providers" -> nav.go(Screen.Providers(t?.peer ?: v.state.self))
            "plugins" -> nav.go(Screen.Plugins(t?.peer ?: v.state.self))
        }
    }

    /** Opens a Home row or a running Orb in its tab. */
    fun open(key: String) = rt.scope.launch { v.ask("open", "key" to key).obj?.optString("tab")?.let(nav::show) }

    /** Starts Orb on a machine, in a folder or (cwd empty) where it starts, and opens it. */
    fun start(machine: String, cwd: String = "") = rt.scope.launch { v.ask("start", "machine" to machine, "cwd" to cwd).obj?.optString("tab")?.let(nav::show) }

    suspend fun image(tab: String, ref: String, px: Int): String? = v.ask("image", "tab" to tab, "ref" to ref, "px" to px).obj?.optString("data")?.ifEmpty { null }

    /** Where the next message goes: a new conversation on this phone, an Orb running somewhere, or a folder of a machine. */
    fun chooseWhere() {
        val running = v.home.machines.flatMap { it.running }
        val hosts = v.home.machines.filter { !it.self && it.launch }
        val labels = listOf("new on this phone") + running.map { it.label } + hosts.map { "a folder on ${it.name}…" }
        pick(Picker("run on", labels) { choice ->
            val at = labels.indexOf(choice)
            when {
                at == 0 -> start(v.state.self)
                at <= running.size -> open("i:" + running[at - 1].instance)
                else -> nav.go(Screen.Device(hosts[at - 1 - running.size].id))
            }
        })
    }

    fun chooseModel(t: Tab?) {
        if (t == null || t.models.isEmpty()) nav.go(Screen.Providers(t?.peer ?: v.state.self)) else deck(t.id)
    }

    /** Renames a Home row's thread through the Orb that has it open; [delete] offers deleting it. */
    fun renameThread(e: Entry) = rename(Rename(e.title, if (e.deletable) ({ v.send("delete", "key" to e.key) }) else null) { name -> v.send("rename", "key" to e.key, "name" to name) })

    /** A tab held down: rename its conversation, or stop following it. */
    fun tabMenu(t: Tab) = pick(Picker(t.title.ifEmpty { "new session" }, listOf("rename", "close tab")) {
        if (it == "rename") rename(Rename(t.title) { name -> v.send("rename", "tab" to t.id, "name" to name) }) else close(t)
    })

    /** Stops following a conversation; Home shows if it was on screen. Its thread stays where it runs. */
    fun close(t: Tab) {
        if (nav.open?.id == t.id) nav.home()
        v.send("close", "tab" to t.id)
    }

    /** A conversation's plan windows: what is left of each and when it resets, from its Orb's last reading. */
    fun usage(t: Tab) = t.usage?.let { u ->
        val time = java.text.SimpleDateFormat("EEE HH:mm", java.util.Locale.getDefault())
        pick(Picker(listOf(t.model.substringBefore('/'), u.plan, "read " + ago(u.at)).filter(String::isNotEmpty).joinToString(" · "),
            u.windows.map { "${it.name} · ${it.left.roundToInt()}% left" + if (it.resets > 0) " · resets ${time.format(it.resets)}" else "" }) {})
    }

    fun menu() = pick(Picker("orb", listOf("terminal", "providers", "bridge", "plugins")) {
        val open = nav.open
        nav.go(when (it) { "terminal" -> Screen.Terminal(open); "bridge" -> Screen.Bridge; "plugins" -> Screen.Plugins(open?.peer ?: v.state.self); else -> Screen.Providers(open?.peer ?: v.state.self) })
    })
}
