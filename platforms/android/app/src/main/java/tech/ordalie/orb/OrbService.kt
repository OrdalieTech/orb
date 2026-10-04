package tech.ordalie.orb

import android.app.*
import android.content.*
import android.content.pm.ServiceInfo
import androidx.compose.runtime.snapshotFlow
import kotlinx.coroutines.*

/** Keeps the core and its Bridge alive while the screen is off; the notification is the lock-screen slot. */
class OrbService : Service() {
    private var watch: Job? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val rt = runtime
        rt.start()
        getSystemService(NotificationManager::class.java)
            .createNotificationChannel(NotificationChannel(CHANNEL, "Orb", NotificationManager.IMPORTANCE_LOW))
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
        return START_STICKY
    }

    private fun note(text: String): Notification = Notification.Builder(this, CHANNEL)
        .setSmallIcon(R.drawable.ic_orb_mark).setContentTitle("Orb · " + runtime.orb.device).setContentText(text)
        .setOngoing(true).setOnlyAlertOnce(true).setCategory(Notification.CATEGORY_SERVICE)
        .setContentIntent(PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE))
        .build()

    override fun onDestroy() { watch?.cancel(); super.onDestroy() }
    override fun onBind(intent: Intent?) = null

    /** Starts the service after a reboot; only the system can send BOOT_COMPLETED. */
    class Boot : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            if (intent.action == Intent.ACTION_BOOT_COMPLETED) context.startForegroundService(Intent(context, OrbService::class.java))
        }
    }

    companion object { const val CHANNEL = "orb" }
}
