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
        getSystemService(NotificationManager::class.java).createNotificationChannels(listOf(
            NotificationChannel(CHANNEL, "Orb", NotificationManager.IMPORTANCE_LOW),
            NotificationChannel(ALERTS, "Conversations", NotificationManager.IMPORTANCE_HIGH),
        ))
        startForeground(1, note("starting"), ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        watch?.cancel()
        watch = rt.scope.launch {
            snapshotFlow { rt.view.state.summary }.collect { getSystemService(NotificationManager::class.java).notify(1, note(it)) }
        }
        rt.onAlert = ::alert
        return START_STICKY
    }

    private fun note(text: String): Notification = Notification.Builder(this, CHANNEL)
        .setSmallIcon(R.drawable.ic_orb_mark).setContentTitle("Orb · " + runtime.orb.device).setContentText(text)
        .setOngoing(true).setOnlyAlertOnce(true).setCategory(Notification.CATEGORY_SERVICE)
        .setContentIntent(PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE))
        .build()

    /** A conversation off screen finished a turn or asks something: it says so once. */
    private fun alert(tab: String, title: String, text: String) {
        val open = PendingIntent.getActivity(this, tab.hashCode(), Intent(this, MainActivity::class.java).putExtra(TAB, tab)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        getSystemService(NotificationManager::class.java).notify(tab.hashCode(), Notification.Builder(this, ALERTS)
            .setSmallIcon(R.drawable.ic_orb_mark).setContentTitle(title).setContentText(text)
            .setAutoCancel(true).setCategory(Notification.CATEGORY_MESSAGE).setContentIntent(open).build())
    }

    override fun onDestroy() { watch?.cancel(); super.onDestroy() }
    override fun onBind(intent: Intent?) = null

    /** Starts the service after a reboot; only the system can send BOOT_COMPLETED. */
    class Boot : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            if (intent.action == Intent.ACTION_BOOT_COMPLETED) context.startForegroundService(Intent(context, OrbService::class.java))
        }
    }

    companion object { const val CHANNEL = "orb"; const val ALERTS = "alerts"; const val TAB = "tab" }
}
