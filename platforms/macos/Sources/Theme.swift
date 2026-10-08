import SwiftUI

/// Ordalie tokens, as on Android (ui/Theme.kt): paper and charcoal, one rupture red, a blue kept
/// for remote Orbs acting here. Each tone follows the system's light or dark appearance.
enum Ink {
    static let bg = tone(0xE3E7E0, 0x1C201E)
    static let fg = tone(0x2A2D2B, 0xDADFD8)
    static let mute = tone(0x4F5550, 0xA8AEA9)
    static let meta = tone(0x7E847F, 0x7A817C)
    static let rule = tone(0x9EA49F, 0x4E5550)
    static let raised = tone(0xECEFE9, 0x242927)
    static let rupture = Color(rgb: 0xC94A3D)
    static let blue = Color(rgb: 0x173E78)
    static let paper = Color(rgb: 0xFAF9F6)

    /// A peer's hue, as the view assigns it (1 to 6; this machine, 0, takes the ink): six tones
    /// apart from each other and from the rupture red, as on Android.
    static func hue(_ n: Int) -> Color {
        let tones: [(UInt32, UInt32)] = [(0x2F5FA8, 0x6E9BE0), (0x1F7F73, 0x4FBFAE), (0x9A6A12, 0xD9A441), (0x6A47A8, 0xA88BE0), (0x4A7A22, 0x8CC255), (0x9C3F86, 0xD77CC4)]
        return n > 0 ? tone(tones[(n - 1) % tones.count].0, tones[(n - 1) % tones.count].1) : fg
    }

    private static func tone(_ light: UInt32, _ dark: UInt32) -> Color {
        Color(nsColor: NSColor(name: nil) { $0.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua ? NSColor(rgb: dark) : NSColor(rgb: light) })
    }
}

extension Orb {
    /// The colour a machine's conversations are told apart by.
    func hue(_ machine: String) -> Color { Ink.hue(self.machine(machine)?.hue ?? 0) }
}

extension NSColor {
    convenience init(rgb: UInt32) {
        self.init(srgbRed: CGFloat(rgb >> 16 & 0xFF) / 255, green: CGFloat(rgb >> 8 & 0xFF) / 255, blue: CGFloat(rgb & 0xFF) / 255, alpha: 1)
    }
}

extension Color {
    init(rgb: UInt32) { self.init(nsColor: NSColor(rgb: rgb)) }
}

/// One scale, denser than the phone's: body 14 · small 12 · label 10.
enum Size {
    static let body: CGFloat = 14, small: CGFloat = 12, label: CGFloat = 10
}

extension Font {
    /// Ubuntu Sans Mono, variable, so its weights are real (Orb.app carries it; run from a checkout,
    /// the system's face stands in).
    static func mono(_ size: CGFloat = Size.body, _ weight: Font.Weight = .regular) -> Font {
        .custom("Ubuntu Sans Mono", size: size).weight(weight)
    }
}

extension NSFont {
    static func mono(_ size: CGFloat) -> NSFont {
        NSFont(name: "Ubuntu Sans Mono", size: size) ?? .monospacedSystemFont(ofSize: size, weight: .regular)
    }
}

/// A label: small caps in medium weight, for the names of regions and states.
struct Caps: View {
    let text: String
    var color = Ink.meta
    init(_ text: String, color: Color = Ink.meta) { self.text = text; self.color = color }
    var body: some View { Text(text.uppercased()).font(.mono(Size.label, .medium)).tracking(0.3).foregroundStyle(color) }
}

/// The EVA title card: Ubuntu Sans Mono Bold stretched 1.9 times as tall. Reserved for events and identities.
struct Stretch: View {
    let text: String
    var height: CGFloat = 28
    var color = Ink.fg
    var body: some View {
        Text(text).font(.mono(height / 1.9 / 0.7, .bold)).tracking(-0.5).foregroundStyle(color)
            .scaleEffect(x: 1, y: 1.9, anchor: .center).frame(height: height).fixedSize()
    }
}

/// Live is a pulsing dot; asking for someone is a solid one.
struct Dot: View {
    var color = Ink.rupture
    var size: CGFloat = 7
    var pulse = false
    @State private var dim = false
    var body: some View {
        Circle().fill(color).frame(width: size, height: size).opacity(pulse && dim ? 0.25 : 1)
            .onAppear { if pulse { withAnimation(.easeInOut(duration: 0.7).repeatForever()) { dim = true } } }
    }
}

extension View {
    /// The surface a box floats on over the conversation: Liquid Glass where macOS has it (built
    /// with its SDK), the raised ink with a hairline before; [accent] edges it either way.
    @ViewBuilder func surface(_ radius: CGFloat, accent: Color? = nil) -> some View {
        let shape = RoundedRectangle(cornerRadius: radius)
        #if compiler(>=6.2)
        if #available(macOS 26, *) {
            // Tinted with the page, so what scrolls under it never competes with what is typed.
            glassEffect(.regular.tint(Ink.bg.opacity(0.65)), in: shape).overlay(shape.stroke(accent ?? .clear))
        } else {
            background(Ink.raised, in: shape).overlay(shape.stroke(accent ?? Ink.rule.opacity(0.7)))
        }
        #else
        background(Ink.raised, in: shape).overlay(shape.stroke(accent ?? Ink.rule.opacity(0.7)))
        #endif
    }
}

/// A hairline separates regions, never words.
struct Rule: View {
    var body: some View { Rectangle().fill(Ink.rule.opacity(0.6)).frame(height: 1) }
}

/// Puts [text] on the clipboard.
@MainActor func copy(_ text: String) {
    NSPasteboard.general.clearContents()
    NSPasteboard.general.setString(text, forType: .string)
}

/// Parts of a line that are there, between dots: "mac · orb · 3h".
func dotted(_ parts: String...) -> String { parts.filter { !$0.isEmpty }.joined(separator: " · ") }

/// A folder's last component, "" for none.
func base(_ path: String) -> String { (path as NSString).lastPathComponent }

extension Int64 {
    /// A time in milliseconds, as a reader wants it: "now", "12m", "3h", "Tue", "4 Oct".
    var ago: String {
        let s = Date.now.timeIntervalSince(Date(timeIntervalSince1970: Double(self) / 1000))
        switch s {
        case ..<60: return "now"
        case ..<3600: return "\(Int(s / 60))m"
        case ..<86400: return "\(Int(s / 3600))h"
        case ..<(6 * 86400): return Date(timeIntervalSince1970: Double(self) / 1000).formatted(.dateTime.weekday())
        default: return Date(timeIntervalSince1970: Double(self) / 1000).formatted(.dateTime.day().month())
        }
    }
}
