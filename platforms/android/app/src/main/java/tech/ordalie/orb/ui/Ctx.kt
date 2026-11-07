package tech.ordalie.orb.ui

import android.content.Context
import androidx.compose.runtime.snapshots.SnapshotStateList
import kotlin.math.roundToInt
import androidx.compose.runtime.snapshotFlow
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import tech.ordalie.orb.Runtime
import tech.ordalie.orb.core.*

/** What every screen needs, passed as one value, and the app's actions on conversations. */
class Ctx(
    val rt: Runtime, val nav: Nav, val cites: SnapshotStateList<String>, val onCite: () -> Unit, val context: Context, val fresh: Tab?,
    val deck: (Tab) -> Unit, val rename: (Rename) -> Unit, val pick: (Picker) -> Unit, val view: (ref: String, tab: String) -> Unit,
) {
    val v get() = rt.view

    /** The slash palette: the app's own commands, then the Orb's (extensions, templates, skills). */
    fun palette(t: Tab?): List<Command> = v.state.commands + t?.commands.orEmpty()

    /** Sends what the prompt box holds: the view runs commands and messages, and names what only the app does. */
    fun send(t: Tab?, typed: String) = rt.scope.launch {
        val to = nav.target.takeIf { t == null && !typed.startsWith("/") }
        val machine = if (t != null) t.peer.takeIf { t.remote } else to?.first?.takeIf { it != v.state.self }
        var text = typed
        // Cited files live on this phone: another machine gets a copy, which the message cites.
        if (machine != null) for (m in Regex("@(cites/\\S+)").findAll(typed)) {
            val path = v.ask("attach", "machine" to machine, "path" to java.io.File(rt.orb.cwd, m.groupValues[1]).path).obj?.optString("path").orEmpty()
            if (path.isEmpty()) { nav.unsent = t?.id.orEmpty() to typed; return@launch }
            text = text.replace(m.value, "@$path")
        }
        val r = if (to != null) v.ask("start", "machine" to to.first, "cwd" to to.second, "text" to text) else v.ask("send", "tab" to t?.id.orEmpty(), "text" to text)
        // Refused (no Orb could start, say): the text goes back where it was typed.
        if (r.error != null) nav.unsent = t?.id.orEmpty() to typed
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

    /** Makes a machine's folder where Home's next message goes, and shows Home: a conversation opens
     *  with its first message, never as an empty tab, as on the desktop. */
    fun start(machine: String, cwd: String = "") {
        nav.target = (machine to cwd).takeIf { machine != v.state.self || cwd.isNotEmpty() }
        nav.home()
    }

    suspend fun image(tab: String, ref: String, px: Int): String? = v.ask("image", "tab" to tab, "ref" to ref, "px" to px).obj?.optString("data")?.ifEmpty { null }

    /** Where the next message goes: a new conversation on this phone, an Orb running somewhere, or a folder of a machine. */
    fun chooseWhere() {
        val running = v.home.machines.flatMap { it.running }.filterNot { it.open } // an open conversation already follows those
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

    /** The model sheet, for a conversation or (t null) for the next one; with no model to choose,
     *  Providers, where one is connected. */
    fun chooseModel(t: Tab?) {
        val on = t ?: fresh
        if (on == null || on.models.isEmpty()) nav.go(Screen.Providers(on?.peer ?: v.state.self)) else deck(on)
    }

    /** A Home row held down: what its tab's menu offers, plus deleting a thread no Orb has open,
     *  which, unlike closing, removes it for good (and asks first). */
    fun threadMenu(e: Entry) {
        val t = v.tab(e.tab)
        pick(Picker(named(e.title), listOfNotNull("rename", "close conversation".takeIf { t != null }, "delete forever".takeIf { e.deletable })) {
            when (it) {
                "rename" -> rename(Rename(e.title) { name -> v.send("rename", "key" to e.key, "name" to name) })
                "close conversation" -> t?.let(::close)
                else -> pick(Picker("delete ${named(e.title)}? it cannot come back", listOf("delete forever", "keep it")) { yes -> if (yes == "delete forever") v.send("delete", "key" to e.key) })
            }
        })
    }

    /** A tab held down: rename its conversation, or stop following it. */
    fun tabMenu(t: Tab) = pick(Picker(t.name, listOf("rename", "close conversation")) {
        if (it == "rename") rename(Rename(t.title) { name -> v.send("rename", "tab" to t.id, "name" to name) }) else close(t)
    })

    /** Stops following a conversation; if it was on screen, its neighbour shows (the next, else the
     *  previous), Home once none is left. Its thread stays where it runs, so for a few seconds an
     *  undo opens it again in its place. */
    fun close(t: Tab) {
        val tabs = v.state.tabs
        val at = tabs.indexOfFirst { it.id == t.id }
        val shown = nav.open?.id == t.id
        v.send("close", "tab" to t.id)
        v.home.entries.firstOrNull { it.tab == t.id }?.let { e ->
            val again: () -> Unit = {
                rt.scope.launch { v.ask("open", "key" to e.key).obj?.optString("tab")?.takeIf(String::isNotEmpty)?.let { id -> v.send("move", "tab" to id, "to" to at); nav.show(id) } }
            }
            nav.closed = t.name to again
            nav.reopen += again
        }
        if (!shown) return
        val next = tabs.getOrNull(at + 1) ?: tabs.getOrNull(at - 1) ?: return nav.home()
        rt.scope.launch { snapshotFlow { v.state.tabs.none { it.id == t.id } }.first { it }; nav.show(next.id) }
    }

    /** A conversation's plan windows: what is left of each and when it resets, from its Orb's last reading. */
    fun usage(t: Tab) = t.usage?.let { u ->
        val time = java.text.SimpleDateFormat("EEE HH:mm", java.util.Locale.getDefault())
        pick(Picker(listOf(t.model.substringBefore('/'), u.plan, "read " + ago(u.at)).filter(String::isNotEmpty).joinToString(" · "),
            u.windows.map { "${it.name} · ${it.left.roundToInt()}% left" + if (it.resets > 0) " · resets ${time.format(it.resets)}" else "" }) {})
    }
}
