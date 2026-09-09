package io.github.plasmafr.relay

import android.content.ContentValues
import android.graphics.Bitmap
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File

/** Saves and consumes a rendered Compose bitmap; shared media survives test app uninstall. */
internal fun saveTestScreenshot(name: String, bitmap: Bitmap) {
    require(name.matches(Regex("[a-z0-9-]+\\.png")))
    val instrumentation = InstrumentationRegistry.getInstrumentation()
    val context = instrumentation.targetContext
    try {
        checkScreenshotHasContent(bitmap)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val resolver = context.contentResolver
            val metadata = ContentValues().apply {
                put(MediaStore.Images.Media.DISPLAY_NAME, name)
                put(MediaStore.Images.Media.MIME_TYPE, "image/png")
                put(MediaStore.Images.Media.RELATIVE_PATH, "${Environment.DIRECTORY_PICTURES}/RelayTests/")
                put(MediaStore.Images.Media.IS_PENDING, 1)
            }
            val collection = MediaStore.Images.Media.getContentUri(MediaStore.VOLUME_EXTERNAL_PRIMARY)
            val uri = requireNotNull(resolver.insert(collection, metadata)) { "Cannot create screenshot artifact" }
            try {
                requireNotNull(resolver.openOutputStream(uri, "w")).use { output ->
                    check(bitmap.compress(Bitmap.CompressFormat.PNG, 100, output)) { "Cannot encode screenshot" }
                }
                val published = ContentValues().apply { put(MediaStore.Images.Media.IS_PENDING, 0) }
                check(resolver.update(uri, published, null, null) == 1) { "Cannot publish screenshot artifact" }
            } catch (error: Throwable) {
                resolver.delete(uri, null, null)
                throw error
            }
        } else {
            val directory = requireNotNull(context.getExternalFilesDir("screenshots"))
            check(directory.mkdirs() || directory.isDirectory)
            File(directory, name).outputStream().use { output ->
                check(bitmap.compress(Bitmap.CompressFormat.PNG, 100, output)) { "Cannot encode screenshot" }
            }
        }
    } finally {
        bitmap.recycle()
    }
}

/** Reject a blank render instead of publishing a successful-looking screenshot artifact. */
private fun checkScreenshotHasContent(bitmap: Bitmap) {
    check(bitmap.width > 0 && bitmap.height > 0) { "Screenshot has no dimensions" }
    // Sampling the Compose content (without system bars) catches uniform white/black frames.
    // Check RGB range rather than absolute brightness so this works in both themes.
    var minRed = 255
    var minGreen = 255
    var minBlue = 255
    var maxRed = 0
    var maxGreen = 0
    var maxBlue = 0
    for (row in 0 until 64) {
        val y = ((row + 0.5) * bitmap.height / 64).toInt().coerceAtMost(bitmap.height - 1)
        for (column in 0 until 64) {
            val x = ((column + 0.5) * bitmap.width / 64).toInt().coerceAtMost(bitmap.width - 1)
            val pixel = bitmap.getPixel(x, y)
            val red = (pixel shr 16) and 255
            val green = (pixel shr 8) and 255
            val blue = pixel and 255
            minRed = minOf(minRed, red); maxRed = maxOf(maxRed, red)
            minGreen = minOf(minGreen, green); maxGreen = maxOf(maxGreen, green)
            minBlue = minOf(minBlue, blue); maxBlue = maxOf(maxBlue, blue)
        }
    }
    check(maxOf(maxRed - minRed, maxGreen - minGreen, maxBlue - minBlue) >= 32) {
        "Screenshot is blank or lacks visible content"
    }
}
