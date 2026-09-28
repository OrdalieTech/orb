package tech.ordalie.orb.core

import android.util.Log
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import org.json.JSONObject
import java.io.File
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicLong

/**
 * One long-lived orb process speaking JSON lines — `orb --mode rpc` or `orb bridge pipe`.
 * Lines carrying an id this side issued resolve [call]; every other line is an event.
 * The process is supervised: when it dies it starts again (backing off to 30 s, with a fresh
 * [env]), and the owner hears an `exit` event, then a `restart` event once it is back.
 */
class Lines(scope: CoroutineScope, private val args: List<String>, private val env: () -> Map<String, String>, private val dir: () -> File, private val tag: String) {
    @Volatile private var process = spawn()
    @Volatile private var closed = false
    @Volatile private var soon = false // an asked-for restart comes back at once
    private val pending = ConcurrentHashMap<String, CompletableDeferred<JSONObject>>()
    private val ids = AtomicLong()
    val events = MutableSharedFlow<JSONObject>(extraBufferCapacity = 1024)
    val errors = MutableSharedFlow<String>(extraBufferCapacity = 64)

    private fun spawn(): Process = ProcessBuilder(args).directory(dir()).apply { environment().putAll(env()) }.start()

    init {
        scope.launch(Dispatchers.IO) {
            var backoff = 1000L
            while (true) {
                val p = process
                val began = System.currentTimeMillis()
                launch { runCatching { p.errorStream.bufferedReader().forEachLine { Log.i(tag, it); errors.tryEmit(it) } } }
                runCatching {
                    p.inputStream.bufferedReader().forEachLine { line ->
                        val o = runCatching { JSONObject(line) }.getOrNull() ?: return@forEachLine
                        pending.remove(o.optString("id"))?.complete(o) ?: events.tryEmit(o)
                    }
                }
                runCatching { p.waitFor() }
                pending.values.forEach { it.complete(failure("unavailable", "orb exited")) }
                events.tryEmit(JSONObject().put("type", "exit"))
                if (closed) break
                if (System.currentTimeMillis() - began > 60_000) backoff = 1000L
                if (!soon) delay(backoff).also { backoff = (backoff * 2).coerceAtMost(30_000) }
                soon = false
                if (closed) break
                runCatching { spawn() }.onSuccess { process = it; events.tryEmit(JSONObject().put("type", "restart")) }
                    .onFailure { Log.w(tag, "restart failed", it) }
            }
        }
    }

    /** Writes one line; false when the process is not there to read it. */
    suspend fun send(message: JSONObject): Boolean = withContext(Dispatchers.IO) {
        runCatching { process.outputStream.let { synchronized(this@Lines) { it.write((message.toString() + "\n").toByteArray()); it.flush() } } }.isSuccess
    }

    /** Sends [message] with a fresh id and waits for its answer; a dead process or a timeout answers with an error. */
    suspend fun call(message: JSONObject, timeoutMs: Long = 60_000): JSONObject {
        val id = "k" + ids.incrementAndGet()
        val reply = CompletableDeferred<JSONObject>().also { pending[id] = it }
        return try {
            if (!send(message.put("id", id))) failure("unavailable", "orb is not running")
            else withTimeoutOrNull(timeoutMs) { reply.await() } ?: failure("timeout", "no answer in ${timeoutMs / 1000} s")
        } finally { pending.remove(id) }
    }

    /** Ends this process now; the supervisor starts it again right away, with the current [env] and [dir]. */
    fun restart() { soon = true; process.destroy() }

    /** Ends the process for good and waits for it, so a successor never overlaps it. */
    suspend fun close() = withContext(Dispatchers.IO) {
        closed = true
        process.destroy()
        if (!process.waitFor(3, TimeUnit.SECONDS)) process.destroyForcibly()
    }

    companion object {
        /** An answer in both shapes the owners read: RPC's `success` and the pipe's `error`. */
        fun failure(code: String, message: String): JSONObject =
            JSONObject().put("success", false).put("error", JSONObject().put("code", code).put("message", message))
    }
}
