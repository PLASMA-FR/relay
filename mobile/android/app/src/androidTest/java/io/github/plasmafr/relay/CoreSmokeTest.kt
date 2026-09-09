package io.github.plasmafr.relay

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import relaycore.Observer
import relaycore.Relaycore
import java.io.File
import java.util.UUID
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/** Runs the actual GoMobile AAR on Android; no fake repository or network bypass. */
@RunWith(AndroidJUnit4::class)
class CoreSmokeTest {
    @Test fun nativeIdentityPrivateFilesystemAndSettingsSurviveReopen() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val directory = File(context.cacheDir, "native-smoke-${UUID.randomUUID()}").apply { mkdirs() }
        val state = File(directory, "state").canonicalPath
        val inbox = File(directory, "inbox").canonicalPath
        val updates = CountDownLatch(1)
        val observer = object : Observer {
            override fun onChange(snapshotJSON: String) {
                if (JSONObject(snapshotJSON).optBoolean("auto_accept")) updates.countDown()
            }
        }
        try {
            val first = Relaycore.newClient(state, inbox, "Android native smoke", observer)
            val fingerprint: String
            try {
                val snapshot = JSONObject(first.snapshot())
                fingerprint = snapshot.getString("fingerprint")
                assertTrue("Native core must create a full SHA256 identity", fingerprint.matches(Regex("[a-f0-9]{64}")))
                assertFalse(snapshot.getBoolean("running"))
                assertEquals("tailnet", snapshot.getString("trust_mode"))
                assertFalse("File approval is the default", snapshot.getBoolean("auto_accept"))
                assertEquals(0, snapshot.getJSONArray("peers").length())
                assertEquals(inbox, snapshot.getString("receive_directory"))
                first.start("")
                first.stop()
                assertFalse(JSONObject(first.snapshot()).getBoolean("running"))
                first.setTrustTailnet(false)
                assertEquals("manual", JSONObject(first.snapshot()).getString("trust_mode"))
                first.setAutoAccept(true)
                assertTrue("Native observer should deliver a state update", updates.await(5, TimeUnit.SECONDS))
            } finally { first.close() }
            val reopened = Relaycore.newClient(state, inbox, "Android native smoke", null)
            try {
                val restored = JSONObject(reopened.snapshot())
                assertEquals(fingerprint, restored.getString("fingerprint"))
                assertEquals("Manual trust preference is durable", "manual", restored.getString("trust_mode"))
                reopened.setTrustTailnet(true)
                assertEquals("tailnet", JSONObject(reopened.snapshot()).getString("trust_mode"))
                assertTrue("Auto-accept preference is durable", restored.getBoolean("auto_accept"))
                assertFalse("Reopen never starts receiving automatically", restored.getBoolean("running"))
            } finally { reopened.close() }
        } finally { directory.deleteRecursively() }
    }

    @Test fun nativeBridgeRejectsUnsafeAddressesInvitesAndUntrustedSending() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val directory = File(context.cacheDir, "native-validation-${UUID.randomUUID()}").apply { mkdirs() }
        try {
            val native = Relaycore.newClient(File(directory, "state").canonicalPath, File(directory, "inbox").canonicalPath, "Android validation", null)
            try {
                assertRejected("wildcard listener") { native.start("0.0.0.0") }
                assertRejected("loopback listener") { native.start("127.0.0.1") }
                assertRejected("hostname listener") { native.start("localhost") }
                assertRejected("unavailable invitation") { native.invite() }
                assertRejected("malformed invitation") { native.importInvite("relay://pair?v=900") }
                assertRejected("non-Tailscale address") { native.addDevice("127.0.0.1:7331") }
                assertRejected("unknown trust target") { native.trust("unknown", "ab".repeat(32)) }
                assertRejected("untrusted send") { native.send("unknown", "text", "private fixture", "[]") }
                assertFalse(JSONObject(native.snapshot()).getBoolean("running"))
                assertEquals(0, JSONObject(native.snapshot()).getJSONArray("peers").length())
            } finally { native.close() }
        } finally { directory.deleteRecursively() }
    }

    private fun assertRejected(label: String, operation: () -> Unit) {
        var rejected = false
        try { operation() } catch (_: Exception) { rejected = true }
        assertTrue("Native core accepted $label", rejected)
    }
}
