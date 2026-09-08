package io.github.plasmafr.relay.platform

import java.net.InetAddress
import org.junit.Assert.*
import org.junit.Test

class TailscaleNetworkTest {
    @Test fun acceptsOnlyLiteralTailnetAddressRanges() {
        listOf("100.64.0.1", "100.127.255.254", "fd7a:115c:a1e0::1").forEach {
            assertTrue(it, TailscaleNetwork.isTailscaleAddress(InetAddress.getByName(it)))
        }
        listOf("0.0.0.0", "127.0.0.1", "100.63.255.255", "100.128.0.1", "192.168.1.1", "::", "::1", "fd7a:115c:a1e1::1", "2001:db8::1").forEach {
            assertFalse(it, TailscaleNetwork.isTailscaleAddress(InetAddress.getByName(it)))
        }
    }
}
