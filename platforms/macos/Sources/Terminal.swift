import SwiftTerm
import SwiftUI

/// A terminal where a conversation runs: a login shell in its folder on this Mac, a peer's through
/// `orb bridge shell`. Each lives while its conversation is open; hidden, it keeps its state.
@MainActor final class Terminals {
    private var views: [String: LocalProcessTerminalView] = [:]
    private let ended = Ended()

    func view(_ t: Tab, nav: Nav) -> LocalProcessTerminalView {
        if let view = views[t.id] { return view }
        let view = LocalProcessTerminalView(frame: .zero)
        view.font = .mono(Size.small + 1)
        view.nativeForegroundColor = NSColor(Ink.fg)
        view.nativeBackgroundColor = NSColor(Ink.bg)
        view.caretColor = NSColor(Ink.rupture)
        view.optionAsMetaKey = true
        view.processDelegate = ended
        ended.close = { [weak self, weak nav] source in
            guard let id = self?.views.first(where: { $0.value === source })?.key else { return }
            self?.views[id] = nil
            nav?.terminals.remove(id)
        }
        let env = Orb.environment.merging(["TERM": "xterm-256color"]) { _, b in b }.map { "\($0)=\($1)" }
        let cwd = t.cwd.isEmpty ? NSHomeDirectory() : t.cwd
        if t.remote {
            view.startProcess(executable: Orb.binary.path, args: ["bridge", "shell", t.peer, t.cwd], environment: env)
        } else {
            let shell = Orb.environment["SHELL"] ?? "/bin/zsh"
            view.startProcess(executable: shell, args: ["-l"], environment: env, execName: "-" + (shell as NSString).lastPathComponent, currentDirectory: cwd)
        }
        views[t.id] = view
        return view
    }

    func close(_ tab: String) { views.removeValue(forKey: tab)?.terminate() }

    /// A shell that exits closes its pane.
    private final class Ended: LocalProcessTerminalViewDelegate {
        var close: (LocalProcessTerminalView) -> Void = { _ in }
        func processTerminated(source: TerminalView, exitCode: Int32?) {
            if let view = source as? LocalProcessTerminalView { close(view) }
        }
        func sizeChanged(source: LocalProcessTerminalView, newCols: Int, newRows: Int) {}
        func setTerminalTitle(source: LocalProcessTerminalView, title: String) {}
        func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
    }
}

struct TerminalPane: NSViewRepresentable {
    @Environment(Nav.self) private var nav
    let t: Tab

    func makeNSView(context: Context) -> LocalProcessTerminalView {
        let view = nav.shells.view(t, nav: nav)
        DispatchQueue.main.async { view.window?.makeFirstResponder(view) }
        return view
    }

    func updateNSView(_ view: LocalProcessTerminalView, context: Context) {}
}
