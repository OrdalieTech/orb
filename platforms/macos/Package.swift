// swift-tools-version: 6.0
import PackageDescription

// Orb for macOS: a native renderer of `orb app` (bridge/view). It holds no logic of its own; the
// view, shared with every Orb app, does. Built into Orb.app by build.sh.
let package = Package(
    name: "Orb",
    platforms: [.macOS(.v15)],
    targets: [.executableTarget(name: "Orb", path: "Sources")]
)
