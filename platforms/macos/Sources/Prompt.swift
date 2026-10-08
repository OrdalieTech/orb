import SwiftUI

/// The prompt box: a draft per conversation, the slash palette and `@` completion above it, and
/// under it where it runs, the model and its reasoning, what the context and plan have left, and
/// send, or queue and steer while Orb works. ↩ sends, ⇧↩ breaks the line, esc stops a turn.
struct PromptBox<Place: View>: View {
    @Environment(Orb.self) private var orb
    @Environment(Nav.self) private var nav
    let tab: Tab?
    let place: Place
    let send: (String) -> Void
    @State private var cursor = 0
    @State private var found: [Completion] = []
    @State private var picked = 0
    @State private var height: CGFloat = 22
    @State private var recalled: Int? = nil // which of the messages sent here the box holds, ↑ walking back

    init(tab: Tab?, place: Place, send: @escaping (String) -> Void) {
        self.tab = tab
        self.place = place
        self.send = send
    }

    private var draft: Binding<String> {
        Binding { nav.drafts[tab?.id ?? "", default: ""] } set: { nav.drafts[tab?.id ?? ""] = $0 }
    }

    /// Typing / opens the palette: the app's commands and the Orb's, filtered as you type.
    private var commands: [Command] {
        let text = draft.wrappedValue
        guard text.hasPrefix("/"), !text.contains(where: \.isWhitespace) else { return [] }
        let q = text.dropFirst().lowercased()
        let all = orb.state.commands + (tab?.commands ?? [])
        return all.filter { $0.name.lowercased().hasPrefix(q) } + all.filter { !$0.name.lowercased().hasPrefix(q) && $0.name.lowercased().contains(q) }
    }

    /// The `@` token the cursor is in, which the Orb completes as the TUI does: skills, then files.
    private var at: String? {
        let before = (draft.wrappedValue as NSString).substring(to: min(cursor, (draft.wrappedValue as NSString).length))
        let token = before.split(separator: /\s/, omittingEmptySubsequences: false).last.map(String.init) ?? ""
        return token.hasPrefix("@") && tab != nil ? token : nil
    }

    private var choices: [(label: String, hint: String, pick: () -> Void)] {
        let commands = commands
        if !commands.isEmpty {
            return commands.map { c in
                ("/" + c.name, c.hint, {
                    if c.now { draft.wrappedValue = ""; send("/" + c.name) } else { draft.wrappedValue = "/" + c.name + " " }
                })
            }
        }
        guard let at else { return [] }
        return found.map { f in
            (f.text.hasPrefix("/skill:") ? "◆ " + f.label : f.label, f.detail, {
                // A folder keeps the token open, so completion goes on inside it.
                let text = draft.wrappedValue as NSString
                let end = text.rangeOfCharacter(from: .whitespacesAndNewlines, range: NSRange(location: cursor, length: text.length - cursor)).location
                let gap = f.text.hasSuffix("/") || f.text.hasSuffix("/\"") ? "" : " "
                draft.wrappedValue = text.substring(to: cursor - (at as NSString).length) + f.text + gap + (end == NSNotFound ? "" : text.substring(from: end))
            })
        }
    }

    var body: some View {
        let choices = choices
        let busy = tab?.busy == true
        VStack(alignment: .leading, spacing: 0) {
            if !choices.isEmpty {
                ScrollViewReader { scroll in
                    ScrollView {
                        VStack(spacing: 0) {
                            ForEach(choices.indices, id: \.self) { i in
                                HStack(spacing: 12) {
                                    Text(choices[i].label).font(.mono(Size.body, .semibold)).lineLimit(1)
                                    Text(choices[i].hint).font(.mono(Size.small)).foregroundStyle(Ink.meta).lineLimit(1)
                                    Spacer(minLength: 0)
                                }
                                .padding(.horizontal, 14).padding(.vertical, 6)
                                .background(i == picked ? Ink.fg.opacity(0.08) : .clear).contentShape(.rect)
                                .onTapGesture { choices[i].pick() }
                                .id(i)
                            }
                        }
                    }
                    .frame(maxHeight: 220).fixedSize(horizontal: false, vertical: true)
                    .onChange(of: picked) { scroll.scrollTo(picked) }
                }
                Rule()
            }
            Editor(text: draft, cursor: $cursor, height: $height, focus: nav.focus) { key in
                switch key {
                case .up where !choices.isEmpty: picked = max(0, picked - 1)
                case .down where !choices.isEmpty: picked = min(choices.count - 1, picked + 1)
                // ↑ in an empty box brings back what was sent before, as in the TUI; ↓ comes forward.
                case .up, .down:
                    let sent = tab.map { orb.feed($0.id).rows.filter { $0.kind == "you" && $0.via.isEmpty }.map(\.text) } ?? []
                    guard draft.wrappedValue.isEmpty || recalled.map({ sent.indices.contains($0) && sent[$0] == draft.wrappedValue }) == true else { return false }
                    let next = (recalled ?? sent.count) + (key == .up ? -1 : 1)
                    guard next >= 0 else { return true }
                    recalled = next < sent.count ? next : nil
                    draft.wrappedValue = recalled.map { sent[$0] } ?? ""
                // ↩ on a command typed in full runs it; otherwise ↩ and ⇥ take the choice.
                case .enter where commands.indices.contains(picked) && draft.wrappedValue == "/" + commands[picked].name: take().map(send)
                case .tab where !choices.isEmpty, .enter where !choices.isEmpty: choices[min(picked, choices.count - 1)].pick()
                case .enter: take().map(send)
                case .escape where !choices.isEmpty: found = []; if draft.wrappedValue.hasPrefix("/") { draft.wrappedValue = "" }
                case .escape where busy: orb.send("abort", ["tab": tab!.id])
                default: return false
                }
                return true
            }
            .frame(height: min(max(height, 22), 260))
            .overlay(alignment: .topLeading) {
                if draft.wrappedValue.isEmpty {
                    Text(busy ? "steer or queue a message" : "What should we work on?").font(.mono(Size.body + 1)).foregroundStyle(Ink.meta).allowsHitTesting(false)
                }
            }
            .padding(.horizontal, 14).padding(.top, 12).padding(.bottom, 6)
            HStack(spacing: 14) {
                place.font(.mono(Size.small, .semibold))
                if let tab { Model(t: tab) }
                Spacer(minLength: 0)
                if let tab {
                    Gauges(t: tab)
                    Button(">_") { nav.toggleTerminal(tab.id) }.buttonStyle(.plain).font(.mono(Size.body, .semibold))
                        .foregroundStyle(nav.terminals.contains(tab.id) ? Ink.fg : Ink.mute).help("Terminal where it runs  ⌘J")
                }
                if busy && draft.wrappedValue.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    Pill("stop", color: Ink.rupture) { orb.send("abort", ["tab": tab!.id]) }
                } else if busy {
                    Pill("queue") { take().map(send) }
                    Pill("steer", filled: true) { take().map { orb.send("steer", ["tab": tab!.id, "text": $0]) } }
                } else {
                    Pill("send", filled: true) { take().map(send) }
                }
            }
            .padding(.horizontal, 14).padding(.bottom, 10)
        }
        .surface(14)
        .frame(maxWidth: 820).padding(.horizontal, 16).padding(.bottom, 16)
        // Files dropped from the Finder are cited, as `@` would: this Mac's own, for a conversation here.
        .dropDestination(for: URL.self) { urls, _ in
            guard tab?.remote == false else { return false }
            let cites = urls.map { $0.path.contains(" ") ? "@\"\($0.path)\"" : "@" + $0.path }
            draft.wrappedValue += (draft.wrappedValue.isEmpty || draft.wrappedValue.hasSuffix(" ") ? "" : " ") + cites.joined(separator: " ") + " "
            return true
        }
        .onChange(of: choices.count) { picked = 0 }
        .task(id: at) {
            guard let at, let tab else { found = []; return }
            try? await Task.sleep(for: .milliseconds(120))
            found = (try? await orb.ask("complete", ["tab": tab.id, "text": String(at.dropFirst())], as: [Completion].self)) ?? []
        }
    }

    private func take() -> String? {
        let text = draft.wrappedValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return nil }
        draft.wrappedValue = ""
        recalled = nil
        return text
    }
}

/// The model and its reasoning as a small meter (one bar per level it takes, lit up to the
/// current one); a click, ⌘⇧M or /model opens the picker.
private struct Model: View {
    @Environment(Nav.self) private var nav
    let t: Tab

    var body: some View {
        let at = t.levels.firstIndex(of: t.thinking) ?? -1
        Button { nav.picking.toggle() } label: {
            HStack(spacing: 6) {
                Text(t.model.isEmpty ? "model" : String(t.model.drop { $0 != "/" }.dropFirst())).lineLimit(1)
                HStack(alignment: .bottom, spacing: 2) {
                    ForEach(t.levels.indices, id: \.self) { i in
                        Rectangle().fill(i <= at && t.levels[i] != "off" ? Ink.fg : Ink.rule).frame(width: 3, height: CGFloat(5 + 2 * i))
                    }
                }
                Image(systemName: "chevron.up.chevron.down").font(.system(size: 8, weight: .semibold))
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain).foregroundStyle(Ink.mute).help(dotted(t.model, t.thinking))
        .popover(isPresented: Bindable(nav).picking, arrowEdge: .top) { ModelPicker(t: t) { nav.picking = false } }
    }
}

/// The models a conversation can switch to, by provider, filtered as you type: ↩ takes the one
/// selected or the first match, ↑↓ move, a double click works too; its reasoning levels under them.
private struct ModelPicker: View {
    @Environment(Orb.self) private var orb
    @Environment(Nav.self) private var nav
    @Environment(\.openSettings) private var openSettings
    let t: Tab
    let done: () -> Void
    @State private var query = ""
    @State private var selected: String? = nil
    @FocusState private var searching: Bool

    var body: some View {
        let shown = t.catalog.map { ($0.provider, $0.models.filter { query.isEmpty || $0.localizedCaseInsensitiveContains(query) }) }.filter { !$0.1.isEmpty }
        let flat = shown.flatMap(\.1)
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                TextField("Search \(t.models.count) models", text: $query).textFieldStyle(.plain).focused($searching)
                    .onSubmit { pick(selected.flatMap { flat.contains($0) ? $0 : nil } ?? flat.first) }
                    .onKeyPress(.downArrow) { selected = step(flat, 1); return .handled }
                    .onKeyPress(.upArrow) { selected = step(flat, -1); return .handled }
            }
            .font(.mono(Size.body + 1)).padding(.horizontal, 14).padding(.vertical, 11)
            Divider()
            Group {
                if t.models.isEmpty {
                    ContentUnavailableView {
                        Label("No model yet", systemImage: "key")
                    } description: { Text("Sign in to a provider, or add an API key.") } actions: {
                        Button("Providers…") { done(); nav.pane = "providers"; openSettings() }
                    }
                } else if flat.isEmpty {
                    ContentUnavailableView.search(text: query)
                } else {
                    ScrollViewReader { scroll in
                        List(selection: $selected) {
                            ForEach(shown, id: \.0) { provider, models in
                                // A quiet heading, not a sticky one: it scrolls with its models.
                                Caps(provider).padding(.top, 8).selectionDisabled().listRowSeparator(.hidden)
                                ForEach(models, id: \.self) { m in
                                    HStack {
                                        Text(m.dropFirst(provider.count + 1)).font(.mono(Size.body))
                                        Spacer()
                                        if m == t.model { Image(systemName: "checkmark").font(.system(size: 11, weight: .bold)) }
                                    }
                                    .tag(m).listRowSeparator(.hidden)
                                }
                            }
                        }
                        .listStyle(.plain).scrollContentBackground(.hidden)
                        .contextMenu(forSelectionType: String.self, menu: { _ in }, primaryAction: { pick($0.first) })
                        .onChange(of: selected) { if let selected { scroll.scrollTo(selected) } }
                    }
                }
            }
            .frame(maxHeight: .infinity)
            if !t.levels.isEmpty {
                Divider()
                HStack {
                    Label("Reasoning", systemImage: "brain").foregroundStyle(.secondary)
                    Spacer()
                    Picker("Reasoning", selection: Binding { t.thinking } set: { orb.send("thinking", ["tab": t.id, "name": $0]) }) {
                        ForEach(t.levels, id: \.self) { Text($0).tag($0) }
                    }
                    .labelsHidden().pickerStyle(.menu).fixedSize()
                }
                .font(.mono(Size.small + 1)).padding(.horizontal, 14).padding(.vertical, 8)
            }
        }
        .frame(width: 360, height: 420)
        .onAppear {
            searching = true
            selected = t.model
        }
    }

    private func step(_ flat: [String], _ by: Int) -> String? {
        guard !flat.isEmpty else { return nil }
        let at = selected.flatMap { flat.firstIndex(of: $0) } ?? (by > 0 ? -1 : flat.count)
        return flat[min(max(at + by, 0), flat.count - 1)]
    }

    private func pick(_ model: String?) {
        if let model, model != t.model { orb.send("model", ["tab": t.id, "name": model]) }
        done()
    }
}

/// What the context holds, the plan's window nearest its limit (a click shows them all), what it cost.
private struct Gauges: View {
    let t: Tab
    @State private var plan = false
    var body: some View {
        HStack(spacing: 12) {
            if t.context >= 0.01 { Text("\(Int(t.context * 100))%").foregroundStyle(t.context > 0.8 ? Ink.rupture : Ink.meta).help("of the context window in use") }
            if let u = t.usage, let w = u.windows.min(by: { $0.left < $1.left }) {
                Button("\(w.name) \(Int(w.left))% left") { plan.toggle() }.buttonStyle(.plain).foregroundStyle(w.left < 15 ? Ink.rupture : Ink.meta)
                    .popover(isPresented: $plan) {
                        VStack(alignment: .leading, spacing: 8) {
                            Caps(dotted(String(t.model.prefix { $0 != "/" }), u.plan, "read " + u.at.ago))
                            ForEach(u.windows, id: \.self) { Quota(w: $0) }
                        }
                        .padding(14).frame(width: 340).font(.mono())
                    }
            }
            if t.cost >= 0.005 { Text(String(format: "$%.2f", t.cost)).foregroundStyle(Ink.meta) }
        }
        .font(.mono(Size.small)).lineLimit(1)
    }
}

private struct Pill: View {
    let label: String
    var filled = false
    var color = Ink.fg
    let action: () -> Void
    init(_ label: String, filled: Bool = false, color: Color = Ink.fg, action: @escaping () -> Void) {
        self.label = label; self.filled = filled; self.color = color; self.action = action
    }
    var body: some View {
        Button(action: action) {
            Text(label).font(.mono(Size.small + 1, .semibold)).foregroundStyle(filled ? Ink.bg : color)
                .padding(.horizontal, 12).padding(.vertical, 4)
                .background(filled ? color : .clear, in: .capsule).overlay(Capsule().stroke(color))
        }
        .buttonStyle(.plain)
    }
}

/// A plugin stopping the world until someone answers: an approval, or one question of a series.
struct Interrupt: View {
    let ask: Ask
    let answer: (String?) -> Void
    @State private var text = ""

    /// A choice, ⌘1 to ⌘9 for the first nine.
    private func choice(_ i: Int) -> some View {
        Button { answer(ask.choices[i]) } label: {
            HStack(spacing: 10) {
                Text("⌘\(i + 1)").font(.mono(Size.small, .semibold)).foregroundStyle(Ink.meta)
                Text(ask.choices[i]).frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.horizontal, 12).padding(.vertical, 7).background(Ink.fg.opacity(0.06), in: .rect(cornerRadius: 8)).contentShape(.rect)
        }
        .buttonStyle(.plain)
        .keyboardShortcut(i < 9 ? KeyboardShortcut(KeyEquivalent(Character(String(i + 1)))) : nil)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Stretch(text: ask.owner == "permissions" ? "APPROVAL" : "QUESTION", height: 30, color: Ink.rupture)
            Text(ask.title).font(.mono(Size.body + 1, .semibold)).textSelection(.enabled)
            if !ask.message.isEmpty { Text(ask.message).font(.mono(Size.small)).foregroundStyle(Ink.meta) }
            ForEach(ask.choices.indices, id: \.self) { i in choice(i) }
            HStack {
                if ask.free {
                    TextField("answer", text: $text).textFieldStyle(.plain).padding(8)
                        .background(Ink.fg.opacity(0.06), in: .rect(cornerRadius: 8))
                        .onSubmit { if !text.isEmpty { answer(text); text = "" } }
                }
                Spacer()
                Button("dismiss") { answer(nil) }.buttonStyle(.plain).foregroundStyle(Ink.meta).keyboardShortcut(.cancelAction)
            }
        }
        .padding(16)
        .surface(14, accent: Ink.rupture.opacity(0.7))
        .frame(maxWidth: 820).padding(.horizontal, 16).padding(.bottom, 16)
    }
}

enum Key { case enter, up, down, tab, escape }

/// The text the prompt box edits: AppKit's own, so typing, undo, dictation and the cursor behave as
/// everywhere on the Mac; it reports its height and the keys the box acts on.
private struct Editor: NSViewRepresentable {
    @Binding var text: String
    @Binding var cursor: Int
    @Binding var height: CGFloat
    let focus: Int // grows each time the box is asked for (⌘L)
    let key: (Key) -> Bool

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSTextView.scrollableTextView()
        let view = scroll.documentView as! NSTextView
        view.delegate = context.coordinator
        view.font = .mono(Size.body + 1)
        view.textColor = NSColor(Ink.fg)
        view.insertionPointColor = NSColor(Ink.fg)
        view.drawsBackground = false
        view.isRichText = false
        view.allowsUndo = true
        view.isAutomaticQuoteSubstitutionEnabled = false
        view.isAutomaticDashSubstitutionEnabled = false
        view.textContainerInset = .zero
        view.textContainer?.lineFragmentPadding = 0
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = false
        DispatchQueue.main.async { view.window?.makeFirstResponder(view) }
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        let view = scroll.documentView as! NSTextView
        if focus != context.coordinator.parent.focus { view.window?.makeFirstResponder(view) }
        context.coordinator.parent = self
        if view.string != text {
            view.string = text
            view.setSelectedRange(NSRange(location: (text as NSString).length, length: 0))
            context.coordinator.changed(view)
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    @MainActor final class Coordinator: NSObject, NSTextViewDelegate {
        var parent: Editor
        init(_ parent: Editor) { self.parent = parent }

        func textDidChange(_ note: Notification) {
            guard let view = note.object as? NSTextView else { return }
            parent.text = view.string
            changed(view)
        }

        func textViewDidChangeSelection(_ note: Notification) {
            guard let view = note.object as? NSTextView else { return }
            let at = view.selectedRange().location
            if parent.cursor != at { parent.cursor = at }
        }

        /// Tokens stay plain text on the wire; the box only draws them, skills inverted and citations underlined.
        func changed(_ view: NSTextView) {
            guard let store = view.textStorage, let layout = view.layoutManager, let container = view.textContainer else { return }
            let all = NSRange(location: 0, length: store.length)
            store.setAttributes([.font: NSFont.mono(Size.body + 1), .foregroundColor: NSColor(Ink.fg)], range: all)
            for m in view.string.matches(of: /^\/[\w:.-]+|(?:^|\s)(\/skill:[\w.-]+|@\S+|#[\w-]+)/) {
                let token = m.output.1 ?? m.output.0
                let range = NSRange(token.startIndex..<token.endIndex, in: view.string)
                if token.hasPrefix("/") {
                    store.addAttributes([.foregroundColor: NSColor(Ink.bg), .backgroundColor: NSColor(Ink.fg)], range: range)
                } else { store.addAttribute(.underlineStyle, value: NSUnderlineStyle.single.rawValue, range: range) }
            }
            layout.ensureLayout(for: container)
            let h = ceil(layout.usedRect(for: container).height)
            if parent.height != h { parent.height = h }
        }

        func textView(_ view: NSTextView, doCommandBy selector: Selector) -> Bool {
            switch selector {
            case #selector(NSResponder.insertNewline(_:)):
                // ⇧↩ and ⌥↩ break the line; ↩ alone is the box's.
                if NSApp.currentEvent?.modifierFlags.intersection([.shift, .option]).isEmpty == false { return false }
                return parent.key(.enter)
            case #selector(NSResponder.moveUp(_:)): return parent.key(.up)
            case #selector(NSResponder.moveDown(_:)): return parent.key(.down)
            case #selector(NSResponder.insertTab(_:)): return parent.key(.tab)
            case #selector(NSResponder.cancelOperation(_:)): return parent.key(.escape)
            default: return false
            }
        }
    }
}
