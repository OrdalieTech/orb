package tech.ordalie.orb

import android.app.Application
import android.content.Context
import androidx.compose.runtime.*
import kotlinx.coroutines.*
import tech.ordalie.orb.core.*

/**
 * The app's whole model: this phone's Orb setup, and the view of every conversation on Bridge,
 * this phone's own included (`orb app`). What only a phone has — its Linux, its updates, being on
 * screen or not — lives here; the rest is the view's.
 */
class Runtime(context: Context) {
    val orb = Orb(context)
    val scope = MainScope()
    /** What happened out of sight: a turn ending, a question arriving. The service turns it into a notification. */
    var onAlert: (tab: String, title: String, text: String) -> Unit = { _, _, _ -> }
    // The conversations followed off screen each hold their rows: the heap decides how many (one per 48 MB, up to 8).
    val view by lazy { View(scope, orb, (java.lang.Runtime.getRuntime().maxMemory() / (48L shl 20)).toInt().coerceIn(1, 8)) { tab, title, text -> onAlert(tab, title, text) } }
    /** Whether the app is on screen; a conversation shown there needs no notification. */
    var visible by mutableStateOf(false)
    /** A conversation a notification asked to show. */
    var show by mutableStateOf<String?>(null)

    /** This app's Orb version. */
    val version: String = context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: "0.0.0-dev"
    private var started = false
    fun start() {
        if (started) return
        started = true
        scope.launch { snapshotFlow { visible }.collect(view::visible) }
        scope.launch { withContext(Dispatchers.IO) { orb.seed() }; setupLinux() }
    }

    /** The first start installs the Linux in the background (a failed one again on a tap); Orbs started after it run there. */
    fun setupLinux() = scope.launch {
        if (orb.linux.ready || !orb.linux.install()) return@launch
        withContext(Dispatchers.IO) { orb.seed() }
        view.restart()
    }
}

class OrbApp : Application() {
    val runtime by lazy { Runtime(this) }
}

val Context.runtime get() = (applicationContext as OrbApp).runtime
