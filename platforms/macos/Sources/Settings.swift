import CoreImage.CIFilterBuiltins
import SwiftUI

/// Settings: the providers and accounts of each machine, Bridge and its devices, and Orb's plugins.
struct Preferences: View {
    @Environment(Nav.self) private var nav
    var body: some View {
        @Bindable var nav = nav
        TabView(selection: $nav.pane) {
            Providers().tabItem { Label("Providers", systemImage: "key") }.tag("providers")
            Devices().tabItem { Label("Bridge", systemImage: "point.3.connected.trianglepath.dotted") }.tag("pair")
            Plugins().tabItem { Label("Plugins", systemImage: "puzzlepiece.extension") }.tag("plugins")
        }
        .frame(width: 780, height: 600)
        .font(.mono())
    }
}

/// Every provider a machine offers and its accounts, as Internet Accounts shows them: the list at
/// left, ready ones first, and what the selected one holds and offers at right; with none
/// selected, every account's quota on one screen.
private struct Providers: View {
    @Environment(Orb.self) private var orb
    @State private var machine = ""
    @State private var providers: [Provider] = []
    @State private var accounts: [Account] = []
    @State private var selected: String? = nil
    @State private var search = ""
    @State private var note = ""

    /// The machine shown: "" is this Mac.
    private var here: String { machine.isEmpty ? orb.state.here : machine }

    var body: some View {
        let shown = providers.filter { search.isEmpty || $0.name.localizedCaseInsensitiveContains(search) }
        HSplitView {
            VStack(spacing: 8) {
                Picker("Machine", selection: $machine) {
                    ForEach(orb.home.machines.filter { $0.here || $0.launch }) { Text($0.name).tag($0.here ? "" : $0.id) }
                }
                .labelsHidden()
                TextField("Search providers", text: $search).textFieldStyle(.roundedBorder)
                List(selection: $selected) {
                    group("Ready", shown.filter(\.ready))
                    group("Your subscription", shown.filter { !$0.ready && $0.methods.contains(where: \.account) })
                    group("API key", shown.filter { !$0.ready && !$0.methods.contains(where: \.account) })
                }
                .listStyle(.sidebar)
                .overlay { if providers.isEmpty { ProgressView() } }
            }
            .padding(.top, 8).padding(.horizontal, 8)
            .frame(minWidth: 230, idealWidth: 250, maxWidth: 300)
            Form {
                if !note.isEmpty { Text(note).foregroundStyle(Ink.rupture) }
                if let p = providers.first(where: { $0.id == selected }) { detail(p) } else { quotas }
            }
            .formStyle(.grouped)
            .frame(minWidth: 380, maxWidth: .infinity, maxHeight: .infinity)
        }
        .task(id: machine) { await load() }
        .onChange(of: orb.state.login?.state) { if orb.state.login?.state == "done" { Task { await load() } } }
        .sheet(isPresented: .constant(orb.state.login != nil)) {
            if let l = orb.state.login { SignIn(l: l, remote: l.machine != orb.state.here) }
        }
    }

    @ViewBuilder private func group(_ name: String, _ rows: [Provider]) -> some View {
        if !rows.isEmpty {
            Section(name) {
                ForEach(rows) { p in
                    Label(p.name, systemImage: p.ready ? "checkmark.circle.fill" : p.methods.contains(where: \.account) ? "person.crop.circle" : "key")
                        .badge(p.ready ? p.models : 0)
                        .tag(p.id)
                }
            }
        }
    }

    @ViewBuilder private func detail(_ p: Provider) -> some View {
        Section {
            LabeledContent("Status", value: p.ready ? "Ready · \(p.holds)" : "Not signed in")
            if p.ready { LabeledContent("Models", value: "\(p.models)") }
        } header: { Text(p.name).font(.mono(Size.body + 4, .semibold)) }
        Section("Sign in") {
            ForEach(p.methods, id: \.self) { m in
                LabeledContent {
                    Button(m.account ? (p.ready ? "Sign In Again" : "Sign In…") : (p.ready ? "Replace Key…" : "Add Key…")) {
                        note = ""
                        orb.send("login", ["machine": here, "provider": p.id, "auth": m.auth])
                    }
                } label: {
                    Text(m.label)
                    Text(m.about)
                }
            }
            // Only this Mac's own store; a credential configured elsewhere is changed there.
            if p.ready && machine.isEmpty {
                if p.status == "oauth" || p.source == "stored" {
                    Button(p.status == "oauth" ? "Sign Out" : "Remove Key", role: .destructive) { Task { note = await run("logout", ["provider": p.id]); await load() } }
                } else {
                    Text("Configured outside Orb (\(p.source)): change it there.").foregroundStyle(Ink.meta)
                }
            }
        }
        let mine = accounts.filter { $0.provider == p.id }
        if !mine.isEmpty { Section("Accounts") { ForEach(mine.indices, id: \.self) { account(mine[$0]) } } }
    }

    /// Every account's plan windows on one screen, as the TUI's Providers view shows them.
    @ViewBuilder private var quotas: some View {
        if accounts.isEmpty {
            ContentUnavailableView("Choose a provider", systemImage: "key", description: Text("Sign in with an account or add an API key; accounts and their quotas show here."))
        } else {
            // An account's id is its provider's: two providers may each have a "default".
            Section("Accounts · quota left") { ForEach(accounts.indices, id: \.self) { account(accounts[$0]) } }
        }
    }

    private func account(_ a: Account) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Label(a.name, systemImage: a.active ? "person.crop.circle.badge.checkmark" : "person.crop.circle")
                    .fontWeight(a.active ? .semibold : .regular)
                Text(dotted(a.provider_name, a.plan.capitalized)).foregroundStyle(Ink.meta)
                Spacer()
                if a.active { Text("in use").foregroundStyle(Ink.meta) } else {
                    Button("Use") { Task { note = await run("account", ["machine": here, "provider": a.provider, "account": a.id]); await load() } }
                }
            }
            ForEach(a.windows, id: \.self) { Quota(w: $0) }
        }
        .padding(.vertical, 2)
    }

    private func load() async {
        let here = here
        async let p = try? orb.ask("providers", ["machine": here], as: [Provider].self)
        async let a = try? orb.ask("accounts", ["machine": here], as: [Account].self)
        providers = await p ?? providers
        accounts = await a ?? accounts
    }

    private func run(_ intent: String, _ args: [String: Any]) async -> String {
        do { _ = try await orb.ask(intent, args); return "" } catch { return error.localizedDescription }
    }
}

/// One plan window: what is left as a gauge, in the rupture red once it runs low, and when it resets.
struct Quota: View {
    let w: Window
    var body: some View {
        HStack(spacing: 10) {
            Text(w.name).foregroundStyle(Ink.meta).frame(width: 64, alignment: .leading)
            Gauge(value: min(max(w.left, 0), 100), in: 0...100) {}.gaugeStyle(.linearCapacity).labelsHidden()
                .tint(w.left < 15 ? Ink.rupture : w.left < 40 ? .orange : Ink.fg)
            Text("\(Int(w.left))%").monospacedDigit().frame(width: 40, alignment: .trailing)
            Group {
                if w.resets > 0 { Text(Date(timeIntervalSince1970: Double(w.resets) / 1000), style: .relative) } else { Text("") }
            }
            .foregroundStyle(Ink.meta).frame(width: 96, alignment: .trailing)
        }
        .font(.mono(Size.small))
    }
}

/// A sign-in a machine runs: a browser page, a device code, or a question, as the TUI shows them.
private struct SignIn: View {
    @Environment(Orb.self) private var orb
    @Environment(\.openURL) private var openURL
    let l: Login, remote: Bool
    @State private var text = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Caps("sign in · \(l.provider)")
            switch l.state {
            case "browser":
                Text("Continue in the browser").font(.mono(Size.body + 4, .semibold))
                // A machine signing in over Bridge listens on its own localhost: the code or final URL comes back by paste.
                Text(remote ? "Finish on the page that opened, then paste the code it shows or the final redirect URL here." : "Finish on the page that opened; it comes back to Orb by itself.").foregroundStyle(Ink.mute)
                Button("Open the page again") { open(l.url) }
                if let p = l.prompt, p.kind == "manual_code" { answer(p) }
            case "code":
                Text("Enter this code").foregroundStyle(Ink.mute)
                Text(l.code).font(.mono(30, .bold)).textSelection(.enabled)
                Text("It is on your clipboard. The page is " + l.url.replacingOccurrences(of: "https://", with: "")).foregroundStyle(Ink.meta)
                Button("Open the page") { open(l.url) }
            case "asking":
                if let p = l.prompt {
                    if p.options.isEmpty { answer(p) } else {
                        Text(p.message).foregroundStyle(Ink.mute)
                        ForEach(p.options, id: \.self) { o in Button(o.label) { orb.send("login.answer", ["text": o.id]) } }
                    }
                }
            case "done":
                Text("Signed in").font(.mono(Size.body + 4, .semibold))
                Text(remote ? "That machine holds the new credential; its conversations can use the models now." : "Orb holds the new credential; its models are in the model menu.").foregroundStyle(Ink.mute)
            case "failed":
                Text(l.detail.isEmpty ? "Sign-in failed" : l.detail).foregroundStyle(Ink.rupture)
            default:
                HStack { ProgressView().controlSize(.small); Text(l.detail.isEmpty ? "starting…" : l.detail).foregroundStyle(Ink.mute) }
            }
            if !l.detail.isEmpty && !["failed", "starting"].contains(l.state) { Text(l.detail).font(.mono(Size.small)).foregroundStyle(Ink.meta) }
            HStack {
                Spacer()
                Button(["done", "failed"].contains(l.state) ? "Close" : "Cancel") { orb.send("login.cancel") }.keyboardShortcut(.cancelAction)
            }
        }
        .padding(24).frame(width: 460)
        .font(.mono())
        .onChange(of: l.url, initial: true) { if ["browser", "code"].contains(l.state) { open(l.url) } }
        .onChange(of: l.code, initial: true) {
            // The code rides the clipboard to the page.
            if !l.code.isEmpty {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(l.code, forType: .string)
            }
        }
    }

    private func answer(_ p: Prompt) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(p.message).foregroundStyle(Ink.mute)
            HStack {
                Group {
                    if p.kind == "secret" { SecureField(p.placeholder, text: $text) } else { TextField(p.placeholder, text: $text) }
                }
                .textFieldStyle(.roundedBorder)
                .onSubmit(save)
                Button("Save", action: save).keyboardShortcut(.defaultAction)
            }
        }
    }

    private func save() {
        guard !text.trimmingCharacters(in: .whitespaces).isEmpty else { return }
        orb.send("login.answer", ["text": text.trimmingCharacters(in: .whitespaces)])
        text = ""
    }

    private func open(_ url: String) { if let u = URL(string: url), !url.isEmpty { openURL(u) } }
}

/// Bridge: this Mac's devices, pairing one by code either way, and approving by fingerprint.
private struct Devices: View {
    @Environment(Orb.self) private var orb
    @State private var code = ""
    @State private var error = ""
    @State private var updating: [String: String] = [:]
    @State private var forgetting: Machine? = nil

    var body: some View {
        Form {
            Section("Devices") {
                let peers = orb.home.machines.filter { !$0.here }
                if peers.isEmpty {
                    ContentUnavailableView("No device yet", systemImage: "point.3.connected.trianglepath.dotted", description: Text("Invite one below, or join one that ran orb bridge pair."))
                }
                ForEach(peers) { m in
                    HStack {
                        Label {
                            Text(m.name).fontWeight(m.connected ? .semibold : .regular)
                            Text(dotted(m.connected ? "\(m.running.count) running" : "disconnected", String(m.id.split(separator: ":").last?.prefix(6) ?? ""), m.version.isEmpty ? "" : "orb " + m.version, updating[m.id] ?? ""))
                        } icon: {
                            Image(systemName: m.launch ? "desktopcomputer" : "iphone").symbolEffect(.pulse, isActive: m.running.contains(where: \.busy))
                                .foregroundStyle(m.connected ? Ink.fg : Ink.rule)
                        }
                        Spacer()
                        // A device behind the latest release updates from here (its Orb swaps itself and restarts Bridge).
                        if m.connected, orb.state.behind(m.version), updating[m.id] == nil {
                            Button("Update") {
                                updating[m.id] = "updating to \(orb.state.latest)…"
                                Task {
                                    do { updating[m.id] = try await orb.ask("update", ["machine": m.id], as: String.self) } catch { updating[m.id] = error.localizedDescription }
                                }
                            }
                        }
                        Button("Forget…") { forgetting = m }
                    }
                }
            }
            Section("Invite a device") {
                if let inv = orb.state.invitation {
                    let expires = Date(timeIntervalSince1970: TimeInterval(inv.expires))
                    LabeledContent("Single use, for") { Text(timerInterval: .now...max(.now, expires), countsDown: true).monospacedDigit() }
                    // A phone scans the code; a computer runs the command.
                    HStack(alignment: .top, spacing: 16) {
                        if let qr = qr(inv.code) { Image(nsImage: qr).interpolation(.none).resizable().frame(width: 168, height: 168) }
                        VStack(alignment: .leading, spacing: 10) {
                            Text("orb bridge join " + inv.code).font(.mono(Size.small)).textSelection(.enabled).lineLimit(4)
                            HStack {
                                Button("Copy Command") {
                                    NSPasteboard.general.clearContents()
                                    NSPasteboard.general.setString("orb bridge join " + inv.code, forType: .string)
                                }
                                ShareLink(item: "orb bridge join " + inv.code)
                            }
                            Text("You approve it here by fingerprint once it runs the command.").font(.mono(Size.small)).foregroundStyle(Ink.meta)
                        }
                    }
                } else {
                    Button("Create an Invitation") { orb.send("invite") }
                }
            }
            Section("Join a device") {
                switch orb.state.joining {
                case "":
                    TextField("orb-bridge:v1:…", text: $code, axis: .vertical).lineLimit(2...4).font(.mono(Size.small))
                    HStack {
                        PasteButton(payloadType: String.self) { code = $0.first ?? code }
                        Button("Pair") {
                            error = ""
                            Task {
                                do { _ = try await orb.ask("join", ["text": code]) } catch where error.localizedDescription != "cancelled" { self.error = error.localizedDescription } catch {}
                            }
                        }
                        .disabled(code.isEmpty)
                    }
                    if !error.isEmpty { Text(error).foregroundStyle(Ink.rupture) }
                case "paired":
                    Text("Paired. Its conversations appear in the sidebar.")
                    Button("Done") { code = ""; orb.send("join.cancel") }
                default:
                    // The fingerprint exactly as the terminal prints it, its start large enough to compare at a glance.
                    Text(orb.state.joining == "claiming" ? "Reaching the device…" : "On the device, answer y. It shows this Mac as").foregroundStyle(Ink.mute)
                    Text(String(orb.state.here.split(separator: ":").last?.prefix(8) ?? "")).font(.mono(26, .bold))
                    Text(orb.state.here).font(.mono(Size.small)).foregroundStyle(Ink.meta).textSelection(.enabled)
                    Button("Cancel") { orb.send("join.cancel") }
                }
            }
            Section("This Mac") {
                Text(orb.state.here).font(.mono(Size.small)).foregroundStyle(Ink.mute).textSelection(.enabled)
                Text("Each device is approved by fingerprint on its own screen. Pairs stay paired and reconnect by themselves; Forget revokes one.")
                    .font(.mono(Size.small)).foregroundStyle(Ink.meta)
            }
        }
        .formStyle(.grouped)
        .confirmationDialog("Forget this device?", isPresented: .constant(forgetting != nil), presenting: forgetting) { m in
            Button("Forget \(m.name)", role: .destructive) { orb.send("forget", ["machine": m.id]); forgetting = nil }
            Button("Cancel", role: .cancel) { forgetting = nil }
        } message: { _ in Text("It loses its access to this Mac; pairing again takes a new code.") }
    }
}

/// A QR code of [text], drawn sharp at any size.
private func qr(_ text: String) -> NSImage? {
    let filter = CIFilter.qrCodeGenerator()
    filter.message = Data(text.utf8)
    guard let code = filter.outputImage else { return nil }
    let image = NSCIImageRep(ciImage: code)
    let out = NSImage(size: image.size)
    out.addRepresentation(image)
    return out
}

/// Orb's bundled plugins on this Mac. Each one only ever renders into a slot or raises an interrupt.
private struct Plugins: View {
    @Environment(Orb.self) private var orb
    @State private var list: [Plugin] = []
    @State private var dirty = false

    var body: some View {
        Form {
            Section {
                ForEach(list) { p in
                    Toggle(isOn: Binding { p.on } set: { on in
                        Task {
                            guard (try? await orb.ask("plugin", ["name": p.name, "on": on])) != nil else { return }
                            dirty = true
                            await load()
                        }
                    }) {
                        Text(p.name)
                        Text(p.about).font(.mono(Size.small)).foregroundStyle(Ink.meta)
                    }
                }
            } footer: {
                HStack {
                    Text("Toggles write Orb's own settings, the same as orb plugins enable.").font(.mono(Size.small)).foregroundStyle(Ink.meta)
                    Spacer()
                    if dirty { Button("Apply · restart Orbs") { orb.send("restart"); dirty = false } }
                }
            }
        }
        .formStyle(.grouped)
        .task { await load() }
    }

    private func load() async { list = (try? await orb.ask("plugins", as: [Plugin].self)) ?? list }
}
