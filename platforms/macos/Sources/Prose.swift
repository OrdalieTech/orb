import AppKit
import SwiftUI

/// Consecutive paragraphs, headings, list items, quotes and rules of an answer as one AppKit text:
/// a selection runs across them, copies with their formatting, and gets the system's Look Up,
/// Services and links; lists hang from their marks, quotes keep their bar.
struct Prose: NSViewRepresentable, Equatable {
    let blocks: [Block]

    /// Whether a block reads as prose; code, drawings and tables keep views of their own.
    static func holds(_ b: Block) -> Bool { ["p", "h", "li", "quote", "rule"].contains(b.type) }

    func makeNSView(context: Context) -> NSTextView {
        // TextKit 1: quotes and rules are text blocks, which it lays out.
        let view = NSTextView(usingTextLayoutManager: false)
        view.isEditable = false
        view.isSelectable = true
        view.drawsBackground = false
        view.textContainerInset = .zero
        view.textContainer?.lineFragmentPadding = 0
        view.textContainer?.widthTracksTextView = true
        view.linkTextAttributes = [.foregroundColor: NSColor(Ink.fg), .underlineStyle: NSUnderlineStyle.single.rawValue, .cursor: NSCursor.pointingHand]
        view.textStorage?.setAttributedString(Self.text(blocks))
        return view
    }

    func updateNSView(_ view: NSTextView, context: Context) {
        guard let storage = view.textStorage else { return }
        let text = Self.text(blocks)
        guard !storage.isEqual(to: text) else { return }
        // A streamed answer grows under a reader's selection: it stays where it was.
        let selected = view.selectedRanges
        storage.setAttributedString(text)
        view.selectedRanges = selected.filter { NSMaxRange($0.rangeValue) <= storage.length }.nonEmpty ?? [NSValue(range: NSRange(location: 0, length: 0))]
    }

    func sizeThatFits(_ proposal: ProposedViewSize, nsView view: NSTextView, context: Context) -> CGSize? {
        guard let width = proposal.width, width > 0, let container = view.textContainer, let layout = view.layoutManager else { return nil }
        container.containerSize = NSSize(width: width, height: .greatestFiniteMagnitude)
        layout.ensureLayout(for: container)
        return CGSize(width: width, height: ceil(layout.usedRect(for: container).height))
    }

    /// The blocks as attributed text, styled as the app's markdown is.
    static func text(_ blocks: [Block]) -> NSAttributedString {
        let out = NSMutableAttributedString()
        for (i, b) in blocks.enumerated() {
            if i > 0 { out.append(NSAttributedString(string: "\n")) }
            // A list ends with a paragraph's room after it, not an item's.
            let after = i > 0 && blocks[i - 1].type == "li" && b.type != "li" ? 6.0 : 0
            append(b, to: out, indent: CGFloat(b.depth) * 18, ink: NSColor(Ink.fg), quotes: [], before: after)
        }
        return out
    }

    private static func append(_ b: Block, to out: NSMutableAttributedString, indent: CGFloat, ink: NSColor, quotes: [NSTextBlock], before: CGFloat = 0) {
        let style = NSMutableParagraphStyle()
        style.lineSpacing = 4
        style.paragraphSpacing = 10
        style.paragraphSpacingBefore = before
        style.textBlocks = quotes
        style.firstLineHeadIndent = indent
        style.headIndent = indent
        switch b.type {
        case "quote":
            // A quote holds blocks of its own, its bar as tall as they are.
            let bar = NSTextBlock()
            bar.setWidth(2, type: .absoluteValueType, for: .border, edge: .minX)
            bar.setWidth(14, type: .absoluteValueType, for: .padding, edge: .minX)
            bar.setBorderColor(NSColor(Ink.rule), for: .minX)
            for (j, inner) in b.blocks.enumerated() {
                if j > 0 { out.append(NSAttributedString(string: "\n")) }
                append(inner, to: out, indent: indent, ink: NSColor(Ink.mute), quotes: quotes + [bar])
            }
            return
        case "rule":
            let line = NSTextBlock()
            line.setWidth(1, type: .absoluteValueType, for: .border, edge: .maxY)
            line.setBorderColor(NSColor(Ink.rule), for: .maxY)
            style.textBlocks = quotes + [line]
            out.append(NSAttributedString(string: " ", attributes: [.paragraphStyle: style, .font: NSFont.mono(4)]))
            return
        case "h":
            style.paragraphSpacingBefore = max(before, 6)
            out.append(runs(b.spans, size: b.level <= 2 ? Size.body + 4 : Size.body + 1, weight: 600, ink: ink, style: style))
        case "li":
            // The mark sits in the indent; the item's lines hang from where its text starts.
            let width: CGFloat = b.mark == "·" ? 16 : 26
            style.headIndent = indent + width
            style.tabStops = [NSTextTab(textAlignment: .left, location: indent + width)]
            style.paragraphSpacing = 4
            let item = NSMutableAttributedString(string: b.mark + "\t", attributes: [.font: NSFont.mono(Size.body, weight: 500), .foregroundColor: NSColor(Ink.meta), .paragraphStyle: style])
            item.append(runs(b.spans, size: Size.body, weight: 400, ink: ink, style: style))
            out.append(item)
        default:
            out.append(runs(b.spans, size: Size.body, weight: 400, ink: ink, style: style))
        }
    }

    /// Styled runs: bold, italic, code on a raised ground, struck, links that open.
    private static func runs(_ spans: [Span], size: CGFloat, weight: CGFloat, ink: NSColor, style: NSParagraphStyle) -> NSAttributedString {
        let out = NSMutableAttributedString()
        for s in spans {
            var a: [NSAttributedString.Key: Any] = [.font: NSFont.mono(size, weight: s.b ? max(weight, 600) : weight), .foregroundColor: ink, .paragraphStyle: style]
            if s.i { a[.obliqueness] = 0.15 }
            if s.c { a[.foregroundColor] = NSColor(Ink.mute); a[.backgroundColor] = NSColor(Ink.raised) }
            if s.s { a[.strikethroughStyle] = NSUnderlineStyle.single.rawValue }
            if !s.h.isEmpty, let url = URL(string: s.h) { a[.link] = url }
            out.append(NSAttributedString(string: s.t, attributes: a))
        }
        return out
    }
}

private extension Array {
    var nonEmpty: Self? { isEmpty ? nil : self }
}

extension NSFont {
    /// Ubuntu Sans Mono at a weight of its variable axis (400 regular, 500 medium, 600 semibold).
    static func mono(_ size: CGFloat, weight: CGFloat) -> NSFont {
        let base = mono(size)
        let wght = NSNumber(value: 0x7767_6874) // 'wght'
        return NSFont(descriptor: base.fontDescriptor.addingAttributes([.variation: [wght: weight]]), size: size) ?? base
    }
}
