package tech.ordalie.orb

import android.app.*
import android.content.*
import android.content.pm.ServiceInfo
import androidx.compose.runtime.snapshotFlow
import kotlinx.coroutines.*
import tech.ordalie.orb.core.Session

/** Keeps the core and its Bridge alive while the screen is off; the notification is the lock-screen slot. */
class OrbService : Service() {
    private var watch: Job? = null
    private var alerts: Job? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val rt = runtime
        rt.start()
        getSystemService(NotificationManager::class.java).createNotificationChannels(listOf(
            NotificationChannel(CHANNEL, "Orb", NotificationManager.IMPORTANCE_LOW),
            NotificationChannel(ALERTS, "Conversations", NotificationManager.IMPORTANCE_HIGH),
        ))
        startForeground(1, note("starting"), ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        watch?.cancel()
        watch = rt.scope.launch {
            snapshotFlow {
                val asking = rt.sessions.firstOrNull { it.ask != null }
                val working = rt.sessions.firstOrNull { it.busy }
                when {
                    asking != null -> "needs you · " + asking.ask!!.title.take(48)
                    rt.acting -> "a peer is acting here"
                    working != null -> "working · " + working.title
                    else -> (rt.bridge.peers.count { it.connected } - 1).coerceAtLeast(0).let { "ready · $it peer" + if (it == 1) "" else "s" }
                }
            }.collect { getSystemService(NotificationManager::class.java).notify(1, note(it)) }
        }
        // A tab that finishes a turn or asks something while it is not on screen says so once.
        alerts?.cancel()
        alerts = rt.scope.launch {
            var last = emptyMap<Session, Pair<Boolean, Boolean>>()
            snapshotFlow { rt.sessions.associateWith { it.busy to (it.ask != null) } }.collect { now ->
                for ((s, state) in now) {
                    val was = last[s] ?: continue
                    if (rt.visible && s.watched) continue
                    if (!was.second && state.second) alert(s, "Needs you · " + s.ask?.title?.lineSequence()?.firstOrNull().orEmpty())
                    else if (was.first && !state.first) alert(s, s.lastText()?.trim()?.lineSequence()?.firstOrNull()?.take(160) ?: "Finished")
                }
                last = now
            }
        }
        return START_STICKY
    }

    private fun note(text: String): Notification = Notification.Builder(this, CHANNEL)
        .setSmallIcon(R.drawable.ic_orb_mark).setContentTitle("Orb · " + runtime.orb.device).setContentText(text)
        .setOngoing(true).setOnlyAlertOnce(true).setCategory(Notification.CATEGORY_SERVICE)
        .setContentIntent(PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE))
        .build()

    private fun alert(s: Session, text: String) {
        val open = PendingIntent.getActivity(this, s.id.hashCode(), Intent(this, MainActivity::class.java).putExtra(SESSION, s.id)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        getSystemService(NotificationManager::class.java).notify(s.id.hashCode(), Notification.Builder(this, ALERTS)
            .setSmallIcon(R.drawable.ic_orb_mark).setContentTitle(s.title.ifEmpty { "Orb" } + " · " + s.where).setContentText(text)
            .setAutoCancel(true).setCategory(Notification.CATEGORY_MESSAGE).setContentIntent(open).build())
    }

    override fun onDestroy() { watch?.cancel(); alerts?.cancel(); super.onDestroy() }
    override fun onBind(intent: Intent?) = null

    /** Starts the service after a reboot; only the system can send BOOT_COMPLETED. */
    class Boot : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            if (intent.action == Intent.ACTION_BOOT_COMPLETED) context.startForegroundService(Intent(context, OrbService::class.java))
        }
    }

    companion object { const val CHANNEL = "orb"; const val ALERTS = "alerts"; const val SESSION = "session" }
}
