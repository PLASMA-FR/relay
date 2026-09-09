package io.github.plasmafr.relay.data

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import androidx.core.content.ContextCompat
import io.github.plasmafr.relay.platform.PrivateFiles
import io.github.plasmafr.relay.service.RelayService
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import org.json.JSONArray
import relaycore.Client
import relaycore.Observer
import relaycore.Relaycore
import java.io.File
import java.net.URI
import java.util.concurrent.atomic.AtomicLong

class RelayRepository(private val context: Context) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val mutableState = MutableStateFlow(RelayState())
    val state: StateFlow<RelayState> = mutableState.asStateFlow()
    private val client = CompletableDeferred<Client>()
    private val snapshots = Channel<String>(Channel.CONFLATED)
    private val networkMutex = Mutex()
    private val availabilityEpoch = AtomicLong(0)
    private val stateDirectory = File(context.noBackupFilesDir, "relay").canonicalFile
    private val inbox = File(context.filesDir, "inbox").canonicalFile
    private val privateFiles = PrivateFiles(context, File(stateDirectory, "staging"), inbox)
    // Keep this binding reachable for the entire client's lifetime.
    private val observer = object : Observer {
        override fun onChange(snapshotJSON: String) {
            if (snapshotJSON.length <= 8 * 1024 * 1024) snapshots.trySend(snapshotJSON)
            else reportError("Relay returned an oversized update")
        }
    }

    init {
        scope.launch {
            for (raw in snapshots) {
                try {
                    val parsed = SnapshotParser.snapshot(raw)
                    mutableState.update { parsed.copy(availableRequested = it.availableRequested, error = it.error) }
                } catch (error: Exception) { reportError("Cannot read Relay state: ${error.message.orEmpty()}") }
            }
        }
        scope.launch {
            try {
                check(stateDirectory.mkdirs() || stateDirectory.isDirectory)
                check(inbox.mkdirs() || inbox.isDirectory)
                check(privateFiles.staging.mkdirs() || privateFiles.staging.isDirectory)
                val native = Relaycore.newClient(stateDirectory.absolutePath, inbox.absolutePath, Build.MODEL.take(100), observer)
                client.complete(native)
                snapshots.trySend(native.snapshot())
            } catch (error: Throwable) {
                client.completeExceptionally(IllegalStateException("Relay native initialization failed", error))
                reportError("Relay could not start: ${error.message ?: error.javaClass.simpleName}")
                mutableState.update { it.copy(loading = false) }
            }
        }
    }

    suspend fun refresh(): Result<Unit> = operation { client.await().refresh() }

    suspend fun addDevice(input: String): Result<Peer> = operation {
        require(input.isNotBlank() && input.length <= 2048) { "Enter a device address or Relay invitation" }
        val value = input.trim()
        val raw = if (value.startsWith("relay:", ignoreCase = true)) client.await().importInvite(value)
            else client.await().addDevice(value)
        SnapshotParser.peerResult(raw)
    }

    suspend fun trust(peer: Peer): Result<Unit> = operation {
        require(peer.id.isNotBlank() && peer.fingerprint.matches(Regex("[a-f0-9]{64}"))) { "Device identity is unavailable. Refresh and try again." }
        client.await().trust(peer.id, peer.fingerprint)
    }
    suspend fun untrust(peerId: String): Result<Unit> = operation { client.await().untrust(peerId) }

    suspend fun sendText(peerId: String, text: String, kind: String = "text"): Result<Unit> = operation {
        require(kind == "text" || kind == "url") { "Unsupported transfer type" }
        require(text.isNotBlank() && text.toByteArray(Charsets.UTF_8).size <= 1024 * 1024) { "Text must contain between 1 byte and 1 MiB" }
        if (kind == "url") {
            val uri = URI(text)
            require(uri.scheme?.lowercase() in setOf("http", "https") && !uri.host.isNullOrBlank() && uri.rawUserInfo == null) {
                "Links must use http:// or https:// with a valid host"
            }
        }
        SnapshotParser.result(client.await().send(peerId, kind, text, "[]"))
        Unit
    }

    suspend fun sendFiles(peerId: String, uris: List<Uri>): Result<Unit> = operation {
        val native = client.await()
        val batch = privateFiles.importFiles(uris)
        var accepted = false
        try {
            // A Go call cannot be interrupted. Finish enqueueing before deciding who owns its files.
            withContext(NonCancellable) {
                SnapshotParser.result(native.send(peerId, "file", "", JSONArray(batch.files.map { it.absolutePath }).toString()))
                accepted = true
            }
        } finally {
            if (!accepted) batch.directory.deleteRecursively()
        }
    }

    suspend fun action(action: String, id: String): Result<Unit> = operation {
        require(action in setOf("accept", "reject", "pause", "resume", "cancel")) { "Unsupported transfer action" }
        SnapshotParser.result(client.await().action(action, id))
        Unit
    }
    suspend fun setTrustTailnet(enabled: Boolean): Result<Unit> = operation { client.await().setTrustTailnet(enabled) }
    suspend fun setAutoAccept(enabled: Boolean): Result<Unit> = operation { client.await().setAutoAccept(enabled) }
    suspend fun invite(): Result<String> = operation { client.await().invite() }
    suspend fun exportFile(sourcePath: String, destination: Uri): Result<Unit> = operation { privateFiles.export(sourcePath, destination) }
    fun shareFile(sourcePath: String): Result<Unit> = runCatching { privateFiles.share(sourcePath) }.onFailure { reportError(it.message.orEmpty()) }

    /** Call from a visible Activity following the user's availability toggle. */
    fun setAvailable(enabled: Boolean): Result<Unit> = runCatching {
        if (enabled) {
            ContextCompat.startForegroundService(context, Intent(context, RelayService::class.java).setAction(RelayService.ACTION_START))
        } else {
            // Stop even if a service was never created or its foreground promotion failed.
            endAvailability(availabilityEpoch.get())
            val serviceIntent = Intent(context, RelayService::class.java)
            context.stopService(serviceIntent)
        }
        Unit
    }.onFailure { reportError("Cannot change availability: ${it.message.orEmpty()}") }

    internal fun beginAvailability(): Long {
        val epoch = availabilityEpoch.incrementAndGet()
        mutableState.update { it.copy(availableRequested = true, error = "") }
        return epoch
    }
    internal suspend fun connect(epoch: Long, address: String): Result<Unit> = operation {
        networkMutex.withLock {
            if (availabilityEpoch.get() == epoch && state.value.availableRequested) {
                val native = client.await()
                // Each network session has fresh authorization, even if its address is unchanged.
                native.stop()
                if (address.isNotBlank()) native.start(address)
            }
        }
    }
    internal fun endAvailability(epoch: Long) {
        if (!availabilityEpoch.compareAndSet(epoch, epoch + 1)) return
        mutableState.update { it.copy(availableRequested = false) }
        scope.launch {
            operation {
                networkMutex.withLock {
                    if (availabilityEpoch.get() == epoch + 1 && !state.value.availableRequested) client.await().stop()
                }
            }
        }
    }
    internal fun reportError(message: String) {
        mutableState.update { it.copy(error = message.take(1000)) }
    }

    private suspend fun <T> operation(block: suspend () -> T): Result<T> = withContext(Dispatchers.IO) {
        try {
            val value = block()
            mutableState.update { it.copy(error = "") }
            Result.success(value)
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            reportError(error.message ?: "Relay could not complete this action")
            Result.failure(error)
        }
    }

}
