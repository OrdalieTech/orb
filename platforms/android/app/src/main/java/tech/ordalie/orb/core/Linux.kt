package tech.ordalie.orb.core

import android.content.Context
import android.os.Environment
import android.system.Os
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import java.util.zip.ZipInputStream

/**
 * Orb's Linux: Termux's base system unpacked in the app's files and run through proot, which ships
 * in the APK with its launcher (liblinux.so). The agent's bash tool and the terminal start that
 * launcher, so every command runs in a real Linux with apt, and the owner's storage in ~/storage.
 * Nothing of it is visible: the app installs it by itself the first time it starts.
 */
class Linux(private val context: Context) {
    /** Mapped to /data/data/com.termux inside, where Termux's packages expect to live. */
    val root = File(context.filesDir, "linux")
    private val usr = File(root, "files/usr")
    val home = File(root, "files/home")
    val launcher: String = File(context.applicationInfo.nativeLibraryDir, "liblinux.so").path
    val ready get() = File(usr, ".orb-installed").exists()
    /** What the setup is doing, for the one line Home shows while it runs; empty when idle. */
    var state by mutableStateOf("")
    /** Whether the phone's files are there, as ~/storage/shared. */
    var storage by mutableStateOf(shared())

    /** The environment every process that starts the launcher needs. */
    fun env(): Map<String, String> = if (ready) mapOf("ORB_LINUX" to root.path) else emptyMap()

    /** Installs Termux's latest base system, checked against the sha256 GitHub publishes for it. */
    suspend fun install(): Boolean = withContext(Dispatchers.IO) {
        if (!installing.compareAndSet(false, true)) return@withContext false
        runCatching {
            state = "setting up Linux · downloading"
            val release = JSONObject(fetch(LATEST).decodeToString())
            val asset = release.getJSONArray("assets").let { a -> (0 until a.length()).map(a::getJSONObject).first { it.optString("name") == "bootstrap-aarch64.zip" } }
            val zip = File(context.cacheDir, "bootstrap.zip")
            zip.writeBytes(fetch(asset.getString("browser_download_url")))
            val sha = MessageDigest.getInstance("SHA-256").digest(zip.readBytes()).joinToString("") { "%02x".format(it) }
            check(asset.optString("digest") == "sha256:$sha") { "the download failed its checksum" }
            state = "setting up Linux · unpacking"
            val staging = File(root, "files/usr.new").apply { deleteRecursively(); mkdirs() }
            unpack(zip, staging)
            zip.delete()
            usr.deleteRecursively()
            check(staging.renameTo(usr)) { "could not move the new system into place" }
            home.mkdirs(); File(usr, "tmp").mkdirs() // proot's and the system's scratch space
            state = "setting up Linux · configuring packages"
            File(usr, ".orb-installed").writeText(release.optString("tag_name")) // the launcher needs ORB_LINUX set
            val (code, out) = run("bash /data/data/com.termux/files/usr/etc/termux/termux-bootstrap/second-stage/termux-bootstrap-second-stage.sh")
            if (code != 0) android.util.Log.w("orb-linux", out.takeLast(2000))
            linkStorage()
            state = ""
            true
        }.getOrElse { state = "Linux setup failed · " + (it.message ?: it.javaClass.simpleName) + " · tap to retry"; File(usr, ".orb-installed").delete(); false }
            .also { installing.set(false) }
    }
    private val installing = java.util.concurrent.atomic.AtomicBoolean(false)

    /** ~/storage/shared is the phone's own storage, once Orb may read it (all-files access). */
    fun linkStorage() {
        storage = shared()
        if (!ready || !storage) return
        val link = File(home, "storage/shared")
        if (link.exists()) return
        link.parentFile?.mkdirs()
        runCatching { Os.symlink(Environment.getExternalStorageDirectory().path, link.path) }
    }

    /** All-files access, which Android has asked for since 11; Android 10 keeps the phone's files to itself. */
    private fun shared() = android.os.Build.VERSION.SDK_INT >= 30 && Environment.isExternalStorageManager()

    /** Runs one command in the Linux and returns its exit code and output. */
    fun run(command: String): Pair<Int, String> {
        val p = ProcessBuilder(launcher, "-lc", command).directory(home).redirectErrorStream(true)
            .apply { environment()["ORB_LINUX"] = root.path }.start()
        val out = p.inputStream.bufferedReader().readText()
        return p.waitFor() to out
    }

    /** Termux's layout: the bootstrap's files, its symlinks from SYMLINKS.txt, programs executable. */
    private fun unpack(zip: File, into: File) {
        val links = mutableListOf<Pair<String, String>>()
        ZipInputStream(zip.inputStream().buffered()).use { z ->
            generateSequence { z.nextEntry }.forEach { e ->
                val file = File(into, e.name)
                check(file.canonicalPath.startsWith(into.canonicalPath)) { "unsafe path in the archive: ${e.name}" }
                when {
                    // Read as bytes: a reader over the zip would close it.
                    e.name == "SYMLINKS.txt" -> z.readBytes().decodeToString().lines().forEach { l -> l.split('←').takeIf { it.size == 2 }?.let { links += it[0] to it[1] } }
                    e.isDirectory -> file.mkdirs()
                    else -> {
                        file.parentFile?.mkdirs()
                        file.outputStream().use { z.copyTo(it) }
                        if (EXECUTABLE.any { e.name.startsWith(it) }) Os.chmod(file.path, "700".toInt(8))
                    }
                }
            }
        }
        links.forEach { (target, path) -> File(into, path).let { it.parentFile?.mkdirs(); Os.symlink(target, it.path) } }
    }

    private fun fetch(url: String): ByteArray {
        val c = URL(url).openConnection() as HttpURLConnection
        c.connectTimeout = 15_000; c.readTimeout = 120_000
        c.setRequestProperty("User-Agent", "orb-android")
        try {
            check(c.responseCode == 200) { "GitHub returned ${c.responseCode}" }
            return c.inputStream.use { it.readBytes() }
        } finally { c.disconnect() }
    }

    companion object {
        private const val LATEST = "https://api.github.com/repos/termux/termux-packages/releases/latest"
        /** What Termux itself makes executable after unpacking its bootstrap. */
        private val EXECUTABLE = listOf("bin/", "libexec/", "lib/apt/apt-helper", "lib/apt/methods/")
    }
}
