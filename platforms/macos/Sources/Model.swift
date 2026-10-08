import Foundation

// What `orb app` (bridge/view) sends, as the app draws it; its Go types name each field. Go leaves
// empty fields out, so each takes its empty value when absent (@D).

protocol Empty { init() }
extension String: Empty {}
extension Bool: Empty {}
extension Int: Empty {}
extension Int64: Empty {}
extension Double: Empty {}
extension Array: Empty {}

@propertyWrapper struct D<T: Decodable & Empty & Equatable>: Decodable, Equatable {
    var wrappedValue: T
    init(wrappedValue: T) { self.wrappedValue = wrappedValue }
    init(from decoder: Decoder) throws { wrappedValue = try T(from: decoder) }
}

extension D: Sendable where T: Sendable {}
extension D: Hashable where T: Hashable {}

extension Decodable {
    /// The value with every field empty, as the view's "{}" would be.
    static var blank: Self { try! JSONDecoder().decode(Self.self, from: Data("{}".utf8)) }
}

extension KeyedDecodingContainer {
    func decode<T>(_ type: D<T>.Type, forKey key: Key) throws -> D<T> { try decodeIfPresent(type, forKey: key) ?? D(wrappedValue: T()) }
}

struct Command: Decodable, Equatable, Hashable {
    @D var name: String
    @D var hint: String
    @D var now: Bool
}

struct Ask: Decodable, Equatable {
    @D var id: String
    @D var owner: String
    @D var title: String
    @D var message: String
    @D var choices: [String]
    @D var free: Bool
}

struct Window: Decodable, Equatable, Hashable {
    @D var name: String
    @D var left: Double
    @D var resets: Int64
}

struct Usage: Decodable, Equatable {
    @D var plan: String
    @D var at: Int64
    @D var windows: [Window]
}

struct Tab: Decodable, Equatable, Identifiable {
    @D var id: String
    @D var title: String
    @D var peer: String
    @D var `where`: String
    @D var remote: Bool
    @D var cwd: String
    @D var busy: Bool
    @D var online: Bool
    @D var loaded: Bool
    @D var earlier: Bool
    @D var streaming: Bool
    @D var status: String
    @D var model: String
    @D var thinking: String
    @D var levels: [String]
    @D var context: Double
    @D var cost: Double
    var usage: Usage?
    var ask: Ask?
    @D var commands: [Command]
    @D var models: [String]
}

extension Tab {
    /// The models by provider ("anthropic/claude-…"), in the order the Orb lists them.
    var catalog: [(provider: String, models: [String])] {
        models.reduce(into: []) { out, m in
            let provider = String(m.prefix { $0 != "/" })
            if out.last?.provider == provider { out[out.count - 1].models.append(m) } else { out.append((provider, [m])) }
        }
    }
}

struct Claim: Decodable, Equatable {
    @D var invitation: String
    @D var claimant: String
    var id: String { invitation }
}

struct Invitation: Decodable, Equatable {
    @D var code: String
    @D var expires: Int64
}

struct Option: Decodable, Equatable, Hashable {
    @D var id: String
    @D var label: String
}

struct Prompt: Decodable, Equatable {
    @D var kind: String
    @D var message: String
    @D var placeholder: String
    @D var options: [Option]
}

struct Login: Decodable, Equatable {
    @D var machine: String
    @D var provider: String
    @D var state: String
    @D var url: String
    @D var instructions: String
    @D var code: String
    @D var detail: String
    var prompt: Prompt?
}

struct ViewState: Decodable, Equatable {
    @D var `self`: String
    @D var up: Bool
    @D var tabs: [Tab]
    var claim: Claim?
    var invitation: Invitation?
    @D var joining: String
    @D var launching: String
    var login: Login?
    @D var acting: Bool
    @D var summary: String
    @D var latest: String
    @D var commands: [Command]
}

extension ViewState {
    /// This machine's peer id ("self" on the wire).
    var here: String { self.`self` }

    /// A machine on [version] is behind the latest release.
    func behind(_ version: String) -> Bool { !version.isEmpty && latest.compare(version, options: .numeric) == .orderedDescending }
}

struct Running: Decodable, Equatable, Hashable {
    @D var instance: String
    @D var label: String
    @D var busy: Bool
}

struct Folder: Decodable, Equatable, Hashable {
    @D var cwd: String
    @D var threads: Int
    @D var modified: Int64
    @D var live: Bool
}

struct Machine: Decodable, Equatable, Identifiable {
    @D var id: String
    @D var name: String
    @D var `self`: Bool
    @D var connected: Bool
    @D var version: String
    @D var launch: Bool
    @D var hue: Int
    @D var running: [Running]
    @D var folders: [Folder]
}

extension Machine {
    /// It is this machine ("self" on the wire).
    var here: Bool { self.`self` }
}

struct Entry: Decodable, Equatable, Identifiable {
    @D var key: String
    @D var title: String
    @D var machine: String
    @D var cwd: String
    @D var modified: Int64
    @D var live: Bool
    @D var asks: Bool
    @D var open: Bool
    @D var unstored: Bool
    @D var deletable: Bool
    var id: String { key }
}

struct Home: Decodable, Equatable {
    @D var machines: [Machine]
    @D var entries: [Entry]
}

struct Span: Decodable, Equatable, Hashable {
    @D var t: String
    @D var b: Bool
    @D var i: Bool
    @D var c: Bool
    @D var s: Bool
    @D var h: String
}

struct Block: Decodable, Equatable {
    @D var type: String
    @D var level: Int
    @D var mark: String
    @D var depth: Int
    @D var spans: [Span]
    @D var lang: String
    @D var text: String
    @D var blocks: [Block]
    @D var rows: [[[Span]]]
}

struct Action: Decodable, Equatable, Identifiable {
    @D var k: String
    @D var verb: String
    @D var target: String
    @D var result: String
    @D var live: Bool
    @D var failed: Bool
    var id: String { k }
}

struct Row: Decodable, Equatable, Identifiable {
    @D var k: String
    @D var kind: String
    @D var text: String
    @D var via: String
    @D var alarm: Bool
    @D var images: [String]
    var block: Block?
    @D var live: Bool
    @D var what: String
    @D var now: String
    @D var failed: Int
    @D var actions: [Action]
    var id: String { k }
}

struct Method: Decodable, Equatable, Hashable {
    @D var auth: String
    @D var label: String
    @D var about: String
    @D var account: Bool
}

struct Provider: Decodable, Equatable, Identifiable {
    @D var id: String
    @D var name: String
    @D var methods: [Method]
    @D var models: Int
    @D var ready: Bool
    @D var holds: String
    @D var status: String
    @D var source: String
}

struct Account: Decodable, Equatable {
    @D var provider: String
    @D var provider_name: String
    @D var id: String
    @D var name: String
    @D var active: Bool
    @D var plan: String
    @D var windows: [Window]
}

struct Plugin: Decodable, Equatable, Identifiable {
    @D var name: String
    @D var on: Bool
    @D var about: String
    var id: String { name }
}

struct Completion: Decodable, Equatable, Hashable {
    @D var text: String
    @D var label: String
    @D var detail: String
}
