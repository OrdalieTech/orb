package tech.ordalie.orb.core

import android.content.*
import androidx.core.content.FileProvider
import java.io.File
import java.net.*
import java.security.MessageDigest
import kotlinx.coroutines.*
import org.json.JSONObject

/**
 * The app is Orb, so it updates from Orb's GitHub releases like `orb update` does: the release
 * carries `orb_<version>_android_arm64.apk` and its sha256. Android installs it only over an app
 * signed with the same key, so a tampered APK fails twice.
 */
object Release {
    private const val LATEST = "https://api.github.com/repos/OrdalieTech/orb/releases/latest"
    private const val DOWNLOAD = "https://github.com/OrdalieTech/orb/releases/download"

    /** The latest released version ("0.13.1"), or null when GitHub cannot be reached. */
    suspend fun latest(): String? = withContext(Dispatchers.IO) {
        runCatching { JSONObject(fetch(LATEST).decodeToString()).optString("tag_name").removePrefix("v").ifEmpty { null } }.getOrNull()
    }

    /** Whether [candidate] is a later release than [current] (plain x.y.z; a dev build is always older). */
    fun newer(candidate: String, current: String): Boolean {
        fun parts(v: String) = v.substringBefore('-').split('.').map { it.toIntOrNull() ?: 0 }.let { it + List(3 - it.size.coerceAtMost(3)) { 0 } }
        val (a, b) = parts(candidate) to parts(current)
        return (0 until 3).firstOrNull { a[it] != b[it] }?.let { a[it] > b[it] } ?: false
    }

    /** Downloads and verifies the release's APK, then hands it to Android's installer. */
    suspend fun install(context: Context, version: String) {
        val name = "orb_${version}_android_arm64.apk"
        val file = withContext(Dispatchers.IO) {
            val apk = fetch("$DOWNLOAD/v$version/$name")
            val want = fetch("$DOWNLOAD/v$version/$name.sha256").decodeToString().trim().substringBefore(' ')
            val got = MessageDigest.getInstance("SHA-256").digest(apk).joinToString("") { "%02x".format(it) }
            check(got == want) { "the downloaded app failed its checksum" }
            File(context.cacheDir, "updates").apply { mkdirs(); listFiles()?.forEach(File::delete) }.resolve(name).apply { writeBytes(apk) }
        }
        val uri = FileProvider.getUriForFile(context, context.packageName + ".updates", file)
        context.startActivity(Intent(Intent.ACTION_VIEW).setDataAndType(uri, "application/vnd.android.package-archive")
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK))
    }
}

/** GET over HTTPS, the whole body; Orb's releases and the Linux come from GitHub. */
fun fetch(url: String): ByteArray {
    val c = URL(url).openConnection() as HttpURLConnection
    c.connectTimeout = 15_000; c.readTimeout = 120_000
    c.setRequestProperty("User-Agent", "orb-android")
    try {
        check(c.responseCode == 200) { "GitHub returned ${c.responseCode} for ${url.substringAfterLast('/')}" }
        return c.inputStream.use { it.readBytes() }
    } finally { c.disconnect() }
}
