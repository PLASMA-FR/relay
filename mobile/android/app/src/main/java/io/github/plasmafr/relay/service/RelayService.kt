package io.github.plasmafr.relay.service

import android.annotation.SuppressLint
import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.net.Uri
import android.os.Build
import android.os.IBinder
import android.text.format.Formatter
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import io.github.plasmafr.relay.R
import io.github.plasmafr.relay.RelayApplication
import io.github.plasmafr.relay.platform.TailscaleNetwork
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.launch

/** Visible, explicitly started receiving session. It never restarts at boot or after process death. */
class RelayService : Service() {
    private val repository get() = (application as RelayApplication).repository
    private val manager get() = getSystemService(NotificationManager::class.java)
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val offers = IncomingOffers()
    private val deciding = mutableSetOf<String>()
    private var network: TailscaleNetwork? = null
    private var epoch: Long? = null
    private var availabilityMessage = ""

    override fun onBind(intent: Intent?): IBinder? = null

    @SuppressLint("MissingPermission") // Every notification update is guarded by canNotify().
    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> { finishSession(); return START_NOT_STICKY }
            ACTION_DECISION -> { decide(intent, startId); return START_NOT_STICKY }
            ACTION_START -> Unit
            else -> { if (epoch == null) stopSelfResult(startId); return START_NOT_STICKY }
        }
        try {
            manager.createNotificationChannel(NotificationChannel(CHANNEL, "Device availability", NotificationManager.IMPORTANCE_LOW))
            manager.createNotificationChannel(NotificationChannel(INCOMING_CHANNEL, "Incoming transfers", NotificationManager.IMPORTANCE_DEFAULT))
            // Promote before native initialization, link discovery, or any suspension.
            ServiceCompat.startForeground(this, NOTIFICATION_ID, notification("Waiting for Tailscale…"),
                if (Build.VERSION.SDK_INT >= 29) ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE else 0)
            if (epoch == null) {
                clearOfferNotifications()
                val session = repository.beginAvailability()
                epoch = session
                val watcher = TailscaleNetwork(this)
                network = watcher
                watcher.start()
                scope.launch {
                    watcher.session.collect { networkSession ->
                        repository.connect(session, networkSession.address).onFailure {
                            repository.reportError("Device availability failed: ${it.message.orEmpty()}")
                        }
                    }
                }
                scope.launch {
                    repository.state.collect { state ->
                        val message = when {
                            state.error.isNotEmpty() -> "Attention needed — open Relay"
                            state.running -> if (state.trustMode == "tailnet") "Available to your Tailnet devices" else "Available to your trusted devices"
                            else -> "Paused — connect to Tailscale"
                        }
                        if (message != availabilityMessage && canNotify()) {
                            manager.notify(NOTIFICATION_ID, notification(message))
                            availabilityMessage = message
                        }
                        deciding.removeAll { id -> state.transfers.none { it.id == id && IncomingOffers.pending(it) } }
                        updateOffers()
                    }
                }
            }
        } catch (error: Exception) {
            repository.reportError("Cannot receive in the background: ${error.message.orEmpty()}")
            finishSession()
        }
        return START_NOT_STICKY
    }

    private fun decide(intent: Intent, startId: Int) {
        val id = intent.getStringExtra(EXTRA_ID).orEmpty()
        val action = intent.getStringExtra(EXTRA_DECISION).orEmpty()
        // PendingIntent is immutable and this service is unexported. Still reject stale/unexpected IDs.
        val pending = repository.state.value.transfers.any { it.id == id && IncomingOffers.pending(it) }
        if (action !in setOf("accept", "reject") || !pending || !deciding.add(id)) {
            if (id.isNotEmpty()) manager.cancel(offerTag(id), OFFER_NOTIFICATION_ID)
            if (epoch == null) stopSelfResult(startId)
            return
        }
        updateOffers()
        scope.launch {
            var succeeded = false
            try {
                repository.action(action, id).onSuccess { succeeded = true }.onFailure {
                    repository.reportError("Could not $action this transfer: ${it.message.orEmpty()}")
                }
            } finally {
                if (!succeeded) deciding.remove(id)
                if (epoch == null) stopSelfResult(startId) else updateOffers()
            }
        }
    }

    @SuppressLint("MissingPermission") // canNotify checks the Android 13 runtime permission.
    private fun updateOffers() {
        val changes = offers.update(if (epoch != null && canNotify()) repository.state.value.transfers else emptyList(), deciding)
        changes.dismiss.forEach { manager.cancel(offerTag(it), OFFER_NOTIFICATION_ID) }
        if (canNotify()) changes.show.forEach { offer ->
            val detail = "${offer.sender} · ${Formatter.formatShortFileSize(this, offer.bytes)}"
            val notification = NotificationCompat.Builder(this, INCOMING_CHANNEL)
                .setSmallIcon(R.drawable.ic_relay_notification)
                .setContentTitle(offer.name).setContentText(detail)
                .setStyle(NotificationCompat.BigTextStyle().bigText("$detail\nAccept this transfer from your trusted device?"))
                .setContentIntent(openApp(true)).setAutoCancel(false).setOnlyAlertOnce(true)
                .setCategory(NotificationCompat.CATEGORY_MESSAGE).setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
                .addAction(0, "Accept", decisionIntent(offer.id, "accept"))
                .addAction(0, "Reject", decisionIntent(offer.id, "reject"))
                .build()
            manager.notify(offerTag(offer.id), OFFER_NOTIFICATION_ID, notification)
        }
    }

    private fun decisionIntent(id: String, action: String): PendingIntent {
        val intent = Intent(this, RelayService::class.java).setAction(ACTION_DECISION)
            .setData(Uri.Builder().scheme("relay-action").authority("transfer").appendPath(id).appendPath(action).build())
            .putExtra(EXTRA_ID, id).putExtra(EXTRA_DECISION, action)
        return PendingIntent.getService(this, 0, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
    }
    private fun openApp(transfers: Boolean = false): PendingIntent? = packageManager.getLaunchIntentForPackage(packageName)?.let {
        it.addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        if (transfers) it.putExtra("relay.open_tab", "transfers")
        PendingIntent.getActivity(this, if (transfers) 3 else 2, it, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
    }
    private fun notification(message: String): Notification {
        val stop = PendingIntent.getService(this, 1, Intent(this, RelayService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        return NotificationCompat.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_relay_notification)
            .setContentTitle("Relay").setContentText(message).setOngoing(true).setOnlyAlertOnce(true)
            .setContentIntent(openApp()).setCategory(NotificationCompat.CATEGORY_SERVICE)
            .addAction(0, "Stop", stop).build()
    }
    private fun canNotify(): Boolean = Build.VERSION.SDK_INT < 33 || ContextCompat.checkSelfPermission(this,
        Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED

    private fun clearOfferNotifications() {
        offers.clear().forEach { manager.cancel(offerTag(it), OFFER_NOTIFICATION_ID) }
        // Also discard requests left by a process killed before onDestroy could run.
        manager.activeNotifications.filter { it.notification.channelId == INCOMING_CHANNEL }
            .forEach { manager.cancel(it.tag, it.id) }
    }
    private fun finishSession() {
        clearOfferNotifications()
        epoch?.let(repository::endAvailability)
        epoch = null
        ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
        stopSelf()
    }
    override fun onDestroy() {
        runCatching { network?.close() }
        network = null
        scope.cancel()
        clearOfferNotifications()
        epoch?.let(repository::endAvailability)
        epoch = null
        super.onDestroy()
    }
    companion object {
        const val ACTION_START = "io.github.plasmafr.relay.START"
        const val ACTION_STOP = "io.github.plasmafr.relay.STOP"
        private const val ACTION_DECISION = "io.github.plasmafr.relay.DECISION"
        private const val EXTRA_ID = "transfer_id"
        private const val EXTRA_DECISION = "decision"
        private const val CHANNEL = "relay_availability"
        private const val INCOMING_CHANNEL = "relay_incoming"
        private const val NOTIFICATION_ID = 7331
        private const val OFFER_NOTIFICATION_ID = 7332
        private fun offerTag(id: String) = "relay-offer:$id"
    }
}
