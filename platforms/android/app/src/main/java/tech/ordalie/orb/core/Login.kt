package tech.ordalie.orb.core

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject

/** One way to sign in to a provider, as `orb login --json` lists it: the TUI's /login, row for row. */
data class Method(val id: String, val auth: String, val label: String, val about: String) {
    val account get() = auth == "oauth"
}

/** A provider, its sign-in methods, and whether Orb holds a credential for it and where from. */
data class Provider(val id: String, val name: String, val methods: List<Method>, val models: Int, val status: String?, val source: String) {
    val ready get() = status != null
    /** Where the credential lives, in words: an account, a key Orb stored, or the app's environment. */
    val holds get() = when {
        status == null -> ""
        status == "oauth" -> "account"
        source == "stored" -> "api key"
        source.matches(Regex("[A-Z0-9_, ]+")) -> "key in this app"
        else -> source.replace('_', ' ')
    }

    companion object {
        fun parse(lines: String): List<Provider> = lines.lineSequence().mapNotNull { runCatching { JSONObject(it) }.getOrNull()?.takeIf { j -> j.has("id") } }
            .groupBy { it.getString("id") }.map { (id, rows) ->
                val status = rows[0].optJSONObject("status")
                Provider(id, rows[0].optString("name", id), rows.map { Method(id, it.optString("auth"), it.optString("label"), it.optString("method")) },
                    rows[0].optInt("models"), status?.optString("type"), status?.optString("source").orEmpty())
            }
    }
}

/** Every provider Orb can sign in to, with its status: `orb login --json`. */
fun Orb.providers(): List<Provider> = Provider.parse(run("login", "--json").second)

/** What a sign-in is asking for: a menu, a line of text, a secret, or a pasted code or redirect URL. */
data class Prompt(val kind: String, val message: String, val placeholder: String, val options: List<Pair<String, String>>)

/**
 * `orb login --json <provider> <method>`: the TUI's sign-in flow, reported as JSON lines. A browser
 * sign-in redirects to Orb's own listener on this phone's localhost, so it completes in place.
 */
class Login(scope: CoroutineScope, orb: Orb, val method: Method, private val finished: (Boolean) -> Unit) {
    var state by mutableStateOf("starting") // starting · browser · code · asking · done · failed
    var url by mutableStateOf<String?>(null)
    var instructions by mutableStateOf("")
    var code by mutableStateOf<String?>(null)
    var prompt by mutableStateOf<Prompt?>(null)
    var detail by mutableStateOf("")
    private val process = ProcessBuilder(orb.binary, "login", "--json", method.id, method.auth).directory(orb.workspace).redirectErrorStream(true)
        .apply { environment().putAll(orb.env()) }.start()
    private val input = process.outputStream.bufferedWriter()

    init {
        scope.launch(Dispatchers.IO) {
            runCatching { process.inputStream.bufferedReader().forEachLine { line -> scope.launch(Dispatchers.Main) { read(line) } } }
            process.waitFor()
            withContext(Dispatchers.Main) { if (state != "done" && state != "failed") { state = "failed"; if (detail.isEmpty()) detail = "sign-in stopped" } }
        }
    }

    private fun read(line: String) {
        val e = runCatching { JSONObject(line) }.getOrNull() ?: return
        when (e.optString("type")) {
            "auth_url" -> { url = e.optString("url"); instructions = e.optString("instructions"); state = "browser" }
            "device_code" -> { code = e.optString("code"); url = e.optString("uri"); state = "code" }
            "progress", "info" -> detail = e.optString("message")
            "prompt" -> {
                val options = e.optJSONArray("options")?.let { a -> (0 until a.length()).map { a.getJSONObject(it).let { o -> o.optString("id") to o.optString("label") } } }.orEmpty()
                prompt = Prompt(e.optString("kind"), e.optString("message"), e.optString("placeholder"), options)
                // A pasted code is the fallback while the browser is out; anything else is the question now.
                if (e.optString("kind") != "manual_code") state = "asking"
            }
            "done" -> { state = "done"; prompt = null; finished(true) }
            "error" -> { state = "failed"; prompt = null; detail = e.optString("message"); finished(false) }
        }
    }

    /** Answers the open prompt: a menu option id, text, a secret, or a pasted code or redirect URL. */
    fun answer(text: String) {
        prompt = null
        if (state == "asking") state = "starting"
        runCatching { synchronized(input) { input.write(text.replace('\n', ' ')); input.newLine(); input.flush() } }
    }

    fun cancel() { runCatching { input.close() }; process.destroy() }
}
