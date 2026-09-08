package io.github.plasmafr.relay.data

import org.json.JSONArray
import org.json.JSONObject

/** The native bridge is a boundary: never coerce arbitrary JSON values into UI text or trust. */
internal object SnapshotParser {
    private const val MAX_JSON = 8 * 1024 * 1024
    private const val MAX_ROWS = 10_000
    private const val MAX_TEXT = 1024 * 1024

    fun snapshot(raw: String): RelayState {
        val value = json(raw)
        return RelayState(
            name = value.string("name"), fingerprint = value.string("fingerprint"),
            address = value.string("address"), running = value.boolean("running"),
            autoAccept = value.boolean("auto_accept"),
            peers = value.array("peers").objects(::peer),
            transfers = value.array("transfers").objects(::transfer),
            history = value.array("history").objects(::transfer),
            clipboard = value.string("clipboard"), clipboardTransferId = value.string("clipboard_transfer_id"),
            notices = value.array("notices").strings(), loading = false,
        )
    }

    fun peerResult(raw: String): Peer {
        val value = json(raw)
        // AddDevice/ImportInvite return a Peer, not a model.Result envelope.
        return peer(value).also { require(it.id.isNotBlank()) { "Device response has no identity" } }
    }

    fun result(raw: String): String {
        val value = json(raw)
        require(value.boolean("ok")) { value.string("message").ifBlank { "The device could not complete this action" } }
        return value.string("id")
    }

    private fun json(raw: String): JSONObject {
        require(raw.length <= MAX_JSON) { "Device response exceeds the size limit" }
        return JSONObject(raw)
    }

    private fun peer(o: JSONObject) = Peer(
        id = o.string("id"), name = o.string("name"), hostname = o.string("hostname"),
        address = o.string("address"), os = o.string("os"), arch = o.string("arch"),
        version = o.string("version"), fingerprint = o.string("fingerprint"), error = o.string("error"),
        online = o.boolean("online"), relay = o.boolean("relay"), trusted = o.boolean("trusted"),
        protocol = o.number("protocol").toInt(), latencyMs = o.number("latency_ms").toLong(),
    )

    private fun transfer(o: JSONObject) = Transfer(
        id = o.string("id"), name = o.string("name"), peer = o.string("peer"), peerId = o.string("peer_id"),
        direction = o.string("direction"), kind = o.string("kind"), status = o.string("status"),
        error = o.string("error"), destination = o.string("destination"),
        bytes = o.number("bytes").toLong(), total = o.number("total").toLong(),
        speed = o.number("speed").toDouble(), etaSeconds = o.number("eta_seconds").toDouble(),
        verified = o.boolean("verified"), paths = o.array("paths").strings(),
    )

    private fun JSONObject.string(key: String): String {
        val value = opt(key)
        if (value == null || value === JSONObject.NULL) return ""
        require(value is String && value.length <= MAX_TEXT) { "Invalid text field: $key" }
        return value
    }
    private fun JSONObject.boolean(key: String): Boolean {
        val value = opt(key)
        if (value == null || value === JSONObject.NULL) return false
        require(value is Boolean) { "Invalid boolean field: $key" }
        return value
    }
    private fun JSONObject.number(key: String): Number {
        val value = opt(key)
        if (value == null || value === JSONObject.NULL) return 0
        require(value is Number && value.toDouble().isFinite() && value.toDouble() >= 0) { "Invalid number field: $key" }
        return value
    }
    private fun JSONObject.array(key: String): JSONArray {
        val value = opt(key)
        if (value == null || value === JSONObject.NULL) return JSONArray()
        require(value is JSONArray && value.length() <= MAX_ROWS) { "Invalid list field: $key" }
        return value
    }
    private fun <T> JSONArray.objects(parse: (JSONObject) -> T): List<T> = List(length()) { index ->
        val value = get(index)
        require(value is JSONObject) { "Invalid list entry" }
        parse(value)
    }
    private fun JSONArray.strings(): List<String> = List(length()) { index ->
        val value = get(index)
        require(value is String && value.length <= MAX_TEXT) { "Invalid text list entry" }
        value
    }
}
