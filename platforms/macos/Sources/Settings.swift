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
        .frame(width: 620, height: 560)
        .font(.mono())
    }
}

/// Every provider a machine offers, signed in or not, and its accounts with what their plans have left.
private struct Providers: View {
    @Environment(Orb.self) private var orb
    @State private var machine = ""
    @State private var providers: [Provider] = []
    @State private var accounts: [Account] = []
    @State private var note = ""

    /// The machine shown: "" is this Mac.
    private var here: String { machine.isEmpty ? orb.state.here : machine }

    var body: some View {
        Form {
            Picker("Machine", selection: $machine) {
                ForEach(orb.home.machines.filter { $0.here || $0.launch }) { Text($0.name).tag($0.here ? "" : $0.id) }
            }
            if !accounts.isEmpty {
                Section("Accounts · quota left") {
                    // An account's id is its provider's: two providers may each have a "default".
                    ForEach(accounts.indices, id: \.self) { i in
                        let a = accounts[i]
                        VStack(alignment: .leading, spacing: 6) {
                            HStack {
                                Text(a.name).fontWeight(a.active ? .semibold : .regular)
                                Text(dotted(a.provider_name, a.plan.capitalized)).foregroundStyle(Ink.meta)
                                Spacer()
                                if a.active { Text("in use").foregroundStyle(Ink.meta) } else {
                                    Button("Use") { Task { note = await run("account", ["machine": here, "provider": a.provider, "account": a.id]); await load() } }
                                }
                            }
                            ForEach(a.windows, id: \.self) { Quota(w: $0) }
                        }
                    }
                }
            }
            if !note.isEmpty { Text(note).foregroundStyle(Ink.rupture) }
            section("Ready", providers.filter(\.ready))
            section("Your subscription", providers.filter { !$0.ready && $0.methods.contains(where: \.account) })
            section("API key", providers.filter { !$0.ready && !$0.methods.contains(where: \.account) })
        }
        .formStyle(.grouped)
        .task(id: machine) { await load() }
        .onChange(of: orb.state.login?.state) { if orb.state.login?.state == "done" { Task { await load() } } }
        .sheet(isPresented: .constant(orb.state.login != nil)) {
            if let l = orb.state.login { SignIn(l: l, remote: l.machine != orb.state.here) }
        }
    }

    @ViewBuilder private func section(_ name: String, _ rows: [Provider]) -> some View {
        if !rows.isEmpty {
            Section(name) {
                ForEach(rows) { p in
                    HStack(alignment: .firstTextBaseline) {
                        Dot(color: p.ready ? Ink.fg : Ink.rule)
                        VStack(alignment: .leading) {
                            Text(p.name).fontWeight(p.ready ? .semibold : .regular)
                            Text(p.ready ? "\(p.holds) · \(p.models) models" : p.methods.map(\.about).joined(separator: " · ")).font(.mono(Size.small)).foregroundStyle(Ink.meta)
                        }
                        Spacer()
                        ForEach(p.methods, id: \.self) { m in
                            Button(m.account ? (p.ready ? "Sign in again" : "Sign in") : (p.ready ? "Replace key" : "Add key")) {
                                orb.send("login", ["machine": here, "provider": p.id, "auth": m.auth])
                            }
                        }
                        // Only this Mac's own store; a credential configured elsewhere is changed there.
                        if p.ready && machine.isEmpty && (p.status == "oauth" || p.source == "stored") {
                            Button(p.status == "oauth" ? "Sign out" : "Remove key") { Task { note = await run("logout", ["provider": p.id]); await load() } }
                        }
                    }
                }
            }
        }
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

/// One plan window: the share left as a bar, in the rupture red once it runs low, and its reset.
private struct Quota: View {
    let w: Window
    var body: some View {
        HStack(spacing: 10) {
            Text(w.name).foregroundStyle(Ink.meta).frame(width: 70, alignment: .leading)
            ProgressView(value: min(max(w.left / 100, 0), 1)).tint(w.left < 15 ? Ink.rupture : Ink.fg)
            Text("\(Int(w.left))%").frame(width: 40, alignment: .trailing)
            Text(w.resets > 0 ? Date(timeIntervalSince1970: Double(w.resets) / 1000).formatted(.dateTime.weekday().hour().minute()) : "")
                .foregroundStyle(Ink.meta).frame(width: 90, alignment: .trailing)
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
                if peers.isEmpty { Text("None yet. Pair one below.").foregroundStyle(Ink.meta) }
                ForEach(peers) { m in
                    HStack {
                        Dot(color: m.connected ? Ink.fg : Ink.rule, pulse: m.running.contains(where: \.busy))
                        VStack(alignment: .leading) {
                            Text(m.name).fontWeight(m.connected ? .semibold : .regular)
                            Text(dotted(m.connected ? "\(m.running.count) running" : "disconnected", String(m.id.split(separator: ":").last?.prefix(6) ?? ""), m.version.isEmpty ? "" : "orb " + m.version, updating[m.id] ?? "")).font(.mono(Size.small)).foregroundStyle(Ink.meta)
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
                    TimelineView(.periodic(from: .now, by: 1)) { ctx in
                        let left = max(0, Int(inv.expires) - Int(ctx.date.timeIntervalSince1970))
                        LabeledContent("On it, run") { Text(String(format: "%d:%02d · single use", left / 60, left % 60)).foregroundStyle(Ink.meta) }
                    }
                    Text("orb bridge join " + inv.code).font(.mono(Size.small)).textSelection(.enabled).lineLimit(3)
                    Button("Copy") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString("orb bridge join " + inv.code, forType: .string)
                    }
                    Text("You approve it here by fingerprint once it runs the command.").font(.mono(Size.small)).foregroundStyle(Ink.meta)
                } else {
                    Button("Create an invitation") { orb.send("invite") }
                }
            }
            Section("Join a device") {
                switch orb.state.joining {
                case "":
                    TextField("orb-bridge:v1:…", text: $code, axis: .vertical).lineLimit(2...4).font(.mono(Size.small))
                    Button("Pair") {
                        error = ""
                        Task {
                            do { _ = try await orb.ask("join", ["text": code]) } catch where error.localizedDescription != "cancelled" { self.error = error.localizedDescription } catch {}
                        }
                    }
                    .disabled(code.isEmpty)
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
