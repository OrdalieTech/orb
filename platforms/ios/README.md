# Orb for iOS

The iPhone app draws `orb app` (`bridge/view`) like the Mac and Android apps, and holds no logic of
its own. It drives the Orbs of the machines it is paired with: the owner's Mac, a server. iOS 18 or
later; iPad comes later.

## How it is built

1. **One Swift renderer for macOS and iOS.** `platforms/apple` is one SwiftPM target compiled for
   both. What differs sits in `#if os(iOS)`, and stays small: how `orb app` starts, the answer's
   text view (UIKit's instead of AppKit's), the prompt's field, Settings as a sheet, and pairing by
   camera. The terminal, the menu bar and the Settings window stay on the Mac.
2. **The same Go core, in process.** iOS runs no binary but the app's, so `cmd/orb` is linked into
   it as a static library (`go build -buildmode=c-archive`) with one exported entry,
   `core/app.go`, laid into `cmd/orb` at build time (`-overlay`, `-tags appshell`), which keeps cgo
   in the app shell (P2). `OrbApp(home, in, out)` runs `orb bridge run`, then `orb app` over two
   pipes: the very CLI the Mac app runs as a process, with pipes instead of a process, and the same
   JSON lines. Orb's state lives in the app's `Library`; the Bridge's sockets under `Library/b`
   (a short profile, `p`, so their paths fit the 104 bytes macOS allows; the simulator's container
   is deeper, and unsandboxed, so there they are under `/tmp`).
3. **What it runs.** No shell and no exec, so the phone starts no Orb of its own: a new
   conversation starts on a paired machine, and the phone's Bridge is a peer like the Android
   app's. Pairing is the Android app's: scan the code the Mac shows (Settings, Bridge, Create an
   Invitation) or that `orb bridge pair` prints, from the app (VisionKit) or the system camera,
   whose `orb-bridge:` link opens the app; a pasted code works too. Then approve the fingerprint
   on the other machine.
4. **Look.** The Mac's Theme, shared: Ubuntu Sans Mono, the same inks and the one red, stretched
   capitals on interrupts, the Android app's mark for the icon.

## Build

With Xcode (the Command Line Tools have no iOS SDK) and Go:

```sh
./build.sh 0.20.0-dev simulator        # build/iphonesimulator/Orb.app, signed ad hoc
xcrun simctl install booted build/iphonesimulator/Orb.app
xcrun simctl launch booted tech.ordalie.orb
```

`.github/workflows/ios.yml` does that on a macOS runner for every change under `platforms/`,
`bridge/view/` or `cmd/orb/`, pairs the simulator with an Orb on the runner and keeps screenshots.

Without Xcode, the Swift still type-checks for iOS as Mac Catalyst, from `platforms/apple`
(`SDK=$(xcrun --show-sdk-path)`, `F=$SDK/System/iOSSupport`):

```sh
swift build --triple arm64-apple-ios18.0-macabi --scratch-path .build/catalyst \
  -Xswiftc -Fsystem -Xswiftc $F/System/Library/Frameworks -Xswiftc -I -Xswiftc $F/usr/lib/swift \
  -Xlinker -undefined -Xlinker dynamic_lookup
```

## The App Store

- Bundle id `tech.ordalie.orb` (the Mac app's), version from `build.sh`'s first argument, without
  its `-dev`; each upload needs a version App Store Connect has not seen.
- The icon is rendered by `platforms/apple/icon.sh`, opaque at 1024 px, into the asset catalog.
- `PrivacyInfo.xcprivacy`: no tracking, no data collected; the reasons for the file timestamps,
  boot time and disk space the Go runtime and the store read, and the app's own defaults.
- Info.plist asks for the camera (to read a pairing code) and the local network (to reach paired
  devices directly).
- Export compliance: `ITSAppUsesNonExemptEncryption` is true. Bridge secures its links itself, with
  standard algorithms only: Ed25519 identities, WireGuard through Tailscale's tailcat (X25519,
  ChaCha20-Poly1305) and TLS, all from Go's and Tailscale's libraries. In App Store Connect the
  answer is "standard encryption algorithms instead of, or in addition to, the operating system's",
  nothing proprietary. Selling in France may also take a declaration to the ANSSI.

Signing and upload, with the team's Apple Distribution identity in the keychain, its App Store
provisioning profile for `tech.ordalie.orb`, and an App Store Connect API key in
`~/.appstoreconnect/private_keys/`:

```sh
./build.sh 0.20.0 device "Apple Distribution: …" Orb_App_Store.mobileprovision   # build/Orb.ipa
xcrun altool --upload-app --type ios --file build/Orb.ipa --apiKey <key-id> --apiIssuer <issuer-id>
```
