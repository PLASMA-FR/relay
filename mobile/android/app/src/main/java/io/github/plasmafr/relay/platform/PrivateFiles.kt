package io.github.plasmafr.relay.platform

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.OpenableColumns
import androidx.core.content.FileProvider
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import java.io.File
import java.util.UUID

internal class PrivateFiles(private val context: Context, val staging: File, val inbox: File) {
    data class Batch(val directory: File, val files: List<File>)

    suspend fun importFiles(uris: List<Uri>): Batch {
        require(uris.isNotEmpty() && uris.size <= 32) { "Choose between 1 and 32 files" }
        require(uris.all { it.scheme == "content" }) { "Choose files using the Android file picker" }
        val directory = File(staging, UUID.randomUUID().toString()).apply { check(mkdirs()) { "Cannot prepare file storage" } }
        try {
            var total = 0L
            val files = uris.mapIndexed { index, uri ->
                currentCoroutineContext().ensureActive()
                val name = context.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { cursor ->
                    if (cursor.moveToFirst()) cursor.getString(0) else null
                }
                // A provider's display name is untrusted and never a filesystem path.
                val target = File(directory, "${index + 1}-${safeName(name)}")
                val stream = context.contentResolver.openInputStream(uri) ?: error("Cannot read the selected file")
                stream.use { input -> target.outputStream().use { output ->
                    val buffer = ByteArray(64 * 1024)
                    while (true) {
                        currentCoroutineContext().ensureActive()
                        val count = input.read(buffer)
                        if (count < 0) break
                        total += count
                        require(total <= MAX_BATCH_BYTES) { "Selected files exceed the 2 GiB transfer limit" }
                        output.write(buffer, 0, count)
                    }
                } }
                target
            }
            return Batch(directory, files)
        } catch (error: Throwable) {
            directory.deleteRecursively()
            throw error
        }
    }

    suspend fun export(source: String, destination: Uri) {
        require(destination.scheme == "content") { "Select an export location using the file picker" }
        val file = inboxFile(source)
        file.inputStream().use { input ->
            val stream = context.contentResolver.openOutputStream(destination, "wt") ?: error("Cannot open the export location")
            stream.use { output ->
                val buffer = ByteArray(64 * 1024)
                while (true) {
                    currentCoroutineContext().ensureActive()
                    val count = input.read(buffer)
                    if (count < 0) break
                    output.write(buffer, 0, count)
                }
            }
        }
    }

    fun share(source: String) {
        val file = inboxFile(source)
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.files", file)
        val send = Intent(Intent.ACTION_SEND).apply {
            type = context.contentResolver.getType(uri) ?: "application/octet-stream"
            putExtra(Intent.EXTRA_STREAM, uri)
            clipData = android.content.ClipData.newRawUri(file.name, uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        context.startActivity(Intent.createChooser(send, "Share received file").addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    private fun inboxFile(path: String): File = confinedFile(inbox, path).also {
        require(it.isFile) { "The received file is no longer available" }
    }

    companion object {
        const val MAX_BATCH_BYTES = 2L * 1024 * 1024 * 1024
        internal fun safeName(value: String?): String = value.orEmpty()
            .replace(Regex("[\\p{Cntrl}/\\\\]"), "_")
            .trim().trim('.').take(120).ifBlank { "file" }

        internal fun confinedFile(root: File, path: String): File {
            require(path.isNotBlank() && File(path).isAbsolute) { "Invalid received file path" }
            val canonicalRoot = root.canonicalFile
            val file = File(path).canonicalFile
            require(file.path.startsWith(canonicalRoot.path + File.separator)) { "The file is outside Relay storage" }
            return file
        }
    }
}
