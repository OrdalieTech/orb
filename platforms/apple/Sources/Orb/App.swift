import SwiftUI
import UserNotifications

/// Where the app is: the conversation shown (nil is the start screen, kept across launches), each
/// one's draft, and Settings, its pane, and a pairing code to join there.
@MainActor @Observable final class Nav {
    var tab = UserDefaults.standard.string(forKey: "tab") {
        didSet { UserDefaults.standard.set(tab, forKey: "tab") }
    }
    var drafts: [String: String] = [:]
    var inspector = false
    var picking = false // the model picker shows
    var focus = 0 // asked to focus the prompt box, each time it grows
    var settings = false // Settings shows
    var pane = "providers"
    var machine = "" // the machine Settings shows the providers and plugins of: "" is this device
    var code = "" // a pairing code to join, from a link
    var answered = "" // the pairing claim already answered, until the view drops it

    /// Stops following a conversation; the one after it shows in its place.
    func close(_ id: String, _ orb: Orb) {
        let tabs = orb.state.tabs, rest = tabs.filter { $0.id != id }
        if tab == id, let at = tabs.firstIndex(where: { $0.id == id }) { tab = rest.isEmpty ? nil : rest[min(at, rest.count - 1)].id }
        drafts[id] = nil
        orb.send("close", ["tab": id])
    }

    /// Opens Settings on a pane (the providers by default) of where [t] runs (this device with none).
    func providers(of t: Tab?, pane: String = "providers") {
        self.pane = pane
        machine = t?.remote == true ? t!.peer : ""
        settings = true
    }
}

/// What `send`, `open` and `start` come back with: the tab to show, a screen to open, text to copy.
struct Outcome: Decodable, Empty {
    @D var tab = ""
    @D var nav = ""
    @D var copy = ""
}

@main struct OrbApp: App {
    @State private var orb = Orb()
    @State private var nav = Nav()
    @UIApplicationDelegateAdaptor private var delegate: Delegate

    var body: some Scene {
        WindowGroup {
            Main().environment(orb).environment(nav)
                .onAppear {
                    orb.alert = delegate.notify
                    delegate.open = { nav.tab = $0 }
                }
        }
    }
}

struct Main: View {
    @Environment(Orb.self) private var orb
    @Environment(Nav.self) private var nav

    var body: some View {
        @Bindable var nav = nav
        layout
        .font(.mono())
        .foregroundStyle(Ink.fg)
        .tint(Ink.fg)
        .onChange(of: nav.tab, initial: true) {
            // A Home row selected opens in a tab: the conversation it is, or one started on it.
            if let key = nav.tab, key.hasPrefix("e:") {
                Task { nav.tab = try? await orb.ask("open", ["key": String(key.dropFirst(2))], as: Outcome.self).tab }
            }
            orb.show(nav.tab.map { [$0] } ?? [])
        }
        .modifier(Lifecycle())
        .alert("Pair a device?", isPresented: .constant(orb.state.claim.map { $0.id != nav.answered } ?? false), presenting: orb.state.claim) { claim in
            Button("Allow") { nav.answered = claim.id; orb.send("approve") }
            Button("Deny", role: .cancel) { nav.answered = claim.id; orb.send("deny") }
        } message: { claim in
            Text("Check the other device shows this fingerprint:\n\(claim.claimant)\n\nAllowing lets it read and drive \(device)'s conversations, run what they run, and start Orb here.")
        }
        .environment(\.go) { outcome in
            if !outcome.tab.isEmpty { nav.tab = outcome.tab }
            if !outcome.copy.isEmpty { copy(outcome.copy) }
            switch outcome.nav {
            case "home": nav.tab = nil
            // A conversation's models are in its picker; with none shown, they come from the providers.
            case "model" where nav.tab != nil: nav.picking = true
            case "model", "providers", "plugins": nav.providers(of: orb.tab(nav.tab), pane: outcome.nav == "plugins" ? "plugins" : "providers")
            case "pair":
                nav.pane = outcome.nav
                nav.settings = true
            default: break
            }
        }
    }
}

extension Main {
    /// The phone draws its own bar over the conversation, as the Android app does, with no system
    /// navigation.
    @ViewBuilder private var layout: some View {
        VStack(spacing: 0) {
            Bar()
            Rule()
            page
        }
        .background(Ink.bg)
    }

    private var page: some View {
        Group {
            if let t = orb.tab(nav.tab) { Conversation(t: t).id(t.id) } else { Start() }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Ink.bg)
    }
}

/// The phone's one bar, as on Android: the wordmark, which is Home; a tab per open conversation,
/// its square in the colour of the machine it runs on, red while it asks; the menu.
private struct Bar: View {
    @Environment(Orb.self) private var orb
    @Environment(Nav.self) private var nav

    var body: some View {
        HStack(spacing: 0) {
            Button { nav.tab = nil } label: { Stretch(text: "ORB", height: 18, color: orb.tab(nav.tab) == nil ? Ink.fg : Ink.meta) }
                .padding(.horizontal, 14)
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 0) { ForEach(orb.state.tabs) { tab($0) } }
            }
            Menu {
                if let t = orb.tab(nav.tab) { Button("Details") { nav.inspector = true }; Button("Providers") { nav.providers(of: t) } }
                Button("Bridge") { nav.pane = "pair"; nav.settings = true }
                Button("Plugins") { nav.providers(of: orb.tab(nav.tab), pane: "plugins") }
            } label: { Image(systemName: "line.3.horizontal").frame(width: 44, height: 44) }.accessibilityLabel("Menu")
        }
        .frame(height: 46).buttonStyle(.plain).foregroundStyle(Ink.fg)
    }

    private func tab(_ t: Tab) -> some View {
        let on = t.id == nav.tab
        return HStack(spacing: 7) {
            Button { nav.tab = t.id } label: {
                HStack(spacing: 7) {
                    Rectangle().fill(t.ask != nil ? Ink.rupture : orb.hue(t.peer)).frame(width: 7, height: 7).opacity(t.busy ? 0.5 : 1)
                    Text(t.title.isEmpty ? "new session" : t.title).font(.mono(Size.small + 1, on || t.unread > 0 ? .semibold : .regular)).lineLimit(1).frame(maxWidth: 150)
                    if t.unread > 0 { Text("\(t.unread)").font(.mono(Size.label, .semibold)) }
                }
                .frame(maxHeight: .infinity)
            }
            if on { Button("×") { nav.close(t.id, orb) }.foregroundStyle(Ink.meta) }
        }
        .foregroundStyle(on ? Ink.fg : Ink.mute).padding(.horizontal, 8)
        .overlay(alignment: .bottom) { Rectangle().fill(on ? Ink.fg : .clear).frame(height: 2) }
    }
}

/// What the system tells the view and shows of it: whether the app is in front, a count on its
/// icon of what waits for you (questions, answers not read yet), Settings, and links to pair.
private struct Lifecycle: ViewModifier {
    @Environment(Orb.self) private var orb
    @Environment(Nav.self) private var nav
    @Environment(\.scenePhase) private var phase

    func body(content: Content) -> some View {
        let waiting = orb.state.tabs.reduce(0) { $0 + $1.unread + ($1.ask == nil ? 0 : 1) }
        @Bindable var nav = nav
        content
            .onChange(of: waiting, initial: true) { _, n in UNUserNotificationCenter.current().setBadgeCount(n) }
            .onChange(of: phase, initial: true) { orb.visible(phase == .active) }
            // Asked once paired, when there is something to tell.
            .onChange(of: orb.home.machines.count > 1, initial: true) { _, paired in
                if paired { UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) { _, _ in } }
            }
            .sheet(isPresented: $nav.settings) { Preferences() }
            // A pairing code opened as a link (the camera's, a message's) joins from Settings.
            .onOpenURL { url in
                guard url.scheme == "orb-bridge" else { return }
                nav.code = url.absoluteString
                nav.pane = "pair"
                nav.settings = true
            }
    }
}


/// Does what an intent came back with: show a tab, open a screen, copy.
private struct Go: EnvironmentKey {
    static let defaultValue: @MainActor (Outcome) -> Void = { _ in }
}

extension EnvironmentValues {
    var go: @MainActor (Outcome) -> Void {
        get { self[Go.self] }
        set { self[Go.self] = newValue }
    }
}

/// Notifications for what the view says happened out of sight; clicking one shows its conversation.
@MainActor final class Delegate: NSObject, UNUserNotificationCenterDelegate {
    var open: (String) -> Void = { _ in }
    private var center: UNUserNotificationCenter? { Bundle.main.bundleIdentifier == nil ? nil : .current() }

    func started() {
        center?.delegate = self
    }

    func notify(tab: String, title: String, text: String) {
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = text
        content.userInfo = ["tab": tab]
        center?.add(UNNotificationRequest(identifier: tab, content: content, trigger: nil))
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .sound]
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let tab = response.notification.request.content.userInfo["tab"] as? String ?? ""
        await MainActor.run {
            open(tab)
        }
    }
}

extension Delegate: UIApplicationDelegate {
    func application(_ application: UIApplication, didFinishLaunchingWithOptions options: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        started()
        return true
    }
}
