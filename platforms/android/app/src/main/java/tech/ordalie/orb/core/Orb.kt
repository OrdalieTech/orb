package tech.ordalie.orb.core

import android.content.Context
import android.os.Build
import java.io.File
import kotlinx.coroutines.CoroutineScope

data class Plugin(val name: String, val on: Boolean, val about: String)

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

    /** The environment of every orb the app starts, its Bridge and the Orbs that Bridge starts included. */
    fun env(): Map<String, String> = buildMap {
        put("HOME", home.path)
        put("TMPDIR", context.cacheDir.path)
        put("PATH", "/system/bin:/system/xbin:/vendor/bin")
        put("TERM", "dumb")
        put("ORB_CLIENT", "android")
        putAll(linux.env())
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

    /** Orb's bundled plugins, straight from `orb plugins list`. */
    fun plugins(): List<Plugin> = run("plugins", "list").second.lines()
        .mapNotNull { l -> l.split('\t').takeIf { it.size >= 3 }?.let { Plugin(it[0], it[1] == "on", it[2]) } }
    fun plugin(name: String, on: Boolean) = run("plugins", if (on) "enable" else "disable", name).first == 0

    /** The open conversations (the tabs) as JSON, kept so they come back when the app restarts. */
    var tabs: String
        get() = prefs.getString("tabs", "[]") ?: "[]"
        set(value) = prefs.edit().putString("tabs", value).apply()

    /** The permissions plugin's mode: auto approves quietly, enforce asks through an interrupt. */
    var permissions: String
        get() = prefs.getString("permissions", "auto") ?: "auto"
        set(value) { if (run("plugins", "set", "permissions", "mode", "\"$value\"").first == 0) prefs.edit().putString("permissions", value).apply() }

    /** Edits one of Orb's config documents (`orb storage config export|import`); an error's text, or null. */
    private fun config(name: String, edit: (org.json.JSONObject) -> Unit): String? {
        val file = File(context.cacheDir, name)
        run("storage", "config", "export", name, file.path).let { (code, out) -> if (code != 0) return out.trim() }
        val doc = runCatching { org.json.JSONObject(file.readText()) }.getOrDefault(org.json.JSONObject())
        edit(doc)
        file.writeText(doc.toString(2))
        return run("storage", "config", "import", name, file.path).let { (code, out) -> file.delete(); if (code == 0) null else out.trim() }
    }

    /**
     * A phone starts with the plugins that make a conversation interactive (the owner changes them
     * in Plugins) and keeps retrying a provider for about four minutes rather than the desktop's
     * fourteen seconds: it loses its network for minutes at a time, in tunnels and lifts.
     */
    fun seed() {
        if (!prefs.getBoolean("seeded", false) && listOf("questions", "tasks", "permissions", "titles").all { plugin(it, true) } &&
            config("settings.json") { it.put("retry", org.json.JSONObject().put("enabled", true).put("maxRetries", 8).put("baseDelayMs", 2000)) } == null)
            prefs.edit().putBoolean("seeded", true).apply()
        // The agent's bash tool runs in the Linux: its launcher is the shell Orb starts for commands.
        // It moves with every app update (the lib directory does), so it is written again then.
        if (linux.ready && prefs.getString("seeded:shell", "") != linux.launcher &&
            config("settings.json") { it.put("shellPath", linux.launcher) } == null) prefs.edit().putString("seeded:shell", linux.launcher).apply()
        if (linux.ready) linux.brief(File(home, ".pi/agent"))
    }
}
