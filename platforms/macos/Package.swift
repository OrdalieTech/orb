// swift-tools-version: 6.0
import PackageDescription

// Orb for macOS: a native renderer of `orb app` (bridge/view). It holds no logic of its own; the
// view, shared with every Orb app, does. Built into Orb.app by build.sh. SwiftTerm draws the
// terminal, natively (AppKit, CoreText).
let package = Package(
    name: "Orb",
    platforms: [.macOS(.v15)],
    dependencies: [.package(url: "https://github.com/migueldeicaza/SwiftTerm", from: "1.9.0")],
    targets: [.executableTarget(name: "Orb", dependencies: ["SwiftTerm"], path: "Sources")]
)
