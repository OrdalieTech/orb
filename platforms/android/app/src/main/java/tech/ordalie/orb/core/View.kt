package tech.ordalie.orb.core

import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.SnapshotStateList
import kotlinx.coroutines.*
import org.json.*

// What `orb app` (bridge/view) sends, as the app draws it; its Go types name each field.

data class Command(val name: String, val hint: String, val now: Boolean = false)
data class Ask(val id: String, val owner: String, val title: String, val message: String, val choices: List<String>, val free: Boolean)
data class Window(val name: String, val left: Double, val resets: Long)
data class Usage(val plan: String, val at: Long, val windows: List<Window>)
data class Tab(
    val id: String, val title: String, val peer: String, val where: String, val remote: Boolean, val cwd: String, val busy: Boolean, val online: Boolean,
    val loaded: Boolean, val earlier: Boolean, val streaming: Boolean, val status: String, val model: String, val thinking: String, val levels: List<String>,
    val context: Double, val cost: Double, val usage: Usage?, val ask: Ask?, val unread: Int, val commands: List<Command>, val models: List<String>,
)
data class Claim(val invitation: String, val claimant: String)
data class Invitation(val code: String, val expires: Long)
data class Prompt(val kind: String, val message: String, val placeholder: String, val options: List<Pair<String, String>>)
data class Login(val machine: String, val provider: String, val state: String, val url: String, val instructions: String, val code: String, val detail: String, val prompt: Prompt?)
data class State(
    val self: String = "", val up: Boolean = false, val tabs: List<Tab> = emptyList(), val claim: Claim? = null, val invitation: Invitation? = null,
    val joining: String = "", val launching: String = "", val login: Login? = null, val acting: Boolean = false, val summary: String = "starting",
    val latest: String = "", val commands: List<Command> = emptyList(),
)
data class Running(val instance: String, val label: String, val busy: Boolean)
data class Folder(val cwd: String, val threads: Int, val modified: Long, val live: Boolean)
data class Machine(val id: String, val name: String, val self: Boolean, val connected: Boolean, val version: String, val launch: Boolean, val hue: Int, val running: List<Running>, val folders: List<Folder>)
data class Entry(val key: String, val title: String, val machine: String, val cwd: String, val modified: Long, val live: Boolean, val asks: Boolean, val open: Boolean, val unstored: Boolean, val deletable: Boolean)
data class Home(val machines: List<Machine> = emptyList(), val entries: List<Entry> = emptyList())
data class Span(val text: String, val bold: Boolean, val italic: Boolean, val code: Boolean, val strike: Boolean, val href: String)
data class Block(val type: String, val level: Int, val mark: String, val depth: Int, val spans: List<Span>, val lang: String, val text: String, val blocks: List<Block>, val rows: List<List<List<Span>>>)
data class Action(val key: String, val verb: String, val target: String, val result: String, val live: Boolean, val failed: Boolean)
data class Row(
    val key: String, val kind: String, val text: String, val via: String, val alarm: Boolean, val images: List<String>, val block: Block?,
    val live: Boolean, val what: String, val now: String, val failed: Int, val actions: List<Action>,
)
data class Method(val auth: String, val label: String, val about: String, val account: Boolean)
data class Provider(val id: String, val name: String, val methods: List<Method>, val models: Int, val ready: Boolean, val holds: String, val status: String, val source: String)
data class Account(val provider: String, val providerName: String, val id: String, val name: String, val active: Boolean, val plan: String, val windows: List<Window>)
data class Plugin(val name: String, val on: Boolean, val about: String, val choices: List<Choice>)
/** A plugin setting that takes one of a few values. */
data class Choice(val key: String, val values: List<String>, val value: String)
data class Completion(val text: String, val label: String, val detail: String)

private fun <T> JSONArray?.map(f: (JSONObject) -> T): List<T> = this?.let { a -> (0 until a.length()).mapNotNull { a.optJSONObject(it)?.let(f) } }.orEmpty()
private fun <T> JSONObject.list(name: String, f: (JSONObject) -> T): List<T> = optJSONArray(name).map(f)
private fun JSONObject.strings(name: String): List<String> = optJSONArray(name)?.let { a -> (0 until a.length()).map(a::optString) }.orEmpty()
private fun JSONObject.obj(name: String) = optJSONObject(name)

private fun command(o: JSONObject) = Command(o.optString("name"), o.optString("hint"), o.optBoolean("now"))
private fun window(o: JSONObject) = Window(o.optString("name"), o.optDouble("left"), o.optLong("resets"))
private fun tab(o: JSONObject) = Tab(
    o.optString("id"), o.optString("title"), o.optString("peer"), o.optString("where"), o.optBoolean("remote"), o.optString("cwd"), o.optBoolean("busy"), o.optBoolean("online"),
    o.optBoolean("loaded"), o.optBoolean("earlier"), o.optBoolean("streaming"), o.optString("status"), o.optString("model"), o.optString("thinking"), o.strings("levels"),
    o.optDouble("context", 0.0), o.optDouble("cost", 0.0), o.obj("usage")?.let { Usage(it.optString("plan"), it.optLong("at"), it.list("windows", ::window)) },
    o.obj("ask")?.let { Ask(it.optString("id"), it.optString("owner"), it.optString("title"), it.optString("message"), it.strings("choices"), it.optBoolean("free")) },
    o.optInt("unread"), o.list("commands", ::command), o.strings("models"),
)
private fun state(o: JSONObject) = State(
    o.optString("self"), o.optBoolean("up"), o.list("tabs", ::tab), o.obj("claim")?.let { Claim(it.optString("invitation"), it.optString("claimant")) },
    o.obj("invitation")?.let { Invitation(it.optString("code"), it.optLong("expires")) }, o.optString("joining"), o.optString("launching"),
    o.obj("login")?.let { l ->
        Login(l.optString("machine"), l.optString("provider"), l.optString("state"), l.optString("url"), l.optString("instructions"), l.optString("code"), l.optString("detail"),
            l.obj("prompt")?.let { p -> Prompt(p.optString("kind"), p.optString("message"), p.optString("placeholder"), p.list("options") { it.optString("id") to it.optString("label") }) })
    },
    o.optBoolean("acting"), o.optString("summary"), o.optString("latest"), o.list("commands", ::command),
)
private fun home(o: JSONObject) = Home(
    o.list("machines") { m ->
        Machine(m.optString("id"), m.optString("name"), m.optBoolean("self"), m.optBoolean("connected"), m.optString("version"), m.optBoolean("launch"), m.optInt("hue"),
            m.list("running") { Running(it.optString("instance"), it.optString("label"), it.optBoolean("busy")) },
            m.list("folders") { Folder(it.optString("cwd"), it.optInt("threads"), it.optLong("modified"), it.optBoolean("live")) })
    },
    o.list("entries") { e ->
        Entry(e.optString("key"), e.optString("title"), e.optString("machine"), e.optString("cwd"), e.optLong("modified"), e.optBoolean("live"), e.optBoolean("asks"),
            e.optBoolean("open"), e.optBoolean("unstored"), e.optBoolean("deletable"))
    },
)
private fun spans(a: JSONArray?) = a.map { Span(it.optString("t"), it.optBoolean("b"), it.optBoolean("i"), it.optBoolean("c"), it.optBoolean("s"), it.optString("h")) }
private fun block(o: JSONObject): Block = Block(
    o.optString("type"), o.optInt("level"), o.optString("mark"), o.optInt("depth"), spans(o.optJSONArray("spans")), o.optString("lang"), o.optString("text"),
    o.list("blocks", ::block), o.optJSONArray("rows")?.let { rows -> (0 until rows.length()).map { r -> rows.optJSONArray(r)?.let { cells -> (0 until cells.length()).map { spans(cells.optJSONArray(it)) } }.orEmpty() } }.orEmpty(),
)
private fun row(o: JSONObject) = Row(
    o.optString("k"), o.optString("kind"), o.optString("text"), o.optString("via"), o.optBoolean("alarm"), o.strings("images"), o.obj("block")?.let(::block),
    o.optBoolean("live"), o.optString("what"), o.optString("now"), o.optInt("failed"),
    o.list("actions") { Action(it.optString("k"), it.optString("verb"), it.optString("target"), it.optString("result"), it.optBoolean("live"), it.optBoolean("failed")) },
)

fun providers(a: JSONArray?) = a.map { p ->
    Provider(p.optString("id"), p.optString("name"), p.list("methods") { Method(it.optString("auth"), it.optString("label"), it.optString("about"), it.optBoolean("account")) },
        p.optInt("models"), p.optBoolean("ready"), p.optString("holds"), p.optString("status"), p.optString("source"))
}
fun accounts(a: JSONArray?) = a.map { Account(it.optString("provider"), it.optString("provider_name"), it.optString("id"), it.optString("name"), it.optBoolean("active"), it.optString("plan"), it.list("windows", ::window)) }
fun plugins(a: JSONArray?) = a.map { Plugin(it.optString("name"), it.optBoolean("on"), it.optString("about"), it.list("choices") { c -> Choice(c.optString("key"), c.strings("values"), c.optString("value")) }) }
fun completions(a: JSONArray?) = a.map { Completion(it.optString("text"), it.optString("label"), it.optString("detail")) }

/** A reply to an intent: its result, or the words of why it failed. */
class Reply(val result: Any?, val error: String?) {
    val obj get() = result as? JSONObject
    val array get() = result as? JSONArray
}

/**
 * The app's side of `orb app`, the view every Orb app draws (bridge/view): what it shows, kept as
 * the view sends it, and the intents it sends back. Everything the app does with Orb goes here.
 */
class View(private val scope: CoroutineScope, orb: Orb, private val budget: Int, private val alert: (tab: String, title: String, text: String) -> Unit) {
    var state by mutableStateOf(State())
        private set
    var home by mutableStateOf(Home())
        private set
    /** Each followed conversation's rows, as the view last patched them. */
    val rows = mutableStateMapOf<String, SnapshotStateList<Row>>()
    private var visible = false
    private var shown = emptyList<String>()

    // A view started again (its process died) hears what this side shows, and sends everything again.
    private val lines = orb.lines(scope, "orb-app", "app", "--name", "this phone", heard = ::heard) {
        send("hello", "budget" to budget); send("visible", "on" to visible); send("show", "tabs" to JSONArray(shown))
    }

    init { send("hello", "budget" to budget) }

    private fun heard(o: JSONObject) {
        scope.launch(Dispatchers.Main) {
            when (o.optString("t")) {
                "state" -> {
                    state = state(o)
                    rows.keys.retainAll(state.tabs.map(Tab::id).toSet())
                }
                "home" -> home = home(o)
                "rows" -> rows.getOrPut(o.optString("tab")) { mutableStateListOf() }.apply {
                    val at = o.optInt("at").coerceAtMost(size)
                    subList(at, size).clear()
                    addAll(o.optJSONArray("rows").map(::row))
                }
                "alert" -> alert(o.optString("tab"), o.optString("title"), o.optString("text"))
            }
        }
    }

    private fun intent(name: String, args: Array<out Pair<String, Any?>>) = JSONObject().put("do", name).apply { args.forEach { (k, v) -> put(k, v ?: JSONObject.NULL) } }

    /** Asks the view something it does at once or on its own time; nothing comes back. */
    fun send(name: String, vararg args: Pair<String, Any?>) {
        scope.launch(Dispatchers.IO) { lines.send(intent(name, args)) }
    }

    /** Asks the view something and waits for its reply. */
    suspend fun ask(name: String, vararg args: Pair<String, Any?>): Reply {
        val r = lines.call(intent(name, args), 120_000)
        return Reply(r.opt("result").takeUnless { it == JSONObject.NULL }, r.optJSONObject("error")?.optString("message") ?: r.optString("error").ifEmpty { null })
    }

    fun visible(on: Boolean) { visible = on; send("visible", "on" to on) }
    fun show(tabs: List<String>) { if (tabs != shown) { shown = tabs; send("show", "tabs" to JSONArray(tabs)) } }
    fun tab(id: String?) = state.tabs.firstOrNull { it.id == id }
    val self get() = home.machines.firstOrNull { it.self }
    fun machine(id: String) = home.machines.firstOrNull { it.id == id }
    /** The hue a machine's sessions are told apart by: 0 for this phone, 1 to 6 for a peer, as the view assigns them. */
    fun hue(peer: String?) = if (peer == null || peer == state.self) 0 else machine(peer)?.hue?.coerceAtLeast(1) ?: 1

    /** Stops this machine's Bridge: it starts again with the current environment (the Linux). */
    fun restart() = send("restart")
}
