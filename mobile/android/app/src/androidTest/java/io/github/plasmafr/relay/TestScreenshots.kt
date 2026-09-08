package io.github.plasmafr.relay

import android.content.ContentValues
import android.graphics.Bitmap
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File

/** Test artifacts go into shared media so Gradle's app uninstall cannot remove them. */
internal fun saveTestScreenshot(name: String) {
    require(name.matches(Regex("[a-z0-9-]+\\.png")))
    val instrumentation = InstrumentationRegistry.getInstrumentation()
    val context = instrumentation.targetContext
    val bitmap = requireNotNull(instrumentation.uiAutomation.takeScreenshot())
    try {
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
