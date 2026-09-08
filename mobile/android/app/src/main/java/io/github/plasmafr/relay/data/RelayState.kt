package io.github.plasmafr.relay.data

data class RelayState(
    val name: String = "", val fingerprint: String = "", val address: String = "",
    val running: Boolean = false, val autoAccept: Boolean = false,
    val peers: List<Peer> = emptyList(), val transfers: List<Transfer> = emptyList(),
    val history: List<Transfer> = emptyList(), val clipboard: String = "",
    val clipboardTransferId: String = "", val notices: List<String> = emptyList(),
    val loading: Boolean = true, val error: String = "", val availableRequested: Boolean = false,
)

data class Peer(
    val id: String = "", val name: String = "", val hostname: String = "", val address: String = "",
    val os: String = "", val arch: String = "", val version: String = "", val fingerprint: String = "",
    val error: String = "", val online: Boolean = false, val relay: Boolean = false,
    val trusted: Boolean = false, val protocol: Int = 0, val latencyMs: Long = 0,
)

data class Transfer(
    val id: String = "", val name: String = "", val peer: String = "", val peerId: String = "",
    val direction: String = "", val kind: String = "", val status: String = "",
    val error: String = "", val destination: String = "", val bytes: Long = 0, val total: Long = 0,
    val speed: Double = 0.0, val etaSeconds: Double = 0.0, val verified: Boolean = false,
    val paths: List<String> = emptyList(),
)
