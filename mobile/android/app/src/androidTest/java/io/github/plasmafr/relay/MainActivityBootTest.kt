package io.github.plasmafr.relay

import android.graphics.Bitmap
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import java.io.File

/** Actual app startup and screenshot, with the real repository and native identity. */
class MainActivityBootTest {
    @get:Rule val compose = createAndroidComposeRule<MainActivity>()

    @Test fun realAppLoadsNativeIdentityAndStartsUnavailable() {
        val repository = (compose.activity.application as RelayApplication).repository
        compose.waitUntil(timeoutMillis = 20_000) { !repository.state.value.loading }
        val state = repository.state.value
        assertEquals("Native startup should have no error", "", state.error)
        assertTrue(state.fingerprint.matches(Regex("[a-f0-9]{64}")))
        assertFalse("Receiving must be explicitly enabled", state.running)
        compose.onNodeWithText("Your devices.").assertIsDisplayed()
        compose.onNodeWithTag("availability").assertIsDisplayed()
        compose.waitForIdle()
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val bitmap = requireNotNull(instrumentation.uiAutomation.takeScreenshot())
        val output = File(instrumentation.targetContext.getExternalFilesDir("screenshots"), "native-app-startup.png")
        output.parentFile?.mkdirs()
        output.outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        bitmap.recycle()
    }
}
