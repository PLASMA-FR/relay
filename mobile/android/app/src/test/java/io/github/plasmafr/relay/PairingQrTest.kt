package io.github.plasmafr.relay

import com.google.zxing.BarcodeFormat
import com.google.zxing.DecodeHintType
import com.google.zxing.RGBLuminanceSource
import com.google.zxing.Result
import com.google.zxing.client.android.Intents
import com.google.zxing.qrcode.QRCodeWriter
import com.journeyapps.barcodescanner.DefaultDecoderFactory
import com.journeyapps.barcodescanner.MixedDecoder
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Decode actual QR pixels on the JVM; no camera, Android Bitmap, or mocked decoder. */
class PairingQrTest {
    private val invite = "relay://pair?v=1&name=desktop&address=100.64.0.5%3A7331&fingerprint=" + "abcdef0123456789".repeat(4)

    @Test fun configuredPairingScannerReadsLightAndDarkTerminalQrCodes() {
        val scanType = pairingScanOptions().moreExtras[Intents.Scan.SCAN_TYPE] as Int
        val decoder = DefaultDecoderFactory(listOf(BarcodeFormat.QR_CODE), null, null, scanType).createDecoder(emptyMap<DecodeHintType, Any>())
        assertTrue("Pairing scan must alternate normal and inverted camera frames", decoder is MixedDecoder)
        for (inverted in listOf(false, true, false)) {
            val frame = qrFrame(inverted)
            var decoded: Result? = null
            // MixedDecoder changes polarity every frame. Either polarity must decode within two.
            repeat(2) { if (decoded == null) decoded = decoder.decode(frame) }
            assertEquals("Could not decode ${if (inverted) "light-on-dark" else "dark-on-light"} QR", invite, decoded?.text)
        }
    }

    @Test fun darkTerminalFixtureReproducesNormalScannerFailure() {
        val decoder = DefaultDecoderFactory(listOf(BarcodeFormat.QR_CODE), null, null, Intents.Scan.NORMAL_SCAN).createDecoder(emptyMap<DecodeHintType, Any>())
        assertEquals(invite, decoder.decode(qrFrame(inverted = false))?.text)
        assertNull("This fixture must catch regression to normal-only scanning", decoder.decode(qrFrame(inverted = true)))
    }

    private fun qrFrame(inverted: Boolean): RGBLuminanceSource {
        val size = 384
        val matrix = QRCodeWriter().encode(invite, BarcodeFormat.QR_CODE, size, size)
        val pixels = IntArray(size * size) { index ->
            val dark = matrix[index % size, index / size] xor inverted
            if (dark) 0xFF000000.toInt() else 0xFFFFFFFF.toInt()
        }
        return RGBLuminanceSource(size, size, pixels)
    }
}
