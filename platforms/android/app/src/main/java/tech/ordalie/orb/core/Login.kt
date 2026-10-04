package tech.ordalie.orb.core

import androidx.compose.runtime.*
import kotlinx.coroutines.*
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

/** What a sign-in is asking for: a menu, a line of text, a secret, or a pasted code or redirect URL. */
data class Prompt(val kind: String, val message: String, val placeholder: String, val options: List<Pair<String, String>>)

/**
 * A sign-in a machine runs for this phone (host.login.*), this phone's own included: its link,
 * device code and questions come here, and the answers go back.
 */
class SignIn(private val bridge: Bridge, private val peer: String, private val method: Method) {
    @Volatile private var id = ""
    private var job: Job? = null
    private lateinit var scope: CoroutineScope

    /** Starts the flow; each JSON line it reports goes to [line], then [end] once it stopped. */
    fun start(scope: CoroutineScope, line: (String) -> Unit, end: () -> Unit) {
        this.scope = scope
        val failed = { message: String -> line(JSONObject().put("type", "error").put("message", message).toString()) }
        job = scope.launch(Dispatchers.IO) {
            try {
                val started = bridge.remote(peer, "host.login.start", JSONObject().put("provider", method.id).put("method", method.auth))
                started.optJSONObject("error")?.let { e ->
                    return@launch failed(if (e.optString("code") == "unauthorized") "that device does not let this phone sign it in" else e.optString("message"))
                }
                id = started.optJSONObject("result")?.optString("login_id").orEmpty()
                var cursor = 0
                while (isActive) {
                    val page = bridge.remote(peer, "host.login.poll", JSONObject().put("login_id", id).put("cursor", cursor))
                    val result = page.optJSONObject("result") ?: return@launch failed(page.optJSONObject("error")?.optString("message") ?: "the device stopped answering")
                    result.optJSONArray("events")?.let { a -> for (i in 0 until a.length()) line(a.get(i).toString()) }
                    cursor = result.optInt("cursor", cursor)
                    if (result.optBoolean("done")) break
                }
            } finally { end() }
        }
    }

    fun answer(text: String) {
        scope.launch(Dispatchers.IO) { bridge.remote(peer, "host.login.answer", JSONObject().put("login_id", id).put("value", text)) }
    }

    fun cancel() {
        job?.cancel()
        scope.launch(Dispatchers.IO) { bridge.remote(peer, "host.login.cancel", JSONObject().put("login_id", id)) }
    }
}

/** The TUI's sign-in flow, reported as JSON lines by a [SignIn]. */
class Login(private val scope: CoroutineScope, private val sign: SignIn, val method: Method, private val finished: (Boolean) -> Unit) {
    var state by mutableStateOf("starting") // starting · browser · code · asking · done · failed
    var url by mutableStateOf<String?>(null)
    var instructions by mutableStateOf("")
    var code by mutableStateOf<String?>(null)
    var prompt by mutableStateOf<Prompt?>(null)
    var detail by mutableStateOf("")

    init {
        sign.start(scope, { line -> scope.launch(Dispatchers.Main) { read(line) } }) {
            scope.launch(Dispatchers.Main) { if (state != "done" && state != "failed") { state = "failed"; if (detail.isEmpty()) detail = "sign-in stopped" } }
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
        sign.answer(text)
    }

    fun cancel() = sign.cancel()
}
