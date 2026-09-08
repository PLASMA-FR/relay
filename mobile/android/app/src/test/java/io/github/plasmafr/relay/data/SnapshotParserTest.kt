package io.github.plasmafr.relay.data

import org.junit.Assert.*
import org.junit.Test

class SnapshotParserTest {
    @Test fun emptyStateHasNoInventedTrustOrAvailability() {
        val state = SnapshotParser.snapshot("{}")
        assertFalse(state.running)
        assertFalse(state.autoAccept)
        assertFalse(state.loading)
        assertTrue(state.peers.isEmpty())
    }
    @Test fun preservesOpaqueIdentityAndTreatsContentAsText() {
        val state = SnapshotParser.snapshot("""{"peers":[{"id":"a/b?x", "name":"<script>alert(1)</script>", "trusted":false}],"clipboard":"$(touch /tmp/no)","clipboard_transfer_id":"text-1"}""")
        assertEquals("a/b?x", state.peers.single().id)
        assertEquals("<script>alert(1)</script>", state.peers.single().name)
        assertFalse(state.peers.single().trusted)
        assertEquals("$(touch /tmp/no)", state.clipboard)
    }
    @Test fun rejectsBooleanCoercion() {
        assertThrows(IllegalArgumentException::class.java) { SnapshotParser.snapshot("""{"peers":[{"trusted":"true"}]}""") }
        assertThrows(IllegalArgumentException::class.java) { SnapshotParser.snapshot("""{"running":1}""") }
    }
    @Test fun rejectsObjectsMasqueradingAsNamesOrPaths() {
        assertThrows(IllegalArgumentException::class.java) { SnapshotParser.snapshot("""{"name":{"text":"device"}}""") }
        assertThrows(IllegalArgumentException::class.java) { SnapshotParser.snapshot("""{"transfers":[{"paths":[{"path":"/tmp/file"}]}]}""") }
    }
    @Test fun rejectsMalformedOversizedAndNegativeNumbers() {
        assertThrows(Exception::class.java) { SnapshotParser.snapshot("[") }
        assertThrows(IllegalArgumentException::class.java) { SnapshotParser.snapshot(" ".repeat(8 * 1024 * 1024 + 1)) }
        assertThrows(IllegalArgumentException::class.java) { SnapshotParser.snapshot("""{"transfers":[{"bytes":-1}]}""") }
    }
    @Test fun nullGoSlicesBecomeEmptyLists() {
        val state = SnapshotParser.snapshot("""{"peers":null,"transfers":null,"history":null,"notices":null}""")
        assertTrue(state.peers.isEmpty())
        assertTrue(state.notices.isEmpty())
    }
    @Test fun invitationCandidateRemainsUntrustedUntilCoreSaysOtherwise() {
        val peer = SnapshotParser.peerResult("""{"id":"device","fingerprint":"${"ab".repeat(32)}","trusted":false}""")
        assertFalse(peer.trusted)
        assertThrows(IllegalArgumentException::class.java) { SnapshotParser.peerResult("{}") }
    }
    @Test fun failedActionNeverLooksSuccessful() {
        assertThrows(IllegalArgumentException::class.java) { SnapshotParser.result("""{"ok":false,"message":"Fingerprint changed"}""") }
        assertThrows(IllegalArgumentException::class.java) { SnapshotParser.result("""{"ok":"true"}""") }
        assertEquals("job", SnapshotParser.result("""{"ok":true,"id":"job"}"""))
    }
}
