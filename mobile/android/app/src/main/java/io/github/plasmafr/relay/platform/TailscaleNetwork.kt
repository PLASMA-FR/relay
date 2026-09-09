package io.github.plasmafr.relay.platform

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import java.net.InetAddress

/**
 * Observe the active VPN for this app. An installed VPN or an unrelated VPN network is
 * insufficient (Relay may be excluded by split tunneling). Android does not expose a
 * public way to verify the VPN provider: the user must connect Tailscale as their VPN.
 */
internal class TailscaleNetwork(context: Context) : AutoCloseable {
    internal data class Session(val address: String = "", val generation: Long = 0)

    private val manager = context.getSystemService(ConnectivityManager::class.java)
    private val lock = Any()
    private val mutableSession = MutableStateFlow(Session())
    val session: StateFlow<Session> = mutableSession
    private var currentNetwork: Network? = null
    private var registered = false
    private val callback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) = refresh(network)
        override fun onCapabilitiesChanged(network: Network, capabilities: NetworkCapabilities) =
            refresh(network, capabilities = capabilities)
        override fun onLinkPropertiesChanged(network: Network, properties: LinkProperties) =
            refresh(network, properties = properties)
        override fun onLost(network: Network) {
            synchronized(lock) {
                if (network == currentNetwork) publish(null, "")
            }
        }
    }

    fun start() {
        synchronized(lock) {
            if (registered) return
            manager.registerDefaultNetworkCallback(callback)
            registered = true
            refresh()
        }
    }

    private fun refresh(network: Network? = null, capabilities: NetworkCapabilities? = null, properties: LinkProperties? = null) {
        synchronized(lock) {
            if (!registered) return
            val active = manager.activeNetwork
            // Callback updates from a replaced default network must not authorize it.
            val caps = if (active != null && active == network && capabilities != null) capabilities
                else active?.let(manager::getNetworkCapabilities)
            val links = if (active != null && active == network && properties != null) properties
                else active?.let(manager::getLinkProperties)
            val address = selectAddress(
                active != null && caps?.hasTransport(NetworkCapabilities.TRANSPORT_VPN) == true,
                links?.linkAddresses.orEmpty().map { it.address },
            )
            publish(active.takeIf { address.isNotEmpty() }, address)
        }
    }

    private fun publish(network: Network?, address: String) {
        if (currentNetwork != network || mutableSession.value.address != address) {
            currentNetwork = network
            // Preserve a network replacement even when StateFlow conflates an intervening loss.
            mutableSession.value = Session(address, mutableSession.value.generation + 1)
        }
    }

    override fun close() {
        synchronized(lock) {
            if (registered) { manager.unregisterNetworkCallback(callback); registered = false }
            publish(null, "")
        }
    }

    companion object {
        internal fun selectAddress(activeVpn: Boolean, addresses: List<InetAddress>): String {
            if (!activeVpn) return ""
            return addresses.filter(::isTailscaleAddress).mapNotNull { it.hostAddress?.substringBefore('%') }
                .sortedWith(compareBy<String> { ':' in it }.thenBy { it }).firstOrNull().orEmpty()
        }

        internal fun isTailscaleAddress(address: InetAddress): Boolean {
            val b = address.address.map { it.toInt() and 255 }
            return (b.size == 4 && b[0] == 100 && b[1] in 64..127) ||
                (b.size == 16 && b.take(6) == listOf(0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe0))
        }
    }
}
