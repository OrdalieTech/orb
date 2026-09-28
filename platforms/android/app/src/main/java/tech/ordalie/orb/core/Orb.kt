package tech.ordalie.orb.core

import android.content.Context
import android.os.Build
import kotlinx.coroutines.CoroutineScope
import java.io.File

data class Plugin(val name: String, val on: Boolean, val about: String)

/** A stored conversation on this phone, as `orb storage sessions` lists it. */
data class Past(val id: String, val title: String, val modified: Long, val messages: Int)

/** Where the Orb core lives on this device and how it is started. Everything else is Orb's own CLI. */
class Orb(private val context: Context) {
    val binary: String = File(context.applicationInfo.nativeLibraryDir, "liborb.so").path
    val home: File = context.filesDir
    val workspace: File = File(home, "workspace").apply { mkdirs() }
    /** The phone's Linux, where the agent's commands run once it is installed. */
    val linux = Linux(context)
    /** Where the core works: the Linux home once it exists, the app's workspace before. */
    val cwd: File get() = if (linux.ready) linux.home else workspace
    val device: String = (Build.MODEL ?: "android").lowercase().replace(Regex("[^a-z0-9-]"), "-").take(24)
    private val prefs = context.getSharedPreferences("orb", Context.MODE_PRIVATE)

    /** Provider keys stay in the app sandbox and reach Orb as the environment variables it already reads. */
    fun key(env: String): String = prefs.getString(env, "") ?: ""
    fun setKey(env: String, value: String) = prefs.edit().putString(env, value.trim()).apply()
    /** Conversation text size in sp: pinch the chat or /text to change it; it stays. */
    private val chat = androidx.compose.runtime.mutableFloatStateOf(prefs.getFloat("chat", 15f))
    var chatSize: Float
        get() = chat.floatValue
        set(value) { chat.floatValue = value.coerceIn(12f, 21f); prefs.edit().putFloat("chat", chat.floatValue).apply() }

    /** Small facts the app learns and keeps, like what a Bridge peer is called. */
    fun recall(key: String): String = prefs.getString("k:$key", "") ?: ""
    fun remember(key: String, value: String) = prefs.edit().putString("k:$key", value).apply()
    /** The model and reasoning last chosen on this phone: every new session starts with them. */
    var model: String
        get() = prefs.getString("model", "") ?: ""
        set(value) = prefs.edit().putString("model", value).apply()
    var thinking: String
        get() = prefs.getString("thinking", "") ?: ""
        set(value) = prefs.edit().putString("thinking", value).apply()

    fun env(): Map<String, String> = buildMap {
        put("HOME", home.path)
        put("TMPDIR", context.cacheDir.path)
        put("PATH", "/system/bin:/system/xbin:/vendor/bin")
        put("TERM", "dumb")
        put("ORB_CLIENT", "android")
        putAll(linux.env())
        PROVIDERS.forEach { (env, _) -> key(env).takeIf { it.isNotEmpty() }?.let { put(env, it) } }
    }

    fun lines(scope: CoroutineScope, tag: String, vararg args: String) =
        Lines(scope, listOf(binary) + args, ::env, ::cwd, tag)

    /** Runs one orb command to completion; [stdin] lines are written up front. */
    fun run(vararg args: String, stdin: String? = null): Pair<Int, String> {
        val p = ProcessBuilder(listOf(binary) + args).directory(workspace).redirectErrorStream(true)
            .apply { environment().putAll(env()) }.start()
        stdin?.let { p.outputStream.bufferedWriter().use { w -> w.write(it) } } ?: p.outputStream.close()
        val out = p.inputStream.bufferedReader().readText()
        return p.waitFor() to out
    }

    /** This phone's conversations, newest first. */
    fun sessions(): List<Past> = run("storage", "sessions").second.lineSequence().mapNotNull { line ->
        runCatching { org.json.JSONObject(line) }.getOrNull()?.takeIf { it.optInt("messages") > 0 }?.let {
            Past(it.getString("id"), it.optString("name").ifBlank { it.optString("first").lineSequence().firstOrNull().orEmpty() }.take(80).ifBlank { "untitled" }, it.optLong("modified"), it.optInt("messages"))
        }
    }.toList()

    /** Orb's bundled plugins, straight from `orb plugins list`. */
    fun plugins(): List<Plugin> = run("plugins", "list").second.lines()
        .mapNotNull { l -> l.split('\t').takeIf { it.size >= 3 }?.let { Plugin(it[0], it[1] == "on", it[2]) } }
    fun plugin(name: String, on: Boolean) = run("plugins", if (on) "enable" else "disable", name).first == 0

    /** The permissions plugin's mode: auto approves quietly, enforce asks through an interrupt. */
    var permissions: String
        get() = prefs.getString("permissions", "auto") ?: "auto"
        set(value) { if (run("plugins", "set", "permissions", "mode", "\"$value\"").first == 0) prefs.edit().putString("permissions", value).apply() }

    /** Adds a provider through Orb's own models.json document (`orb storage config export|import`). */
    fun addProvider(name: String, api: String, baseUrl: String, apiKey: String, models: List<String>): String? = config("models.json") { doc ->
        val providers = doc.optJSONObject("providers") ?: org.json.JSONObject().also { doc.put("providers", it) }
        providers.put(name, org.json.JSONObject().put("api", api).put("baseUrl", baseUrl).apply {
            if (apiKey.isNotEmpty()) put("apiKey", apiKey)
            // Third-party OpenAI-compatible endpoints (Google, Mistral, local servers…) reject OpenAI's `store`.
            if (api == "openai-completions") put("compat", org.json.JSONObject().put("supportsStore", false))
        }
            .put("models", org.json.JSONArray(models.map { id ->
                org.json.JSONObject().put("id", id).put("name", id).put("reasoning", false).put("input", org.json.JSONArray(listOf("text"))).put("contextWindow", 128000).put("maxTokens", 8192)
            })))
    }

    /** Edits one of Orb's config documents (`orb storage config export|import`); an error's text, or null. */
    private fun config(name: String, edit: (org.json.JSONObject) -> Unit): String? {
        val file = File(context.cacheDir, name)
        run("storage", "config", "export", name, file.path).let { (code, out) -> if (code != 0) return out.trim() }
        val doc = runCatching { org.json.JSONObject(file.readText()) }.getOrDefault(org.json.JSONObject())
        edit(doc)
        file.writeText(doc.toString(2))
        return run("storage", "config", "import", name, file.path).let { (code, out) -> file.delete(); if (code == 0) null else out.trim() }
    }

    /** A phone starts with the plugins that make a conversation interactive; the owner changes them in Plugins. */
    fun seed() {
        if (!prefs.getBoolean("seeded", false)) {
            listOf("questions", "tasks", "permissions").forEach { plugin(it, true) }
            prefs.edit().putBoolean("seeded", true).apply()
        }
        // The agent's bash tool runs in the Linux: its launcher is the shell Orb starts for commands.
        // It moves with every app update (the lib directory does), so it is written again then.
        if (linux.ready && prefs.getString("seeded:shell", "") != linux.launcher &&
            config("settings.json") { it.put("shellPath", linux.launcher) } == null) prefs.edit().putString("seeded:shell", linux.launcher).apply()
        if (linux.ready) linux.brief(File(home, ".pi/agent"))
        // A phone loses its network for minutes at a time (tunnels, lifts): provider calls keep
        // retrying for about four minutes instead of the desktop's fourteen seconds.
        if (!prefs.getBoolean("seeded:retry", false) && config("settings.json") { it.put("retry", org.json.JSONObject().put("enabled", true).put("maxRetries", 8).put("baseDelayMs", 2000)) } == null)
            prefs.edit().putBoolean("seeded:retry", true).apply()
    }

    companion object {
        val PROVIDERS = listOf(
            "ANTHROPIC_API_KEY" to "anthropic", "OPENAI_API_KEY" to "openai", "GEMINI_API_KEY" to "google",
            "OPENROUTER_API_KEY" to "openrouter", "XAI_API_KEY" to "xai", "MISTRAL_API_KEY" to "mistral",
        )
    }
}
