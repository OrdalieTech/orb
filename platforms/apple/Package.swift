// swift-tools-version: 6.0
import PackageDescription

// Orb for iOS: a native renderer of `orb app` (bridge/view). It holds no logic of its own; the
// view, shared with every Orb app, does. The orb runs in process (OrbCore, the c-archive of
// cmd/orb); platforms/ios/build.sh assembles the app.
let package = Package(
    name: "Orb",
    platforms: [.iOS(.v18)],
    targets: [
        .executableTarget(name: "Orb", dependencies: ["OrbCore"]),
        .target(name: "OrbCore"),
    ]
)
