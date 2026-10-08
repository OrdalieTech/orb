import SwiftUI
import UserNotifications

/// Where the app is: the conversation shown (nil is the start screen, kept across launches), each
/// one's draft and terminal, and the Settings pane to open.
@MainActor @Observable final class Nav {
    var tab = UserDefaults.standard.string(forKey: "tab") {
        didSet { UserDefaults.standard.set(tab, forKey: "tab") }
    }
    var drafts: [String: String] = [:]
    var terminals: Set<String> = [] // the conversations whose terminal shows
    var inspector = false
    var picking = false // the model picker shows
    var focus = 0 // asked to focus the prompt box, each time it grows
    var pane = "providers"
    var answered = "" // the pairing claim already answered, until the view drops it
    @ObservationIgnored let shells = Terminals()

    /// Stops following a conversation; the one after it shows in its place.
    func close(_ id: String, _ orb: Orb) {
        let tabs = orb.state.tabs, rest = tabs.filter { $0.id != id }
        if tab == id, let at = tabs.firstIndex(where: { $0.id == id }) { tab = rest.isEmpty ? nil : rest[min(at, rest.count - 1)].id }
        terminals.remove(id)
        shells.close(id)
        drafts[id] = nil
        orb.send("close", ["tab": id])
    }

    func toggleTerminal(_ id: String? = nil) {
        guard let id = id ?? tab else { return }
        if terminals.remove(id) == nil { terminals.insert(id) }
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
    @NSApplicationDelegateAdaptor private var delegate: Delegate

    var body: some Scene {
        SwiftUI.Window("Orb", id: "main") {
            Main().environment(orb).environment(nav)
                .onAppear {
                    orb.alert = delegate.notify
                    delegate.open = { nav.tab = $0 }
                }
        }
        .defaultSize(width: 980, height: 720)
        .commands {
            CommandGroup(replacing: .newItem) {
                Button("New Conversation") { nav.tab = nil }.keyboardShortcut("n")
                Button("Close Conversation") { close() }.keyboardShortcut("w")
            }
            CommandMenu("Conversation") {
                Button("Stop") { nav.tab.map { orb.send("abort", ["tab": $0]) } }.keyboardShortcut(".").disabled(nav.tab == nil)
                Button("Model…") { nav.picking = true }.keyboardShortcut("m", modifiers: [.command, .shift]).disabled(nav.tab == nil)
                Button("Terminal") { nav.toggleTerminal() }.keyboardShortcut("j").disabled(nav.tab == nil)
                Button("Inspector") { nav.inspector.toggle() }.keyboardShortcut("i", modifiers: [.command, .option])
                Button("Write a Message") { nav.focus += 1 }.keyboardShortcut("l")
                Button("Copy Last Answer") {
                    guard let tab = nav.tab else { return }
                    Task {
                        if let answer = try? await orb.ask("send", ["tab": tab, "text": "/copy"], as: Outcome.self).copy, !answer.isEmpty { copy(answer) }
                    }
                }
                .keyboardShortcut("c", modifiers: [.command, .shift]).disabled(nav.tab == nil)
                Divider()
                Button("Previous Conversation") { move(-1) }.keyboardShortcut("[", modifiers: [.command, .shift])
                Button("Next Conversation") { move(1) }.keyboardShortcut("]", modifiers: [.command, .shift])
            }
        }
        Settings { Preferences().environment(orb).environment(nav) }
        MenuBarExtra { Status().environment(orb).environment(nav) } label: {
            let tabs = orb.state.tabs
            Image(systemName: tabs.contains { $0.ask != nil || $0.unread > 0 } ? "circle.inset.filled" : tabs.contains(where: \.busy) ? "circle.dotted" : "circle")
        }
    }

    /// ⌘W closes the conversation shown in the main window; with none shown, or in another window, the window.
    private func close() {
        guard NSApp.keyWindow?.identifier?.rawValue == "main", let id = nav.tab, orb.tab(id) != nil else {
            return NSApp.keyWindow?.performClose(nil) ?? ()
        }
        nav.close(id, orb)
    }

    private func move(_ by: Int) {
        let tabs = orb.state.tabs
        guard !tabs.isEmpty else { return }
        let at = tabs.firstIndex { $0.id == nav.tab } ?? (by > 0 ? -1 : 0)
        nav.tab = tabs[(at + by + tabs.count) % tabs.count].id
    }
}

struct Main: View {
    @Environment(Orb.self) private var orb
    @Environment(Nav.self) private var nav
    @Environment(\.openSettings) private var openSettings

    var body: some View {
        @Bindable var nav = nav
        NavigationSplitView {
            Sidebar().navigationSplitViewColumnWidth(min: 200, ideal: 250, max: 360)
        } detail: {
            Group {
                if let t = orb.tab(nav.tab) { Conversation(t: t).id(t.id) } else { Start() }
            }
            .background(Ink.bg)
        }
        .frame(minWidth: 720, minHeight: 480)
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
        // The Dock counts what waits for you: questions, and answers not read yet.
        .onChange(of: orb.state.tabs.reduce(0) { $0 + $1.unread + ($1.ask == nil ? 0 : 1) }, initial: true) { _, n in NSApp.dockTile.badgeLabel = n > 0 ? "\(n)" : nil }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in orb.visible(true) }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didResignActiveNotification)) { _ in orb.visible(false) }
        .alert("Pair a device?", isPresented: .constant(orb.state.claim.map { $0.id != nav.answered } ?? false), presenting: orb.state.claim) { claim in
            Button("Allow") { nav.answered = claim.id; orb.send("approve") }
            Button("Deny", role: .cancel) { nav.answered = claim.id; orb.send("deny") }
        } message: { claim in
            Text("Check the other device shows this fingerprint:\n\(claim.claimant)\n\nAllowing lets it read and drive this Mac's conversations, run what they run, and start Orb here.")
        }
        .environment(\.go) { outcome in
            if !outcome.tab.isEmpty { nav.tab = outcome.tab }
            if !outcome.copy.isEmpty { copy(outcome.copy) }
            switch outcome.nav {
            case "home": nav.tab = nil
            // A conversation's models are in its picker; with none shown, they come from the providers.
            case "model" where nav.tab != nil: nav.picking = true
            case "model", "providers", "plugins", "pair":
                nav.pane = outcome.nav == "model" ? "providers" : outcome.nav
                openSettings()
            default: break
            }
        }
    }
}

/// The menu bar's Orb: each open conversation, working or asking, a click from the window.
private struct Status: View {
    @Environment(Orb.self) private var orb
    @Environment(Nav.self) private var nav
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Text(orb.state.summary)
        Divider()
        ForEach(orb.state.tabs) { t in
            Button(dotted(t.ask != nil ? "needs you" : t.unread > 0 ? "\(t.unread) new" : t.busy ? "working" : "", t.title.isEmpty ? "new session" : t.title, t.remote ? t.where : "")) { show(t.id) }
        }
        Divider()
        Button("Open Orb") { show(nav.tab) }
        Button("Quit Orb") { NSApp.terminate(nil) }.keyboardShortcut("q")
    }

    private func show(_ tab: String?) {
        nav.tab = tab
        openWindow(id: "main")
        NSApp.activate()
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
@MainActor final class Delegate: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    var open: (String) -> Void = { _ in }
    private var center: UNUserNotificationCenter? { Bundle.main.bundleIdentifier == nil ? nil : .current() }

    /// Closing the window leaves Orb running: what finishes out of sight still says so.
    func applicationShouldTerminateAfterLastWindowClosed(_ app: NSApplication) -> Bool { false }

    func applicationDidFinishLaunching(_ notification: Notification) {
        center?.delegate = self
        center?.requestAuthorization(options: [.alert, .sound]) { _, _ in }
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
            NSApp.activate()
        }
    }
}
