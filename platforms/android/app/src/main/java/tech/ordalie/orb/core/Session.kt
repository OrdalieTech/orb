package tech.ordalie.orb.core

import android.util.Base64
import androidx.compose.runtime.*
import java.security.SecureRandom
import kotlinx.coroutines.*
import org.json.*

/** An error reply's words: Bridge and [Lines] put an object with a message. */
fun JSONObject.problem(): String = optJSONObject("error")?.optString("message") ?: optString("error")

/** Bridge IDs are 16 random bytes, base64url without padding (protocol.ValidID). */
fun newId(): String = Base64.encodeToString(ByteArray(16).also(SecureRandom()::nextBytes), Base64.URL_SAFE or Base64.NO_PADDING or Base64.NO_WRAP)

private fun JSONArray?.strings() = this?.let { a -> (0 until a.length()).map(a::optString) }.orEmpty()

/** An interrupt: a plugin stopping the world until someone answers. */
data class Ask(val id: String, val owner: String, val title: String, val message: String = "", val choices: List<String> = emptyList(), val free: Boolean = false)

/**
 * The questions plugin asks up to four questions and validates one JSON Result as the reply
 * (plugins/questions); Bridge carries them as the input's presentation, so the app walks them.
 */
class Questions(private val id: String, private val questions: JSONArray) {
    private val answers = JSONArray()
    private var index = 0
    private fun labels(q: JSONObject) = q.optJSONArray("options")?.let { a -> (0 until a.length()).map { a.getJSONObject(it).optString("label") } } ?: emptyList()
    fun ask(): Ask = questions.getJSONObject(index).let { q ->
        Ask(id, "questions", q.optString("question"), q.optString("header") + if (questions.length() > 1) "  ${index + 1} / ${questions.length()}" else "", labels(q), free = true)
    }
    /** The reply once every question is answered; null while more remain. */
    fun answer(value: String): String? {
        val q = questions.getJSONObject(index++)
        val picked = value in labels(q)
        answers.put(JSONObject().put("id", q.optString("id")).put("selected", JSONArray(if (picked) listOf(value) else emptyList<String>())).apply { if (!picked) put("custom", value) })
        return if (index < questions.length()) null else JSONObject().put("answers", answers).toString()
    }
    companion object { const val CANCELLED = """{"cancelled":true}""" }
}

/** A slash command the Orb offers: an extension command, a prompt template or a skill. */
data class Command(val name: String, val hint: String)

/**
 * A conversation in an Orb on Bridge — one of this phone's or a paired device's — driven with the
 * calls `orb bridge view` makes and followed by long polls. The phone is a peer of itself, so this
 * is the only conversation the app knows.
 */
class Session(private val scope: CoroutineScope, private val bridge: Bridge, val peer: String, instance: String, val where: String) {
    val remote get() = peer != bridge.self
    val transcript = Transcript()
    var title by mutableStateOf("")
    var busy by mutableStateOf(false)
    var model by mutableStateOf("")
    var thinking by mutableStateOf("")
    var status by mutableStateOf("")
    var ask by mutableStateOf<Ask?>(null)
    var online by mutableStateOf(true)
    var context by mutableStateOf(0f) // share of the context window in use
    var cost by mutableStateOf(0.0)
    /** Reasoning levels the current model accepts, lowest first; empty when it has none. */
    var levels by mutableStateOf(emptyList<String>())
    var commands by mutableStateOf(emptyList<Command>())
    var id by mutableStateOf("")
    var cwd by mutableStateOf("")

    private var info = JSONObject()
    private var cursor = ""
    private var pulse = "" // the Orb's pulse when last described: a new one means describe again
    private var stale = true
    private var asked = ""
    private var questions: Questions? = null
    private var gone = false // the Orb ended there; the thread reopens with the next message
    private var queued: String? = null // a message waiting for the Orb (a new or reopened thread) to be ready
    private var queuedFrom = "" // ...and for its session to differ from this one
    /** The Orb serving this thread; a reopened thread gets a new one. */
    @Volatile var instance = instance
        private set
    /** Whether a screen shows this session. Unwatched, it only keeps its state fresh, slowly. */
    @Volatile var watched = false
    private val job = scope.launch { follow() }

    private suspend fun remote(method: String, params: JSONObject) = bridge.remote(peer, method, params)
    private val target get() = info.optJSONObject("target")

    /** Follows the Orb until closed; no failure ends it, the next round simply tries again. */
    private suspend fun follow() {
        while (scope.isActive) {
            val wait = try { step() } catch (e: CancellationException) { throw e } catch (e: Exception) { online = false; status = "reconnecting"; 2000L }
            delay(wait)
        }
    }

    private suspend fun step(): Long {
        val waits = info.optBoolean("waits")
        if (stale || !waits || !watched) { if (!describe()) return if (gone) 5000 else 2000; stale = false }
        if (!watched) { cursor = ""; return 5000 } // the transcript is fetched again when a screen shows it
        if (cursor.isEmpty()) snapshot() else events(waits)
        return if (waits) 0 else if (busy) 350 else 900
    }

    private suspend fun describe(): Boolean {
        val d = remote("instances.describe", JSONObject().put("instance_id", instance))
        val next = d.optJSONObject("result") ?: run {
            online = false
            // Unreachable is a network matter; not found means the Orb itself ended over there.
            gone = d.optJSONObject("error")?.optString("code") == "not_found" && !target?.optString("session_id").isNullOrEmpty()
            status = if (gone) "ended on that device · send a message to reopen it" else "offline · read-only · reconnecting"
            return false
        }
        val session = next.optJSONObject("target")?.optString("session_id").orEmpty()
        if (session != target?.optString("session_id")) { cursor = ""; transcript.clear() }
        info = next; online = true; gone = false
        if (status.startsWith("offline") || status.startsWith("ended") || status == "reconnecting") status = ""
        id = session; cwd = next.optString("cwd")
        title = next.optString("name").ifBlank { (transcript.items.firstOrNull { it is You } as? You)?.text?.lineSequence()?.first()?.take(48) ?: cwd.substringAfterLast('/') }
        // The Orb names its model by display name; its catalog maps that to an id and the levels it takes.
        val current = next.optJSONArray("models")?.let { a -> (0 until a.length()).map(a::getJSONObject).firstOrNull { it.optString("name") == next.optString("model") } }
        model = current?.let { it.optString("provider") + "/" + it.optString("id") } ?: next.optString("model")
        levels = current?.optJSONArray("thinking").strings()
        thinking = next.optString("thinking")
        busy = next.optJSONObject("target")?.optString("execution_id").orEmpty().isNotEmpty() || next.optString("status").contains("stream", true)
        next.optJSONObject("stats")?.let { cost = it.optDouble("cost", 0.0); context = (it.optJSONObject("contextUsage")?.optDouble("percent", 0.0) ?: 0.0).toFloat() / 100f }
        commands = next.optJSONArray("commands")?.let { a -> (0 until a.length()).map(a::getJSONObject).map { Command(it.optString("name"), it.optString("description")) } }.orEmpty()
        next.optJSONObject("input")?.let { i ->
            if (i.optString("id") != asked) {
                asked = i.optString("id")
                val presented = i.optJSONObject("presentation")?.takeIf { it.optString("kind") == "questions" }?.optJSONObject("data")
                val choices = i.optJSONArray("choices").strings()
                questions = presented?.optJSONArray("questions")?.let { Questions(asked, it) }
                ask = questions?.ask() ?: Ask(asked, if (choices.any { "approve" in it }) "permissions" else "questions", i.optString("title"), choices = choices, free = choices.isEmpty())
            }
        } ?: run { asked = ""; questions = null; if (ask != null) ask = null }
        queued?.takeIf { session.isNotEmpty() && session != queuedFrom }?.let { queued = null; call("prompt", JSONObject().put("text", it)) }
        return true
    }

    private suspend fun snapshot() {
        var snap = ""; var offset = ""
        transcript.clear()
        repeat(64) {
            val r = remote("events.subscribe", JSONObject().put("instance_id", instance).put("snapshot_id", snap).put("offset", offset)).optJSONObject("result") ?: return
            transcript.load(r.optJSONArray("messages") ?: JSONArray())
            r.optJSONObject("partial")?.let { transcript.apply(JSONObject().put("type", "message_update").put("message", it)) }
            snap = r.optString("snapshot_id"); offset = r.optString("offset")
            if (offset.isEmpty()) { cursor = r.optString("cursor"); remote("events.unsubscribe", JSONObject().put("instance_id", instance).put("snapshot_id", snap)); return }
        }
    }

    private suspend fun events(waits: Boolean) {
        val p = JSONObject().put("instance_id", instance).put("cursor", cursor).apply { if (waits) put("wait", true).put("state", pulse) }
        val r = remote("events.subscribe", p).optJSONObject("result") ?: run { cursor = ""; return }
        val events = r.optJSONArray("events") ?: JSONArray()
        for (i in 0 until events.length()) events.optJSONObject(i)?.optJSONObject("data")?.let { e ->
            if (e.optString("type") == "session_info_changed") e.optString("name").takeIf { it.isNotBlank() && it != "null" }?.let { title = it }
            transcript.apply(e)
        }
        cursor = r.optString("cursor", cursor)
        r.optString("state").takeIf { it != pulse }?.let { pulse = it; stale = true }
    }

    private fun call(method: String, args: JSONObject = JSONObject()) {
        val target = target ?: return
        val call = JSONObject().put("instance_id", instance).put("service", "orb.instance/1").put("method", method)
            .put("session_id", target.optString("session_id")).put("operation_id", newId())
            .put("expected", JSONObject().put("registration_generation", info.optString("registration_generation")).put("session_revision", target.optString("session_revision")))
            .put("args", args)
        scope.launch { remote("instances.call", call).optJSONObject("error")?.let { status = it.optString("message") } }
    }
    private fun execution(extra: JSONObject = JSONObject()) = extra.put("execution_id", target?.optString("execution_id"))
    /** Sends [text] once the Orb holds a session other than [from]: a new thread, or one reopened. */
    private fun later(text: String, from: String = "") { queued = text; queuedFrom = from }

    /** Sends a prompt; while a turn runs it queues after it. */
    fun prompt(text: String) {
        transcript.sent += text
        when {
            gone -> reopen(text)
            target == null -> later(text)
            busy -> { call("follow_up", execution(JSONObject().put("text", text))); transcript.waiting(steer = false) }
            else -> call("prompt", JSONObject().put("text", text))
        }
    }

    /** Starts Orb on this thread again over there, then sends [text] once it is on Bridge. */
    private fun reopen(text: String) = scope.launch {
        status = "reopening the thread…"
        bridge.launch(peer, session = id).onSuccess { instance = it.id; gone = false; cursor = ""; later(text) }.onFailure { status = it.message.orEmpty() }
    }
    fun steer(text: String) { transcript.sent += text; call("steer", execution(JSONObject().put("text", text))); transcript.waiting(steer = true) }
    fun abort() = call("cancel", execution())
    fun answer(value: String?) {
        val a = ask ?: return
        ask = null
        val reply = questions?.let { q -> if (value == null) Questions.CANCELLED else q.answer(value) ?: run { ask = q.ask(); return } } ?: value.orEmpty()
        call("input.reply", execution(JSONObject().put("id", a.id).put("value", reply)))
    }
    fun models() = info.optJSONArray("models")?.let { a -> (0 until a.length()).map { a.getJSONObject(it).let { m -> m.optString("provider") + "/" + m.optString("id") } } } ?: emptyList()
    /** The Orb keeps the choice as its default, so the threads that follow there start with it. */
    fun useModel(id: String) {
        val (p, m) = id.split("/", limit = 2).let { it[0] to it.getOrElse(1) { "" } }
        call("session.model", JSONObject().put("provider", p).put("model", m).apply { if (thinking.isNotEmpty()) put("thinking", thinking) })
    }
    fun useThinking(level: String) { thinking = level; model.takeIf { it.contains('/') }?.let(::useModel) }
    // An Orb takes calls once it has described its session: a rename right after a launch waits for that.
    fun rename(name: String) {
        title = name
        scope.launch { repeat(40) { if (target != null) return@launch call("session.name", JSONObject().put("name", name)); delay(250) } }
    }
    fun compact(instructions: String) = call("session.compact", JSONObject().apply { if (instructions.isNotBlank()) put("instructions", instructions) })
    /** `!command`: runs in that Orb's shell and joins the conversation, as in the TUI. */
    fun shell(command: String) { transcript.shell(command); call("shell", JSONObject().put("command", command)) }
    fun lastText(): String? = (transcript.items.lastOrNull { it is Said } as? Said)?.text
    /** Starts a fresh conversation in this Orb, then sends [first] into it once it exists. */
    fun newSession(first: String? = null) { call("session.new"); first?.let { later(it, id) } }
    fun switchTo(session: String) { if (session != id) call("session.switch", JSONObject().put("session_id", session)) }
    fun close() = job.cancel()
}
