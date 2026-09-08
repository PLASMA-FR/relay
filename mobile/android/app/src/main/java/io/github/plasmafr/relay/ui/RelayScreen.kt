@file:OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)

package io.github.plasmafr.relay.ui

import android.net.Uri
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.outlined.ArrowForward
import androidx.compose.material.icons.automirrored.outlined.Notes
import androidx.compose.material.icons.automirrored.outlined.InsertDriveFile
import androidx.compose.material.icons.automirrored.outlined.Send
import androidx.compose.material.icons.outlined.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.net.toUri
import io.github.plasmafr.relay.data.Peer
import io.github.plasmafr.relay.data.RelayState
import io.github.plasmafr.relay.data.Transfer
import java.util.Locale

/** External share data only. It is never sent before a person chooses a trusted device. */
data class IncomingShare(val uris: List<Uri> = emptyList(), val text: String = "") {
    val isEmpty: Boolean get() = uris.isEmpty() && text.isBlank()
}

/** Screen callbacks keep the UI testable without starting the native networking runtime. */
data class RelayUiActions(
    val onAvailable: (Boolean) -> Unit = {},
    val onRefresh: () -> Unit = {},
    val onAddDevice: (String, (Peer) -> Unit) -> Unit = { _, _ -> },
    val onTrust: (Peer, () -> Unit) -> Unit = { _, _ -> },
    val onUntrust: (String) -> Unit = {},
    val onPickFiles: (String) -> Unit = {},
    val onSendText: (String, String, String, () -> Unit) -> Unit = { _, _, _, _ -> },
    val onSendShared: (String, () -> Unit) -> Unit = { _, _ -> },
    val onReadClipboard: ((String) -> Unit) -> Unit = {},
    val onCopyText: (String) -> Unit = {},
    val onShareInvite: () -> Unit = {},
    val onCopyInvite: () -> Unit = {},
    val onScan: () -> Unit = {},
    val onAction: (String, String) -> Unit = { _, _ -> },
    val onAutoAccept: (Boolean) -> Unit = {},
    val onExport: (String) -> Unit = {},
    val onShareFile: (String) -> Unit = {},
    val onClearShared: () -> Unit = {},
    val onConsumedInvite: () -> Unit = {},
    val onConsumedTab: () -> Unit = {},
    val onOpenTailscale: () -> Unit = {},
    val onDismissMessage: () -> Unit = {},
)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun RelayScreen(
    state: RelayState,
    actions: RelayUiActions,
    incoming: IncomingShare = IncomingShare(),
    initialInvite: String = "",
    requestedTab: String? = null,
    busy: Boolean = false,
    message: String? = null,
) {
    var tab by rememberSaveable { mutableIntStateOf(0) }
    var showAdd by rememberSaveable { mutableStateOf(false) }
    var showIdentity by rememberSaveable { mutableStateOf(false) }
    var addInput by rememberSaveable { mutableStateOf("") }
    var selectedPeer by remember { mutableStateOf<Peer?>(null) }
    var trustCandidate by remember { mutableStateOf<Peer?>(null) }
    var revokeCandidate by remember { mutableStateOf<Peer?>(null) }
    var editorPeer by remember { mutableStateOf<Peer?>(null) }
    var editorKind by rememberSaveable { mutableStateOf("text") }
    var editorText by rememberSaveable { mutableStateOf("") }
    val snackbar = remember { SnackbarHostState() }

    LaunchedEffect(initialInvite) {
        if (initialInvite.isNotBlank()) {
            addInput = initialInvite
            showAdd = true
            actions.onConsumedInvite()
        }
    }
    LaunchedEffect(incoming) { if (!incoming.isEmpty) tab = 0 }
    LaunchedEffect(requestedTab) {
        if (requestedTab != null) {
            tab = when (requestedTab) { "transfers" -> 1; "inbox" -> 2; else -> 0 }
            actions.onConsumedTab()
        }
    }
    LaunchedEffect(message) {
        if (!message.isNullOrBlank()) {
            snackbar.showSnackbar(message, withDismissAction = true)
            actions.onDismissMessage()
        }
    }
    val pendingCount = state.transfers.count { it.status in listOf("offered", "pending") && isIncoming(it) }
    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
        bottomBar = {
            NavigationBar(containerColor = MaterialTheme.colorScheme.surface) {
                listOf("Devices" to Icons.Outlined.Devices, "Transfers" to Icons.Outlined.SwapVert, "Inbox" to Icons.Outlined.Inbox).forEachIndexed { index, (label, icon) ->
                    NavigationBarItem(
                        selected = tab == index, onClick = { tab = index },
                        icon = {
                            BadgedBox(badge = { if (index == 1 && pendingCount > 0) Badge { Text(pendingCount.toString()) } }) {
                                Icon(icon, null)
                            }
                        },
                        label = { Text(label) }, modifier = Modifier.testTag("nav_$label"),
                    )
                }
            }
        },
    ) { insets ->
        BoxWithConstraints(Modifier.fillMaxSize().padding(insets)) {
            val wideLayout = maxWidth > 700.dp
            val horizontal = if (wideLayout) 40.dp else 24.dp
            LazyColumn(
                modifier = Modifier.fillMaxSize().testTag("main_content"),
                contentPadding = PaddingValues(horizontal = horizontal, vertical = 16.dp),
                verticalArrangement = Arrangement.spacedBy(18.dp),
            ) {
                item {
                    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                        Box(Modifier.size(36.dp).background(MaterialTheme.colorScheme.primary, RoundedCornerShape(12.dp)), contentAlignment = Alignment.Center) {
                            Icon(Icons.Outlined.SyncAlt, null, tint = MaterialTheme.colorScheme.onPrimary, modifier = Modifier.size(23.dp))
                        }
                        Text("relay", style = MaterialTheme.typography.titleLarge, modifier = Modifier.padding(start = 10.dp).weight(1f))
                        IconButton(onClick = { showIdentity = true }, modifier = Modifier.testTag("my_device")) { Icon(Icons.Outlined.PhonelinkRing, "My device and pairing details") }
                    }
                }
                item {
                    Column(verticalArrangement = Arrangement.spacedBy(5.dp)) {
                        Text(listOf("Your devices.", "In motion.", "Within reach.")[tab], style = MaterialTheme.typography.headlineLarge)
                        Text(listOf("A little less distance between your things.", "Every handoff, from start to finish.", "Everything you’ve received, in one place.")[tab], style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                item { AvailabilityCard(state, actions, busy) }
                if (!state.running && state.availableRequested) item { OutlinedButton(onClick = actions.onOpenTailscale, modifier = Modifier.fillMaxWidth()) { Text("Open Tailscale") } }
                if (state.loading || busy) item {
                    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        LinearProgressIndicator(Modifier.fillMaxWidth().semantics { contentDescription = if (state.loading) "Loading Relay" else "Action in progress" })
                        if (busy) Text("Working on your request…", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                if (state.error.isNotBlank()) item { NoticeCard(state.error, Icons.Outlined.ErrorOutline) }
                state.notices.takeLast(3).forEach { notice -> item { NoticeCard(notice, Icons.Outlined.Info) } }
                when (tab) {
                    0 -> {
                        if (!incoming.isEmpty) item {
                            Card(colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.primaryContainer)) {
                                Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
                                    Icon(Icons.AutoMirrored.Outlined.Send, null)
                                    Column(Modifier.weight(1f).padding(horizontal = 12.dp)) {
                                        Text("Choose a device to send to", style = MaterialTheme.typography.titleMedium)
                                        Text(if (incoming.uris.isNotEmpty()) "${incoming.uris.size} selected file${if (incoming.uris.size == 1) "" else "s"}${if (incoming.text.isNotBlank()) " + text" else ""}" else incoming.text.take(100), maxLines = 2, overflow = TextOverflow.Ellipsis, style = MaterialTheme.typography.bodyMedium)
                                    }
                                    IconButton(onClick = actions.onClearShared) { Icon(Icons.Outlined.Close, "Cancel shared content") }
                                }
                            }
                        }
                        item {
                            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                                Text("CONNECTED SPACE", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
                                IconButton(onClick = actions.onRefresh, enabled = !busy && state.running) { Icon(Icons.Outlined.Refresh, "Refresh devices") }
                                FilledTonalButton(onClick = { addInput = ""; showAdd = true }, enabled = !busy, modifier = Modifier.testTag("add_device")) {
                                    Icon(Icons.Outlined.Add, null, Modifier.size(18.dp)); Spacer(Modifier.width(6.dp)); Text("Add device")
                                }
                            }
                        }
                        if (state.peers.isEmpty()) item { Onboarding(actions, onAdd = { showAdd = true }) }
                        val rows = if (wideLayout) state.peers.chunked(2) else state.peers.chunked(1)
                        items(rows, key = { it.first().id }) { peers ->
                            Row(horizontalArrangement = Arrangement.spacedBy(16.dp)) {
                                peers.forEach { peer -> Box(Modifier.weight(1f)) { DeviceCard(peer, onClick = { selectedPeer = peer }) } }
                                if (wideLayout && peers.size == 1) Spacer(Modifier.weight(1f))
                            }
                        }
                        if (state.peers.isNotEmpty()) item {
                            Text("Only devices you verify can exchange with you. Pair both ways to send and receive.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                    1 -> {
                        item { SectionLabel("TRANSFER QUEUE", "${state.transfers.size}") }
                        if (state.transfers.isEmpty()) item { EmptyCard(Icons.Outlined.SwapVert, "Nothing in motion", "Send something from Devices. Incoming requests will appear here for your approval.") }
                        items(state.transfers, key = { it.id }) { transfer -> TransferCard(transfer, actions, busy) }
                        val recent = state.history.filter { old -> state.transfers.none { it.id == old.id } }.take(30)
                        if (recent.isNotEmpty()) item { SectionLabel("RECENT ACTIVITY") }
                        items(recent, key = { "history_${it.id}" }) { transfer -> TransferCard(transfer, actions, busy) }
                    }
                    2 -> {
                        val received = (state.transfers + state.history).distinctBy { it.id }.filter { isIncoming(it) && it.status == "completed" && it.verified }
                        item { SectionLabel("RECEIVED & VERIFIED", "${received.size}") }
                        if (state.clipboard.isNotBlank()) item {
                            Card {
                                Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                                    Text("Latest received text", style = MaterialTheme.typography.titleMedium)
                                    SelectionContainer { Text(state.clipboard, modifier = Modifier.heightIn(max = 200.dp).verticalScroll(rememberScrollState())) }
                                    OutlinedButton(onClick = { actions.onCopyText(state.clipboard) }) { Icon(Icons.Outlined.ContentCopy, null, Modifier.size(18.dp)); Spacer(Modifier.width(8.dp)); Text("Copy text") }
                                }
                            }
                        }
                        if (received.isEmpty() && state.clipboard.isBlank()) item { EmptyCard(Icons.Outlined.Inbox, "A place for your handoffs", "Received files stay privately in Relay until you save or share them. Text is copied only when you choose.") }
                        items(received, key = { "received_${it.id}" }) { transfer -> ReceivedCard(transfer, actions) }
                    }
                }
                item { Spacer(Modifier.height(8.dp)) }
            }
        }
    }

    if (showAdd) {
        AlertDialog(
            onDismissRequest = { if (!busy) showAdd = false },
            icon = { Icon(Icons.Outlined.AddLink, null) },
            title = { Text("Bring a device closer") },
            text = {
                Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(14.dp)) {
                    Text("Run Relay on the other device and connect both devices to your Tailscale network. Paste its pairing link or Tailscale address.")
                    OutlinedTextField(value = addInput, onValueChange = { addInput = it }, label = { Text("Pairing link or address") }, placeholder = { Text("100.64.0.5:7331") }, modifier = Modifier.fillMaxWidth().testTag("device_address"), minLines = 2, maxLines = 4, enabled = !busy)
                    OutlinedButton(onClick = actions.onScan, enabled = !busy, modifier = Modifier.fillMaxWidth()) { Icon(Icons.Outlined.QrCodeScanner, null); Spacer(Modifier.width(8.dp)); Text("Scan pairing QR code") }
                    Text("You’ll compare its full fingerprint before trusting it.", style = MaterialTheme.typography.bodySmall)
                }
            },
            confirmButton = { TextButton(enabled = addInput.isNotBlank() && !busy, onClick = { actions.onAddDevice(addInput.trim()) { peer -> showAdd = false; trustCandidate = peer } }, modifier = Modifier.testTag("connect_device")) { Text(if (busy) "Connecting…" else "Continue") } },
            dismissButton = { TextButton(enabled = !busy, onClick = { showAdd = false }) { Text("Cancel") } },
        )
    }
    if (showIdentity) {
        ModalBottomSheet(onDismissRequest = { showIdentity = false }) {
            SheetContent {
                Text("This is your device", style = MaterialTheme.typography.headlineMedium)
                Text(state.name.ifBlank { "Relay for Android" }, style = MaterialTheme.typography.titleLarge)
                Text(state.address.ifBlank { "Connect Tailscale and turn availability on to get a pairing link." }, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Text("DEVICE FINGERPRINT", style = MaterialTheme.typography.labelMedium)
                Fingerprint(state.fingerprint)
                Text("Compare this fingerprint on your other device. Pairing details are public; only share them with devices you want to connect.", style = MaterialTheme.typography.bodyMedium)
                Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    Button(onClick = actions.onShareInvite, enabled = state.running && !busy, modifier = Modifier.weight(1f)) { Icon(Icons.Outlined.Share, null, Modifier.size(18.dp)); Spacer(Modifier.width(8.dp)); Text("Share link") }
                    OutlinedButton(onClick = actions.onCopyInvite, enabled = state.running && !busy, modifier = Modifier.weight(1f)) { Text("Copy link") }
                }
                HorizontalDivider()
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text("Automatically accept", style = MaterialTheme.typography.titleMedium)
                        Text("Receive from trusted devices without asking. Off by default.", style = MaterialTheme.typography.bodySmall)
                    }
                    Switch(checked = state.autoAccept, onCheckedChange = actions.onAutoAccept, enabled = !busy, modifier = Modifier.semantics { contentDescription = "Automatically accept from trusted devices" })
                }
            }
        }
    }
    selectedPeer?.let { selected ->
        val peer = state.peers.find { it.id == selected.id } ?: selected
        ModalBottomSheet(onDismissRequest = { selectedPeer = null }) {
            SheetContent {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    DeviceGlyph(peer, Modifier.padding(end = 14.dp))
                    Column(Modifier.weight(1f)) {
                        Text(peer.name.ifBlank { peer.hostname.ifBlank { "Device" } }, style = MaterialTheme.typography.headlineMedium)
                        Text(peer.address, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                if (!peer.trusted) {
                    NoticeCard("Verify this device’s fingerprint before sending or receiving.", Icons.Outlined.Shield)
                    Button(onClick = { selectedPeer = null; trustCandidate = peer }, enabled = !busy, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp).testTag("verify_peer")) { Text("Verify device") }
                } else {
                    if (!peer.online || !state.running) NoticeCard("${if (!state.running) "Turn availability on" else "This device is offline"}. Check that both devices are running Relay on the same Tailscale network, then refresh.", Icons.Outlined.WifiOff)
                    val canSend = !busy && state.running && peer.online
                    if (!incoming.isEmpty) {
                        Button(onClick = { actions.onSendShared(peer.id) { selectedPeer = null } }, enabled = canSend, modifier = Modifier.fillMaxWidth().testTag("send_shared")) {
                            Text(if (incoming.uris.isNotEmpty()) "Send ${incoming.uris.size} file${if (incoming.uris.size == 1) "" else "s"}${if (incoming.text.isNotBlank()) " + text" else ""}" else "Send shared text")
                        }
                    }
                    SheetAction(Icons.Outlined.AttachFile, "Send files", "Choose documents, photos, or anything else", canSend) { selectedPeer = null; actions.onPickFiles(peer.id) }
                    SheetAction(Icons.AutoMirrored.Outlined.Notes, "Send text", "A thought, a note, a quick handoff", canSend) { selectedPeer = null; editorPeer = peer; editorKind = "text"; editorText = "" }
                    SheetAction(Icons.Outlined.Link, "Send a link", "Keep going on another screen", canSend) { selectedPeer = null; editorPeer = peer; editorKind = "url"; editorText = "" }
                    SheetAction(Icons.Outlined.ContentPaste, "Send clipboard", "Review before sending", canSend) {
                        actions.onReadClipboard { text -> selectedPeer = null; editorPeer = peer; editorKind = "text"; editorText = text }
                    }
                    HorizontalDivider()
                    TextButton(onClick = { selectedPeer = null; trustCandidate = peer }) { Text("View fingerprint") }
                    TextButton(onClick = { selectedPeer = null; revokeCandidate = peer }, enabled = !busy) { Text("Remove trust", color = MaterialTheme.colorScheme.error) }
                }
            }
        }
    }
    trustCandidate?.let { peer ->
        var confirmed by remember(peer.id) { mutableStateOf(false) }
        AlertDialog(
            onDismissRequest = { if (!busy) trustCandidate = null },
            icon = { Icon(Icons.Outlined.VerifiedUser, null) },
            title = { Text(if (peer.trusted) "Trusted device" else "Verify ${peer.name.ifBlank { "device" }}") },
            text = {
                Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(14.dp)) {
                    Text(peer.address, style = MaterialTheme.typography.bodyMedium)
                    Text("Compare every group below with the fingerprint shown by Relay on the other device, or confirm that its pairing link came directly from that device.")
                    Fingerprint(peer.fingerprint)
                    if (!peer.trusted) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Checkbox(checked = confirmed, onCheckedChange = { confirmed = it }, modifier = Modifier.testTag("confirm_fingerprint").semantics { contentDescription = "I verified the full device fingerprint or trusted pairing link" })
                            Text("I verified this fingerprint or the pairing link’s source.", style = MaterialTheme.typography.bodyMedium)
                        }
                        Text("Trust allows this device to offer you files and text. Your other device must also trust this phone.", style = MaterialTheme.typography.bodySmall)
                    }
                }
            },
            confirmButton = {
                if (peer.trusted) TextButton(onClick = { trustCandidate = null }) { Text("Done") }
                else TextButton(onClick = { actions.onTrust(peer) { trustCandidate = null } }, enabled = confirmed && peer.fingerprint.length == 64 && !busy, modifier = Modifier.testTag("trust_device")) { Text("Trust device") }
            },
            dismissButton = { if (!peer.trusted) TextButton(onClick = { trustCandidate = null }, enabled = !busy) { Text("Not now") } },
        )
    }
    revokeCandidate?.let { peer ->
        AlertDialog(
            onDismissRequest = { revokeCandidate = null }, title = { Text("Remove trust?") },
            text = { Text("${peer.name.ifBlank { "This device" }} will need to be verified again before exchanging files or text.") },
            confirmButton = { TextButton(onClick = { actions.onUntrust(peer.id); revokeCandidate = null }) { Text("Remove trust") } },
            dismissButton = { TextButton(onClick = { revokeCandidate = null }) { Text("Keep device") } },
        )
    }
    editorPeer?.let { peer ->
        val valid = editorText.isNotBlank() && (editorKind != "url" || validWebUrl(editorText.trim()))
        AlertDialog(
            onDismissRequest = { if (!busy) editorPeer = null },
            title = { Text(if (editorKind == "url") "Send a link" else "Send text") },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    Text("To ${peer.name.ifBlank { peer.hostname }}", color = MaterialTheme.colorScheme.onSurfaceVariant)
                    OutlinedTextField(value = editorText, onValueChange = { editorText = it }, modifier = Modifier.fillMaxWidth().testTag("send_text_input"), label = { Text(if (editorKind == "url") "https://…" else "Your text") }, minLines = 3, maxLines = 8, enabled = !busy, isError = editorKind == "url" && editorText.isNotEmpty() && !valid)
                    if (editorKind == "url") Text("Use a complete http or https link. Links aren’t opened automatically.", style = MaterialTheme.typography.bodySmall)
                }
            },
            confirmButton = { TextButton(onClick = { actions.onSendText(peer.id, editorText, editorKind) { editorPeer = null } }, enabled = valid && !busy, modifier = Modifier.testTag("send_text_confirm")) { Text(if (busy) "Sending…" else "Send") } },
            dismissButton = { TextButton(onClick = { editorPeer = null }, enabled = !busy) { Text("Cancel") } },
        )
    }
}

@Composable
private fun AvailabilityCard(state: RelayState, actions: RelayUiActions, busy: Boolean) {
    Card(colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.primaryContainer), shape = RoundedCornerShape(24.dp)) {
        Row(Modifier.fillMaxWidth().padding(20.dp), verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(10.dp).background(if (state.running) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outline, CircleShape))
            Column(Modifier.weight(1f).padding(horizontal = 14.dp)) {
                Text(if (state.running) "Ready to connect" else if (state.availableRequested) "Waiting for Tailscale" else "Your space is quiet", style = MaterialTheme.typography.titleMedium, color = MaterialTheme.colorScheme.onPrimaryContainer)
                Text(if (state.running) "${state.name.ifBlank { "This device" }} is available" else if (state.availableRequested) "Open Tailscale and connect your VPN" else "Turn on to send and receive", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onPrimaryContainer)
            }
            Switch(checked = state.availableRequested || state.running, onCheckedChange = actions.onAvailable, enabled = !busy && !state.loading, modifier = Modifier.testTag("availability").semantics { contentDescription = "Device availability" })
        }
    }
}

@Composable
private fun Onboarding(actions: RelayUiActions, onAdd: () -> Unit) {
    Card(shape = RoundedCornerShape(24.dp), colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.55f))) {
        Column(Modifier.padding(24.dp), verticalArrangement = Arrangement.spacedBy(20.dp)) {
            Icon(Icons.Outlined.Devices, null, Modifier.size(54.dp), tint = MaterialTheme.colorScheme.primary)
            Text("Your devices,\none small circle.", style = MaterialTheme.typography.headlineMedium)
            Text("Move files, links, and text between your phone and computers over your private Tailscale network.", color = MaterialTheme.colorScheme.onSurfaceVariant)
            SetupStep("1", "Connect Tailscale", "Use the same Tailscale network on both devices.")
            SetupStep("2", "Open Relay on both", "Turn availability on. On a computer, run the Relay daemon.")
            SetupStep("3", "Pair and verify", "Add the other device’s pairing link or address, then compare fingerprints.")
            Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Button(onClick = onAdd, modifier = Modifier.weight(1f)) { Text("Add your first device") }
                OutlinedButton(onClick = actions.onOpenTailscale, modifier = Modifier.weight(1f)) { Text("Tailscale") }
            }
        }
    }
}

@Composable
private fun SetupStep(number: String, title: String, detail: String) {
    Row(horizontalArrangement = Arrangement.spacedBy(14.dp)) {
        Box(Modifier.size(28.dp).background(MaterialTheme.colorScheme.surface, CircleShape), contentAlignment = Alignment.Center) { Text(number, style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.primary) }
        Column { Text(title, style = MaterialTheme.typography.titleMedium); Text(detail, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
    }
}

@Composable
private fun DeviceCard(peer: Peer, onClick: () -> Unit) {
    OutlinedCard(onClick = onClick, shape = RoundedCornerShape(22.dp), modifier = Modifier.fillMaxWidth().testTag("peer_${peer.id}")) {
        Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
                DeviceGlyph(peer)
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(5.dp)) {
                    Icon(if (peer.trusted) Icons.Outlined.VerifiedUser else Icons.Outlined.Shield, null, Modifier.size(14.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                    Text(if (peer.trusted) "Verified" else "Verify device", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
                Text(peer.name.ifBlank { peer.hostname.ifBlank { "Unnamed device" } }, style = MaterialTheme.typography.titleLarge, maxLines = 2, overflow = TextOverflow.Ellipsis)
                Text(peer.address, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.size(7.dp).background(if (peer.online) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outline, CircleShape))
                Text(if (peer.online) "Online${if (peer.latencyMs > 0) " · ${peer.latencyMs} ms" else ""}" else "Offline", style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f).padding(start = 7.dp), color = MaterialTheme.colorScheme.onSurfaceVariant)
                Icon(Icons.AutoMirrored.Outlined.ArrowForward, "Open device", Modifier.size(19.dp), tint = MaterialTheme.colorScheme.primary)
            }
            if (peer.error.isNotBlank()) Text(peer.error, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
        }
    }
}

@Composable
private fun DeviceGlyph(peer: Peer, modifier: Modifier = Modifier) {
    Box(modifier.size(48.dp).background(MaterialTheme.colorScheme.surfaceVariant, RoundedCornerShape(16.dp)), contentAlignment = Alignment.Center) {
        Icon(if (peer.os.lowercase() in listOf("android", "ios")) Icons.Outlined.Smartphone else Icons.Outlined.Laptop, null, Modifier.size(25.dp), tint = MaterialTheme.colorScheme.primary)
    }
}

@Composable
private fun TransferCard(transfer: Transfer, actions: RelayUiActions, busy: Boolean) {
    val pending = transfer.status in listOf("offered", "pending") && isIncoming(transfer)
    val terminal = transfer.status in listOf("completed", "cancelled", "rejected", "failed")
    OutlinedCard(shape = RoundedCornerShape(22.dp), modifier = Modifier.fillMaxWidth().testTag("transfer_${transfer.id}")) {
        Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                Icon(if (isIncoming(transfer)) Icons.Outlined.SouthWest else Icons.Outlined.NorthEast, null, tint = MaterialTheme.colorScheme.primary)
                Column(Modifier.weight(1f)) {
                    Text(transfer.name.ifBlank { if (transfer.kind == "file") "File transfer" else "${transfer.kind.replaceFirstChar { it.uppercase() }} transfer" }, style = MaterialTheme.typography.titleMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    Text("${if (isIncoming(transfer)) "From" else "To"} ${transfer.peer.ifBlank { "Device" }}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                Text(if (pending) "Approval needed" else transfer.status.replaceFirstChar { it.uppercase() }, style = MaterialTheme.typography.labelSmall, color = if (transfer.status == "failed") MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.primary)
            }
            if (pending) Text("${if (transfer.kind == "file") formatBytes(transfer.total) + " · " else ""}Receive this ${if (transfer.kind == "file") "file" else "${transfer.kind} item"} into Relay?", style = MaterialTheme.typography.bodyMedium)
            if (!terminal && !pending && transfer.total > 0) {
                LinearProgressIndicator(progress = { (transfer.bytes.toFloat() / transfer.total).coerceIn(0f, 1f) }, modifier = Modifier.fillMaxWidth().semantics { contentDescription = "Transfer progress" })
                Text("${formatBytes(transfer.bytes)} / ${formatBytes(transfer.total)}${if (transfer.speed > 0) " · ${formatBytes(transfer.speed.toLong())}/s" else ""}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            if (transfer.error.isNotBlank()) Text(transfer.error, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
            if (transfer.status == "completed") Text(if (transfer.verified) "Integrity verified" else "Verification not confirmed", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (pending) {
                    Button(onClick = { actions.onAction("accept", transfer.id) }, enabled = !busy, modifier = Modifier.testTag("accept_${transfer.id}")) { Text("Receive") }
                    OutlinedButton(onClick = { actions.onAction("reject", transfer.id) }, enabled = !busy) { Text("Decline") }
                } else if (transfer.status == "failed") {
                    OutlinedButton(onClick = { actions.onAction("resume", transfer.id) }, enabled = !busy) { Text("Retry") }
                } else if (!terminal) {
                    if (transfer.status in listOf("paused", "interrupted")) OutlinedButton(onClick = { actions.onAction("resume", transfer.id) }, enabled = !busy) { Text("Resume") }
                    else OutlinedButton(onClick = { actions.onAction("pause", transfer.id) }, enabled = !busy) { Text("Pause") }
                    TextButton(onClick = { actions.onAction("cancel", transfer.id) }, enabled = !busy) { Text("Cancel") }
                }
            }
        }
    }
}

@Composable
private fun ReceivedCard(transfer: Transfer, actions: RelayUiActions) {
    val files = if (transfer.paths.isNotEmpty()) transfer.paths else listOf(transfer.destination).filter { it.isNotBlank() }
    OutlinedCard(shape = RoundedCornerShape(22.dp), modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Row(horizontalArrangement = Arrangement.spacedBy(12.dp), verticalAlignment = Alignment.CenterVertically) {
                Icon(if (transfer.kind == "file") Icons.AutoMirrored.Outlined.InsertDriveFile else Icons.AutoMirrored.Outlined.Notes, null, tint = MaterialTheme.colorScheme.primary)
                Column(Modifier.weight(1f)) {
                    Text(transfer.name.ifBlank { "Received ${transfer.kind}" }, style = MaterialTheme.typography.titleMedium)
                    Text("From ${transfer.peer.ifBlank { "Device" }} · Verified", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                Icon(Icons.Outlined.CheckCircle, "Integrity verified", Modifier.size(18.dp), tint = MaterialTheme.colorScheme.primary)
            }
            if (transfer.kind == "file") {
                Text("${formatBytes(transfer.total)} · Stored privately in Relay", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                files.forEach { path ->
                    if (files.size > 1) Text(path.substringAfterLast('/'), style = MaterialTheme.typography.bodySmall)
                    Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                        OutlinedButton(onClick = { actions.onExport(path) }) { Icon(Icons.Outlined.SaveAlt, null, Modifier.size(17.dp)); Spacer(Modifier.width(6.dp)); Text("Save to device") }
                        TextButton(onClick = { actions.onShareFile(path) }) { Text("Share") }
                    }
                }
            } else Text("The latest received text appears above. Earlier text content is not kept in transfer history.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

@Composable
private fun EmptyCard(icon: ImageVector, title: String, detail: String) {
    Column(Modifier.fillMaxWidth().padding(vertical = 36.dp, horizontal = 16.dp), verticalArrangement = Arrangement.spacedBy(14.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        Box(Modifier.size(78.dp).background(MaterialTheme.colorScheme.surfaceVariant, RoundedCornerShape(28.dp)), contentAlignment = Alignment.Center) { Icon(icon, null, Modifier.size(36.dp), tint = MaterialTheme.colorScheme.primary) }
        Text(title, style = MaterialTheme.typography.titleLarge)
        Text(detail, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

@Composable
private fun NoticeCard(text: String, icon: ImageVector) {
    Surface(color = MaterialTheme.colorScheme.surfaceVariant, shape = RoundedCornerShape(16.dp)) {
        Row(Modifier.fillMaxWidth().padding(14.dp), horizontalArrangement = Arrangement.spacedBy(10.dp)) { Icon(icon, null, Modifier.size(20.dp)); Text(text, style = MaterialTheme.typography.bodySmall) }
    }
}

@Composable
private fun SectionLabel(title: String, count: String = "") {
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) { Text(title, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant); if (count.isNotBlank()) Text(count, style = MaterialTheme.typography.labelMedium) }
}

@Composable
private fun Fingerprint(value: String) {
    SelectionContainer {
        Surface(color = MaterialTheme.colorScheme.surfaceVariant, shape = RoundedCornerShape(12.dp)) {
            Text(value.ifBlank { "Identity is loading…" }.chunked(8).joinToString(" "), modifier = Modifier.fillMaxWidth().padding(14.dp).testTag("fingerprint"), fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodyMedium)
        }
    }
}

@Composable
private fun SheetContent(content: @Composable ColumnScope.() -> Unit) {
    Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(horizontal = 24.dp).padding(bottom = 32.dp), verticalArrangement = Arrangement.spacedBy(16.dp), content = content)
}

@Composable
private fun SheetAction(icon: ImageVector, title: String, subtitle: String, enabled: Boolean, onClick: () -> Unit) {
    Surface(onClick = onClick, enabled = enabled, shape = RoundedCornerShape(16.dp), color = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = if (enabled) 0.6f else 0.3f), modifier = Modifier.fillMaxWidth()) {
        Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
            Icon(icon, null, tint = if (enabled) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outline)
            Column(Modifier.weight(1f)) { Text(title, style = MaterialTheme.typography.titleMedium); Text(subtitle, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
            Icon(Icons.Outlined.ChevronRight, null, Modifier.size(20.dp))
        }
    }
}

private fun isIncoming(transfer: Transfer): Boolean = transfer.direction in listOf("receive", "incoming", "in", "received")

private fun validWebUrl(text: String): Boolean = runCatching { val uri = text.toUri(); uri.scheme in listOf("http", "https") && !uri.host.isNullOrBlank() }.getOrDefault(false)

private fun formatBytes(bytes: Long): String {
    if (bytes < 1024) return "$bytes B"
    val units = arrayOf("KiB", "MiB", "GiB", "TiB")
    var value = bytes.toDouble() / 1024
    var unit = 0
    while (value >= 1024 && unit < units.lastIndex) { value /= 1024; unit++ }
    return String.format(Locale.US, if (value >= 10) "%.0f %s" else "%.1f %s", value, units[unit])
}
