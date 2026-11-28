import SwiftUI

struct LiveProcess: Identifiable {
    let id: String
    let kind: String
    let verb: String
    let target: String
}

struct LiveInput: Identifiable {
    var id: String { tabId }
    let tabId: String
    let title: String
    let question: String
    let whereLabel: String
    let hue: Color
}

struct LiveAgent: Identifiable {
    var id: String { tabId }
    let tabId: String
    let title: String
    let whereLabel: String
    let hue: Color
    let processes: [LiveProcess]
}

struct LiveUnopened: Identifiable {
    var id: String { instance }
    let instance: String
    let label: String
    let whereLabel: String
    let hue: Color
}

struct LiveActivitySnapshot {
    let inputs: [LiveInput]
    let agents: [LiveAgent]
    let unopened: [LiveUnopened]
    let summary: String

    var isEmpty: Bool { inputs.isEmpty && agents.isEmpty && unopened.isEmpty }
}

@MainActor
func collectLiveActivity(orb: Orb) -> LiveActivitySnapshot {
    let inputs: [LiveInput] = orb.state.tabs.compactMap { t in
        guard let ask = t.ask else { return nil }
        let title = t.title.isEmpty ? "New session" : t.title
        let question = ask.title.isEmpty ? ask.message : ask.title
        let whereLabel = t.where.isEmpty ? (t.remote ? (orb.machine(t.peer)?.name ?? "") : device) : t.where
        return LiveInput(tabId: t.id, title: title, question: question, whereLabel: whereLabel, hue: orb.hue(t.peer))
    }

    let agents: [LiveAgent] = orb.state.tabs.filter { $0.busy || $0.streaming }.map { t in
        let rows = orb.feed(t.id).rows
        var processes: [LiveProcess] = []
        for (ri, r) in rows.enumerated() {
            if r.kind == "run", r.live {
                let liveActions = r.actions.filter(\.live)
                let chosen = liveActions.isEmpty ? (r.actions.last.map { [$0] } ?? []) : liveActions
                if !chosen.isEmpty {
                    for a in chosen {
                        processes.append(LiveProcess(
                            id: "\(r.k)-\(a.k)",
                            kind: "tool",
                            verb: a.verb.isEmpty ? "tool" : a.verb,
                            target: a.target
                        ))
                    }
                } else if !r.now.isEmpty || !r.what.isEmpty {
                    processes.append(LiveProcess(
                        id: "\(r.k)-\(ri)",
                        kind: "tool",
                        verb: r.what.isEmpty ? "working" : r.what,
                        target: r.now
                    ))
                }
            } else if r.kind == "peer", let ex = r.exchange, ex.live {
                let detail = !ex.text.isEmpty ? ex.text : (!ex.steps.isEmpty ? ex.steps : ex.status)
                processes.append(LiveProcess(
                    id: "\(r.k)-ex-\(ri)",
                    kind: "subagent",
                    verb: ex.who.isEmpty ? "subagent" : ex.who,
                    target: detail
                ))
            }
        }
        if processes.isEmpty {
            let verb = t.streaming ? "streaming" : "working"
            let detail = t.status.isEmpty ? (t.streaming ? "writing reply…" : "working…") : t.status
            processes.append(LiveProcess(id: "\(t.id)-idle", kind: "thinking", verb: verb, target: detail))
        }
        let title = t.title.isEmpty ? "New session" : t.title
        let whereLabel = t.where.isEmpty ? (t.remote ? (orb.machine(t.peer)?.name ?? "") : device) : t.where
        return LiveAgent(tabId: t.id, title: title, whereLabel: whereLabel, hue: orb.hue(t.peer), processes: processes)
    }

    let unopened: [LiveUnopened] = orb.home.machines.flatMap { m in
        m.running.filter { $0.busy && !$0.open }.map { r in
            LiveUnopened(
                instance: r.instance,
                label: r.label.isEmpty ? "running agent" : r.label,
                whereLabel: m.here ? device : m.name,
                hue: orb.hue(m.id)
            )
        }
    }

    let totalRunning = agents.count + unopened.count
    var parts: [String] = []
    if totalRunning > 0 { parts.append("\(totalRunning) running") }
    if !inputs.isEmpty { parts.append("\(inputs.count) \(inputs.count == 1 ? "input" : "inputs")") }
    if let first = agents.first?.processes.first {
        parts.append(first.target.isEmpty ? first.verb : "\(first.verb): \(first.target)")
    } else if let q = inputs.first?.question, !q.isEmpty {
        parts.append(q)
    } else if let u = unopened.first {
        parts.append(u.label)
    }

    return LiveActivitySnapshot(
        inputs: inputs,
        agents: agents,
        unopened: unopened,
        summary: parts.joined(separator: " · ")
    )
}

struct LiveActivityBar: View {
    @Environment(Orb.self) private var orb
    @Environment(Nav.self) private var nav
    @Environment(\.go) private var go
    var currentTabId: String? = nil
    @State private var open = false

    var body: some View {
        let snap = collectLiveActivity(orb: orb)
        if !snap.isEmpty {
            let hasInputs = !snap.inputs.isEmpty
            VStack(alignment: .leading, spacing: 0) {
                Button {
                    withAnimation(.easeInOut(duration: 0.18)) { open.toggle() }
                } label: {
                    HStack(spacing: 7) {
                        Dot(color: hasInputs ? Ink.rupture : Ink.fg, size: 6, pulse: true)
                        Text(snap.summary)
                            .font(.mono(12.5, .medium))
                            .foregroundStyle(hasInputs ? Ink.rupture : Ink.fg)
                            .lineLimit(1)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        Text(open ? "⌄" : "›")
                            .font(.mono(14))
                            .foregroundStyle(Ink.mute)
                    }
                    .padding(.vertical, 5)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)

                if open {
                    HStack(alignment: .top, spacing: 10) {
                        Rectangle().fill(Ink.rule).frame(width: 1)
                        VStack(alignment: .leading, spacing: 6) {
                            ForEach(snap.inputs) { item in
                                Button {
                                    if item.tabId != currentTabId { nav.tab = item.tabId } else { open = false }
                                } label: {
                                    HStack(spacing: 6) {
                                        Rectangle().fill(Ink.rupture).frame(width: 6, height: 6)
                                        Text("input").font(.mono(11.5, .semibold)).foregroundStyle(Ink.rupture)
                                        Text(item.question.isEmpty ? item.title : item.question)
                                            .font(.mono(12.5, .medium))
                                            .foregroundStyle(Ink.fg)
                                            .lineLimit(1)
                                            .frame(maxWidth: .infinity, alignment: .leading)
                                        if !item.whereLabel.isEmpty {
                                            Text(item.whereLabel).font(.mono(11)).foregroundStyle(Ink.meta).lineLimit(1)
                                        }
                                    }
                                    .padding(.vertical, 2)
                                    .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                            }

                            ForEach(snap.agents) { agent in
                                Button {
                                    if agent.tabId != currentTabId { nav.tab = agent.tabId } else { open = false }
                                } label: {
                                    VStack(alignment: .leading, spacing: 3) {
                                        HStack(spacing: 6) {
                                            Rectangle().fill(agent.hue).frame(width: 6, height: 6)
                                            Text(agent.title)
                                                .font(.mono(12.5, .medium))
                                                .foregroundStyle(Ink.fg)
                                                .lineLimit(1)
                                                .frame(maxWidth: .infinity, alignment: .leading)
                                            if !agent.whereLabel.isEmpty {
                                                Text(agent.whereLabel).font(.mono(11)).foregroundStyle(Ink.meta).lineLimit(1)
                                            }
                                        }
                                        HStack(alignment: .top, spacing: 9) {
                                            Rectangle().fill(Ink.rule).frame(width: 1)
                                            VStack(alignment: .leading, spacing: 2) {
                                                ForEach(agent.processes) { proc in
                                                    HStack(alignment: .top, spacing: 6) {
                                                        Text(proc.verb).font(.mono(11.5, .medium)).foregroundStyle(Ink.fg).lineLimit(1)
                                                        if !proc.target.isEmpty {
                                                            Text(proc.target)
                                                                .font(.mono(11.5))
                                                                .foregroundStyle(Ink.mute)
                                                                .lineLimit(2)
                                                                .frame(maxWidth: .infinity, alignment: .leading)
                                                        }
                                                    }
                                                }
                                            }
                                        }
                                        .padding(.leading, 3)
                                    }
                                    .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                            }

                            ForEach(snap.unopened) { u in
                                Button {
                                    Task {
                                        if let out = try? await orb.ask("open", ["key": "i:" + u.instance], as: Outcome.self) {
                                            go(out)
                                        }
                                    }
                                } label: {
                                    HStack(spacing: 6) {
                                        Rectangle().fill(u.hue).frame(width: 6, height: 6)
                                        Text(u.label)
                                            .font(.mono(12.5, .medium))
                                            .foregroundStyle(Ink.fg)
                                            .lineLimit(1)
                                            .frame(maxWidth: .infinity, alignment: .leading)
                                        if !u.whereLabel.isEmpty {
                                            Text(u.whereLabel).font(.mono(11)).foregroundStyle(Ink.meta).lineLimit(1)
                                        }
                                        Text("open ›").font(.mono(11.5, .medium)).foregroundStyle(Ink.mute)
                                    }
                                    .padding(.vertical, 2)
                                    .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                    .padding(.leading, 3)
                    .padding(.bottom, 6)
                }
            }
        }
    }
}
