package tech.ordalie.orb

import android.app.Application
import android.content.Context
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import tech.ordalie.orb.core.Bridge
import tech.ordalie.orb.core.Instance
import tech.ordalie.orb.core.LocalSession
import tech.ordalie.orb.core.Orb
import tech.ordalie.orb.core.Release
import tech.ordalie.orb.core.RemoteSession
import tech.ordalie.orb.core.Session
import tech.ordalie.orb.core.You

/** The app's whole model: this phone's Orb, its Bridge, and the peer sessions opened through it. */
class Runtime(context: Context) {
    val orb = Orb(context)
    val scope = MainScope()
    val bridge by lazy { Bridge(scope, orb) }
    var local by mutableStateOf<LocalSession?>(null)
        private set
    private val remotes = mutableStateMapOf<String, RemoteSession>()

    /** This app's Orb version, and the latest release when it is newer (checked at start, then every six hours). */
    val version: String = context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: "0.0.0-dev"
    var latest by mutableStateOf<String?>(null)
        private set
    private var checking = false

    private var starting = false
    fun start() {
        if (!checking) {
            checking = true
            scope.launch { while (true) { Release.latest()?.let { latest = it }; kotlinx.coroutines.delay(6 * 3600_000L) } }
        }
        if (local != null || starting) return
        starting = true
        bridge.up // the pipe starts the Bridge service before the session attaches to it
        scope.launch {
            try { withContext(Dispatchers.IO) { orb.seed() }; local = LocalSession(scope, orb) } finally { starting = false }
            setupLinux()
        }
    }

    /** The first start installs the Linux in the background (a failed one again on a tap); the core then moves into it. */
    fun setupLinux() = scope.launch {
        if (orb.linux.ready || !orb.linux.install()) return@launch
        withContext(Dispatchers.IO) { orb.seed() }
        local?.restart()
    }

    /** Provider keys are process environment: a new core picks them up. */
    fun restart() { local?.restart() }

    /** This phone's stored conversations, refreshed when Home shows and after each turn. */
    var history by mutableStateOf(emptyList<tech.ordalie.orb.core.Past>())
        private set
    fun reload() {
        scope.launch { history = withContext(Dispatchers.IO) { orb.sessions() } }
        bridge.peers.forEach { peer -> scope.launch { runCatching { bridge.loadThreads(peer.id) } } }
        // Sessions of instances that are gone stop following them.
        val live = bridge.peers.flatMap { it.instances }.map { it.id }.toSet()
        remotes.entries.filter { (_, s) -> s.instance !in live && !s.watched }.map { it.key }.forEach { remotes.remove(it)?.close() }
    }

    // Looked up by the Orb a session follows now: a reopened thread moves to a new one.
    fun open(i: Instance): RemoteSession = opened(i.id) ?: RemoteSession(scope, bridge, i.peer, i.id, i.cwd.substringAfterLast('/').ifEmpty { i.alias }).also { remotes[i.id] = it }
    /** What starting Orb on a device is doing ("starting Orb on lab-3…", or why it failed); empty when idle. */
    var launching by mutableStateOf("")
    fun opened(instance: String): RemoteSession? = remotes.values.firstOrNull { it.instance == instance }

    val sessions: List<Session> get() = listOfNotNull(local) + remotes.values

    /** Pattern blue: a peer's prompt is running on this phone. */
    val acting: Boolean get() = local?.let { l -> l.busy && (l.transcript.items.lastOrNull { it is You } as? You)?.via != null } == true
}

class OrbApp : Application() {
    val runtime by lazy { Runtime(this) }
}

val Context.runtime get() = (applicationContext as OrbApp).runtime
