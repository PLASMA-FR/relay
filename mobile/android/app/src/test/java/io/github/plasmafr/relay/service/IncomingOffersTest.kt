package io.github.plasmafr.relay.service

import io.github.plasmafr.relay.data.Transfer
import org.junit.Assert.*
import org.junit.Test

class IncomingOffersTest {
    private fun offer(id: String = "one") = Transfer(id = id, name = "photo.jpg", peer = "Laptop", direction = "receive", status = "offered", total = 123)
    @Test fun onlyIncomingPendingOffersGenerateApprovals() {
        val planner = IncomingOffers()
        val changes = planner.update(listOf(offer(), offer("sending").copy(direction = "send"), offer("done").copy(status = "completed")))
        assertEquals(listOf("one"), changes.show.map { it.id })
    }
    @Test fun repeatedSnapshotsAndProgressDoNotAlertAgain() {
        val planner = IncomingOffers()
        planner.update(listOf(offer()))
        assertTrue(planner.update(listOf(offer())).show.isEmpty())
        assertTrue(planner.update(listOf(offer().copy(bytes = 50, speed = 25.0))).show.isEmpty())
        assertEquals("renamed", planner.update(listOf(offer().copy(name = "renamed"))).show.single().name)
    }
    @Test fun acceptedRejectedRemovedAndSuppressedOffersDismiss() {
        for (next in listOf(listOf(offer().copy(status = "transferring")), listOf(offer().copy(status = "rejected")), emptyList())) {
            val planner = IncomingOffers()
            planner.update(listOf(offer()))
            assertEquals(setOf("one"), planner.update(next).dismiss)
        }
        val planner = IncomingOffers()
        planner.update(listOf(offer()))
        assertEquals(setOf("one"), planner.update(listOf(offer()), setOf("one")).dismiss)
        assertTrue(planner.update(listOf(offer()), setOf("one")).show.isEmpty())
        assertEquals("one", planner.update(listOf(offer())).show.single().id)
    }
    @Test fun labelsAreBoundedAndDoNotInjectLinesOrDirectionControls() {
        val planner = IncomingOffers()
        val notice = planner.update(listOf(offer().copy(name = "a\n\u202E" + "x".repeat(500), peer = "\u0000"))).show.single()
        assertTrue(notice.name.length <= 120)
        assertFalse(notice.name.contains('\n'))
        assertFalse(notice.name.contains('\u202E'))
        assertEquals("Your device", notice.sender)
    }
    @Test fun clearAndInvalidIdentitiesCannotLeaveActionableNotifications() {
        val planner = IncomingOffers()
        assertTrue(planner.update(listOf(offer(""), offer("x".repeat(513)))).show.isEmpty())
        planner.update(listOf(offer()))
        assertEquals(setOf("one"), planner.clear())
        assertTrue(planner.clear().isEmpty())
    }
}
