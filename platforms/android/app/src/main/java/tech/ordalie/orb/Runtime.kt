package tech.ordalie.orb

import android.app.Application
import android.content.Context
import androidx.compose.runtime.*
import kotlinx.coroutines.*
import tech.ordalie.orb.core.*

/**
 * The app's whole model: this phone's Orb setup, its Bridge, and the conversations opened through
 * it. Every conversation is an Orb on Bridge, this phone's own included.
 */
class Runtime(context: Context) {
    val orb = Orb(context)
    val scope = MainScope()
    val bridge by lazy { Bridge(scope, orb) }
    /** The open conversations, in the order they were opened: the tabs. */
    val sessions = mutableStateListOf<Session>()

    /** This app's Orb version, and the latest release when it is newer (checked at start, then every six hours). */
    val version: String = context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: "0.0.0-dev"
    var latest by mutableStateOf<String?>(null)
        private set
    private var started = false
    fun start() {
        if (started) return
        started = true
        scope.launch { while (true) { Release.latest()?.let { latest = it }; delay(6 * 3600_000L) } }
        bridge.up // the pipe starts this phone's Bridge
        scope.launch { withContext(Dispatchers.IO) { orb.seed() }; setupLinux() }
    }

    /** The first start installs the Linux in the background (a failed one again on a tap); Orbs started after it run there. */
    fun setupLinux() = scope.launch {
        if (orb.linux.ready || !orb.linux.install()) return@launch
        withContext(Dispatchers.IO) { orb.seed() }
        bridge.restart()
    }

    /** Deletes one of this phone's stored conversations (one no Orb has open). */
    fun forget(id: String) = scope.launch { withContext(Dispatchers.IO) { orb.run("storage", "delete", id) }; reload() }

    /** Every machine's threads, refreshed when Home shows and after each turn. */
    fun reload() {
        bridge.peers.forEach { peer -> scope.launch { runCatching { bridge.loadThreads(peer.id) } } }
        // A conversation whose Orb ended on a reachable machine, or whose machine was forgotten, stops being followed.
        val peers = bridge.peers.associateBy { it.id }
        sessions.filter { s -> !s.watched && peers[s.peer]?.let { p -> p.connected && p.instances.none { it.id == s.instance } } != false }.forEach(::close)
    }

    // Looked up by the Orb a conversation follows now: a reopened thread moves to a new one.
    fun open(i: Instance): Session = opened(i.id) ?: Session(scope, bridge, i.peer, i.id, bridge.peers.firstOrNull { it.id == i.peer }?.name ?: "").also { sessions += it }
    /** Stops following a conversation (its tab closes); it runs on where it is. */
    fun close(s: Session) { sessions.remove(s); s.close() }
    fun opened(instance: String): Session? = sessions.firstOrNull { it.instance == instance }
    /** What starting Orb on a machine is doing ("starting Orb on lab-3…", or why it failed); empty when idle. */
    var launching by mutableStateOf("")

    /** Pattern blue: a peer's prompt is running on this phone. */
    val acting: Boolean get() = sessions.any { s -> !s.remote && s.busy && (s.transcript.items.lastOrNull { it is You } as? You)?.via != null }
}

class OrbApp : Application() {
    val runtime by lazy { Runtime(this) }
}

val Context.runtime get() = (applicationContext as OrbApp).runtime
