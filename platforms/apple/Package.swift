// swift-tools-version: 6.0
import PackageDescription

// Orb for macOS and iOS: one native renderer of `orb app` (bridge/view), compiled for both. It
// holds no logic of its own; the view, shared with every Orb app, does. platforms/macos/build.sh
// and platforms/ios/build.sh assemble the apps. On macOS SwiftTerm draws the terminal; on iOS the
// orb runs in process (OrbCore, the c-archive of cmd/orb).
let package = Package(
    name: "Orb",
    platforms: [.macOS(.v15), .iOS(.v18)],
    dependencies: [.package(url: "https://github.com/migueldeicaza/SwiftTerm", from: "1.9.0")],
    targets: [
        .executableTarget(name: "Orb", dependencies: [
            .product(name: "SwiftTerm", package: "SwiftTerm", condition: .when(platforms: [.macOS])),
            .target(name: "OrbCore", condition: .when(platforms: [.iOS, .macCatalyst])),
        ]),
        .target(name: "OrbCore"),
    ]
)
