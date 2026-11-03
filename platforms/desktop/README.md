# Orb for the desktop

The desktop app for macOS, Linux and Windows draws `orb app` (`bridge/view`): the conversations
open on this computer's Bridge, the CLI's own, and on its paired machines. It holds no logic of
its own; the view, shared with the Android and iOS apps, does. It is Go, drawn by
[MyGo](https://github.com/egoist/mygo)'s native UI (no webview, no cgo), in the Android app's look:
a sidebar of the conversations open and each machine's threads, the conversation or a settings
screen beside it, and the terminal where a conversation runs under it (⌘J, Ctrl+J elsewhere).

This directory is a Go module of its own, so MyGo never reaches Orb's module or the `orb` binary.
The app runs `orb app` as a process and decodes what it prints into its own types (`model.go`).

## Run

```sh
go run .                       # the orb on PATH, or $ORB
go tool mygo dev               # the same, with the face and resources mygo.json lists
```

Under `go run` the terminal downloads libghostty-vt once into the user's cache, checked against
the SHA-256 MyGo pins; built apps carry it.

## Build

Go and, off macOS, `rsvg-convert` (librsvg) for the icon:

```sh
./build.sh 0.21.0 linux/amd64,darwin/arm64,windows/amd64   # dist/<os>-<arch>/
```

Each app carries the orb it runs (`resources/<os>-<arch>/bin/orb`, built from this checkout),
Ubuntu Sans Mono from the Android app, and libghostty-vt. On macOS `mygo build` signs the app
(ad hoc unless `macos.signingIdentity` is set in `mygo.json`) and makes a disk image; on Linux an
archive, a Debian package and a PKGBUILD; on Windows the executable, and an installer with NSIS.
