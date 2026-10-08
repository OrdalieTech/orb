import Foundation
import Observation

/// A message from `orb app`, decoded off the main thread.
enum Message: Sendable {
    case state(ViewState), home(Home), rows(tab: String, at: Int, rows: [Row]), reply(id: String, line: Data), alert(tab: String, title: String, text: String)

    private struct Envelope: Decodable {
        @D var t: String
        @D var id: String
        @D var tab: String
        @D var at: Int
        @D var rows: [Row]
        @D var title: String
        @D var text: String
    }

    init?(_ line: Data) {
        let json = JSONDecoder()
        guard let e = try? json.decode(Envelope.self, from: line) else { return nil }
        switch e.t {
        case "state": guard let s = try? json.decode(ViewState.self, from: line) else { return nil }; self = .state(s)
        case "home": guard let h = try? json.decode(Home.self, from: line) else { return nil }; self = .home(h)
        case "rows": self = .rows(tab: e.tab, at: e.at, rows: e.rows)
        case "reply": self = .reply(id: e.id, line: line)
        case "alert": self = .alert(tab: e.tab, title: e.title, text: e.text)
        default: return nil
        }
    }
}

struct Failure: LocalizedError { let errorDescription: String? }

/// A followed conversation's rows, observed on their own: a patch to one redraws its conversation only.
@MainActor @Observable final class Feed {
    var rows: [Row] = []
}

/// The app's side of `orb app`, the view every Orb app draws (bridge/view): what it shows, kept as
/// the view sends it, and the intents it sends back. Everything the app does with Orb goes here.
@MainActor @Observable final class Orb {
    private(set) var state = ViewState.blank
    private(set) var home = Home.blank
    /// Each followed conversation's rows, as the view last patched them.
    @ObservationIgnored private var feeds: [String: Feed] = [:]
    @ObservationIgnored var alert: (_ tab: String, _ title: String, _ text: String) -> Void = { _, _, _ in }
    @ObservationIgnored private var input: FileHandle?
    @ObservationIgnored private var replies: [String: CheckedContinuation<Data, Never>] = [:]
    @ObservationIgnored private var asked = 0
    @ObservationIgnored private var visible = false
    @ObservationIgnored private var shown: [String] = []

    init() {
        signal(SIGPIPE, SIG_IGN) // a view that ended fails the write instead of ending the app
        Task { await run() }
    }

    /// Runs `orb app` for as long as the app lives; one that ends starts again, hears what this side
    /// shows, and sends everything again.
    private func run() async {
        let environment = await Task.detached { Orb.environment }.value
        while true {
            let p = Process(), out = Pipe(), into = Pipe()
            p.executableURL = Orb.binary
            p.arguments = ["app", "--name", "this Mac"]
            p.environment = environment
            p.currentDirectoryURL = FileManager.default.homeDirectoryForCurrentUser
            p.standardInput = into
            p.standardOutput = out
            do { try p.run() } catch {
                state.summary = "orb did not start: \(error.localizedDescription)"
                try? await Task.sleep(for: .seconds(5))
                continue
            }
            input = into.fileHandleForWriting
            send("hello", ["budget": 4])
            send("visible", ["on": visible])
            send("show", ["tabs": shown])
            for await m in Orb.messages(out.fileHandleForReading) { heard(m) }
            input = nil
            for (_, r) in replies { r.resume(returning: Data(#"{"error":"Orb restarted"}"#.utf8)) }
            replies = [:]
            p.terminate()
            try? await Task.sleep(for: .seconds(1))
        }
    }

    /// The view's messages, one per line (JSON keeps newlines escaped), read and decoded on a thread of their own.
    private nonisolated static func messages(_ from: FileHandle) -> AsyncStream<Message> {
        AsyncStream { sink in
            Thread.detachNewThread {
                var buffer = Data()
                while let chunk = try? from.read(upToCount: 1 << 16), !chunk.isEmpty {
                    buffer.append(chunk)
                    var start = buffer.startIndex
                    while let end = buffer[start...].firstIndex(of: 10) {
                        if let m = Message(Data(buffer[start..<end])) { sink.yield(m) }
                        start = end + 1
                    }
                    buffer = Data(buffer[start...])
                }
                sink.finish()
            }
        }
    }

    private func heard(_ m: Message) {
        switch m {
        case .state(let s):
            state = s
            feeds = feeds.filter { id, _ in s.tabs.contains { $0.id == id } }
        case .home(let h): home = h
        case .rows(let tab, let at, let new):
            let feed = feed(tab)
            feed.rows = Array(feed.rows.prefix(at)) + new
        case .reply(let id, let line): replies.removeValue(forKey: id)?.resume(returning: line)
        case .alert(let tab, let title, let text): alert(tab, title, text)
        }
    }

    private func write(_ intent: [String: Any]) {
        guard let data = try? JSONSerialization.data(withJSONObject: intent) else { return }
        try? input?.write(contentsOf: data + Data([10]))
    }

    /// Asks the view something it does at once or on its own time; nothing comes back.
    func send(_ name: String, _ args: [String: Any] = [:]) { write(args.merging(["do": name]) { a, _ in a }) }

    /// Asks the view something and waits for its reply.
    func ask<T: Decodable>(_ name: String, _ args: [String: Any] = [:], as: T.Type = Ignored.self) async throws -> T {
        // No view to answer (it failed to start, or restarts): say so instead of waiting for ever.
        guard input != nil else { throw Failure(errorDescription: "Orb is not running yet") }
        asked += 1
        let id = String(asked)
        let line = await withCheckedContinuation { r in
            replies[id] = r
            write(args.merging(["do": name, "id": id]) { a, _ in a })
        }
        let reply = try JSONDecoder().decode(Reply<T>.self, from: line)
        if let error = reply.error { throw Failure(errorDescription: error) }
        // An empty result comes as null or not at all.
        guard let result = reply.result ?? ((T.self as? Empty.Type)?.init() as? T) else { throw Failure(errorDescription: "no answer") }
        return result
    }

    func visible(_ on: Bool) { visible = on; send("visible", ["on": on]) }
    func show(_ tabs: [String]) { if tabs != shown { shown = tabs; send("show", ["tabs": tabs]) } }
    func tab(_ id: String?) -> Tab? { state.tabs.first { $0.id == id } }
    func feed(_ tab: String) -> Feed {
        if let feed = feeds[tab] { return feed }
        let feed = Feed()
        feeds[tab] = feed
        return feed
    }
    func machine(_ id: String) -> Machine? { home.machines.first { $0.id == id } }
}

private struct Reply<T: Decodable>: Decodable {
    var result: T?
    var error: String?
}

/// A reply whose result does not matter, only whether it failed.
struct Ignored: Decodable, Empty {}

extension Orb {
    /// The orb this app carries; run from a checkout (swift run), $ORB or the one on PATH.
    nonisolated static let binary: URL = {
        let bundled = Bundle.main.bundleURL.appending(path: "Contents/Helpers/orb")
        if FileManager.default.isExecutableFile(atPath: bundled.path) { return bundled }
        if let orb = ProcessInfo.processInfo.environment["ORB"] { return URL(filePath: orb) }
        let path = environment["PATH", default: ""].split(separator: ":").map { URL(filePath: String($0)).appending(path: "orb") }
        return path.first { FileManager.default.isExecutableFile(atPath: $0.path) } ?? bundled
    }()

    /// The login shell's environment: an app opened from the Finder gets a bare PATH, and the Orbs
    /// this machine's Bridge starts, when this app starts it, run with what the terminal has.
    nonisolated static let environment: [String: String] = {
        var env = ProcessInfo.processInfo.environment
        let p = Process(), out = Pipe()
        p.executableURL = URL(filePath: env["SHELL"] ?? "/bin/zsh")
        p.arguments = ["-ilc", "printf '\\0\\0'; env -0"]
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        p.standardInput = FileHandle.nullDevice
        guard (try? p.run()) != nil else { return env }
        let timeout = DispatchWorkItem { p.terminate() }
        DispatchQueue.global().asyncAfter(deadline: .now() + 5, execute: timeout)
        let data = out.fileHandleForReading.readDataToEndOfFile()
        timeout.cancel()
        // What the shell's startup printed comes before the marker.
        guard let marker = data.range(of: Data([0, 0])) else { return env }
        for entry in data[marker.upperBound...].split(separator: 0) {
            if let line = String(data: Data(entry), encoding: .utf8), let eq = line.firstIndex(of: "=") {
                env[String(line[..<eq])] = String(line[line.index(after: eq)...])
            }
        }
        return env
    }()
}
