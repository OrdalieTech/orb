import SwiftUI
import UIKit
typealias NativeColor = UIColor
typealias NativeFont = UIFont
typealias NativeImage = UIImage

/// What the app calls the device it runs on, and whether its conversations can run on it: an
/// iPhone runs no Orb of its own yet, and starts them on the machines it is paired with.
let device = "this iPhone", local = false

/// Ordalie tokens, as on Android (ui/Theme.kt), Desktop and Web: teenage engineering anodized field
/// white & OP-XY matte carbon, International Orange (#FF4F00), and Pattern Blue for remote Orbs.
enum Ink {
    static let bg = tone(0xF4F4F1, 0x0D0E10)
    static let fg = tone(0x141413, 0xF2F2EE)
    static let mute = tone(0x575854, 0xA1A39E)
    static let meta = tone(0x8A8B85, 0x6E706B)
    static let rule = tone(0xDCDCD7, 0x26282C)
    static let raised = tone(0xFFFFFF, 0x16171A)
    static let rupture = Color(rgb: 0xFF4F00)
    static let blue = Color(rgb: 0x1446A0)
    static let paper = Color(rgb: 0xFAFAFA)

    /// A peer's hue, as the view assigns it (1 to 6; this machine, 0, takes the ink): six rotary
    /// encoder LED tones apart from each other and from International Orange.
    static func hue(_ n: Int) -> Color {
        let tones: [(UInt32, UInt32)] = [(0x1D63ED, 0x5B9BFF), (0x009E7A, 0x2DD4BF), (0xD98A00, 0xF5B041), (0x7048E8, 0xA78BFA), (0x379E28, 0x6EE755), (0xD63384, 0xF472B6)]
        return n > 0 ? tone(tones[(n - 1) % tones.count].0, tones[(n - 1) % tones.count].1) : fg
    }

    private static func tone(_ light: UInt32, _ dark: UInt32) -> Color {
        Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? UIColor(rgb: dark) : UIColor(rgb: light) })
    }
}

extension Orb {
    /// The colour a machine's conversations are told apart by.
    func hue(_ machine: String) -> Color { Ink.hue(self.machine(machine)?.hue ?? 0) }
}

extension NativeColor {
    convenience init(rgb: UInt32) {
        self.init(red: CGFloat(rgb >> 16 & 0xFF) / 255, green: CGFloat(rgb >> 8 & 0xFF) / 255, blue: CGFloat(rgb & 0xFF) / 255, alpha: 1)
    }
}

extension Color {
    init(rgb: UInt32) { self.init(.sRGB, red: Double(rgb >> 16 & 0xFF) / 255, green: Double(rgb >> 8 & 0xFF) / 255, blue: Double(rgb & 0xFF) / 255) }
}

extension Image {
    init(native: NativeImage) {
        self.init(uiImage: native)
    }
}

/// One scale: body 16 · small 13 · label 11.
enum Size {
    static let body: CGFloat = 16, small: CGFloat = 13, label: CGFloat = 11
}

extension Font {
    /// Ubuntu Sans Mono, variable, so its weights are real (Orb.app carries it; run from a checkout,
    /// the system's face stands in).
    static func mono(_ size: CGFloat = Size.body, _ weight: Font.Weight = .regular) -> Font {
        .custom("Ubuntu Sans Mono", size: size).weight(weight)
    }
}

extension NativeFont {
    static func mono(_ size: CGFloat) -> NativeFont {
        NativeFont(name: "Ubuntu Sans Mono", size: size) ?? .monospacedSystemFont(ofSize: size, weight: .regular)
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
    /// The surface a box floats on over the conversation: Liquid Glass where iOS has it (built
    /// with its SDK), the raised ink with a hairline before; [accent] edges it either way.
    @ViewBuilder func surface(_ radius: CGFloat, accent: Color? = nil) -> some View {
        let shape = RoundedRectangle(cornerRadius: radius)
        #if compiler(>=6.2)
        if #available(macOS 26, iOS 26, *) {
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
    UIPasteboard.general.string = text
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
