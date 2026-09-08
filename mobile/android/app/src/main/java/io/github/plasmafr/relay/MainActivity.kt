package io.github.plasmafr.relay

import android.Manifest
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.core.content.ContextCompat
import androidx.core.net.toUri
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.google.zxing.client.android.Intents
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import io.github.plasmafr.relay.ui.IncomingShare
import io.github.plasmafr.relay.ui.RelayScreen
import io.github.plasmafr.relay.ui.RelayTheme
import io.github.plasmafr.relay.ui.RelayUiActions
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {
    private var shared by mutableStateOf(IncomingShare())
    private var pairingLink by mutableStateOf("")
    private var requestedTab by mutableStateOf<String?>(null)
    private var intentError by mutableStateOf<String?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        readIncoming(intent)
        setContent { RelayTheme { App() } }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        readIncoming(intent)
    }

    private fun readIncoming(intent: Intent?) {
        if (intent == null) return
        requestedTab = intent.getStringExtra("relay.open_tab")
        if (intent.action == Intent.ACTION_VIEW && intent.data?.scheme == "relay") {
            pairingLink = intent.data.toString()
            return
        }
        if (intent.action !in listOf(Intent.ACTION_SEND, Intent.ACTION_SEND_MULTIPLE)) return
        runCatching {
            val streams = mutableListOf<Uri>()
            @Suppress("DEPRECATION")
            when (intent.action) {
                Intent.ACTION_SEND -> intent.getParcelableExtra<Uri>(Intent.EXTRA_STREAM)?.let(streams::add)
                Intent.ACTION_SEND_MULTIPLE -> intent.getParcelableArrayListExtra<Uri>(Intent.EXTRA_STREAM)?.let(streams::addAll)
            }
            intent.clipData?.let { clip ->
                require(clip.itemCount <= 32) { "Choose up to 32 files per handoff." }
                for (index in 0 until clip.itemCount) clip.getItemAt(index).uri?.let(streams::add)
            }
            val files = streams.distinct()
            require(files.size <= 32) { "Choose up to 32 files per handoff." }
            require(files.all { it.scheme == "content" }) { "This app can share files only through Android’s secure content provider." }
            val text = intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString()
                ?: intent.clipData?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.text?.toString().orEmpty()
            require(text.length <= 1024 * 1024) { "This text is too long. Share it as a file instead." }
            shared = IncomingShare(files, text)
        }.onFailure { intentError = it.message ?: "Could not read shared content." }
    }

    @Composable
    private fun App() {
        val repository = (application as RelayApplication).repository
        val state by repository.state.collectAsStateWithLifecycle()
        val scope = rememberCoroutineScope()
        var activeOperations by remember { mutableIntStateOf(0) }
        var message by remember { mutableStateOf<String?>(null) }
        var fileTarget by rememberSaveable { mutableStateOf("") }
        var exportSource by rememberSaveable { mutableStateOf("") }

        fun <T> perform(successMessage: String? = null, onSuccess: (T) -> Unit = {}, operation: suspend () -> Result<T>) {
            scope.launch {
                activeOperations++
                try {
                    operation().onSuccess { value ->
                        onSuccess(value)
                        if (successMessage != null) message = successMessage
                    }.onFailure { message = it.message ?: "The action could not be completed. Please try again." }
                } catch (failure: Exception) {
                    if (failure is kotlinx.coroutines.CancellationException) throw failure
                    message = failure.message ?: "The action could not be completed. Please try again."
                } finally {
                    activeOperations--
                }
            }
        }

        fun setAvailable(enabled: Boolean) {
            repository.setAvailable(enabled).onFailure { message = it.message ?: "Could not change device availability." }
        }

        val notificationPermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            setAvailable(true)
            if (!granted) message = "Availability enabled. Allow notifications in Android settings to see incoming requests outside Relay."
        }
        val filePicker = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
            if (uris.isNotEmpty() && fileTarget.isNotBlank()) {
                val target = fileTarget
                perform<Unit>("Files queued. Follow their progress in Transfers.") { repository.sendFiles(target, uris) }
            }
            fileTarget = ""
        }
        val exportPicker = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("application/octet-stream")) { destination ->
            val source = exportSource
            exportSource = ""
            if (destination != null && source.isNotBlank()) perform<Unit>("Saved to your chosen location.") { repository.exportFile(source, destination) }
        }
        val scanner = rememberLauncherForActivityResult(ScanContract()) { result ->
            result.contents?.let { contents ->
                if (contents.startsWith("relay://pair")) pairingLink = contents
                else message = "This QR code is not a Relay pairing link. Open pairing details on your other device."
            }
        }
        fun copyText(text: String) {
            getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText("Relay", text))
            message = "Copied to clipboard."
        }
        fun clearShared() {
            shared = IncomingShare()
            if (intent?.action in listOf(Intent.ACTION_SEND, Intent.ACTION_SEND_MULTIPLE)) setIntent(Intent(this@MainActivity, MainActivity::class.java))
        }

        RelayScreen(
            state = state,
            incoming = shared,
            initialInvite = pairingLink,
            requestedTab = requestedTab,
            busy = activeOperations > 0,
            message = intentError ?: message,
            actions = RelayUiActions(
                onAvailable = { enabled ->
                    if (enabled && Build.VERSION.SDK_INT >= 33 && ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
                    else setAvailable(enabled)
                },
                onRefresh = { perform<Unit>("Devices refreshed.") { repository.refresh() } },
                onAddDevice = { input, complete -> perform(onSuccess = complete) { repository.addDevice(input) } },
                onTrust = { peer, complete -> perform<Unit>("Device verified.", { complete() }) { repository.trust(peer) } },
                onUntrust = { id -> perform<Unit>("Device trust removed.") { repository.untrust(id) } },
                onPickFiles = { peer -> fileTarget = peer; filePicker.launch(arrayOf("*/*")) },
                onSendText = { peer, text, kind, complete -> perform<Unit>("Queued for sending.", { complete() }) { repository.sendText(peer, text, kind) } },
                onSendShared = { peer, complete ->
                    val payload = shared
                    perform<Unit>("Queued for sending.", { clearShared(); complete() }) {
                        if (payload.uris.isNotEmpty()) {
                            val queued = repository.sendFiles(peer, payload.uris)
                            if (queued.isFailure) return@perform queued
                            // If the caption fails, keep only that remaining text for a retry.
                            shared = IncomingShare(text = payload.text)
                        }
                        if (payload.text.isNotBlank()) repository.sendText(peer, payload.text, if (isWebUrl(payload.text)) "url" else "text")
                        else Result.success(Unit)
                    }
                },
                onReadClipboard = { complete ->
                    val clip = getSystemService(ClipboardManager::class.java).primaryClip
                    val text = if (clip != null && clip.itemCount > 0) clip.getItemAt(0).text?.toString().orEmpty() else ""
                    if (text.isBlank()) message = "Your clipboard has no text to send." else complete(text)
                },
                onCopyText = ::copyText,
                onShareInvite = {
                    perform<String>(onSuccess = { uri ->
                        startActivity(Intent.createChooser(Intent(Intent.ACTION_SEND).apply { type = "text/plain"; putExtra(Intent.EXTRA_TEXT, uri) }, "Share Relay pairing link"))
                    }) { repository.invite() }
                },
                onCopyInvite = { perform<String>(onSuccess = ::copyText) { repository.invite() } },
                onScan = {
                    scanner.launch(pairingScanOptions())
                },
                onAction = { action, id ->
                    val receivingResume = action == "resume" && (state.transfers + state.history).any { it.id == id && it.direction == "receive" }
                    perform<Unit>(if (receivingResume) "Ready to receive. Resume this transfer on the sending device." else null) { repository.action(action, id) }
                },
                onAutoAccept = { enabled -> perform<Unit> { repository.setAutoAccept(enabled) } },
                onExport = { path -> exportSource = path; exportPicker.launch(path.substringAfterLast('/').ifBlank { "relay-file" }) },
                onShareFile = { path -> repository.shareFile(path).onFailure { message = it.message ?: "Could not share this file." } },
                onClearShared = ::clearShared,
                onConsumedInvite = {
                    pairingLink = ""
                    if (intent?.action == Intent.ACTION_VIEW) setIntent(Intent(this@MainActivity, MainActivity::class.java))
                },
                onConsumedTab = { requestedTab = null; intent?.removeExtra("relay.open_tab") },
                onOpenTailscale = {
                    runCatching {
                        val launch = packageManager.getLaunchIntentForPackage("com.tailscale.ipn")
                            ?: Intent(Intent.ACTION_VIEW, "https://tailscale.com/download/android".toUri())
                        startActivity(launch)
                    }.onFailure { message = "Open the Tailscale app and connect both devices to the same network." }
                },
                onDismissMessage = { message = null; intentError = null },
            ),
        )
    }
}

private fun isWebUrl(text: String): Boolean = runCatching {
    val uri = text.trim().toUri()
    uri.scheme in listOf("http", "https") && !uri.host.isNullOrBlank() && !text.trim().contains('\n')
}.getOrDefault(false)

/** Terminal QR codes can be light-on-dark; alternate polarity across camera frames. */
internal fun pairingScanOptions(): ScanOptions = ScanOptions()
    .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
    .setPrompt("Scan the pairing QR code on your other device")
    .setBeepEnabled(false)
    .setOrientationLocked(false)
    .addExtra(Intents.Scan.SCAN_TYPE, Intents.Scan.MIXED_SCAN)
