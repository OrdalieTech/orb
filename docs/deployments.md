# Deploying Orb

Orb is a runtime you deploy, not a client of someone else's server. Every deployment below runs
the full Orb core: its own model calls, tools, sessions and state. Deployments then reach each
other as peers over [Bridge](#connecting-deployments-with-bridge): you can drive an Orb running on
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
- **Proven by one test.** `platforms/scenario` runs the same scripted, tool-using session on
  every host and requires identical sessions, files and journals. Each target adds its own
  end-to-end test on top. A target is listed as Stable only when both are green.

## Targets at a glance

Status: **Stable** means released artifacts and a blocking CI gate. **Preview** means it works and
is tested but is not released or is limited. **In progress** is under active development.
**Planned** is designed, not built.

| Target | Status | Tools | State lives in | Bridge role | Deploy |
|---|---|---|---|---|---|
| macOS, Linux (desktop, server) | Stable | all | SQLite (`~/.orb/state`) | full peer | install script |
| Containers, headless servers | Stable | all | SQLite or files | full peer | same binary |
| Go SDK embedding | Stable | host-defined | host-defined (`host.Host`) | library | `go get` |
| Windows | Preview: builds, suite not green | all | SQLite | full peer | not released yet |
| Browser (Wasm worker) | Preview | FS tools | tab memory | outbound client | static files |
| Cloudflare Durable Objects | Preview: end-to-end in workerd (CI) and on Cloudflare | FS tools | Durable Object storage | RPC endpoint (Bridge peer next) | `make worker-deploy` |
| Celld cells | Preview: local end-to-end under `celld dev` (CI) | FS tools | cell SQLite | RPC endpoint (Bridge peer next) | `make worker-celld-dev` |
| Android (Termux) | Preview: builds, not validated on device | all | SQLite | full peer | build from source |
| iOS via iSH | Preview: builds, not validated on device | all (slow) | SQLite | full peer | build from source |
| WASI runtimes | Planned (core suites pass under wazero) | FS tools | preopened directories | — | — |
| iOS and Android apps | Planned | FS tools; Exec via WASI commands on iOS | app sandbox | full peer | app stores |

## Targets

### macOS and Linux

The standard install: one static binary with every tool, the TUI, print mode and RPC mode.

```sh
curl -fsSL https://raw.githubusercontent.com/OrdalieTech/orb/main/scripts/install.sh | sh
orb
```

Releases ship darwin and linux for amd64 and arm64. State lives in `~/.orb/state/orb.db`. This
target can run the Bridge service (`orb bridge run`), accept pairings and reach other devices
through Tailcat.

### Containers and headless servers

The same binary runs headless. Use `orb -p "…"` for one-shot tasks and `orb --mode rpc` to drive
Orb over stdio. `orb bridge run` makes the server reachable by your other devices, and
`orb bridge connect-ssh user@host` installs or updates Orb on a server over SSH and pairs it.
Minimal images are supported: without a CA bundle Orb falls back to built-in Mozilla roots, and
without `ps` it inspects `/proc`. The CGo-free static build needs no libc.

### Go SDK embedding

`agent.NewAgentSession` runs Orb inside your own process, and several instances with different
configurations can share it. Pass a `host.Host` to decide where files, documents, credentials and
sessions live: memory, your database, object storage. `platforms/memory` provides an in-memory
FS, and `engine/harness/envtest` is the conformance suite a custom FS must pass. Select only the
providers you need with `api.NewRegistry(...)`. See [sdk.md](sdk.md).

### Windows

Windows builds (amd64, arm64) compile in every `make check`. They include Git Bash discovery,
process-tree cleanup, a native console terminal and Bridge peer checks. The full suite runs on
`windows-latest` in CI but is not green yet, so Windows binaries are not released. The CI job
becomes blocking, and releases add Windows, once it passes (DECISIONS.md).

### Browser (Wasm worker)

A browser tab can run a local Orb agent: the core compiled to `js/wasm` inside a web worker, with
read/write/edit on a bounded virtual workspace and provider calls made directly through `fetch`
(the endpoint must allow CORS). The same page can pair with a remote Orb over Bridge and drive
its conversations, whose tools and models then run on that remote machine.

```sh
make browser-serve        # http://127.0.0.1:8787
```

This is a debug screen today: keys and files live in tab memory. A remote Orb accepts the
browser with `orb bridge run --web-listen … --web-origin …`
(see [ARCHITECTURE.md](ARCHITECTURE.md#wasm-host-and-browser-debug-screen)).

### Cloudflare Durable Objects

One Durable Object (`OrbAgent`) hosts one Orb, addressed by name at `/agents/<name>/rpc`. Its
files, settings and session journal persist in Durable Object storage and survive restarts and
evictions; its model calls go out through `fetch`, and provider keys come from Worker secrets
through the `Env` port. It is driven with RPC frames over a hibernatable WebSocket or a streamed
HTTP POST (NDJSON), behind a bearer token (`ORB_TOKEN`), so any RPC client can use it. There is no
shell. One turn leaves about 38 MB of Wasm memory in use, within the 128 MB object limit; the
bundle is 9.7 MB gzip, inside Cloudflare's 10 MB paid-plan limit. On Cloudflare (2026-09-23), a
tool-using turn streamed its first frame in 1.8 s, and after a redeploy restarted the object its
files and history were intact.

```sh
make worker-dev           # local workerd
make worker-deploy        # your Cloudflare account (wrangler login first)
```

Known gaps: context files, skills and prompt templates are not loaded yet (they are read from the
process filesystem, not the FS port), session fork and clone are unsupported, and an object stays
awake for up to five minutes after a model call before it can hibernate (Go's idle HTTP timer).
The deploy path, endpoint contract, secrets and removal are in
[platforms/worker](../platforms/worker/README.md).

### Celld

[Celld](https://github.com/denoland/celld) runs Workers and Durable Objects on your own machines
with bucket-backed durability. The Durable Object bundle and `wrangler.jsonc` above run on Celld
unchanged: `make worker-celld-dev` serves it locally with no cloud account, and `celld deploy`
publishes it to a fleet.

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

### iOS and Android apps

Standalone apps embed the same core as a library built with gomobile. The app shell supplies the
ports and drives the core with RPC frames through callbacks. Android can run processes natively.
iOS apps cannot spawn processes, so `Exec` would run WASI commands in-process (the a-Shell model).
Termux and iSH are GPLv3, so the apps interoperate with them rather than embed them.

## Connecting deployments with Bridge

Every Orb has a peer identity (an Ed25519 key). Two Orbs become peers by exchanging a one-use
invitation that each owner approves, or through `orb bridge connect-ssh`. Access is granted
explicitly and in two separate kinds:

- **Controllers** are people. A controller grant lets you list, watch and drive another Orb's
  conversations: prompt, steer, cancel, switch or fork sessions. The conversation keeps running on
  its own Orb, with that Orb's tools and credentials, and continues when you disconnect.
- **Agents** are Orbs acting on their own. With the opt-in `bridge_call` tool, one Orb's agent can
  call another Orb, but only under an instance subject with its own grants. A person's controller
  access never passes to their agent.

Transports adapt to the target. Native Orbs use Tailcat (WireGuard with NAT traversal) and local
IPC. Browsers use the WebSocket transport, which keeps Bridge's pinned TLS inside the stream, and
connect outward. A Durable Object Orb is reachable today through its RPC endpoint; next it will
accept Bridge peers on `/bridge` (reserved), so a cloud Orb that is always on can pair with your
laptop both ways.

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
2. `platforms/scenario` produces the same session on it as natively;
3. a target end-to-end test runs in CI (for Workers: `cf dev` and `celld dev`);
4. one documented command deploys it, and one removes it;
5. it states its capability profile and its Bridge role;
6. its artifacts are produced by the release workflow and appear in the release notes.

Steps 1 to 5 make a target Preview; step 6 makes it Stable.
