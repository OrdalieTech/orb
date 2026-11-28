package tech.ordalie.orb.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.*
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.unit.*
import tech.ordalie.orb.core.*

data class LiveProcess(
    val kind: String,
    val verb: String,
    val target: String,
)

data class LiveInput(
    val tabId: String,
    val title: String,
    val question: String,
    val where: String,
    val hue: Int,
)

data class LiveAgent(
    val tabId: String,
    val title: String,
    val where: String,
    val hue: Int,
    val processes: List<LiveProcess>,
)

data class LiveUnopened(
    val instance: String,
    val label: String,
    val where: String,
    val hue: Int,
)

data class LiveActivitySnapshot(
    val inputs: List<LiveInput>,
    val agents: List<LiveAgent>,
    val unopened: List<LiveUnopened>,
    val summary: String,
) {
    val empty: Boolean get() = inputs.isEmpty() && agents.isEmpty() && unopened.isEmpty()
}

fun collectLiveActivity(v: View): LiveActivitySnapshot {
    val inputs = v.state.tabs.mapNotNull { t ->
        val ask = t.ask ?: return@mapNotNull null
        val where = t.where.ifBlank { if (t.remote) v.machine(t.peer)?.name.orEmpty() else "this phone" }
        LiveInput(
            tabId = t.id,
            title = t.name,
            question = ask.title.ifBlank { ask.message },
            where = where,
            hue = v.hue(t.peer),
        )
    }

    val agents = v.state.tabs.filter { it.busy || it.streaming }.map { t ->
        val rows = v.rows[t.id].orEmpty()
        val processes = buildList {
            for (r in rows) {
                when (r.kind) {
                    "run" -> if (r.live) {
                        val liveActions = r.actions.filter { it.live }.ifEmpty { listOfNotNull(r.actions.lastOrNull()) }
                        if (liveActions.isNotEmpty()) {
                            liveActions.forEach { a ->
                                add(LiveProcess("tool", a.verb.ifBlank { "tool" }, a.target))
                            }
                        } else if (r.now.isNotBlank() || r.what.isNotBlank()) {
                            add(LiveProcess("tool", r.what.ifBlank { "working" }, r.now))
                        }
                    }
                    "peer" -> r.exchange?.takeIf { it.live }?.let { ex ->
                        val detail = ex.text.ifBlank { ex.steps }.ifBlank { ex.status }
                        add(LiveProcess("subagent", ex.who.ifBlank { "subagent" }, detail))
                    }
                }
            }
            if (isEmpty()) {
                val verb = if (t.streaming) "streaming" else "working"
                val detail = t.status.ifBlank { if (t.streaming) "writing reply…" else "working…" }
                add(LiveProcess("thinking", verb, detail))
            }
        }
        val where = t.where.ifBlank { if (t.remote) v.machine(t.peer)?.name.orEmpty() else "this phone" }
        LiveAgent(
            tabId = t.id,
            title = t.name,
            where = where,
            hue = v.hue(t.peer),
            processes = processes,
        )
    }

    val unopened = v.home.machines.flatMap { m ->
        m.running.filter { it.busy && !it.open }.map { r ->
            LiveUnopened(
                instance = r.instance,
                label = r.label.ifBlank { "running agent" },
                where = if (m.self) "this phone" else m.name,
                hue = v.hue(m.id),
            )
        }
    }

    val totalRunning = agents.size + unopened.size
    val parts = buildList {
        if (totalRunning > 0) add("$totalRunning running")
        if (inputs.isNotEmpty()) add("${inputs.size} ${if (inputs.size == 1) "input" else "inputs"}")
        val firstProc = agents.firstOrNull()?.processes?.firstOrNull()
        if (firstProc != null) {
            val snippet = if (firstProc.target.isNotBlank()) "${firstProc.verb}: ${firstProc.target}" else firstProc.verb
            add(snippet)
        } else if (inputs.isNotEmpty()) {
            val q = inputs.first().question
            if (q.isNotBlank()) add(q)
        } else if (unopened.isNotEmpty()) {
            add(unopened.first().label)
        }
    }

    return LiveActivitySnapshot(
        inputs = inputs,
        agents = agents,
        unopened = unopened,
        summary = parts.joinToString(" · "),
    )
}

@Composable
fun LiveActivityBar(c: Ctx, currentTabId: String? = null, modifier: Modifier = Modifier) {
    val snap = collectLiveActivity(c.v)
    if (snap.empty) return

    var open by remember { mutableStateOf(false) }
    val hasInputs = snap.inputs.isNotEmpty()
    val rule = p.rule

    Column(modifier.fillMaxWidth().animateContentSize()) {
        Row(
            Modifier.fillMaxWidth().press { open = !open }.padding(vertical = 5.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Dot(color = if (hasInputs) Ink.Rupture else p.fg, size = 6.dp, pulse = true)
            Spacer(Modifier.width(7.dp))
            T(
                snap.summary,
                Modifier.weight(1f),
                size = 12.5.sp,
                weight = Medium,
                color = if (hasInputs) Ink.Rupture else p.fg,
                lines = 1,
            )
            Spacer(Modifier.width(6.dp))
            T(if (open) "⌄" else "›", size = 14.sp, color = p.mute)
        }

        if (open) {
            Column(
                Modifier.fillMaxWidth()
                    .padding(start = 3.dp, bottom = 6.dp)
                    .drawBehind { drawRect(rule, size = this.size.copy(width = 1.dp.toPx())) }
                    .padding(start = 10.dp),
                verticalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                snap.inputs.forEach { item ->
                    Row(
                        Modifier.fillMaxWidth().press {
                            if (item.tabId != currentTabId) c.nav.show(item.tabId) else open = false
                        }.padding(vertical = 2.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(6.dp),
                    ) {
                        Dot(color = Ink.Rupture, size = 6.dp, square = true)
                        T("input", size = 11.5.sp, weight = Strong, color = Ink.Rupture, lines = 1)
                        T(
                            item.question.ifBlank { item.title },
                            Modifier.weight(1f),
                            size = 12.5.sp,
                            weight = Medium,
                            color = p.fg,
                            lines = 1,
                        )
                        if (item.where.isNotBlank()) {
                            T(item.where, size = 11.sp, color = p.meta, lines = 1)
                        }
                    }
                }

                snap.agents.forEach { agent ->
                    Column(
                        Modifier.fillMaxWidth().press {
                            if (agent.tabId != currentTabId) c.nav.show(agent.tabId) else open = false
                        },
                        verticalArrangement = Arrangement.spacedBy(3.dp),
                    ) {
                        Row(
                            Modifier.fillMaxWidth().padding(vertical = 1.dp),
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(6.dp),
                        ) {
                            Dot(color = hue(agent.hue), size = 6.dp, square = true)
                            T(agent.title, Modifier.weight(1f), size = 12.5.sp, weight = Medium, color = p.fg, lines = 1)
                            if (agent.where.isNotBlank()) {
                                T(agent.where, size = 11.sp, color = p.meta, lines = 1)
                            }
                        }
                        Column(
                            Modifier.fillMaxWidth()
                                .padding(start = 3.dp)
                                .drawBehind { drawRect(rule, size = this.size.copy(width = 1.dp.toPx())) }
                                .padding(start = 9.dp),
                            verticalArrangement = Arrangement.spacedBy(2.dp),
                        ) {
                            agent.processes.forEach { proc ->
                                Row(
                                    Modifier.fillMaxWidth(),
                                    horizontalArrangement = Arrangement.spacedBy(6.dp),
                                    verticalAlignment = Alignment.Top,
                                ) {
                                    T(proc.verb, size = 11.5.sp, weight = Medium, color = p.fg, lines = 1)
                                    if (proc.target.isNotBlank()) {
                                        T(proc.target, Modifier.weight(1f), size = 11.5.sp, color = p.mute, lines = 2)
                                    }
                                }
                            }
                        }
                    }
                }

                snap.unopened.forEach { u ->
                    Row(
                        Modifier.fillMaxWidth().press { c.open("i:" + u.instance) }.padding(vertical = 2.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(6.dp),
                    ) {
                        Dot(color = hue(u.hue), size = 6.dp, square = true)
                        T(u.label, Modifier.weight(1f), size = 12.5.sp, weight = Medium, color = p.fg, lines = 1)
                        if (u.where.isNotBlank()) {
                            T(u.where, size = 11.sp, color = p.meta, lines = 1)
                        }
                        T("open ›", size = 11.5.sp, weight = Medium, color = p.mute, lines = 1)
                    }
                }
            }
        }
    }
}
