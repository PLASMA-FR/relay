package io.github.plasmafr.relay.service

import io.github.plasmafr.relay.data.Transfer

internal data class OfferNotice(val id: String, val name: String, val sender: String, val bytes: Long)
internal data class OfferChanges(val show: List<OfferNotice>, val dismiss: Set<String>)

/** Progress updates do not re-alert. Only new offers or changed offer details produce notifications. */
internal class IncomingOffers {
    private var visible = emptyMap<String, OfferNotice>()
    fun update(transfers: List<Transfer>, suppressed: Set<String> = emptySet()): OfferChanges {
        val next = transfers.asSequence().filter { pending(it) && it.id !in suppressed }
            .associate { it.id to OfferNotice(it.id, label(it.name, "Incoming transfer"), label(it.peer, "Your device"), it.total.coerceAtLeast(0)) }
        val changes = OfferChanges(next.values.filter { visible[it.id] != it }, visible.keys - next.keys)
        visible = next
        return changes
    }
    fun clear(): Set<String> = visible.keys.also { visible = emptyMap() }
    companion object {
        fun pending(transfer: Transfer): Boolean = transfer.id.isNotBlank() && transfer.id.length <= 512 &&
            transfer.direction == "receive" && transfer.status == "offered"
        private fun label(raw: String, fallback: String): String = raw
            .replace(Regex("[\\p{Cntrl}\\p{Cf}]"), " ").trim().take(120).ifBlank { fallback }
    }
}
