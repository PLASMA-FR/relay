package io.github.plasmafr.relay.platform

import java.io.File
import java.nio.file.Files
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

class PrivateFilesTest {
    @get:Rule val temporary = TemporaryFolder()
    @Test fun onlyCanonicalDescendantsCanBeSharedOrExported() {
        val root = temporary.newFolder("inbox")
        val child = File(root, "safe.txt").apply { writeText("safe") }
        assertEquals(child, PrivateFiles.confinedFile(root, child.path))
        assertThrows(IllegalArgumentException::class.java) { PrivateFiles.confinedFile(root, root.path) }
        assertThrows(IllegalArgumentException::class.java) { PrivateFiles.confinedFile(root, "relative.txt") }
        assertThrows(IllegalArgumentException::class.java) { PrivateFiles.confinedFile(root, File(root, "../secret").path) }
        assertThrows(IllegalArgumentException::class.java) { PrivateFiles.confinedFile(root, root.path + "-other/file") }
    }
    @Test fun symlinkCannotEscapeInbox() {
        val root = temporary.newFolder("inbox")
        val outside = temporary.newFile("secret")
        val link = File(root, "link")
        Files.createSymbolicLink(link.toPath(), outside.toPath())
        assertThrows(IllegalArgumentException::class.java) { PrivateFiles.confinedFile(root, link.path) }
    }
    @Test fun maliciousProviderNamesNeverBecomePaths() {
        for (name in listOf("../../secret", "a\\b", "\u0000\nfile", ".", "..", "")) {
            val safe = PrivateFiles.safeName(name)
            assertTrue(safe.isNotBlank())
            assertFalse(safe.contains('/'))
            assertFalse(safe.contains('\\'))
            assertFalse(safe.any { it.code < 32 })
            assertFalse(safe == "." || safe == "..")
        }
    }
}
