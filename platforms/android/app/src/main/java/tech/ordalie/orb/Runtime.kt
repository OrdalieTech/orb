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
    /** The open conversations, in the order they were opened: the tabs, kept across restarts. */
    val sessions = mutableStateListOf<Session>()
    /** Whether the app is on screen; a conversation shown there needs no notification. */
    var visible by mutableStateOf(false)
    /** A conversation a notification asked to show. */
    var show by mutableStateOf<Session?>(null)

    /** This app's Orb version, and the latest release when it is newer (checked at start, then every six hours). */
    val version: String = context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: "0.0.0-dev"
    var latest by mutableStateOf<String?>(null)
        private set
    private var started = false
    fun start() {
        if (started) return
        started = true
        // The tabs of the last run come back; each starts its Orb again only once shown.
        runCatching { org.json.JSONArray(orb.tabs) }.getOrNull()?.let { a ->
            for (i in 0 until a.length()) a.getJSONObject(i).let { t -> sessions += Session(scope, bridge, t.getString("peer"), "", t.optString("where"), t.getString("session")).apply { title = t.optString("title") } }
        }
        scope.launch {
            // A conversation that loaded empty is not stored yet, so there would be nothing to reopen.
            snapshotFlow { sessions.filter { it.id.isNotEmpty() && !(it.loaded && it.transcript.items.isEmpty()) }.map { org.json.JSONObject().put("peer", it.peer).put("session", it.id).put("title", it.title).put("where", it.where) } }
                .collect { orb.tabs = org.json.JSONArray(it).toString() }
        }
        // While the app shows, the conversations seen last keep streaming off screen, as many as
        // the heap holds transcripts for (one per 48 MB, up to 8), so a swipe finds them current.
        val budget = (java.lang.Runtime.getRuntime().maxMemory() / (48L shl 20)).toInt().coerceIn(1, 8)
        scope.launch {
            snapshotFlow { if (visible) sessions.sortedByDescending { it.seen }.take(budget) else emptyList() }
                .collect { kept -> sessions.forEach { it.follows = it in kept } }
        }
        scope.launch { while (true) { Release.latest()?.let { latest = it }; delay(6 * 3600_000L) } }
        bridge.up // the pipe starts this phone's Bridge
        scope.launch { snapshotFlow { visible }.collect { bridge.visible = it; if (it) runCatching { bridge.refresh() } } }
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
    fun reload() = bridge.peers.forEach { peer -> scope.launch { runCatching { bridge.loadThreads(peer.id) } } }

    // Looked up by the Orb a conversation follows now: a reopened thread moves to a new one.
    fun open(i: Instance): Session = opened(i.id) ?: Session(scope, bridge, i.peer, i.id, bridge.peers.firstOrNull { it.id == i.peer }?.name ?: "").also { sessions += it }
    /** The tab of a thread, open or restored. */
    fun tab(peer: String, session: String): Session? = sessions.firstOrNull { it.peer == peer && it.id == session }
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
