package io.github.plasmafr.relay.platform

import java.net.InetAddress
import org.junit.Assert.*
import org.junit.Test

class TailscaleNetworkTest {
    @Test fun addressDoesNotAuthorizeWithoutActiveVpnForThisApp() {
        val addresses = listOf(InetAddress.getByName("100.64.0.7"), InetAddress.getByName("fd7a:115c:a1e0::7"))
        assertEquals("", TailscaleNetwork.selectAddress(false, addresses))
        assertEquals("100.64.0.7", TailscaleNetwork.selectAddress(true, addresses))
        assertEquals("", TailscaleNetwork.selectAddress(true, listOf(InetAddress.getByName("192.168.1.7"))))
        assertEquals("", TailscaleNetwork.selectAddress(true, emptyList()))
    }

    @Test fun vpnAddressSelectionSupportsIpv6AndDeterministicIpv4Preference() {
        val v6 = InetAddress.getByName("fd7a:115c:a1e0::7")
        val v4 = InetAddress.getByName("100.64.0.7")
        assertEquals(v6.hostAddress, TailscaleNetwork.selectAddress(true, listOf(v6)))
        assertEquals(v4.hostAddress, TailscaleNetwork.selectAddress(true, listOf(v6, v4)))
        assertEquals(v4.hostAddress, TailscaleNetwork.selectAddress(true, listOf(v4, v6)))
    }

    @Test fun acceptsOnlyLiteralTailnetAddressRanges() {
        listOf("100.64.0.1", "100.127.255.254", "fd7a:115c:a1e0::1").forEach {
            assertTrue(it, TailscaleNetwork.isTailscaleAddress(InetAddress.getByName(it)))
        }
        listOf("0.0.0.0", "127.0.0.1", "100.63.255.255", "100.128.0.1", "192.168.1.1", "::", "::1", "fd7a:115c:a1e1::1", "2001:db8::1").forEach {
            assertFalse(it, TailscaleNetwork.isTailscaleAddress(InetAddress.getByName(it)))
        }
    }
}
