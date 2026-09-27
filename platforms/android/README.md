# Orb for Android

A native app whose core is the unmodified `orb` CLI. The phone runs its own sessions, tools and
plugins, and is a full Bridge peer: paired Orbs see and drive its session, and it drives theirs.

## How it is built

```
Compose UI ── Session (one contract) ──┬── LocalSession ── orb --mode rpc --continue --bridge personal
                                       └── RemoteSession ─┐
Bridge ─────────────────────────────── orb bridge pipe ───┴── Bridge service ── Tailcat ── peers
```

- `core/Lines` is the only transport: one long-lived orb process speaking JSON lines.
- `core/Transcript` turns Orb's agent events into turns, prose and tool lines. Local RPC and a
  peer's `events.subscribe` emit the same events, so both sessions share it.
- `core/Session` holds the interrupts (`Ask`): approvals, questions (walked and answered with
  the plugin's JSON `Result`) and Bridge pairing.
- `ui/` draws six primitives — session row, turn, tool line, prompt box, slot, interrupt — in
  Ubuntu Mono on Ordalie's palette. Plugins toggle through `orb plugins`, keys are passed as the
  environment variables Orb already reads.

The Gradle task `orbCore` cross-compiles `./cmd/orb` (`GOOS=android GOARCH=arm64 CGO_ENABLED=0`)
into `liborb.so`; the APK installs it extracted because Android executes only files in
`nativeLibraryDir`.

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

Each Orb release carries `orb_<version>_android_arm64.apk` and its `.sha256`, built and signed by
the release workflow with `release.sh`. The app checks Orb's latest release and offers it on Home;
the update downloads, verifies and hands the APK to Android's installer, which only accepts it
over an app signed with the same key. The Bridge screen updates paired machines the same way
(`host.update`: their Orb replaces itself, as `orb update` does, and restarts its Bridge).

Signing: the release key (`~/.config/orb/android-release.jks` and `.pass` on the owner's machine,
the `ANDROID_KEYSTORE*` secrets in CI) rotated from the original debug key; `signing/lineage`
proves it, so phones that ran a debug build update in place. Development builds are signed with
the release key when it is present, and take the upcoming version: `-PorbVersion=0.13.0`.
Keep a backup of the key: without it, installed apps cannot be updated.
