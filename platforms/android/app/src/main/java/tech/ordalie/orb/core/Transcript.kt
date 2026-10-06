package tech.ordalie.orb.core

import androidx.compose.runtime.*
import org.json.*

/** The primitives a conversation is drawn from. */
sealed class Item(val key: String)
/** [images] are references: a screen fetches each from the Orb, at the size it shows it (Session.image). */
class You(key: String, val text: String, val via: String? = null, val images: List<String> = emptyList()) : Item(key)
class Said(key: String) : Item(key) {
    var text by mutableStateOf("")
    var thinking by mutableStateOf("")
    var live by mutableStateOf(true)
}
class Tool(key: String, val verb: String, val target: String, val args: String = "") : Item(key) {
    var result by mutableStateOf("")
    var output by mutableStateOf("") // the full text, shown when the line is opened
    var live by mutableStateOf(true)
    var failed by mutableStateOf(false)
    var images by mutableStateOf(emptyList<String>()) // what it showed the model, a screenshot or a read image
}
class Note(key: String, val text: String, val alarm: Boolean = false) : Item(key)

/**
 * Orb's agent events → items, as a Bridge instance's `events.subscribe` streams them and its
 * snapshots replay them.
 */
class Transcript {
    val items = mutableStateListOf<Item>()
    private var said: Said? = null
    private val tools = HashMap<String, Tool>()
    private var n = 0
    private fun key() = "i" + n++

    /** Texts this side sent; a user message not in here arrived through Bridge. */
    val sent = ArrayDeque<String>()

    /** Empties the transcript; keys restart, so a reload of the same messages keeps its rows. */
    fun clear() { items.clear(); tools.clear(); said = null; n = 0 }

    /** Says that a message sent during a run waits: steering lands after the current step, a follow-up after the run. */
    fun waiting(steer: Boolean) { items += Note(key(), if (steer) "steering · lands after the current step" else "queued · sends when this run ends") }

    fun apply(e: JSONObject): Boolean {
        when (e.optString("type")) {
            "message_start", "message_update" -> message(e.optJSONObject("message") ?: return false, final = false)
            "message_end" -> message(e.optJSONObject("message") ?: return false, final = true)
            "tool_execution_start" -> tool(e.optString("toolCallId"), e.optString("toolName"), e.optJSONObject("args"))
            "tool_execution_update" -> tools[e.optString("toolCallId")]?.let { content(e.optJSONObject("partialResult")).let { out -> it.result = lastLine(out); it.output = out.takeLast(OUTPUT) } }
            "tool_execution_end" -> tools[e.optString("toolCallId")]?.let {
                val out = content(e.optJSONObject("result"))
                it.result = summary(it.verb, out); it.output = out.takeLast(OUTPUT); it.failed = e.optBoolean("isError"); it.live = false
                it.images = images(e.optJSONObject("result")?.opt("content"))
            }
            "agent_end" -> { said?.live = false; said = null; tools.values.forEach { it.live = false } }
            "compaction_start" -> items += Note(key(), "compacting context…")
            "compaction_end" -> items += e.optString("errorMessage").takeIf { it.isNotEmpty() && it != "null" }?.let { Note(key(), "compaction failed · " + error(it), alarm = true) }
                ?: Note(key(), if (e.optBoolean("aborted")) "compaction cancelled" else "context compacted" + (e.optJSONObject("result")?.optInt("tokensBefore")?.takeIf { it > 0 }?.let { " · from ${it / 1000}k tokens" } ?: ""))
            // Retries read as one line that updates, not a failure per attempt: the failed attempt's
            // error gives way to it, and it disappears once a retry gets through.
            "auto_retry_start" -> {
                (items.lastOrNull() as? Note)?.takeIf { it.alarm }?.let(items::remove)
                retrying("retrying ${e.optInt("attempt")} / ${e.optInt("maxAttempts")} · " + error(e.optString("errorMessage")))
            }
            "auto_retry_end" -> if (e.optBoolean("success")) retrying(null) else retrying("gave up after ${e.optInt("attempt")} retries", alarm = true)
            else -> return false
        }
        return true
    }

    /** Replays whole messages: a Bridge snapshot, or a history page. */
    private var retry: Note? = null
    private fun retrying(text: String?, alarm: Boolean = false) {
        val at = retry?.let(items::indexOf) ?: -1
        val next = text?.let { Note(key(), it, alarm) }
        when {
            at >= 0 && next != null -> items[at] = next
            at >= 0 -> items.removeAt(at)
            next != null -> items += next
        }
        retry = next.takeUnless { alarm }
    }

    /** A `!command` the owner ran: live until the conversation, reloaded, holds its output. */
    fun shell(command: String) { items += Tool(key(), "bash", command.lineSequence().first().take(120), command) }

    fun load(messages: JSONArray) {
        replaying = true
        last = messages.length() - 1
        for (i in 0 until messages.length()) { index = i; message(messages.optJSONObject(i) ?: continue, final = true) }
        replaying = false
    }
    private var userOpen = false
    private var replaying = false
    private var index = 0
    private var last = 0

    private fun message(m: JSONObject, final: Boolean) {
        when (m.optString("role")) {
            // Live user messages come as start+end; snapshots and history carry only the end.
            "user" -> if (!final || !userOpen) {
                val text = invocation(text(m.opt("content")))
                // Machine-written turns (<task-notification>…) are events, not the person speaking.
                val tag = Regex("^<([a-z][\\w-]*)[ >]").find(text.trimStart())?.groupValues?.get(1)
                items += if (tag != null) Note(key(), tag.replace('-', ' ') + " · " + text.replace(Regex("<[^>]+>"), " ").trim().replace(Regex("\\s+"), " ").take(160))
                else You(key(), text, if (sent.remove(text) || replaying) null else "bridge", images(m.opt("content")))
                userOpen = !final
            } else userOpen = false
            "assistant" -> {
                val s = said?.takeIf { it.live } ?: Said(key()).also { said = it; items += it }
                s.text = text(m.opt("content")); s.thinking = thinking(m.opt("content"))
                val calls = m.optJSONArray("content")
                if (final) {
                    for (i in 0 until (calls?.length() ?: 0)) calls!!.optJSONObject(i)?.takeIf { it.optString("type") == "toolCall" }
                        ?.let { tool(it.optString("id"), it.optString("name"), it.optJSONObject("arguments")) }
                    // In history, a failed attempt that a later message followed was retried: only a final failure shows.
                    if (m.optString("stopReason") == "error" && (!replaying || index == last)) items += Note(key(), error(m.optString("errorMessage", "error")), alarm = true)
                    s.live = false; said = null
                    if (s.text.isBlank() && s.thinking.isBlank()) items.remove(s)
                }
            }
            "compactionSummary" -> items += Note(key(), "context compacted" + m.optInt("tokensBefore").takeIf { it > 0 }?.let { " · from ${it / 1000}k tokens" }.orEmpty())
            "branchSummary" -> items += Note(key(), "branch summary · " + m.optString("summary").lineSequence().first().take(160))
            "bashExecution" -> items += Tool(key(), "bash", m.optString("command").lineSequence().first().take(120), m.optString("command")).apply {
                output = m.optString("output").takeLast(OUTPUT); result = summary("bash", output); failed = m.optInt("exitCode") != 0; live = false
            }
            "custom" -> if (m.optBoolean("display")) items += Note(key(), text(m.opt("content")).take(400))
            "toolResult" -> tools[m.optString("toolCallId")]?.let {
                val out = content(m)
                it.result = summary(it.verb, out); it.output = out.takeLast(OUTPUT); it.failed = m.optBoolean("isError"); it.live = false
                it.images = images(m.opt("content"))
            }
        }
    }

    private fun tool(id: String, name: String, args: JSONObject?) {
        if (tools.containsKey(id)) return
        val target = args?.optJSONArray("questions")?.optJSONObject(0)?.optString("question") ?: args?.let { a -> listOf("path", "command", "pattern", "url", "query").firstNotNullOfOrNull { k -> a.optString(k).takeIf(String::isNotBlank) } ?: a.keys().asSequence().firstOrNull()?.let { a.optString(it) } } ?: ""
        val t = Tool(key(), VERBS[name] ?: name, target.lineSequence().first().take(120), args?.toString(2)?.replace("\\/", "/")?.take(2000).orEmpty()).apply { live = !replaying }
        tools[id] = t
        val at = said?.let { items.indexOf(it) } ?: -1
        if (at >= 0) items.add(at + 1, t) else items += t
    }

    companion object {
        fun text(c: Any?): String = when (c) {
            is String -> c
            is JSONArray -> (0 until c.length()).mapNotNull { c.optJSONObject(it)?.takeIf { p -> p.optString("type") == "text" }?.optString("text") }.joinToString("")
            else -> ""
        }
        fun thinking(c: Any?): String = (c as? JSONArray)?.let { a -> (0 until a.length()).mapNotNull { a.optJSONObject(it)?.takeIf { p -> p.optString("type") == "thinking" }?.optString("thinking") }.joinToString("") } ?: ""
        fun content(r: JSONObject?): String = text(r?.opt("content"))
        fun images(c: Any?): List<String> = (c as? JSONArray)?.let { a -> (0 until a.length()).mapNotNull { a.optJSONObject(it)?.takeIf { p -> p.optString("type") == "image" }?.optString("ref")?.ifEmpty { null } } }.orEmpty()
        private val SKILL = Regex("""(?s)^<skill name="([^"]+)" location="[^"]+">\n.*?\n</skill>(?:\n\n(.+))?$""")
        /** A message that invoked skills, as it was typed: their tokens in place (exporthtml.InvocationText). */
        fun invocation(raw: String): String {
            var text = raw
            val names = mutableListOf<String>()
            while (true) SKILL.find(text)?.let { names += it.groupValues[1]; text = it.groupValues[2].trim() } ?: break
            // A stored preview is clipped, often before the block closes.
            if (names.isEmpty()) return Regex("""^<skill name="([^"]+)"""").find(raw)?.let { "/skill:" + it.groupValues[1] } ?: raw
            return names.asReversed().fold(text) { t, n -> if (Regex("""(?<=^|\s)/skill:${Regex.escape(n)}(?=\s|$)""").containsMatchIn(t)) t else "/skill:$n $t".trim() }
        }
        /** Providers answer errors as JSON bodies; the turn only needs the status and the sentence. */
        fun error(raw: String): String {
            val status = Regex("^\\d{3}").find(raw.trim())?.value
            val message = Regex(""""message"\s*:\s*"((?:[^"\\]|\\.)*)"""").find(raw)?.groupValues?.get(1)?.replace("\\\"", "\"")
            return listOfNotNull(status, message).joinToString(" · ").ifEmpty { raw.lineSequence().first().take(200) }
        }
        fun lastLine(s: String) = s.trimEnd().lineSequence().lastOrNull()?.take(120) ?: ""
        const val OUTPUT = 8000
        val VERBS = mapOf("ask_user_question" to "ask", "todo_write" to "tasks", "web_search" to "search", "web_fetch" to "fetch")
        fun summary(verb: String, out: String): String {
            if (verb == "ask") runCatching { JSONObject(out) }.getOrNull()?.let { r ->
                val a = r.optJSONArray("answers") ?: return if (r.optBoolean("cancelled")) "dismissed" else "done"
                return (0 until a.length()).joinToString(" · ") { i -> a.getJSONObject(i).let { x -> x.optJSONArray("selected")?.optString(0)?.takeIf(String::isNotEmpty) ?: x.optString("custom") } }
            }
            val lines = out.trimEnd().lines().filter(String::isNotBlank)
            return when {
                lines.isEmpty() -> "done"
                verb == "bash" || lines.size == 1 -> lines.last().take(120)
                else -> "${lines.size} lines"
            }
        }
    }
}
