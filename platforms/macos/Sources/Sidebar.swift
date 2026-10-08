import SwiftUI

/// The conversations open in tabs, then each machine's threads, newest first, in a section that
/// folds. Selecting a thread opens it in a tab.
struct Sidebar: View {
    @Environment(Orb.self) private var orb
    @Environment(Nav.self) private var nav
    @State private var search = ""
    @State private var folded: Set<String> = []
    @State private var renaming: (intent: [String: String], name: String)? = nil
    @State private var deleting: Entry? = nil

    var body: some View {
        @Bindable var nav = nav
        List(selection: $nav.tab) {
            Section("Open") {
                ForEach(orb.state.tabs) { tabRow($0) }
                    // Dragged into another place: the view keeps the order, for every app.
                    .onMove { from, to in
                        guard let i = from.first else { return }
                        orb.send("move", ["tab": orb.state.tabs[i].id, "to": to > i ? to - 1 : to])
                    }
            }
            ForEach(orb.home.machines) { m in
                let threads = entries.filter { $0.machine == m.id }
                if !threads.isEmpty {
                    Section(isExpanded: Binding { !folded.contains(m.id) } set: { if $0 { folded.remove(m.id) } else { folded.insert(m.id) } }) {
                        ForEach(threads.prefix(100)) { entryRow($0) }
                    } header: {
                        HStack(spacing: 6) {
                            if !m.here { Circle().fill(Ink.hue(m.hue)).frame(width: 7, height: 7) }
                            Text(m.name)
                        }
                        .contextMenu { if !m.here { Button("Rename…") { renaming = (["machine": m.id], m.name) } } }
                    }
                }
            }
        }
        .searchable(text: $search, placement: .sidebar, prompt: "Threads")
        .toolbar { Button { nav.tab = nil } label: { Label("New Conversation", systemImage: "square.and.pencil") }.help("New conversation  ⌘N") }
        .safeAreaInset(edge: .bottom) {
            VStack(alignment: .leading, spacing: 2) {
                Text(orb.state.up ? orb.state.summary : "starting Bridge…").font(.mono(Size.small)).foregroundStyle(Ink.meta).lineLimit(1)
                if let mine = orb.machine(orb.state.here), orb.state.behind(mine.version) {
                    Text("Orb \(orb.state.latest) is out").font(.mono(Size.small)).foregroundStyle(Ink.mute)
                        .help("This Mac's Bridge runs \(mine.version): orb update, then orb bridge stop && orb bridge start")
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading).padding(12).background(.bar)
        }
        .alert("Rename", isPresented: .constant(renaming != nil)) {
            TextField("name", text: Binding { renaming?.name ?? "" } set: { renaming?.name = $0 })
            Button("Rename") {
                if let r = renaming, !r.name.isEmpty { orb.send("rename", r.intent.merging(["name": r.name]) { a, _ in a }) }
                renaming = nil
            }
            Button("Cancel", role: .cancel) { renaming = nil }
        }
        .confirmationDialog("Delete this thread?", isPresented: .constant(deleting != nil), presenting: deleting) { e in
            Button("Delete “\(e.title)”", role: .destructive) { orb.send("delete", ["key": e.key]); deleting = nil }
            Button("Cancel", role: .cancel) { deleting = nil }
        } message: { _ in Text("It is removed from this Mac for good.") }
    }

    private var entries: [Entry] {
        orb.home.entries.filter { !$0.open && (search.isEmpty || $0.title.localizedCaseInsensitiveContains(search)) }
    }

    private func tabRow(_ t: Tab) -> some View {
        Line(title: t.title.isEmpty ? "new session" : t.title, sub: dotted(t.where, base(t.cwd)), live: t.busy, asks: t.ask != nil, hue: t.remote ? orb.hue(t.peer) : nil)
            .badge(t.unread)
            .tag(t.id)
            .contextMenu {
                Button("Rename…") { renaming = (["tab": t.id], t.title) }
                Button("Close") { nav.close(t.id, orb) }
            }
    }

    private func entryRow(_ e: Entry) -> some View {
        Line(title: e.title, sub: dotted(base(e.cwd), e.modified.ago), live: e.live, asks: e.asks, hue: nil)
            .tag("e:" + e.key)
            .contextMenu {
                if !e.unstored { Button("Rename…") { renaming = (["key": e.key], e.title) } }
                if e.deletable { Button("Delete…", role: .destructive) { deleting = e } }
            }
    }
}

/// A conversation in the sidebar: its name, where and when (a peer's in its hue), and a mark when
/// it works or asks.
private struct Line: View {
    let title: String, sub: String
    let live: Bool, asks: Bool
    let hue: Color?

    var body: some View {
        HStack(spacing: 8) {
            VStack(alignment: .leading, spacing: 1) {
                Text(title).font(.mono()).lineLimit(1)
                Text(sub).font(.mono(Size.small)).foregroundStyle(hue ?? Ink.meta).lineLimit(1)
            }
            Spacer(minLength: 0)
            if asks { Dot() } else if live { Dot(pulse: true) }
        }
        .padding(.vertical, 2)
        .help(title)
    }
}

/// No conversation shown: the wordmark, and a box that starts one, here or wherever is chosen.
struct Start: View {
    @Environment(Orb.self) private var orb
    @Environment(\.go) private var go
    @State private var target: (machine: String, cwd: String, label: String)? = nil

    var body: some View {
        VStack(spacing: 0) {
            Spacer()
            Stretch(text: "ORB", height: 44)
            Text(orb.state.up ? orb.state.launching.isEmpty ? orb.state.summary : orb.state.launching : "starting Bridge…")
                .font(.mono(Size.small)).foregroundStyle(Ink.meta).padding(.top, 14)
            // Where this Mac worked last: one click picks the folder the next message starts in.
            if let mine = orb.machine(orb.state.here), !mine.folders.isEmpty {
                HStack {
                    ForEach(mine.folders.prefix(4), id: \.cwd) { f in
                        Button(base(f.cwd)) { target = target?.cwd == f.cwd ? nil : (mine.id, f.cwd, "this Mac · " + base(f.cwd)) }
                            .buttonStyle(.bordered).tint(target?.cwd == f.cwd ? Ink.rupture : nil).help(f.cwd)
                    }
                }
                .controlSize(.small).padding(.top, 18)
            }
            Spacer()
            PromptBox(tab: nil, place: whereMenu) { text in
                Task {
                    let outcome = if let t = target {
                        try? await orb.ask("start", ["machine": t.machine, "cwd": t.cwd, "text": text], as: Outcome.self)
                    } else {
                        try? await orb.ask("send", ["tab": "", "text": text], as: Outcome.self)
                    }
                    outcome.map(go)
                }
            }
        }
        .navigationTitle("Orb")
    }

    /// Where the next message goes: a new conversation on this Mac, an Orb running somewhere, or a
    /// folder of a machine.
    private var whereMenu: some View {
        Menu(target?.label ?? "this Mac") {
            Button("this Mac") { target = nil }
            let running = orb.home.machines.flatMap { $0.running }
            if !running.isEmpty {
                Section("Running") {
                    ForEach(running, id: \.instance) { r in
                        Button(r.label) { Task { (try? await orb.ask("open", ["key": "i:" + r.instance], as: Outcome.self)).map(go) } }
                    }
                }
            }
            ForEach(orb.home.machines.filter { $0.launch }) { m in
                Menu("on \(m.name)") {
                    Button("where Orb starts") { target = (m.id, "", m.name) }
                    ForEach(m.folders.prefix(12), id: \.cwd) { f in
                        Button(f.cwd) { target = (m.id, f.cwd, dotted(m.name, base(f.cwd))) }
                    }
                }
            }
        }
        .menuStyle(.borderlessButton).fixedSize()
    }
}
