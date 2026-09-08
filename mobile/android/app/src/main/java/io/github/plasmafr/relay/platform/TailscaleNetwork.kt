package io.github.plasmafr.relay.platform

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import java.net.InetAddress

/** Android supplies VPN link addresses; native interface enumeration is intentionally unused. */
internal class TailscaleNetwork(context: Context) : AutoCloseable {
    private val manager = context.getSystemService(ConnectivityManager::class.java)
    private val addresses = mutableMapOf<Network, List<String>>()
    private val mutableAddress = MutableStateFlow("")
    val address: StateFlow<String> = mutableAddress
    private var registered = false
    private val callback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) = changed(network, manager.getLinkProperties(network))
        override fun onLinkPropertiesChanged(network: Network, properties: LinkProperties) = changed(network, properties)
        override fun onLost(network: Network) { synchronized(addresses) { addresses.remove(network); publish() } }
    }

    fun start() {
        val request = NetworkRequest.Builder().removeCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .addTransportType(NetworkCapabilities.TRANSPORT_VPN).build()
        manager.registerNetworkCallback(request, callback)
        registered = true
        manager.allNetworks.forEach { network ->
            if (manager.getNetworkCapabilities(network)?.hasTransport(NetworkCapabilities.TRANSPORT_VPN) == true) {
                changed(network, manager.getLinkProperties(network))
            }
        }
    }
    private fun changed(network: Network, properties: LinkProperties?) {
        synchronized(addresses) {
            addresses[network] = properties?.linkAddresses.orEmpty().mapNotNull { link ->
                link.address.takeIf(::isTailscaleAddress)?.hostAddress?.substringBefore('%')
            }
            publish()
        }
    }
    private fun publish() {
        mutableAddress.value = addresses.values.flatten().sortedWith(compareBy<String> { ':' in it }.thenBy { it }).firstOrNull().orEmpty()
    }
    override fun close() {
        if (registered) { manager.unregisterNetworkCallback(callback); registered = false }
        synchronized(addresses) { addresses.clear(); mutableAddress.value = "" }
    }

    companion object {
        internal fun isTailscaleAddress(address: InetAddress): Boolean {
            val b = address.address.map { it.toInt() and 255 }
            return (b.size == 4 && b[0] == 100 && b[1] in 64..127) ||
                (b.size == 16 && b.take(6) == listOf(0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe0))
        }
    }
}
