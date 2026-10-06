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
    val deck: (Session) -> Unit, val rename: (Rename) -> Unit, val pick: (Picker) -> Unit, val view: (String) -> Unit,
) {
    /** This phone, the first machine on Bridge; null until its Bridge has answered. */
    val phone: Peer? get() = rt.bridge.peers.firstOrNull { it.id == rt.bridge.self }

    /** The slash palette: the app's own commands, then the Orb's (extensions, templates, skills). */
    fun palette(s: Session?): List<Command> = BUILTINS + s?.commands.orEmpty()

    /** Runs a built-in command; false when the text is a prompt for the Orb. */
    fun command(s: Session?, text: String): Boolean {
        // `!command` runs in the conversation's shell and joins it, as in the TUI.
        if (text.startsWith("!") && s != null && text.length > 1) { s.shell(text.drop(1).trim()); return true }
        if (!text.startsWith("/")) return false
        val name = text.drop(1).substringBefore(' ')
        val arg = text.substringAfter(' ', "").trim()
        when (name) {
            "new" -> s?.newSession(arg.ifEmpty { null }) ?: phone?.let { start(it, rt.orb.cwd.path, first = arg.ifEmpty { null }) }
            "compact" -> s?.compact(arg)
            "name" -> if (arg.isNotEmpty()) s?.rename(arg) else return false
            "model" -> chooseModel(s)
            "copy" -> s?.lastText()?.let { context.copy(it, "copied the last answer") }
            "sessions", "resume" -> nav.home()
            "bridge", "pair" -> nav.go(Screen.Bridge)
            "login", "providers" -> nav.go(Screen.Providers(s?.peer ?: rt.bridge.self))
            "plugins" -> nav.go(Screen.Plugins)
            else -> return false
        }
        return true
    }

    /** Where the next message goes: a new conversation on this phone, an Orb running somewhere, or a folder of a machine. */
    fun chooseWhere() {
        val running = rt.bridge.peers.flatMap { p -> p.instances.map { i -> p to i } }
        val hosts = rt.bridge.peers.filter { it.id != rt.bridge.self && rt.bridge.threads.containsKey(it.id) }
        val labels = mutableListOf("new on this phone")
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
                at == 0 -> phone?.let { start(it, rt.orb.cwd.path) }
                at <= running.size -> nav.show(rt.open(running[at - 1].second))
                else -> nav.go(Screen.Device(hosts[at - 1 - running.size].id))
            }
        })
    }
    fun chooseModel(s: Session?) {
        if (s == null || s.models().isEmpty()) nav.go(Screen.Providers(s?.peer ?: rt.bridge.self)) else deck(s)
    }

    /** Renames a thread through the Orb that has it open, starting one if none does; [delete] offers deleting it. */
    fun renameThread(peer: Peer, t: Thread, delete: (() -> Unit)? = null) = rename(Rename(t.title, delete) { name ->
        rt.scope.launch {
            val i = peer.instances.firstOrNull { it.session == t.id } ?: rt.bridge.launch(peer.id, session = t.id).getOrNull() ?: return@launch
            rt.open(i).rename(name)
            delay(1500); rt.reload()
        }
    })

    /** Opens a thread where it lives: the Orb that has it open, or one started on it. */
    fun openThread(peer: Peer, t: Thread) {
        rt.tab(peer.id, t.id)?.let { return nav.show(it) }
        peer.instances.firstOrNull { it.session == t.id }?.let { nav.show(rt.open(it)) } ?: start(peer, session = t.id)
    }

    /** Starts Orb on a machine, in a folder or on a thread, opens it and sends [first]; [Runtime.launching] says how it goes. */
    fun start(peer: Peer, cwd: String? = null, session: String? = null, first: String? = null) = rt.scope.launch {
        rt.launching = "starting Orb on ${peer.name}…"
        rt.bridge.launch(peer.id, cwd, session).onSuccess {
            rt.launching = ""
            nav.show(rt.open(it).also { s -> first?.let(s::prompt) })
        }.onFailure { rt.launching = it.message.orEmpty() }
    }

    /** A tab held down: rename its conversation, or stop following it. */
    fun tabMenu(s: Session) = pick(Picker(s.title.ifEmpty { "new session" }, listOf("rename", "close tab")) {
        if (it == "rename") rename(Rename(s.title) { name -> s.rename(name) })
        else { if (nav.open == s) nav.home(); rt.close(s) }
    })

    /** A session's plan windows: what is left of each and when it resets, from its Orb's last reading. */
    fun usage(s: Session) = s.usage?.let { u ->
        val time = java.text.SimpleDateFormat("EEE HH:mm", java.util.Locale.getDefault())
        pick(Picker(listOf(s.model.substringBefore('/'), u.plan, "read " + ago(u.at)).filter(String::isNotEmpty).joinToString(" · "),
            u.open().map { "${it.name} · ${it.left.roundToInt()}% left" + if (it.resets > 0) " · resets ${time.format(it.resets)}" else "" }) {})
    }

    fun menu() = pick(Picker("orb", listOf("terminal", "providers", "bridge", "plugins")) {
        val open = nav.open
        nav.go(when (it) { "terminal" -> Screen.Terminal(open); "bridge" -> Screen.Bridge; "plugins" -> Screen.Plugins; else -> Screen.Providers(open?.peer ?: rt.bridge.self) })
    })
}

/** The app's commands, named like the TUI's. Those in [NOW] run on tap; the rest take an argument. */
val BUILTINS = listOf(
    Command("new", "fresh session · optional first message"), Command("compact", "summarize to free context · optional focus"),
    Command("name", "name this session"), Command("model", "model and reasoning"), Command("copy", "copy the last answer"),
    Command("sessions", "all sessions"), Command("pair", "Bridge: scan or share a code"), Command("login", "providers and accounts"), Command("plugins", "turn plugins on and off"),
)
val NOW = setOf("model", "copy", "sessions", "pair", "login", "plugins")
