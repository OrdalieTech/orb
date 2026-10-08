# Orb for Android

A native app whose core is the unmodified `orb` CLI. The phone runs its own sessions, tools and
plugins, and is a full Bridge peer: paired Orbs see and drive its session, and it drives theirs.

## How it is built

```
Compose UI ── core/View ── orb app (bridge/view) ── this phone's Bridge ── Tailcat ── peers
```

- `core/Lines` is the only transport: one long-lived `orb app` process speaking JSON lines.
- `core/View` keeps what the view sends (its state, Home, each followed conversation's rows) and
  sends intents back. Following Orbs, folding their events into rows, markdown, interrupts,
  pairing and sign-in are the view's, in Go, shared with the macOS app.
- `ui/` draws six primitives — session row, turn, tool line, prompt box, slot, interrupt — in
  Ubuntu Sans Mono (variable, so medium and semibold are real) on Ordalie's palette. One bar holds
  the wordmark (Home), a tab per open session once there are several, and the menu.

The Gradle task `orbCore` cross-compiles `./cmd/orb` (`GOOS=android GOARCH=arm64 CGO_ENABLED=0`)
into `liborb.so`; the APK installs it extracted because Android executes only files in
`nativeLibraryDir`.

## The phone's Linux

The agent's commands run in a Linux the app carries, invisible to the owner. `linux-tools.sh`
puts proot, its loader and two libraries in the APK (from Termux's package repository, checked
against its index), and `linux.sh` ships beside them as `liblinux.so`: the shell Orb's bash tool
(`shellPath`) and the terminal screen start. At first start `core/Linux` downloads Termux's base
system (`bootstrap-aarch64.zip`, checked against GitHub's sha256) into `files/linux`, which proot
maps onto `/data/data/com.termux`, so `pkg` and `apt` work unmodified. With all-files access
(asked once on Home) the phone's storage is `~/storage/shared`. The terminal is Termux's
terminal view (Apache-2.0); the Termux app itself is never needed.

## Build and run

Requires Go, JDK 17 and the Android SDK (platform 37).

```sh
cd platforms/android
./gradlew installDebug          # builds the core, the app, and installs over adb
```

Pair a computer from the app's Bridge screen: run `orb bridge pair` there and scan its QR code;
the computer asks you to approve the phone's fingerprint. On a Linux server, accept the Bridge
service it offers so Bridge outlives the SSH session and the machine's reboots.

## Releases and updates

Each Orb release carries `orb_<version>_android_arm64.apk` and its `.sha256`, built by the release
workflow with `release.sh build`, then signed in a step of its own with `release.sh sign`, so
Gradle never runs with the key present. The app checks Orb's latest release and offers it on Home;
the update downloads, verifies and hands the APK to Android's installer, which only accepts it
over an app signed with the same key. The Bridge screen updates paired machines the same way
(`host.update`: their Orb replaces itself, as `orb update` does, and restarts its Bridge).

Signing: the release key (`~/.config/orb/android-release.jks` and `.pass` on the owner's machine,
the `ANDROID_KEYSTORE*` secrets in CI) rotated from the original debug key; `signing/lineage`
proves it, so phones that ran a debug build update in place. Development builds are signed with
the release key when it is present, and take the upcoming version: `-PorbVersion=0.13.0`.
Keep a backup of the key: without it, installed apps cannot be updated.
