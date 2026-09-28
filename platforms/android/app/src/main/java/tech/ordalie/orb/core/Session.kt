package tech.ordalie.orb.core

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import org.json.JSONArray
import org.json.JSONObject
import android.util.Base64
import java.security.SecureRandom
import java.util.UUID

/** An error reply's words: RPC puts a string, Bridge and [Lines] an object with a message. */
fun JSONObject.problem(): String = optJSONObject("error")?.optString("message") ?: optString("error")

/** Bridge IDs are 16 random bytes, base64url without padding (protocol.ValidID). */
fun newId(): String = Base64.encodeToString(ByteArray(16).also(SecureRandom()::nextBytes), Base64.URL_SAFE or Base64.NO_PADDING or Base64.NO_WRAP)

/** An interrupt: a plugin stopping the world until someone answers. */
data class Ask(val id: String, val owner: String, val title: String, val message: String = "", val choices: List<String> = emptyList(), val free: Boolean = false)

/**
 * The questions plugin asks up to four questions and validates one JSON Result as the reply
 * (plugins/questions). RPC renders it as a bare input, so the app walks the questions itself.
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

/** A slash command the core offers: an extension command, a prompt template or a skill. */
data class Command(val name: String, val hint: String)

/** A conversation running somewhere — on this phone or on a Bridge peer. The UI only ever sees this. */
abstract class Session(val where: String, val remote: Boolean) {
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

    /** Sends a prompt; while a turn runs it queues after it. */
    abstract fun prompt(text: String)
    abstract fun steer(text: String)
    abstract fun abort()
    abstract fun answer(value: String?)
    open fun models(): List<String> = emptyList()
    open fun useModel(id: String) {}
    /** Reasoning levels the current model accepts, lowest first; empty when it has none. */
    var levels by mutableStateOf(emptyList<String>())
    open fun useThinking(level: String) {}
    var commands by mutableStateOf(emptyList<Command>())
    open fun compact(instructions: String) {}
    open fun rename(name: String) {}
    /** Renames a stored session of this device; a session other than the open one is opened first. */
    open fun rename(id: String, name: String) { if (id == this.id) rename(name) }
    /** `!command`: runs in this device's shell and joins the conversation's context, as in the TUI. */
    open fun shell(command: String) {}
    open suspend fun lastText(): String? = transcript.items.lastOrNull { it is Said }?.let { (it as Said).text }
    /** Starts a fresh conversation, then sends [first] into it once it exists. */
    open fun newSession(first: String? = null) { first?.let(::prompt) }
    open fun switchTo(id: String) {}
    var id by mutableStateOf("")
    open fun close() {}
}

/** This phone's own Orb: `orb --mode rpc`, attached to Bridge so peers see and drive it too. */
class LocalSession(private val scope: CoroutineScope, private val orb: Orb) : Session(orb.device, remote = false) {
    // --continue: a restarted core (new keys, app relaunch) resumes the same conversation.
    private val rpc = orb.lines(scope, "orb-rpc", "--mode", "rpc", "--continue", "--bridge", "personal", "--instance", orb.device)
    private var available = emptyList<String>()

    init {
        scope.launch {
            rpc.events.collect { e ->
                when (e.optString("type")) {
                    "agent_start" -> busy = true
                    "agent_end" -> { busy = false; refresh() }
                    "extension_ui_request" -> interrupt(e)
                    // Named by the owner elsewhere, or by the titles plugin after the first exchange.
                    "session_info_changed" -> e.optString("name").takeIf { it.isNotEmpty() && it != "null" }?.let { title = it }
                    "tool_execution_start" -> if (e.optString("toolName") == "ask_user_question") pendingQuestions = e.optJSONObject("args")?.optJSONArray("questions")
                    // The core is supervised: it comes back by itself, on the same conversation.
                    "exit" -> { cut = busy; online = false; busy = false; status = "core stopped · restarting" }
                    "restart" -> { online = true; status = ""; scope.launch { boot() } }
                }
                transcript.apply(e)
                if (title.isEmpty()) transcript.items.firstOrNull { it is You }?.let { title = (it as You).text.lineSequence().first().take(48) }
            }
        }
        // Extension errors only reach stderr in RPC mode; the conversation should say so.
        scope.launch { rpc.errors.collect { if (it.startsWith("Extension error")) transcript.items += Note("x" + newId(), it.substringAfter("): ").ifBlank { it }, alarm = true) } }
        scope.launch { boot() }
    }

    /** Brings a (re)started core to where this session was: the chosen model, the open conversation. */
    private suspend fun boot() {
        available = cmd("get_available_models")?.optJSONArray("models")?.let { a -> (0 until a.length()).map { a.getJSONObject(it).let { m -> m.optString("provider") + "/" + m.optString("id") } } } ?: emptyList()
        preferred()
        commands = cmd("get_commands")?.optJSONArray("commands")?.let { a ->
            (0 until a.length()).map { a.getJSONObject(it).let { c -> Command(c.optString("name"), c.optString("description").takeIf { d -> d != "null" }.orEmpty().ifEmpty { c.optString("source") }) } }
        } ?: emptyList()
        if (id.isNotEmpty()) cmd("switch_session") { put("sessionPath", id) }
        cmd("get_messages")?.optJSONArray("messages")?.let { transcript.clear(); transcript.load(it) }
        if (cut) transcript.items += Note("r" + newId(), "interrupted · the core restarted mid-turn; send it again", alarm = true)
        cut = false
        refresh()
    }
    private var cut = false // a turn was running when the core went away

    /** An RPC command's data, or null when Orb refused it. */
    private suspend fun cmd(type: String, extra: JSONObject.() -> Unit = {}): JSONObject? =
        rpc.call(JSONObject().put("type", type).apply(extra)).let { if (it.optBoolean("success")) it.optJSONObject("data") ?: JSONObject() else null }

    private suspend fun refresh() {
        val s = cmd("get_state") ?: return
        s.optJSONObject("model")?.let { model = it.optString("provider") + "/" + it.optString("id") }
        thinking = s.optString("thinkingLevel")
        levels = cmd("get_available_thinking_levels")?.optJSONArray("levels")?.let { a -> (0 until a.length()).map(a::optString) }.orEmpty()
        id = s.optString("sessionId")
        s.optString("sessionName").takeIf { it.isNotBlank() && it != "null" }?.let { title = it }
        if (title.isEmpty()) (transcript.items.firstOrNull { it is You } as? You)?.let { title = it.text.lineSequence().first().take(48) }
        cmd("get_session_stats")?.let { st ->
            cost = st.optDouble("cost", 0.0)
            context = (st.optJSONObject("contextUsage")?.optDouble("percent", 0.0) ?: 0.0).toFloat() / 100f
        }
    }

    private fun interrupt(e: JSONObject) {
        val method = e.optString("method")
        when (method) {
            "input" if pendingQuestions != null -> {
                questions = Questions(e.optString("id"), pendingQuestions!!).also { ask = it.ask() }
                askMethod = method
            }
            "select", "confirm", "input", "editor" -> {
                val title = e.optString("title")
                val choices = e.optJSONArray("options")?.let { a -> (0 until a.length()).map(a::optString) } ?: if (method == "confirm") listOf("allow", "deny") else emptyList()
                val owner = if (method == "confirm" || choices.any { it.contains("approve") }) "permissions" else "questions"
                ask = Ask(e.optString("id"), owner, title, e.optString("message"), choices, free = method == "input" || method == "editor")
                askMethod = method
            }
            "notify", "setStatus" -> status = e.optString("message").ifBlank { e.optString("statusText") }
            "setTitle" -> title = e.optString("title")
        }
    }
    private var askMethod = ""
    private var pendingQuestions: JSONArray? = null
    private var questions: Questions? = null

    override fun prompt(text: String) {
        transcript.sent += text
        scope.launch {
            val r = rpc.call(JSONObject().put("type", "prompt").put("message", text).apply { if (busy) put("streamingBehavior", "followUp") })
            if (!r.optBoolean("success", true)) transcript.items += Note("e" + UUID.randomUUID(), r.problem(), alarm = true)
        }
    }
    override fun steer(text: String) { transcript.sent += text; scope.launch { rpc.call(JSONObject().put("type", "steer").put("message", text)) } }
    override fun abort() { scope.launch { rpc.call(JSONObject().put("type", "abort")) } }
    override fun answer(value: String?) {
        val a = ask ?: return
        ask = null
        val reply = JSONObject().put("type", "extension_ui_response").put("id", a.id)
        questions?.let { q ->
            val done = if (value == null) Questions.CANCELLED else q.answer(value) ?: run { ask = q.ask(); return }
            questions = null; pendingQuestions = null
            scope.launch { rpc.send(reply.put("value", done)) }
            return
        }
        when {
            value == null -> reply.put("cancelled", true)
            askMethod == "confirm" -> reply.put("confirmed", value == "allow")
            else -> reply.put("value", value)
        }
        scope.launch { rpc.send(reply) }
    }
    override fun models() = available
    override fun useModel(id: String) {
        orb.model = id
        val (provider, model) = id.split("/", limit = 2).let { it[0] to it.getOrElse(1) { "" } }
        scope.launch { cmd("set_model") { put("provider", provider).put("modelId", model) }; refresh() }
    }
    override fun useThinking(level: String) { orb.thinking = level; scope.launch { cmd("set_thinking_level") { put("level", level) }; refresh() } }

    /** Applies the remembered model, then the remembered reasoning if that model takes it. */
    private suspend fun preferred() {
        orb.model.takeIf { it in available }?.let { cmd("set_model") { put("provider", it.substringBefore('/')).put("modelId", it.substringAfter('/')) } }
        val levels = cmd("get_available_thinking_levels")?.optJSONArray("levels")?.let { a -> (0 until a.length()).map(a::optString) }.orEmpty()
        orb.thinking.takeIf { it in levels }?.let { cmd("set_thinking_level") { put("level", it) } }
    }
    override fun compact(instructions: String) {
        scope.launch {
            val r = rpc.call(JSONObject().put("type", "compact").apply { if (instructions.isNotBlank()) put("customInstructions", instructions) }, 600_000)
            if (!r.optBoolean("success")) transcript.items += Note("c" + newId(), "compaction failed · " + Transcript.error(r.problem()), alarm = true)
            refresh()
        }
    }
    override fun rename(name: String) { scope.launch { cmd("set_session_name") { put("name", name) }?.let { title = name } } }
    override suspend fun lastText() = cmd("get_last_assistant_text")?.optString("text")?.takeIf { it.isNotEmpty() && it != "null" }
    override fun switchTo(id: String) {
        if (id == this.id) return
        scope.launch { open(id) }
    }

    private suspend fun open(id: String): Boolean {
        cmd("switch_session") { put("sessionPath", id) } ?: return false
        transcript.clear(); title = ""
        cmd("get_messages")?.optJSONArray("messages")?.let(transcript::load)
        refresh()
        return true
    }

    override fun rename(id: String, name: String) {
        if (id == this.id) return rename(name)
        scope.launch { if (open(id)) cmd("set_session_name") { put("name", name) }?.let { title = name } }
    }

    override fun shell(command: String) {
        val line = transcript.shell(command)
        scope.launch {
            val r = rpc.call(JSONObject().put("type", "bash").put("command", command), timeoutMs = 30 * 60_000L)
            val d = r.optJSONObject("data")
            transcript.settle(line, d?.optString("output") ?: r.optString("error"), if (d == null) 1 else d.optInt("exitCode"))
        }
    }

    override fun newSession(first: String?) {
        scope.launch {
            cmd("new_session"); transcript.clear(); title = ""
            preferred() // a new conversation keeps the owner's choices, not the settings defaults
            refresh()
            first?.let(::prompt)
        }
    }
    /** A new core with the current keys, on the same conversation. */
    fun restart() = rpc.restart()
    override fun close() { scope.launch { rpc.close() } }
}

/** A live instance on a Bridge peer, driven through the owner pipe with the same calls `orb bridge view` makes. */
class RemoteSession(private val scope: CoroutineScope, private val bridge: Bridge, val peer: String, instance: String, alias: String) :
    Session(alias, remote = true) {
    private var info = JSONObject()
    private var cursor = ""
    private var pending = ""
    private var asked = ""
    private var questions: Questions? = null
    /** The Orb serving this thread on the peer; a reopened thread gets a new one. */
    @Volatile var instance = instance
        private set
    private var gone = false // the Orb ended there; the thread reopens with the next message
    private var reopening: String? = null
    private val job: Job = scope.launch { follow() }

    private suspend fun remote(method: String, params: JSONObject) = bridge.remote(peer, method, params)

    /** Whether a screen shows this session. Unwatched, it only keeps its state fresh, slowly. */
    @Volatile var watched = false

    /** Follows the peer until closed; no failure ends it, the next round simply tries again. */
    private suspend fun follow() {
        while (scope.isActive) {
            val wait = try { step() } catch (e: CancellationException) { throw e } catch (e: Exception) { online = false; status = "reconnecting"; 2000L }
            delay(wait)
        }
    }

    private suspend fun step(): Long {
        val d = remote("instances.describe", JSONObject().put("instance_id", instance))
        val next = d.optJSONObject("result") ?: run {
            online = false
            // Unreachable is a network matter; not found means the Orb itself ended over there.
            gone = d.optJSONObject("error")?.optString("code") == "not_found" && info.optJSONObject("target")?.optString("session_id").orEmpty().isNotEmpty()
            status = if (gone) "ended on that device · send a message to reopen it" else "offline · read-only · reconnecting"
            return if (gone) 5000 else 2000
        }
        reopening?.takeIf { next.optJSONObject("target") != null }?.let { text -> reopening = null; info = next; call("prompt", JSONObject().put("text", text)) }
        if (wanted && next.optJSONObject("target") != null) {
            wanted = false
            info = next
            val (m, t) = bridge.preferred(peer)
            val known = next.optJSONArray("models")?.let { a -> (0 until a.length()).map(a::getJSONObject).firstOrNull { it.optString("provider") + "/" + it.optString("id") == m } }
            if (known != null) { thinking = t; useModel(m) }
        }
        val session = next.optJSONObject("target")?.optString("session_id")
        if (session != info.optJSONObject("target")?.optString("session_id")) { cursor = ""; transcript.clear() }
        info = next; online = true
        title = next.optString("name").ifBlank { (transcript.items.firstOrNull { it is You } as? You)?.text?.lineSequence()?.first()?.take(48) ?: next.optString("cwd").substringAfterLast('/') }
        // The peer names its model by display name; its catalog maps that to an id and the levels it takes.
        val current = next.optJSONArray("models")?.let { a -> (0 until a.length()).map(a::getJSONObject).firstOrNull { it.optString("name") == next.optString("model") } }
        model = current?.let { it.optString("provider") + "/" + it.optString("id") } ?: next.optString("model")
        levels = current?.optJSONArray("thinking")?.let { a -> (0 until a.length()).map(a::optString) }.orEmpty()
        busy = next.optJSONObject("target")?.optString("execution_id").orEmpty().isNotEmpty() || next.optString("status").contains("stream", true)
        next.optJSONObject("input")?.let { i ->
            if (i.optString("id") != asked) {
                asked = i.optString("id")
                val data = i.optJSONObject("presentation")?.takeIf { it.optString("kind") == "questions" }?.optJSONObject("data")
                questions = data?.optJSONArray("questions")?.let { Questions(asked, it) }
                ask = questions?.ask() ?: Ask(asked, "questions", i.optString("title"), choices = i.optJSONArray("choices")?.let { a -> (0 until a.length()).map(a::optString) } ?: emptyList(), free = true)
            }
        } ?: run { asked = ""; questions = null; if (ask != null) ask = null }
        if (!watched) { cursor = ""; return 5000 } // the transcript is fetched again when a screen shows it
        if (pending.isNotEmpty()) remote("operations.get", JSONObject().put("instance_id", instance).put("operation_id", pending)).optJSONObject("result")?.let { status = listOf(it.optString("status"), it.optString("error")).filter(String::isNotEmpty).joinToString(" · ") }
        if (cursor.isEmpty()) snapshot() else events()
        return if (busy) 350 else 900
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

    private suspend fun events() {
        val r = remote("events.subscribe", JSONObject().put("instance_id", instance).put("cursor", cursor)).optJSONObject("result") ?: run { cursor = ""; return }
        val events = r.optJSONArray("events") ?: JSONArray()
        for (i in 0 until events.length()) events.optJSONObject(i)?.optJSONObject("data")?.let(transcript::apply)
        cursor = r.optString("cursor", cursor)
    }

    private fun call(method: String, args: JSONObject) {
        val target = info.optJSONObject("target") ?: return
        val op = newId()
        val call = JSONObject().put("instance_id", instance).put("service", "orb.instance/1").put("method", method)
            .put("session_id", target.optString("session_id")).put("operation_id", op)
            .put("expected", JSONObject().put("registration_generation", info.optString("registration_generation")).put("session_revision", target.optString("session_revision")))
            .put("args", args)
        scope.launch {
            val r = remote("instances.call", call)
            r.optJSONObject("error")?.let { status = it.optString("message") } ?: run { pending = op; status = "sent" }
        }
    }
    private fun execution(extra: JSONObject = JSONObject()) = extra.put("execution_id", info.optJSONObject("target")?.optString("execution_id"))

    override fun prompt(text: String) {
        transcript.sent += text
        if (gone) reopen(text) else if (busy) call("follow_up", execution(JSONObject().put("text", text))) else call("prompt", JSONObject().put("text", text))
    }

    /** Starts Orb on this thread again over there, then sends [text] once it is on Bridge. */
    private fun reopen(text: String) = scope.launch {
        status = "reopening the thread…"
        bridge.launch(peer, session = info.optJSONObject("target")?.optString("session_id")).onSuccess {
            instance = it.id; gone = false; cursor = ""; reopening = text
        }.onFailure { status = it.message.orEmpty() }
    }
    override fun steer(text: String) { transcript.sent += text; call("steer", execution(JSONObject().put("text", text))) }
    override fun abort() = call("cancel", execution())
    override fun answer(value: String?) {
        val a = ask ?: return
        ask = null
        val reply = questions?.let { q -> if (value == null) Questions.CANCELLED else q.answer(value) ?: run { ask = q.ask(); return } } ?: value.orEmpty()
        call("input.reply", execution(JSONObject().put("id", a.id).put("value", reply)))
    }
    override fun models() = info.optJSONArray("models")?.let { a -> (0 until a.length()).map { a.getJSONObject(it).let { m -> m.optString("provider") + "/" + m.optString("id") } } } ?: emptyList()
    override fun useModel(id: String) {
        bridge.prefer(peer, id, thinking)
        val (p, m) = id.split("/", limit = 2).let { it[0] to it.getOrElse(1) { "" } }
        call("session.model", JSONObject().put("provider", p).put("model", m).apply { if (thinking.isNotEmpty()) put("thinking", thinking) })
    }
    // A peer's instance takes calls once it has described its session: a rename right after a launch waits for that.
    override fun rename(name: String) {
        title = name
        scope.launch { repeat(40) { if (info.has("target")) return@launch call("session.name", JSONObject().put("name", name)); delay(250) } }
    }
    // A peer's level is not described on the wire: it is set with the model, and remembered here.
    override fun useThinking(level: String) { thinking = level; model.takeIf { it.contains('/') }?.let(::useModel) }

    /** A thread this phone just started on the device takes the choices last made for that device. */
    fun takePreferred() { wanted = true }
    private var wanted = false
    override fun newSession(first: String?) = call("session.new", JSONObject())
    override fun close() = job.cancel()
}
