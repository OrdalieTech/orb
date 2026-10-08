import SwiftUI

/// A conversation as the view sends it: its rows grouped in turns, what streams in followed only by
/// a reader at the bottom, and the prompt (or the question it asks) under it.
struct Conversation: View {
    @Environment(Orb.self) private var orb
    @Environment(Nav.self) private var nav
    @Environment(\.go) private var go
    let t: Tab
    @State private var position = ScrollPosition(edge: .bottom)
    @State private var follow = true

    var body: some View {
        let turns = turns(orb.rows[t.id] ?? [])
        VStack(spacing: 0) {
            if !t.remote && orb.state.acting { PatternBlue { orb.send("abort", ["tab": t.id]) } }
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    if t.earlier {
                        Button("earlier messages") { orb.send("earlier", ["tab": t.id]) }
                            .buttonStyle(.plain).font(.mono(Size.small, .medium)).foregroundStyle(Ink.meta)
                            .frame(maxWidth: .infinity).padding(.vertical, 12)
                    }
                    ForEach(turns, id: \.first!.k) { Turn(rows: $0, t: t) }
                    // Room under the last message, where the caret waits while Orb writes.
                    Rectangle().fill(t.streaming ? Ink.rupture : .clear).frame(width: 8, height: 16).padding(.leading, 28).padding(.top, 6).padding(.bottom, 24)
                }
                .frame(maxWidth: 820).frame(maxWidth: .infinity)
                .textSelection(.enabled)
            }
            .scrollPosition($position)
            .defaultScrollAnchor(.bottom, for: .initialOffset)
            .overlay { if turns.isEmpty { Caps(t.loaded ? "ready" : "standby") } }
            // Anchored at the top, the list never moves on its own: what streams in follows only a
            // reader at the bottom. Scrolling away leaves them where they read until they come back down, or send.
            .onScrollGeometryChange(for: CGFloat.self, of: \.contentSize.height) { _, _ in if follow { position.scrollTo(edge: .bottom) } }
            .onScrollPhaseChange { _, phase, context in
                if phase == .idle {
                    let g = context.geometry
                    follow = g.contentOffset.y + g.containerSize.height >= g.contentSize.height - 32
                }
            }
            if !t.status.isEmpty {
                Text(t.status).font(.mono(Size.small)).foregroundStyle(Ink.meta).lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 24).padding(.bottom, 4)
            }
            if let ask = t.ask {
                Interrupt(ask: ask) { orb.send("answer", ["tab": t.id, "value": $0 as Any? ?? NSNull()]) }
            } else {
                PromptBox(tab: t, place: Text(t.remote ? t.where : "this Mac").foregroundStyle(t.remote ? Ink.blue.mix(with: Ink.fg, by: 0.3) : Ink.mute)) { text in
                    follow = true
                    position.scrollTo(edge: .bottom)
                    Task { (try? await orb.ask("send", ["tab": t.id, "text": text], as: Outcome.self)).map(go) }
                }
            }
        }
        .navigationTitle(t.title.isEmpty ? "new session" : t.title)
        .navigationSubtitle(dotted(t.remote ? t.where : "", t.cwd.replacingOccurrences(of: NSHomeDirectory(), with: "~"), t.busy ? "working" : t.online ? "" : "offline"))
    }

    /// A turn is one speaker's run: what the person said, or everything Orb said and did until the next one.
    private func turns(_ rows: [Row]) -> [[Row]] {
        var out: [[Row]] = [], run: [Row] = []
        for r in rows {
            if r.kind == "you" {
                if !run.isEmpty { out.append(run) }
                out.append([r])
                run = []
            } else { run.append(r) }
        }
        if !run.isEmpty { out.append(run) }
        return out
    }
}

/// What the person said sits at right on a soft ground (a peer's says it came by Bridge); Orb just
/// speaks, full width, its actions inline.
private struct Turn: View {
    let rows: [Row]
    let t: Tab

    var body: some View {
        if rows.count == 1, let you = rows.first, you.kind == "you" {
            VStack(alignment: .trailing, spacing: 6) {
                if !you.via.isEmpty { Caps("from a peer", color: Ink.blue) }
                Pictures(refs: you.images, tab: t.id)
                if !you.text.isEmpty {
                    Text(tokens(you.text)).lineSpacing(3).padding(.horizontal, 12).padding(.vertical, 8)
                        .background(Ink.fg.opacity(0.07), in: .rect(cornerRadius: 10))
                }
            }
            .frame(maxWidth: .infinity, alignment: .trailing).padding(.leading, 96).padding(.horizontal, 24).padding(.top, 24).padding(.bottom, 12)
        } else {
            VStack(alignment: .leading, spacing: 10) {
                ForEach(rows) { r in
                    switch r.kind {
                    case "md": if let b = r.block { Markdown(b: b) }
                    case "run": if r.actions.count == 1 { ActionView(a: r.actions[0], tab: t.id) } else { Worked(r: r, tab: t.id) }
                    case "note": Folded(text: r.text, color: r.alarm ? Ink.rupture : Ink.meta)
                    default: EmptyView()
                    }
                    // What the tools showed the model stays in view, even while their run is folded.
                    if r.kind == "run" { Pictures(refs: r.images, tab: t.id) }
                }
            }
            .padding(.horizontal, 24).padding(.vertical, 6)
        }
    }

    /// Cited files, skills and references stay recognisable after sending.
    private func tokens(_ text: String) -> AttributedString {
        var out = AttributedString(text)
        for m in text.matches(of: /(?:^|\s)(\/skill:[\w.-]+|@\S+|#[\w-]+)/) {
            let token = m.output.1
            guard let range = Range(token.startIndex..<token.endIndex, in: out) else { continue }
            if token.hasPrefix("/") {
                out[range].foregroundColor = Ink.bg
                out[range].backgroundColor = Ink.fg
            } else { out[range].underlineStyle = .single }
        }
        return out
    }
}

/// A run of actions folds into one line, as the TUI folds exploration, naming what runs now; it opens to the actions.
private struct Worked: View {
    let r: Row, tab: String
    @State private var open = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button { open.toggle() } label: {
                ActionLine(verb: r.live ? "working" : "worked", target: dotted(r.what, open ? "" : r.now),
                           result: r.failed > 0 ? "\(r.failed) failed" : "", live: r.live, failed: r.failed > 0, open: open)
            }
            .buttonStyle(.plain)
            if open { ForEach(r.actions) { ActionView(a: $0, tab: tab).padding(.leading, 16) } }
        }
    }
}

/// A thought or a tool call: one line that opens into its whole text, or what it was given and returned.
private struct ActionView: View {
    @Environment(Orb.self) private var orb
    let a: Action, tab: String
    @State private var open = false
    @State private var detail: [String: String] = [:]

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button { open.toggle() } label: { ActionLine(verb: a.verb, target: a.target, result: a.result, live: a.live, failed: a.failed, open: open) }
                .buttonStyle(.plain)
            if open {
                VStack(alignment: .leading, spacing: 8) {
                    if a.verb == "thought" {
                        Text(detail["text"] ?? "…").font(.mono(Size.small)).foregroundStyle(Ink.mute)
                    } else {
                        if let args = detail["args"], !args.isEmpty { Caps("input"); Text(args).font(.mono(Size.small)).foregroundStyle(Ink.mute) }
                        Caps("output")
                        Text(detail["output"] ?? "…").font(.mono(Size.small)).foregroundStyle(a.failed ? Ink.rupture : Ink.fg)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading).padding(12)
                .background(Ink.raised, in: .rect(cornerRadius: 10)).overlay(RoundedRectangle(cornerRadius: 10).stroke(Ink.rule.opacity(0.6)))
                .padding(.leading, 16).padding(.top, 4)
                .task(id: "\(open)\(a.result)\(a.live)") {
                    detail = (try? await orb.ask("detail", ["tab": tab, "key": a.k], as: [String: String].self)) ?? [:]
                }
            }
        }
    }
}

/// One line per action — a tool call, a thought, a group of them: a mark, the verb, what it acted
/// on, what came of it. Live ones get the dot; ones that open show › or ⌄. Every action aligns.
private struct ActionLine: View {
    let verb: String, target: String, result: String
    let live: Bool, failed: Bool, open: Bool

    var body: some View {
        HStack(spacing: 0) {
            Group { if live { Dot(size: 6, pulse: true) } else { Text(open ? "⌄" : "›").foregroundStyle(Ink.meta) } }.frame(width: 16, alignment: .leading)
            Text(verb).font(.mono(Size.body, .medium)).frame(width: 76, alignment: .leading)
            Text(target).frame(maxWidth: .infinity, alignment: .leading)
            Text(result).font(.mono(Size.small)).foregroundStyle(failed ? Ink.rupture : Ink.meta).frame(maxWidth: 220, alignment: .trailing).padding(.leading, 8)
        }
        .foregroundStyle(live ? Ink.fg : Ink.mute).lineLimit(1).padding(.vertical, 2).contentShape(.rect)
    }
}

/// Long errors fold to three lines; a click opens them.
private struct Folded: View {
    let text: String, color: Color
    @State private var open = false
    var body: some View {
        Text(text).font(.mono(Size.small + 1)).foregroundStyle(color).lineLimit(open ? nil : 3).onTapGesture { open.toggle() }
    }
}

/// One block of what Orb wrote, parsed by the view: a paragraph, heading, list item, quote, code,
/// drawing, table or rule.
struct Markdown: View {
    let b: Block
    var ink = Ink.fg

    var body: some View {
        Group {
            switch b.type {
            case "p": Text(spans(b.spans)).lineSpacing(4)
            case "h": Text(spans(b.spans)).font(.mono(b.level <= 2 ? Size.body + 4 : Size.body + 1, .semibold)).padding(.top, 6)
            case "li":
                HStack(alignment: .firstTextBaseline, spacing: 0) {
                    Text(b.mark).font(.mono(Size.body, .medium)).foregroundStyle(Ink.meta).frame(width: b.mark == "·" ? 16 : 26, alignment: .leading)
                    Text(spans(b.spans)).lineSpacing(4)
                }
            // A quote holds blocks of its own, its bar as tall as they are.
            case "quote":
                VStack(alignment: .leading, spacing: 8) { ForEach(b.blocks.indices, id: \.self) { Markdown(b: b.blocks[$0], ink: Ink.mute) } }
                    .padding(.leading, 14).overlay(alignment: .leading) { Rectangle().fill(Ink.rule).frame(width: 2) }
            case "code", "art": Code(b: b)
            case "table": Table(rows: b.rows)
            case "rule": Rule().padding(.vertical, 6)
            default: EmptyView()
            }
        }
        .foregroundStyle(ink)
        .padding(.leading, CGFloat(b.depth) * 18)
    }
}

/// Styled runs as text: bold, italic, code on a raised ground, struck, links that open.
func spans(_ spans: [Span]) -> AttributedString {
    spans.reduce(into: AttributedString()) { out, s in
        var a = AttributedString(s.t)
        if s.b { a.font = .mono(Size.body, .semibold) }
        if s.i { a.font = (a.font ?? .mono()).italic() }
        if s.c { a.foregroundColor = Ink.mute; a.backgroundColor = Ink.raised }
        if s.s { a.strikethroughStyle = .single }
        if !s.h.isEmpty, let url = URL(string: s.h) { a.link = url; a.underlineStyle = .single }
        out += a
    }
}

/// Code and drawings keep their lines: a long one scrolls sideways.
private struct Code: View {
    let b: Block
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Caps(b.lang)
                Spacer()
                Button("copy") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(b.text, forType: .string)
                }
                .buttonStyle(.plain).font(.mono(Size.label, .medium)).foregroundStyle(Ink.meta)
            }
            ScrollView(.horizontal, showsIndicators: false) {
                Text(b.text).font(.mono(Size.small)).lineSpacing(b.type == "art" ? 0 : 2).fixedSize()
            }
        }
        .padding(12).frame(maxWidth: .infinity, alignment: .leading)
        .background(Ink.raised, in: .rect(cornerRadius: 10)).overlay(RoundedRectangle(cornerRadius: 10).stroke(Ink.rule.opacity(0.6)))
    }
}

/// A table as wide as its cells (each up to 28 characters wide, wrapping beyond); a wide one scrolls sideways.
private struct Table: View {
    let rows: [[[Span]]]
    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            Grid(alignment: .topLeading, horizontalSpacing: 0, verticalSpacing: 0) {
                ForEach(rows.indices, id: \.self) { r in
                    if r > 0 { Rule() }
                    GridRow {
                        ForEach(rows[r].indices, id: \.self) { c in
                            Text(spans(rows[r][c])).font(.mono(Size.body - 1, r == 0 ? .semibold : .regular))
                                .frame(maxWidth: 28 * 8, alignment: .leading).fixedSize(horizontal: false, vertical: true)
                                .padding(.horizontal, 8).padding(.vertical, 5)
                        }
                    }
                    .background(r == 0 ? Ink.raised : .clear)
                }
            }
            .overlay(RoundedRectangle(cornerRadius: 6).stroke(Ink.rule.opacity(0.6)))
        }
    }
}

/// Thumbnails fetched small from the Orb, decoded once; a click opens one large.
private struct Pictures: View {
    let refs: [String], tab: String
    @State private var shown: String? = nil
    var body: some View {
        if !refs.isEmpty {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    ForEach(refs, id: \.self) { ref in Picture(ref: ref, tab: tab, px: 384).frame(height: 112).onTapGesture { shown = ref } }
                }
            }
            .sheet(item: Binding { shown.map(Shown.init) } set: { shown = $0?.ref }) { s in
                Picture(ref: s.ref, tab: tab, px: 2048).frame(minWidth: 480, idealWidth: 900, minHeight: 360, idealHeight: 700).padding().onTapGesture { shown = nil }
            }
        }
    }
    private struct Shown: Identifiable { let ref: String; var id: String { ref } }
}

@MainActor private let pictures = NSCache<NSString, NSImage>()

struct Picture: View {
    @Environment(Orb.self) private var orb
    let ref: String, tab: String, px: Int
    @State private var image: NSImage? = nil
    var body: some View {
        Group {
            if let image { Image(nsImage: image).resizable().scaledToFit().clipShape(.rect(cornerRadius: 6)) } else { RoundedRectangle(cornerRadius: 6).fill(Ink.raised).frame(width: 96) }
        }
        .task(id: ref + "\(px)") {
            let key = "\(ref):\(px)" as NSString
            if let cached = pictures.object(forKey: key) { image = cached; return }
            struct Data64: Decodable { @D var data = "" }
            guard let r = try? await orb.ask("image", ["tab": tab, "ref": ref, "px": px], as: Data64.self), let bytes = Data(base64Encoded: r.data), let decoded = NSImage(data: bytes) else { return }
            pictures.setObject(decoded, forKey: key)
            image = decoded
        }
    }
}

/// Another Orb is driving this Mac. Blue fills space here and nowhere else.
private struct PatternBlue: View {
    let stop: () -> Void
    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 8) {
                Stretch(text: "PATTERN BLUE", height: 26, color: Ink.paper)
                Text("A peer is prompting this Mac").font(.mono(Size.body, .medium)).foregroundStyle(Ink.paper)
            }
            Spacer()
            Button("stop", action: stop).buttonStyle(.plain).font(.mono(Size.body, .semibold)).foregroundStyle(Ink.blue)
                .padding(.horizontal, 14).padding(.vertical, 6).background(Ink.paper, in: .capsule)
        }
        .padding(.horizontal, 24).padding(.vertical, 14).background(Ink.blue)
    }
}
