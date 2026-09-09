package io.github.plasmafr.relay

import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.plasmafr.relay.data.Peer
import io.github.plasmafr.relay.data.RelayState
import io.github.plasmafr.relay.data.Transfer
import io.github.plasmafr.relay.ui.IncomingShare
import io.github.plasmafr.relay.ui.RelayScreen
import io.github.plasmafr.relay.ui.RelayTheme
import io.github.plasmafr.relay.ui.RelayUiActions
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Rule
import org.junit.Test

/** Explicit fixtures: these tests never instantiate RelayRepository or start the Go runtime. */
class RelayScreenTest {
    @get:Rule val compose = createComposeRule()

    private val laptop = Peer(
        id = "fixture-laptop", name = "Studio laptop", hostname = "studio", address = "100.64.0.12:7331",
        os = "darwin", online = true, relay = true, trusted = true,
        fingerprint = "0123456789abcdef".repeat(4), latencyMs = 12,
    )
    private val fixture = RelayState(
        name = "My phone", address = "100.64.0.7:7331", fingerprint = "abcdef0123456789".repeat(4),
        running = true, availableRequested = true, loading = false, peers = listOf(laptop),
    )

    @Test fun emptyStateProvidesRealSetupAndAvailability() {
        var available = false
        compose.setContent { RelayTheme { RelayScreen(RelayState(loading = false), RelayUiActions(onAvailable = { available = it })) } }
        compose.onNodeWithTag("availability").performClick()
        compose.runOnIdle { assertEquals(true, available) }
        compose.onNodeWithText("Enter an address").performScrollTo().assertIsDisplayed()
        compose.onNodeWithText("Connect Tailscale").performScrollTo().assertIsDisplayed()
    }

    @Test fun trustRequiresAnExplicitFingerprintConfirmation() {
        val untrusted = laptop.copy(trusted = false)
        var trusted = false
        compose.setContent {
            RelayTheme { RelayScreen(fixture.copy(trustMode = "manual", peers = listOf(untrusted)), RelayUiActions(onTrust = { peer, done -> assertEquals(untrusted.fingerprint, peer.fingerprint); trusted = true; done() })) }
        }
        compose.onNodeWithTag("peer_${laptop.id}").performScrollTo().performClick()
        compose.onNodeWithTag("verify_peer").performClick()
        compose.onNodeWithTag("trust_device").assertIsNotEnabled()
        compose.onNodeWithTag("fingerprint").assertTextContains(untrusted.fingerprint.chunked(8).joinToString(" "))
        compose.runOnIdle { assertFalse(trusted) }
        compose.onNodeWithTag("confirm_fingerprint").performScrollTo().performClick()
        compose.onNodeWithTag("trust_device").assertIsEnabled().performClick()
        compose.runOnIdle { assertEquals(true, trusted) }
    }

    @Test fun automaticDiscoveryNeedsNoPairingAndKeepsAddressFallback() {
        compose.setContent { RelayTheme { RelayScreen(fixture.copy(peers = emptyList()), RelayUiActions()) } }
        compose.onNodeWithTag("discovery_waiting").performScrollTo().assertIsDisplayed()
        compose.onNodeWithText("Devices appear automatically").performScrollTo().assertIsDisplayed()
        compose.onNodeWithText("Enter an address").performScrollTo().assertIsDisplayed()
        compose.onNodeWithText("Pair and verify").assertDoesNotExist()
    }

    @Test fun addingTailnetDeviceOpensSendActionsWithoutFingerprintConfirmation() {
        var input = ""
        var trustCalls = 0
        compose.setContent {
            RelayTheme { RelayScreen(fixture, RelayUiActions(
                onAddDevice = { value, complete -> input = value; complete(laptop) },
                onTrust = { _, _ -> trustCalls++ },
            )) }
        }
        compose.onNodeWithTag("add_device").performScrollTo().performClick()
        compose.onNodeWithTag("device_address").performTextInput(laptop.address)
        compose.onNodeWithTag("connect_device").performClick()
        compose.onNodeWithText("Send files").assertIsDisplayed()
        compose.onNodeWithTag("confirm_fingerprint").assertDoesNotExist()
        compose.onNodeWithTag("trust_device").assertDoesNotExist()
        compose.runOnIdle { assertEquals(laptop.address, input); assertEquals(0, trustCalls) }
    }

    @Test fun importedDeviceLinkAlsoSkipsPairingInDefaultMode() {
        val link = "relay://pair?v=1&fixture=true"
        var input = ""
        compose.setContent {
            RelayTheme { RelayScreen(fixture, RelayUiActions(onAddDevice = { value, complete -> input = value; complete(laptop) }), initialInvite = link) }
        }
        compose.onNodeWithTag("device_address").assertTextContains(link)
        compose.onNodeWithTag("connect_device").performClick()
        compose.onNodeWithText("Send files").assertIsDisplayed()
        compose.onNodeWithTag("fingerprint").assertDoesNotExist()
        compose.runOnIdle { assertEquals(link, input) }
    }

    @Test fun tailnetDeviceCanBeBlockedWithoutPairing() {
        var blockedId = ""
        compose.setContent { RelayTheme { RelayScreen(fixture, RelayUiActions(onUntrust = { blockedId = it })) } }
        compose.onNodeWithTag("peer_${laptop.id}").performScrollTo().performClick()
        compose.onNodeWithTag("block_peer").performScrollTo().performClick()
        compose.onNodeWithTag("confirm_block").performClick()
        compose.runOnIdle { assertEquals(laptop.id, blockedId) }
        compose.onNodeWithTag("confirm_fingerprint").assertDoesNotExist()
    }

    @Test fun blockedTailnetDeviceCanBeUnblockedWithoutFingerprintConfirmation() {
        val blocked = laptop.copy(blocked = true, trusted = false)
        var unblockedId = ""
        compose.setContent { RelayTheme { RelayScreen(fixture.copy(peers = listOf(blocked)), RelayUiActions(onTrust = { peer, done -> unblockedId = peer.id; done() })) } }
        compose.onNodeWithText("Blocked").performScrollTo().assertIsDisplayed()
        compose.onNodeWithTag("peer_${laptop.id}").performClick()
        compose.onNodeWithText("Send files").assertDoesNotExist()
        compose.onNodeWithTag("unblock_peer").performClick()
        compose.runOnIdle { assertEquals(laptop.id, unblockedId) }
        compose.onNodeWithTag("confirm_fingerprint").assertDoesNotExist()
    }

    @Test fun blockedDeviceInManualModeRequiresFingerprintConfirmationToUnblock() {
        val blocked = laptop.copy(blocked = true, trusted = false)
        var trustedId = ""
        compose.setContent {
            RelayTheme { RelayScreen(fixture.copy(trustMode = "manual", peers = listOf(blocked)), RelayUiActions(
                onTrust = { peer, done ->
                    assertEquals(blocked.fingerprint, peer.fingerprint)
                    trustedId = peer.id
                    done()
                },
            )) }
        }
        compose.onNodeWithTag("peer_${laptop.id}").performScrollTo().performClick()
        compose.onNodeWithTag("unblock_peer").assertDoesNotExist()
        compose.onNodeWithText("Send files").assertDoesNotExist()
        compose.onNodeWithTag("verify_peer").assertTextContains("Verify and unblock").performClick()
        compose.onNodeWithTag("trust_device").assertTextContains("Trust and unblock").assertIsNotEnabled()
        compose.runOnIdle { assertEquals("", trustedId) }
        compose.onNodeWithTag("confirm_fingerprint").performScrollTo().performClick()
        compose.onNodeWithTag("trust_device").assertIsEnabled().performClick()
        compose.runOnIdle { assertEquals(laptop.id, trustedId) }
    }

    @Test fun waitingTailnetDeviceNeverRequestsManualPairingAndCanBeBlocked() {
        compose.setContent { RelayTheme { RelayScreen(fixture.copy(peers = listOf(laptop.copy(trusted = false))), RelayUiActions()) } }
        compose.onNodeWithTag("peer_${laptop.id}").performScrollTo().performClick()
        compose.onNodeWithTag("verify_peer").assertDoesNotExist()
        compose.onNodeWithText("Send files").assertDoesNotExist()
        compose.onNodeWithTag("block_peer").assertIsDisplayed()
    }

    @Test fun advancedManualTrustIsSeparateFromIncomingApproval() {
        var trustTailnet: Boolean? = null
        var autoAccept: Boolean? = null
        compose.setContent { RelayTheme { RelayScreen(fixture, RelayUiActions(onTrustTailnet = { trustTailnet = it }, onAutoAccept = { autoAccept = it })) } }
        compose.onNodeWithTag("my_device").performClick()
        compose.onNodeWithTag("fingerprint").assertDoesNotExist()
        compose.onNodeWithTag("trust_tailnet").performScrollTo().assertIsOn().performClick()
        compose.runOnIdle { assertEquals(false, trustTailnet); assertEquals(null, autoAccept) }
    }

    @Test fun incomingOfferWaitsForManualApproval() {
        var action = ""
        val offer = Transfer(id = "fixture-offer", name = "Weekend photos.zip", peer = laptop.name, peerId = laptop.id, direction = "receive", kind = "file", status = "offered", total = 8_400_000)
        compose.setContent { RelayTheme { RelayScreen(fixture.copy(transfers = listOf(offer)), RelayUiActions(onAction = { verb, id -> action = "$verb:$id" })) } }
        compose.runOnIdle { assertEquals("", action) }
        compose.onNodeWithTag("nav_Transfers").performClick()
        compose.onNodeWithTag("accept_fixture-offer").performScrollTo().performClick()
        compose.runOnIdle { assertEquals("accept:fixture-offer", action) }
    }

    @Test fun sharedTextIsSentOnlyAfterChoosingDeviceAndConfirming() {
        var sentTo = ""
        compose.setContent { RelayTheme { RelayScreen(fixture, RelayUiActions(onSendShared = { peer, done -> sentTo = peer; done() }), incoming = IncomingShare(text = "A note from another app")) } }
        compose.runOnIdle { assertEquals("", sentTo) }
        compose.onNodeWithTag("peer_${laptop.id}").performScrollTo().performClick()
        compose.runOnIdle { assertEquals("", sentTo) }
        compose.onNodeWithTag("send_shared").performClick()
        compose.runOnIdle { assertEquals(laptop.id, sentTo) }
    }

    @Test fun clipboardIsReadOnlyOnTapAndReviewedBeforeSending() {
        var clipboardReads = 0
        var sentText = ""
        compose.setContent {
            RelayTheme {
                RelayScreen(fixture, RelayUiActions(
                    onReadClipboard = { complete -> clipboardReads++; complete("Clipboard fixture") },
                    onSendText = { _, text, _, done -> sentText = text; done() },
                ))
            }
        }
        compose.onNodeWithTag("peer_${laptop.id}").performScrollTo().performClick()
        compose.runOnIdle { assertEquals(0, clipboardReads) }
        compose.onNodeWithText("Send clipboard").performScrollTo().performClick()
        compose.onNodeWithTag("send_text_input").assertTextContains("Clipboard fixture")
        compose.runOnIdle { assertEquals(1, clipboardReads); assertEquals("", sentText) }
        compose.onNodeWithTag("send_text_confirm").performClick()
        compose.runOnIdle { assertEquals("Clipboard fixture", sentText) }
    }

    @Test fun inboxExposesOnlyVerifiedReceivedFilesForExport() {
        var exported = ""
        val verified = Transfer(id = "fixture-good", name = "Verified notes.pdf", peer = laptop.name, direction = "receive", kind = "file", status = "completed", verified = true, total = 12_000, paths = listOf("/private/inbox/notes.pdf"))
        val unverified = verified.copy(id = "fixture-bad", name = "Unverified notes.pdf", verified = false)
        compose.setContent { RelayTheme { RelayScreen(fixture.copy(history = listOf(verified, unverified)), RelayUiActions(onExport = { exported = it })) } }
        compose.onNodeWithTag("nav_Inbox").performClick()
        compose.onNodeWithText("Unverified notes.pdf").assertDoesNotExist()
        compose.onNodeWithText("Save to device").performScrollTo().performClick()
        compose.runOnIdle { assertEquals("/private/inbox/notes.pdf", exported) }
    }

    @Test fun pausedTransferCanResumeAndCancel() {
        val actions = mutableListOf<String>()
        val transfer = Transfer(id = "fixture-paused", name = "Archive.zip", peer = laptop.name, direction = "send", kind = "file", status = "paused", bytes = 2_000_000, total = 8_000_000)
        compose.setContent { RelayTheme { RelayScreen(fixture.copy(transfers = listOf(transfer)), RelayUiActions(onAction = { action, _ -> actions += action })) } }
        compose.onNodeWithTag("nav_Transfers").performClick()
        compose.onNodeWithText("Resume").performScrollTo().performClick()
        compose.onNodeWithText("Cancel").performClick()
        compose.runOnIdle { assertEquals(listOf("resume", "cancel"), actions) }
    }

    @Test fun screenshotLightDevicesExplicitFixture() {
        compose.setContent { RelayTheme(darkTheme = false) { RelayScreen(fixture.copy(peers = listOf(laptop, laptop.copy(id = "fixture-server", name = "Home server", os = "linux", online = false, address = "100.64.0.24:7331"))), RelayUiActions()) } }
        compose.onNodeWithText("Your devices.").assertIsDisplayed()
        compose.onNodeWithTag("peer_${laptop.id}").assertIsDisplayed()
        saveScreenshot("fixture-devices-light.png")
    }

    @Test fun screenshotDarkTransfersExplicitFixture() {
        val transfer = Transfer(id = "fixture-moving", name = "Summer memories.zip", peer = laptop.name, direction = "send", kind = "file", status = "transferring", bytes = 31_000_000, total = 84_000_000, speed = 4_200_000.0)
        val offer = Transfer(id = "fixture-incoming", name = "Project notes.pdf", peer = laptop.name, direction = "receive", kind = "file", status = "offered", total = 120_000)
        compose.setContent { RelayTheme(darkTheme = true) { RelayScreen(fixture.copy(transfers = listOf(transfer, offer)), RelayUiActions()) } }
        compose.onNodeWithTag("nav_Transfers").performClick()
        compose.onNodeWithText("In motion.").assertIsDisplayed()
        compose.onNodeWithText("Summer memories.zip").assertIsDisplayed()
        saveScreenshot("fixture-transfers-dark.png")
    }

    private fun saveScreenshot(name: String) {
        compose.waitForIdle()
        val root = compose.onRoot().assertIsDisplayed()
        saveTestScreenshot(name, root.captureToImage().asAndroidBitmap())
    }
}
