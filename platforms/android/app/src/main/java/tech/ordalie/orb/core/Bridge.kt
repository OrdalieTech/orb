package tech.ordalie.orb.core

import androidx.compose.runtime.*
import kotlinx.coroutines.*
import org.json.*

/** A machine on Bridge — this phone first, as a peer of itself — named as it calls itself (host.sessions). */
data class Peer(val id: String, val state: String, val instances: List<Instance>, val host: String = "", val version: String = "") {
    val name: String get() = host.takeUnless { it.isEmpty() || it == "localhost" } ?: id.substringAfterLast(":").take(6)
    val connected get() = state == "connected"
}
data class Instance(val peer: String, val id: String, val alias: String, val title: String = "", val cwd: String = "", val busy: Boolean = false, val session: String = "")

/** A thread stored on a peer, open or not: what the peer's `host.sessions` lists. */
data class Thread(val peer: String, val id: String, val title: String, val cwd: String, val modified: Long)

/** Bridge through `orb bridge pipe`: the same owner API the CLI's bridge commands call. */
class Bridge(private val scope: CoroutineScope, private val orb: Orb) {
    private val pipe = orb.lines(scope, "orb-bridge", "bridge", "pipe")
    var self by mutableStateOf("")
    var up by mutableStateOf(false)
    val peers = mutableStateListOf<Peer>()
    private val fetching = mutableSetOf<String>() // machines whose Orbs are being read
    var claim by mutableStateOf<JSONObject?>(null) // an invitation someone claimed, awaiting approval
    var invitation by mutableStateOf<JSONObject?>(null)

    init { scope.launch { while (isActive) { runCatching { refresh() }; delay(if (peers.isEmpty()) 4000 else 2500) } } }

    suspend fun call(method: String, params: JSONObject = JSONObject()): JSONObject =
        pipe.call(JSONObject().put("method", method).put("params", params), 25_000)

    /** A call on a peer; Bridge itself retries what is safe to repeat. */
    suspend fun remote(peer: String, method: String, params: JSONObject): JSONObject =
        call("remote", JSONObject().put("peer_id", peer).put("method", method).put("params", params))

    suspend fun refresh() {
        val s = call("status").optJSONObject("result") ?: run { up = false; return }
        up = true
        self = s.optString("peer_id")
        val states = s.optJSONObject("peer_states") ?: JSONObject().put(self, "connected")
        // Publish peers at once; their instances follow as each peer answers (a dial can take seconds).
        val next = (listOf(self) + (s.optJSONArray("peers") ?: JSONArray()).let { a -> (0 until a.length()).map(a::getString) })
            .filter { states.optString(it) != "blocked" } // a forgotten peer is gone from this phone's view
            .map { id ->
                val known = peers.firstOrNull { it.id == id }
                Peer(id, if (id == self) "connected" else states.optString(id, "disconnected"), known?.instances ?: emptyList(), if (id == self) "this phone" else known?.host.orEmpty(), known?.version.orEmpty())
            }
        peers.clear(); peers.addAll(next)
        // Each machine answers in its own time: a slow or unreachable one never holds the others up.
        next.filter { fetching.add(it.id) }.forEach { peer ->
            scope.launch {
                try {
                    val found = runCatching { instances(peer.id) }.getOrNull() ?: return@launch
                    val at = peers.indexOfFirst { it.id == peer.id }
                    if (at >= 0) peers[at] = peers[at].copy(instances = found, state = if (found.isNotEmpty()) "connected" else peers[at].state)
                } finally { fetching.remove(peer.id) }
            }
        }
        claim = (s.optJSONArray("pending") ?: JSONArray()).let { a -> (0 until a.length()).map(a::getJSONObject) }
            .firstOrNull { it.optString("claimant").isNotEmpty() && it.optString("status") == "pending" }
        invitation?.let { inv -> if (claim == null && next.any { it.id == inv.optString("claimant") }) invitation = null }
    }

    private suspend fun instances(peer: String): List<Instance> {
        // The catalog is paged, and a peer may remember many past registrations: keep the running ones.
        val running = mutableListOf<JSONObject>()
        var cursor = ""
        for (page in 0 until 32) {
            val r = remote(peer, "instances.list", JSONObject().apply { if (cursor.isNotEmpty()) put("cursor", cursor) }).optJSONObject("result") ?: break
            r.optJSONArray("items")?.let { a -> (0 until a.length()).map(a::getJSONObject).filterTo(running) { it.optBoolean("available", true) } }
            cursor = r.optString("cursor")
            if (cursor.isEmpty()) break
        }
        return running.map {
            val id = it.optString("instance_id")
            val d = remote(peer, "instances.describe", JSONObject().put("instance_id", id)).optJSONObject("result") ?: JSONObject()
            val target = d.optJSONObject("target")
            Instance(peer, id, it.optString("alias"), d.optString("name"), d.optString("cwd"), target?.optString("execution_id").orEmpty().isNotEmpty(), target?.optString("session_id").orEmpty())
        }
    }

    /** Each peer's stored threads, for peers that let this phone start Orb on them (host.launch). */
    val threads = mutableStateMapOf<String, List<Thread>>()

    suspend fun loadThreads(peer: String) {
        val found = mutableListOf<Thread>()
        var cursor = ""
        for (page in 0 until 32) {
            val r = remote(peer, "host.sessions", JSONObject().apply { if (cursor.isNotEmpty()) put("cursor", cursor) })
            val result = r.optJSONObject("result") ?: run { if (r.optJSONObject("error")?.optString("code") == "unauthorized") threads.remove(peer); return }
            result.optString("host").takeIf { it.isNotBlank() && peer != self }?.let { host ->
                peers.indexOfFirst { it.id == peer }.takeIf { it >= 0 }?.let { peers[it] = peers[it].copy(host = host, version = result.optString("version")) }
            }
            result.optJSONArray("items")?.let { a ->
                (0 until a.length()).map(a::getJSONObject).mapTo(found) { t ->
                    Thread(peer, t.optString("session_id"), t.optString("name").takeIf { it.isNotBlank() && it != "null" } ?: t.optString("first").lineSequence().first().ifBlank { t.optString("cwd").substringAfterLast('/') },
                        t.optString("cwd"), t.optLong("modified"))
                }
            }
            cursor = result.optString("cursor")
            if (cursor.isEmpty()) break
        }
        threads[peer] = found
    }

    /** Starts Orb on a peer — in a folder on a new thread, or on a stored thread — and returns it once it is on Bridge. */
    suspend fun launch(peer: String, cwd: String? = null, session: String? = null): Result<Instance> {
        val r = remote(peer, "host.launch", JSONObject().apply { cwd?.let { put("cwd", it) }; session?.let { put("session_id", it) } })
        r.optJSONObject("error")?.let { e ->
            return Result.failure(Exception(when (e.optString("code")) {
                "unauthorized" -> "this device does not let this phone start Orb · on it, run  orb bridge trust $self"
                "not_found" -> "no such folder or thread there"
                "resource_exhausted" -> "too many Orbs started there already"
                "busy" -> "this thread is open in another Orb on that device · continue it there, or turn on its bridge plugin to follow it here"
                else -> e.optString("message")
            }))
        }
        // Returned at once: the session describes itself, and peers' lists refresh on their own (a
        // refresh here would wait for the slowest machine, a reconnecting one up to its timeout).
        val result = r.optJSONObject("result")
        return Result.success(Instance(peer, result?.optString("instance_id").orEmpty(), result?.optString("alias").orEmpty(), cwd = cwd.orEmpty(), session = session.orEmpty()))
    }

    /** A peer's providers and its sign-in status, as `orb login --json` lists them there (host.providers). */
    suspend fun providers(peer: String): List<Provider> {
        val rows = remote(peer, "host.providers", JSONObject()).optJSONObject("result")?.optJSONArray("providers") ?: return emptyList()
        return Provider.parse((0 until rows.length()).joinToString("\n") { rows.get(it).toString() })
    }

    /** Brings a peer's Orb to the latest release (host.update); the words say what happened. */
    suspend fun update(peer: String): String {
        val r = remote(peer, "host.update", JSONObject())
        return r.optJSONObject("result")?.let { listOf(it.optString("status"), it.optString("to")).filter(String::isNotEmpty).joinToString(" · ") }
            ?: r.optJSONObject("error")?.let { if (it.optString("code") == "unauthorized") "that device does not let this phone update it" else it.optString("message") }.orEmpty()
    }

    /** Invites another Orb; the claimant gets full control of this phone's instances once approved. */
    suspend fun invite(): JSONObject? {
        val grant = JSONObject().put("principal", JSONObject().put("peer_id", "").put("subject", JSONObject().put("kind", "controller")))
            .put("group_id", "*").put("include_future", true).put("permissions", JSONArray(PERMISSIONS))
        return call("invite", JSONObject().put("grants", JSONArray().put(grant))).optJSONObject("result").also { invitation = it }
    }

    /** Where a join stands: claiming · waiting (for the inviter's yes) · paired. */
    var joining by mutableStateOf("")

    /**
     * Claims an invitation, waits until the inviter approves this phone's fingerprint, then
     * grants the inviter the same access: pairing is mutual, and nothing is trusted before the yes.
     */
    suspend fun join(invitation: String): String? {
        val inv = parse(invitation) ?: return "not an Orb invitation"
        val peer = inv.optString("peer_id")
        if (inv.optLong("expires") * 1000 < System.currentTimeMillis()) return "this invitation expired; create a new one"
        joining = "claiming"
        call("join", inv).optJSONObject("error")?.let {
            joining = ""
            return if ("identity_conflict" in listOf(it.optString("code"), it.optString("message"))) "another device already used this code: ask for a new one" else it.optString("message")
        }
        joining = "waiting"
        while (System.currentTimeMillis() < inv.optLong("expires") * 1000 && joining == "waiting") {
            val r = remote(peer, "pair.status", JSONObject().put("invitation_id", inv.optString("invitation_id")))
            if (r.optJSONObject("error")?.optString("code") == "not_found") { joining = ""; return "the computer declined, or the code expired" }
            val status = r.optJSONObject("result")?.optString("status")
            if (status == "approved") {
                trust(peer)?.let { joining = ""; return it }
                joining = "paired"; refresh(); return null
            }
            delay(1000)
        }
        joining = ""
        return "not approved in time · run orb bridge pair again"
    }
    fun cancelJoin() { joining = "" }

    /** Approves exactly the claim the owner was shown; the invitation's grants take effect. */
    suspend fun approve(c: JSONObject): String? {
        call("approve", JSONObject().put("invitation_id", c.optString("invitation_id")).put("claimant", c.optString("claimant"))).optJSONObject("error")?.let {
            return if ("identity_conflict" in listOf(it.optString("code"), it.optString("message"))) "Two devices used this code, so someone else saw it: not paired. Invite again where only you see the code." else it.optString("message")
        }
        if (claim?.optString("invitation_id") == c.optString("invitation_id")) claim = null
        refresh()
        return null
    }

    /** What a joining phone gives back to the Orb it joined: its conversations, never the phone itself. */
    private suspend fun trust(peer: String): String? {
        val grant = JSONObject().put("principal", JSONObject().put("peer_id", peer).put("subject", JSONObject().put("kind", "controller")))
            .put("group_id", "*").put("include_future", true).put("permissions", JSONArray(PERMISSIONS - "host.launch"))
        return call("grant", grant).optJSONObject("error")?.optString("message")?.takeUnless { it == "identity_conflict" }
    }

    suspend fun forget(peer: String) { call("block", JSONObject().put("peer_id", peer)); refresh() }

    /** Stops this phone's Bridge: the pipe starts it again with the current environment, and the
     *  Orbs it started end, to reopen with what changed (the Linux, plugins) at their next message. */
    suspend fun restart() { call("stop"); pipe.restart() }


    /** The code other Orbs show and accept: `orb-bridge:v1:` + base64url of the invitation. */
    fun code(inv: JSONObject): String = PREFIX + android.util.Base64.encodeToString(inv.toString().toByteArray(), android.util.Base64.URL_SAFE or android.util.Base64.NO_PADDING or android.util.Base64.NO_WRAP)

    companion object {
        const val PREFIX = "orb-bridge:v1:"
        /** An invitation from a code, raw JSON, or a message that contains either. */
        fun parse(text: String): JSONObject? {
            Regex(Regex.escape(PREFIX) + "([A-Za-z0-9_-]+)").find(text)?.let { m ->
                return runCatching { JSONObject(String(android.util.Base64.decode(m.groupValues[1], android.util.Base64.URL_SAFE or android.util.Base64.NO_PADDING))) }.getOrNull()
            }
            return runCatching { JSONObject(text.substring(text.indexOf('{'), text.lastIndexOf('}') + 1)) }.getOrNull()
        }
        val PERMISSIONS = listOf("instance.list", "instance.inspect", "instance.prompt", "instance.steer", "instance.follow_up", "instance.input.reply", "instance.cancel", "instance.session.manage", "host.launch")
    }
}
