# Deploying Orb

Orb is a runtime you deploy, not a client of someone else's server. Every deployment below runs
the full Orb core: its own model calls, tools, sessions and state. Deployments then reach each
other as peers over [Mitosis](#connecting-deployments-with-mitosis): you can drive an Orb running on
a server from your laptop or a browser, and an Orb's agent can call another Orb when that Orb has
granted it access. DECISIONS.md P10 and P11 are the design contract. This page lists every target
and how far along it is.

## How a deployment is built

- **One core, many hosts.** A target supplies *ports* to the core (`host.Host`): files (`FS`),
  processes (`Exec`), durable documents (`Store`), ambient credentials (`Env`) and session
  journals. The core never reads the process, disk or environment behind a host's back.
- **Capabilities follow the ports.** A tool exists only where its port does: no `Exec` means no
  `bash`, no process MCP servers and no JavaScript extensions. The tool is omitted rather than
  failing at call time.

  | Ports | Tools and features |
  |---|---|
  | FS | read, write, edit, ls, find; skills, prompts and context files |
  | FS + Exec | bash, grep (ripgrep), process MCP, JS extensions (Node ≥ 22.6 or Bun) |
  | Store | settings, credentials, model catalogs, trust |
  | Env | provider keys from the platform's secrets instead of process variables |

- **One protocol to drive it.** Every target is controlled with the same RPC frames as
  `orb --mode rpc` (pi-compatible, `agent/rpc`); only the transport changes: stdio, WebSocket,
  a worker's `postMessage`, or native callbacks.
- **Proven by one test.** `conformance/scenario` runs the same scripted, tool-using session on
  every host and requires identical sessions, files and journals. Each target adds its own
  end-to-end test on top. A target is listed as Stable only when both are green.

## Targets at a glance

Status: **Stable** means released artifacts and a blocking CI gate. **Preview** means it works and
is tested but is not released or is limited. **In progress** is under active development.
**Planned** is designed, not built.

| Target | Status | Tools | State lives in | Mitosis role | Deploy |
|---|---|---|---|---|---|
| macOS, Linux (desktop, server) | Stable | all | SQLite (`~/.orb/state`) | full peer | install script |
| Containers, headless servers | Stable | all | SQLite or files | full peer | same binary |
| Team agent container | Preview: Buzz (native) live against a Buzz relay and a fake one in the Go suite; Telegram end-to-end in Docker, not in CI | all | SQLite on the agent's volume | full peer | `docker build platforms/agent` |
| Go SDK embedding | Stable | host-defined | host-defined (`host.Host`) | library | `go get` |
| Windows | Preview: full suite green in CI, not released yet | all | SQLite | full peer | build from source |
| Browser (Wasm worker) | Preview: as a Mitosis peer, end-to-end under Node (and once by hand in headless Firefox) | FS tools | tab memory; IndexedDB as a peer | outbound client, or full peer (dials) | static files |
| Cloudflare Durable Objects | Preview: end-to-end in workerd (CI) and on Cloudflare | FS tools | Durable Object storage | full peer (accepts streams) | `make worker-deploy` |
| Android (Termux) | Preview: builds, not validated on device | all | SQLite | full peer | build from source |
| iOS via iSH | Preview: builds, not validated on device | all (slow) | SQLite | full peer | build from source |
| WASI runtimes | Planned (core suites pass under wazero) | FS tools | preopened directories | — | — |
| Android app | Preview: standalone and paired, tested on device | all (Android shell) | app sandbox SQLite | full peer | `./gradlew assembleDebug` |
| iOS app | Preview: simulator build and pairing in CI | none on the phone; conversations run on paired machines | app sandbox SQLite | full peer | `platforms/ios/build.sh` |
| Desktop app | Preview | the local `orb`'s | the local `orb`'s | the local Mitosis | `platforms/desktop/build.sh` |

## Targets

### macOS and Linux

The standard install: one static binary with every tool, the TUI, print mode and RPC mode.

```sh
curl -fsSL https://raw.githubusercontent.com/OrdalieTech/orb/main/scripts/install.sh | sh
orb
```

Releases ship darwin and linux for amd64 and arm64. State lives in `~/.orb/state/orb.db`. This
target can run the Mitosis service (`orb mitosis run`), accept pairings and reach other devices
through Tailcat.

### Containers and headless servers

The same binary runs headless. Use `orb -p "…"` for one-shot tasks and `orb --mode rpc` to drive
Orb over stdio. `orb mitosis run` makes the server reachable by your other devices, and
`orb mitosis connect-ssh user@host` installs or updates Orb on a server over SSH and pairs it.
Minimal images are supported: without a CA bundle Orb falls back to built-in Mozilla roots, and
without `ps` it inspects `/proc`. The CGo-free static build needs no libc.

### Team agent container

One container per team agent: `orb-agent`, the image's entrypoint, sets the agent up from
`/agent/agent.yaml` and runs `orb chat buzz telegram --tools`, one process on both platforms with
one memory (see `platforms/agent/README.md`). Buzz is Orb's own: no Buzz binary, so the image is a
shell, git and curl for the agent's tools beside Orb, and `browser` variants add agent-browser
with Lightpanda or Chromium. `orb chat` never trusts the project it works in, where those tools
write, so the agent's settings come from the agent file alone. The volume `/agent` holds the agent
file, config, state and workspace; secrets arrive as environment only. Scheduled turns come from the
`wake` plugin, which `orb chat` advances. The same `orb chat buzz` runs outside the image, on any
platform Orb builds for, with the key in its environment. Evidence so far: the Go suite drives
`orb --mode acp`, a Telegram conversation sharing memory with an ACP session, the harness's
rendering and the Buzz adapter against a fake relay (authentication, subscription, cursor,
threads, replies), and a dedicated agent onboarded with `orb chat setup buzz` on a real relay
(mentions, thread context, reactions, DMs, presence, catch-up after downtime, seen from the Buzz
app). The image itself has not run native Buzz yet. Not yet: a real relay run in CI, a documented
removal command.

### Go SDK embedding

`agent.NewAgentSession` runs Orb inside your own process, and several instances with different
configurations can share it. Pass a `host.Host` to decide where files, documents, credentials and
sessions live: memory, your database, object storage. `platforms/memory` provides an in-memory
FS, and `engine/harness/envtest` is the conformance suite a custom FS must pass. Select only the
providers you need with `api.NewRegistry(...)`. See [sdk.md](sdk.md).

### Windows

Windows builds (amd64, arm64) compile in every `make check`. They include Git Bash discovery,
process-tree cleanup, a native console terminal and a user-only Mitosis IPC socket. The full suite
runs green on `windows-latest` and blocks every commit. Windows binaries join releases once the
release workflow packages and verifies them; until then, build with `go build ./cmd/orb`.

### Browser (Wasm worker)

A browser tab can run a local Orb agent: the core compiled to `js/wasm` inside a web worker, with
read/write/edit on a bounded virtual workspace and provider calls made directly through `fetch`
(the endpoint must allow CORS). The same page can pair with a remote Orb over Mitosis and drive
its conversations, whose tools and models then run on that remote machine.

```sh
make browser-serve        # http://127.0.0.1:8787
```

This is a debug screen today: typed keys and files live in tab memory. A remote Orb accepts the
browser with `orb mitosis run --web-listen … --web-origin …`
(see [ARCHITECTURE.md](ARCHITECTURE.md#wasm-host-and-browser-debug-screen)).

A tab can also be a full Mitosis peer (`platforms/browser/web/peer.js`, which `make
browser-build` ships with `peer.wasm`): it runs the Worker runtime over IndexedDB, joins an
invitation, and follows its owner's sync group, so its Orb has the owner's providers and memory
("Profile sync" in ARCHITECTURE.md). `make worker-e2e-mitosis-browser` checks it end to end under
Node against a native `orb`. The debug page does this from its Mitosis panel: paste an invitation,
run the two commands it shows on your device, and the tab's agent uses your providers and memory
with no key typed, across reloads.

### Cloudflare Durable Objects

One Durable Object (`OrbAgent`) hosts one Orb, addressed by name at `/agents/<name>/rpc`. Its
files, settings and session journal persist in Durable Object storage and survive restarts and
evictions; its model calls go out through `fetch`, and provider keys come from Worker secrets
through the `Env` port. It is driven with RPC frames over a hibernatable WebSocket or a streamed
HTTP POST (NDJSON), behind a bearer token (`ORB_TOKEN`), so any RPC client can use it. There is no
shell. One turn leaves about 38 MB of Wasm memory in use, within the 128 MB object limit; the
bundle is 7.0 MB gzip, inside Cloudflare's 10 MB paid-plan limit and gated below it. On Cloudflare (2026-09-23), a
tool-using turn streamed its first frame in 1.8 s, and after a redeploy restarted the object its
files and history were intact.

```sh
make worker-dev           # local workerd
make worker-deploy        # your Cloudflare account (wrangler login first)
```

Each object is also a Mitosis peer at `/agents/<name>/mitosis`: its identity and grants persist in
object storage, a native Orb pairs with it through `orb mitosis pair join`, and the owner
administers it through `/agents/<name>/mitosis/admin` (behind `ORB_TOKEN`; the stream route itself
is authenticated by Mitosis's pinned TLS). Verified on Cloudflare and in workerd: a laptop
Orb lists, prompts and reads the object's conversation, and the object's agent calls the laptop
through `mitosis_call` under its own grant. The object cannot dial out, so it reaches a peer only
over a connection that peer opened.

Known gaps: context files, skills and prompt templates are not loaded yet (they are read from the
process filesystem, not the FS port), session fork and clone are unsupported, and an object stays
awake for up to five minutes after a model call before it can hibernate (Go's idle HTTP timer).
The deploy path, endpoint contract, secrets and removal are in
[platforms/worker](../platforms/worker/README.md).

### Android (Termux)

The `android/arm64` build is a complete Orb, and Termux provides the shell it drives. It builds in
every `make check`: `GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/orb`. It has not yet
been validated on a device (DNS and CA-root discovery under Termux) and is not a release artifact.

### iOS via iSH

iSH emulates 32-bit x86 Linux, so the `linux/386` build runs inside it with every tool, slowly.
It builds in every `make check`; it has not been validated inside iSH.

### WASI

The core's `ai` and `engine` suites and the cross-host scenario pass under wazero without host
mounts. There is no WASI entry program yet, and `wasip1` has no sockets, so outbound HTTP needs a
host-provided import.

### Android app

`platforms/android` is a native Kotlin app whose core is the unmodified `orb` binary, built
`CGO_ENABLED=0` for `android/arm64` and shipped as the APK's `liborb.so` (Android executes only
files installed to `nativeLibraryDir`). The app runs `orb app` and draws the view it prints
(`mitosis/view`), as the desktop and iOS apps do, so the phone is a full peer: other Orbs see and
drive its conversations, and it drives theirs. A foreground service keeps it alive while the
screen is off. Tools run in the app sandbox with
Android's `/system/bin/sh`; provider keys stay in the app and reach Orb as environment variables.
Build and install with `./gradlew installDebug` in `platforms/android` (Go, JDK 17, Android SDK 37).
To pair it with a computer, run `orb mitosis pair` there and scan the QR code from the app's Mitosis
screen; the computer asks you to approve the phone's fingerprint, and the pair persists. On a Linux
server, answer yes when `orb mitosis pair` offers the Mitosis service (or run
`orb mitosis service install`): Mitosis then starts with the machine and outlives the SSH session.

### iOS app

iOS apps cannot spawn processes, so `platforms/ios` links `cmd/orb` into the app as a static
library (`go build -buildmode=c-archive`) and runs `orb mitosis run`, then `orb app`, over two
pipes; `platforms/apple` is the Swift renderer of that view. The phone has no shell and starts no
Orb of its own: a new conversation starts on a paired machine. `platforms/ios/README.md` has the
build and the CI run. Termux and iSH are GPLv3, so the apps interoperate with them rather than
embed them.

### Desktop app

`platforms/desktop` is a Go module of its own, drawn by MyGo's native UI on macOS, Linux and
Windows. It runs `orb app` as a process and draws the view it prints: the conversations open on
this computer's Mitosis and on its paired machines. `platforms/desktop/README.md` has the keys and
the build.

## Connecting deployments with Mitosis

Every Orb has a peer identity (an Ed25519 key). Two Orbs become peers by exchanging a one-use
invitation that each owner approves, or through `orb mitosis connect-ssh`. Access is granted
explicitly and in two separate kinds:

- **Controllers** are people. A controller grant lets you list, watch and drive another Orb's
  conversations: prompt, steer, cancel, switch or fork sessions. The conversation keeps running on
  its own Orb, with that Orb's tools and credentials, and continues when you disconnect.
- **Launching** is part of full controller access (`host.launch`): the controller sees the threads
  stored on the machine across folders, browses its folders to pick one (`host.dirs`: the
  folders in a folder, hidden ones left out, git checkouts marked) and starts Orb in a folder
  there, on a new thread or an old one. A file the controller cites in a message is copied there
  first (`host.attach`, into a folder of its own in the machine's temp directory). That Orb is ephemeral; it retires when it exits or when
  the machine's Mitosis stops. The same
  access signs the machine in to providers (`host.login.*`): its sign-in link, device code and
  questions come to the controller (the Android app's device → providers), and answers go back, so a
  headless Orb is signed in from the phone, with copy-code login or a pasted redirect URL. The
  machine's accounts and their plan limits come the same way (`host.accounts`, `orb accounts
  --json` there), and `host.accounts.use` switches the account a provider uses; `host.models`
  (`orb models --json`) lists the models a new session there can use with their reasoning levels
  and its default, and `host.models.use` changes that default and the level it starts at. It
  also opens a terminal there (`host.terminal.*`, `orb mitosis shell <peer> [folder]`): the
  owner's login shell in a pseudo-terminal, which the Android app shows for a device's
  conversation. Nothing is reachable through it that starting Orb there could not already run.
- **The owner** reaches its own machine with the same calls, by its own peer id, with no grant:
  the Android app drives the phone's Orbs exactly as it drives a server's. A follower long-polls
  `events.subscribe` (`wait`, with the last `state` it saw) and gets an answer as soon as there are
  events or the instance's pulse moves (a turn, a question, a rename), and opens a long
  conversation at its end (a snapshot with `tail`, which says where it starts); instances also take
  `session.compact` and `shell` (a `!command` in the conversation).
- **Agents** are Orbs acting on their own. With agent access on (`/mitosis` → Advanced), an Orb's
  agent lists, reads and messages its owner's other conversations, on this machine and connected
  devices, with its owner's reach. A Worker's agent, which is not its owner, calls another Orb
  through `mitosis_call` under its own grant.

Transports adapt to the target. Native Orbs use Tailcat (WireGuard with NAT traversal) and local
IPC. Browsers and Workers use the WebSocket transport, which keeps Mitosis's pinned TLS inside the
stream. Browsers connect outward; a Durable Object accepts peers on `/agents/<name>/mitosis`, so an
always-on cloud Orb pairs with your laptop and both can call each other over that channel.

Typical setups:

- **Laptop and server.** Pair them over SSH. Drive long jobs on the server from the laptop TUI;
  they survive the laptop closing.
- **Browser anywhere.** A static page pairs with your server and controls its conversations
  without installing anything.
- **Cloud agent.** A Durable Object Orb runs unattended, persists its workspace, and calls your
  laptop's Orb for local files only if that Orb granted it.

## How a target becomes supported

A target moves up this page only with evidence (DECISIONS.md P11):

1. its host passes the port conformance suites (`engine/harness/envtest` for FS today; Store and
   Env suites join as those ports gain second implementations);
2. `conformance/scenario` produces the same session on it as natively;
3. a target end-to-end test runs in CI (for Workers: `wrangler dev`);
4. one documented command deploys it, and one removes it;
5. it states its capability profile and its Mitosis role;
6. its artifacts are produced by the release workflow and appear in the release notes.

Steps 1 to 5 make a target Preview; step 6 makes it Stable.
