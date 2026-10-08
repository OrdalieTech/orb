# Orb for macOS

A native SwiftUI app that draws `orb app` (`bridge/view`): the conversations open on this Mac's
Bridge, the CLI's own, and on its paired machines. It holds no logic of its own; the view, shared
with the Android app, does. Its terminal (⌘J) is SwiftTerm's native view. macOS 15 or later.

## Build

Swift 6 (the Command Line Tools are enough, no Xcode project) and Go:

```sh
./build.sh 0.19.3-dev              # build/Orb.app, signed ad hoc
open build/Orb.app
```

The bundle carries the orb it runs (`Contents/Helpers/orb`), built from this checkout, and the
face and mark of the Android app's resources. While working on the Swift side, `swift run` uses
`$ORB` or the `orb` on `PATH`.

## Sign and notarize

With a Developer ID Application identity in the keychain and an App Store Connect API key:

```sh
./build.sh 0.19.3 "Developer ID Application: …"
./build.sh notarize AuthKey.p8 <key-id> <issuer-id>
```
