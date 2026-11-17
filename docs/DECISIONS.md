# Orb — Decision Record

A small constitution of durable paradigms, the compat kernel, the live decisions that bind current
work, and the divergence ledger. Changes to this record require owner sign-off; everything else is
decided by whoever executes the work, within these bounds. Earlier versions live in git history
(`git log -- docs/DECISIONS.md`).

## Provenance

| | |
|---|---|
| Upstream project | **pi** — https://pi.dev, repo `earendil-works/pi` (formerly `badlogic/pi-mono`) |
| Pinned reference | the released version in `UPSTREAM.lock` |
| Upstream license | MIT, © 2025 Mario Zechner |
| This project | `github.com/OrdalieTech/orb`, MIT, © Ordalie — with attribution to upstream in LICENSE and README |

Orb began as a Go port of pi and keeps a tested pi-compatibility **kernel** (below). Beyond it, Orb
is its own product: features, layout, internal APIs and UI evolve on Orb's judgment, and upstream
changes outside the kernel are cherry-picked on merit, never ported by obligation.

## Constitution — durable paradigms

- **P1 — SDK-first, layered.** Orb is a Go module first; the `orb` CLI is one consumer. Layers
  compose upward — `ai/` → `engine/` → `agent/` → assemblies (`cmd/orb`, `chat/`, embedder mains)
  — and a capability's core lives at the lowest layer that can express it. Audience: Ordalie
  production embedding (including servers running many differently-configured instances in one
  process), personal daily-driver, and public OSS, simultaneously. Interfaces are assemblies, never
  obligations: the TUI, chat platforms, RPC, a web app, or an embedder's own surface are peer
  drivers of the same runtime, and no layer below an assembly imports one — a binary that skips an
  interface must contain none of its code.
- **P2 — Pure Go, portable everywhere, slim.** Orb's code is `CGO_ENABLED=0` on every target.
  Tier-1 targets are built and tested by the gate: native static binaries for linux, darwin and
  windows × amd64/arm64, linux/386 and linux/arm (iSH and small hosts), android/arm64 (Termux);
  `js/wasm` (browser workers, and one Worker host shared by Cloudflare Workers/Durable Objects and
  self-hosted workerd); `wasip1/wasm`. Standalone iOS and Android apps embed the same core; cgo
  appears only where the platform linker demands it, in the app shell, never in Orb packages.
  Stdlib first, internal helper next, dependency last and only via the ARCHITECTURE §8 table.
  Dev-only exceptions: `-race` test builds, Node as fixture-extraction and `js/wasm` test tooling.
- **P3 — Everything Orb-original is a capability module.** A capability ships as: a seam package
  (interfaces, no upward imports), an instance-scoped attachment at the lowest applicable layer
  (the `plugins/memory/` + `plugins/memory/agent/` shape), and a default-off assembly row adapting
  it into the CLI through the public extension API. Capability packages hold no package-level
  mutable state and read no environment or home directory implicitly; N instances with N
  configurations must coexist in one process. The core is never widened ad hoc: if the extension
  API cannot express a capability, the API is extended by recorded decision. Seams are cut when a
  second implementation is real, never speculatively. Mitosis is not a capability module: under
  P10/P11 it is core.
- **P4 — Minimal by default.** New capabilities default off. Stock `orb` stays lean; power is
  opt-in per user (settings, `/plugins`) or per assembly (embedder wiring). Configuration pages may
  be available before activation, as Mitosis management is in Settings and Ctrl+P, with service
  activation inside that page. Mitosis defaults are inert rather than off: every Orb has an identity
  but no listener, no advertised address and no grants. An agent reaches its owner's other
  conversations only once the owner turns on agent access (`mitosis-agent-calls`), and then with the
  owner's reach: the switch is the grant (2026-10-10, the owner's call, for simplicity over
  per-agent grants). A Worker's agent, not its owner, still calls under its own grant.
- **P5 — Pi compatibility is interop, not identity.** Orb reads what released pi writes on the
  kernel surfaces listed below and runs pi extensions, skills, prompt templates and packages
  unchanged, verified by conformance fixtures; provider wire shapes follow the providers. Orb may
  improve on upstream anywhere, kernel included: a deliberate improvement makes that fixture
  Orb-owned and adds one divergence ledger line, and upstream quirks are not spec. Orb is deployed
  only inside Ordalie, so it keeps no backward compatibility with its own past formats, settings or
  Go APIs: a breaking change updates the in-repo and Ordalie consumers in the same piece of work,
  and legacy shims are deleted rather than carried.
- **P6 — Orb-owned presentation and identity.** Public identity is `orb`; runtime configuration is Orb-owned: `~/.orb/agent`,
  project `.orb`, `ORB_AGENT_DIR`, `ORB_SESSION_DIR` and other `ORB_*` controls. No automatic
  Pi-directory or `PI_*` fallback, no Pi process impersonation. Compatibility retains file formats,
  `pi-messages`, provider-facing UA/originator strings and the JS `pi` API, not shared storage.
  The extension host alone may expose a private `host/bin/pi` command and
  `PI_CODING_AGENT_DIR` pointing to Orb’s directory for unchanged extensions; neither changes
  installed Pi. Owner-approved runtime separation, 2026-10-09. The TUI is Orb-owned: `F12*`/WP450 render goldens are
  Orb snapshots regenerated by `make fixtures-tui`; behavior-shaped values inside those files stay
  frozen upstream captures; upstream TUI work is cherry-picked on merit.
- **P7 — Host-only JavaScript.** JS/TS extensions run out of process on user-provided Node ≥22.6
  or Bun; no embedded engine, no shims; the static binary ships neither runtime, and their absence
  disables extensions only, never the product.
- **P8 — Trunk-based, every commit green; kernel changes fixtures-first.** One branch, `make check`
  before every commit. Changes to kernel surfaces land their conformance fixtures first (red), then
  the implementation (green).
- **P9 — Never weaken a criterion or a golden to pass it.** A failing fixture means the code is
  wrong. A genuinely impossible criterion stops the work and surfaces to the owner.
- **Layers, enforced.** Every package has a layer, and an import points to its own layer or
  below: libraries (`internal`, `tui`) → `ai` → `engine` → core (`agent`, `mitosis`, `host`) →
  capabilities (`plugins`) → drivers (TUI mode, RPC, ACP, `chat`) → hosts (`platforms`) →
  assemblies (`cmd`, catalogs such as `agent/assembly` and `chat/platforms`, examples, the agent
  harness). Only hosts and assemblies read or change the process environment or register at
  init; everything below takes its configuration as values. `internal/layering` checks it with
  one map and one ratchet of existing violations that only shrinks. Owner, 2026-10-06.
- **P10 — A portable core behind host ports.** Not everything is a plugin; the core is named and
  closed. It is the agent semantics that are identical on every target: the `ai` message/stream
  model and provider wire codecs, the `engine` loop, tool pipeline, events and compaction,
  session/settings/resource formats and their merge rules, the extension API and registry, the
  Mitosis peer protocol, identity, grants, instance registration and operation ledger, and one
  assembly that composes them. Core packages compile on every P2 target and reach the platform only
  through **ports** supplied by a host: `FS` (files), `Exec` (processes, optional), `Store`
  (documents, append-only logs, locks), `Net` (outbound HTTP and Mitosis streams: dial, optional
  listen) and `Env` (variables, directories, clock, randomness). A core package imports no
  `os/exec`, `os/signal` or `syscall`, never dials, listens or resolves through `net` (its portable
  types stay usable), uses no `http.DefaultClient`, and reads no environment, home, working
  directory or process state implicitly; `internal/layering` enforces this with a ratchet that only
  shrinks. A platform is a host (port implementations under `platforms/`) plus an assembly; UIs are
  drivers, not ports. Capabilities (P3) declare the ports they need, and an assembly on a host
  lacking a port omits them instead of failing: no `Exec` means no bash, process MCP or JS
  extension host. Port implementations pass one shared conformance suite, and the same scripted
  scenarios produce identical kernel output on every host.
- **P11 — Deployable anywhere, connected as peers.** Orb is a runtime to deploy, not a remote
  client: every P2 target runs the full core with its own model calls, tools, sessions and state,
  and hosted targets (Cloudflare Durable Objects, self-hosted workerd) are in scope. Deployments
  reach each other as Mitosis peers, and the unit of Mitosis is the agent: an agent directory with
  its identity, memory and tools, which is one peer. Addressing, discovery, grants and the name
  shown are per agent; a conversation is only an address under its agent, and a message to an
  agent with none named goes to its default conversation. People control an agent's conversations
  on any Orb that granted them, and an agent calls another only under its own grants. A target is
  supported only with evidence: its ports pass the conformance suites, the cross-host scenario
  matches native, a target end-to-end test runs in CI, one documented command deploys and removes
  it, and it states its capability profile and Mitosis role. `docs/deployments.md` is the
  catalogue; a target's status there changes only with that evidence. Mitosis is core: every Orb,
  on every host, has a peer identity for its agent, routes a conversation's calls to the runtime
  holding it, and dials peers through the transports its host supplies. Listening, advertised
  addresses and every grant stay explicit and owner-approved; controller and agent authority never
  merge.
- **D10 — Lean provider layer.** Wire codecs are Orb's own, and an official Go SDK stays only
  where it carries protocol work: `aws-sdk-go-v2/config` (the AWS shared config and credential
  chain, on native hosts). The `openai-go/v3` and Anthropic clients only posted bodies Orb had
  built, yet weighed 20.7 MB of the 55.4 MB Worker bundle, so requests go through one stdlib client
  under the shared retry policy, and Anthropic workload-identity federation's token exchange (one
  POST) sends what upstream's TypeScript SDK sends (owner, 2026-10-05); Bedrock's SigV4 signing,
  event stream framing and SDK retry policy are Orb's own too, which keeps the AWS SDK out of the
  Worker, where no shared config exists (owner, 2026-10-04: as much stdlib as possible).
  `google.golang.org/genai` was rejected on measured weight (+8.5 MB, +35 modules), so Gemini and
  Vertex use hand-rolled JSON/SSE. Hand-roll where no sound SDK exists
  (mistral-conversations, pi-messages, OAuth device/PKCE flows). No kitchen sinks.
- **D12 — Model catalog from direct authoritative sources.** Build-time generation uses
  models.dev's v2 catalog (`models.dev/experimental/v2.0/api.json`) for the baseline, intersects
  NVIDIA's manifest with the live NIM listing, and uses the live OpenRouter and Vercel AI Gateway APIs for those catalogs. Runtime refresh is a
  direct models.dev fetch into the `~/.orb` cache, never a pi.dev endpoint, keeping upstream's
  `checkedAt`/`lastModified` freshness semantics. With a ChatGPT login, startup also stores the
  account's `openai-codex` models from the ChatGPT backend list Codex CLI reads. `models.json` user
  overrides behave exactly as upstream (`docs/models.md`). The image catalog is checked in as
  deterministic Go data pinned by digest; generation-time validation plus full-catalog tests stand
  in for upstream's TypeScript validator.
- Shared catalog refreshes port only the joiner half of upstream's coordinator: a caller joining an
  in-flight refresh honors its own cancellation, but the fetch keeps the initiating caller's
  context, because `ai/models.Refresh` must observe cancellation synchronously at its commit points
  (a canceled refresh returns the stored overlay and never mutates the store). An initiator's
  cancellation therefore settles the call for concurrent joiners too.
- `ai.RetryAssistantCall` is the one retry policy for normal turns, compaction and branch
  summaries; retry lifecycle events keep upstream names and payloads on every surface.

### Engine, tools and extensions

- **D14 — Tool schemas.** JSON Schema is a first-class value on tools (raw schema type), plus a
  stdlib reflection helper deriving schemas from Go structs. JavaScript schema objects cross the
  extension-host protocol as JSON Schema.
- **D18 — MCP ships as a bundled first-party Go extension** (`modelcontextprotocol/go-sdk`),
  compiled in, off unless configured.
- **D29 — One high-level agent runtime.** `agent.AgentSession` is the sole high-level embedding
  runtime; harness primitives live in `engine/harness`. Upstream's `AgentHarness` facade and
  `streamProxy` `/api/stream` client are not ported: embedders use `AgentSessionOptions.StreamFn`.
- `SendMessageOptions.TriggerTurn` is `*bool`: upstream's `triggerTurn` is tri-state — unset still
  steers a streaming turn, an explicit `false` suppresses it.
- Entry points set `AI_AGENT=orb` and `ORB_CODING_AGENT=true`, at CLI/RPC entry only, never when
  embedded.
- Transcript-backed system messages and their agent events are on by default. Headless consumers
  must classify system messages explicitly; Ordalie's production SSE forwarding filters them so
  system prompts are never published.
- **Native session executors.** A capability may replace Orb's model/tool loop for its own provider
  only through generic seams: `SessionLoop` and `ContextUsage` on session options/config,
  `SessionRuntime.RequestInput`, `NewSessionOptions.Prepare` and Mitosis `attach.Options.Status`.
  Core names no executor; the TUI routes typed `/compact` to an executor as its own `/compact`
  prompt and renders tools case-insensitively. Claude Sessions is the one implementation: a
  default-off `claude-sessions` catalog row wired in `cmd/orb`. No executor-registration extension
  API is cut until a second executor exists (P3).
- **Permissions.** The policy plugin is optional and enforces rules when enabled. `auto` is the
  default approval mode (including the workspace-write preset): `ask` resolves to one-call consent
  without AI, while denials and guards stay enforced. Explicit `enforce` keeps manual approval;
  missing UI and headless fallback default to deny unless `askFallback: allow` is set, and
  cancellation or authorization errors never grant consent. `--auto` is a non-persistent override.
  An explicitly configured native filesystem sandbox is a host constraint shared by native bash,
  edit, write and child agents, and survives disabling extensions or the policy plugin; native
  executors that cannot honor it refuse the configuration. Go-only `Approved` flags on tool hook
  results distinguish explicit consent from a hook that merely does not block; they are excluded
  from JSON, so the pi/JS extension and RPC contracts are unchanged.
- **Herdr adapter.** A hidden host-environment extension auto-attaches only with `HERDR_ENV=1`,
  `HERDR_PANE_ID` and `HERDR_BIN_PATH`; it is not default-on precedent under P4. It reports
  `custom:orb` / `orb`, never Pi, and an `orb --session` resume command. Released Herdr 0.9.2+
  supports custom resume commands; older versions receive lifecycle state only. Orb skips
  Herdr’s managed Pi extension without changing that extension on disk.

### Extension and package ecosystem

- Skills ignore files follow upstream's `prefixIgnorePattern` exactly: nested patterns anchor to the
  ignore file's own directory and a leading `/` is stripped, so root-level `/pattern` matches
  basenames at any depth. Parity wins over correct gitignore behavior.
- Skills discovery keeps a canonical-path visit stack and returns each skill once under its clean
  path, where upstream recurses symlink cycles to ELOOP.
- MCP `"disabled": true` is honored as `"enabled": false`, and config parsing is per-entry
  tolerant: invalid entries warn and are skipped.
- MCP process servers open with `initialize` at protocol 2025-11-25: servers on SDKs predating
  2026-07-28 (rmcp, for one) exit on the `server/discover` probe the Go SDK sends first, and a dead
  process leaves nothing to fall back on. HTTP servers keep the SDK's discovery and fallback.
- Package dependency installs are Node-optional: the tarball is fetched natively, and `npmCommand`
  (default `npm install --omit=dev`) runs only when `package.json` declares unbundled
  dependencies; a missing npm degrades to a warning. `.npmrc` support is `registry=` and nerf-darted
  `_authToken` only.
- Package git subprocesses are quiet (`clone -q`, `checkout -q` with `advice.detachedHead=false`,
  `fetch -q`).
- Installed abbreviated Git commit pins resolve locally before fetch, since servers reject a short
  object ID as an unadvertised ref even when the clone already holds the commit.
- Ecosystem compatibility claims stay layered: stable loading, registration parity, workflow
  feasibility and executed probes are recorded separately, and a package that only loads is never
  called end-to-end compatible. Variable dynamic imports, top-level-await-only modules, real Node
  streams/sockets and native addons remain explicit ceilings.
- `rg`/`fd` auto-download into `~/.orb/agent/bin` as upstream (system binaries preferred).
  Clipboard uses OSC52 or `pbcopy`/`xclip`/`wl-copy`, no native addon.

### TUI

- **D15 — Component contract.** The TUI is a hand-rolled differential line renderer with the
  `Render(width int) []string` component contract — no TUI framework. JS extension custom UI rides
  on it, so it changes only with the extension-host surface. API style follows upstream names with
  Go-native mechanics (`context.Context`, error returns, functional options).
- **Interactive mode owns its viewport.** The alternate screen holds a scrollable transcript with
  status, extension widgets, editor and footer pinned at the bottom. Mouse-wheel or
  `Ctrl+PageUp` scrolling detaches live follow; scrolling back down, `Ctrl+End` or sending a
  message reattaches it, so streaming frames never move the viewed history. The right edge has a
  one-column proportional thumb with click-to-jump; left-drag highlights, holds the range stable
  during streaming, and copies on release. The reusable TUI stays inline unless a caller opts in,
  and mode 1010 stays disabled while either renderer is live. Upstream's `tuiMode`,
  `fullscreenExitOutput`, `fullscreenScrollbar` and `tui.altScreen.*` are not ported; the
  `searchMatchBg`/`searchMatchText` theme roles are carried for the HTML export (F6).
- **Huge transcripts use windowed layout.** Per-child lines are cached and only the visible range
  renders; steady frames are O(viewport + changed tail), while first render, resize, theme changes
  and global expansion stay O(history).
- **Clear-on-shrink stays differential** when the shorter content can be reconciled inside the
  active viewport: only vacated rows clear. The inline renderer keeps upstream's full redraw for
  true offscreen mutations.

### Chat gateway

- **D27 — `chat/` gateway.** An at-least-once processor around `AgentSession` with a
  `SessionProvider` lease/hydration seam and platform adapters; dependency direction is strictly
  `chat → agent`. Delivery state lives in `orb.chat.turn` custom session entries — the session is
  the single durable history; turn finalization keys off `AgentSettledEvent`, and crash recovery
  reads raw entries, never the built context. Tools are off by default and need an injected
  isolated workspace: `orb chat --tools` is that injection for an agent running isolated (one
  container per agent), and makes each conversation a full session of the agent, built as its ACP
  sessions are. The local provider is single-process; clusters supply fenced conversation
  ownership. Chat tests are plain `go test` goldens under `chat/`, never `conformance/`.
- **D28 — Chat platforms.** Telegram, WhatsApp, Slack, Teams, Discord (its gateway over `coder/websocket`, in the build for Mitosis),
  Messenger and Google Chat; official platform APIs only, stdlib-only clients, shared helpers in
  `chat/internal/`, zero new go.mod dependencies. Bridge-based platforms and E2EE Matrix stay
  excluded.

### ACP and team agents

- **ACP mode.** `orb --mode acp` serves the Agent Client Protocol natively, without Node: one
  process drives any number of sessions, and an ACP session id is the Orb session id, so
  `session/load` or a prompt to a stored id reopens it. Each session is built as the CLI builds
  one, plus the client's `mcpServers`, declared direct. A harness prompt (`systemPrompt`, `_meta.systemPrompt`, or `{append|replace}`) maps to
  `--system-prompt` and `--append-system-prompt`, as pi-acp maps it. Orb answers protocol version 2
  when a client asks for it, the provisional revision that carries the harness prompt in
  `session/new` instead of every message. Usage goes out as the `_goose/unstable/session/update`
  extension, which clients accept from any agent. No `session/request_permission`: the deployment
  is the sandbox, and deferred approval lives at the conversation level, in the `wake` plugin
  (owner, 2026-11-14). A session runs where its client says: the `cwd` of `session/new` and
  `session/load` is the session's, a stored session's included, and one the agent cannot enter
  is refused with its reason instead of failing every tool call.
- **Team agents.** One agent is one process, `orb chat <front>... --tools`: every front drives
  sessions of the same agent dir and store, so identity, memory (the `memory` plugin) and tools are
  shared, and each conversation (a Buzz thread or direct message, a Telegram chat) is a session.
  Owner, 2026-10-05: Orb replaces the Hermes agents, one container each.
- **Chat platforms are plain packages; one catalog composes them.** A platform package
  (`chat/telegram`, `chat/buzz`, …) takes its configuration as Options and reads no environment,
  registers nothing and holds no package state (P3). The catalog `chat/platforms`, an assembly,
  has one row per platform: the variables it reads (`Env`, `About` for help), `Open` (an adapter
  and its `chat.Inbound`: it polls, or it is called and `orb chat` serves its webhook on
  `ORB_CHAT_LISTEN`/`ORB_CHAT_PATH`), `Configure` (its section of an agent file) and `Setup` (its
  onboarding, `orb chat setup <platform>`, whose settings `orb chat` keeps per data directory). `orb` and
  `orb-agent` both read it, so the platform list exists once, and removing a platform is deleting
  its package and its row. A platform reaches the rest of Orb through two optional seams only:
  `Inbound.Authorize` gates its senders in place of the shared `ORB_CHAT_ALLOWED_SENDERS` (Buzz's
  `respond_to`), and an adapter that is `chat.Extended` adds an extension of its tools to every
  session of the agent, chat and ACP alike. Owner, 2026-10-06; fronts, sidecars and aliases went
  with buzz-acp, owner, 2026-11-15.
- **A team agent is one file and its secrets, set up outside Orb.** `orb-agent`
  (`platforms/agent/harness`), the image's entrypoint and a program of its own, validates
  `/agent/agent.yaml` strictly and renders it into the files Orb already reads (settings.json,
  AGENTS.md, mcp.json, models.json) and the browser's config, rewriting them on every start, so the
  file is the agent's configuration of record; secrets stay in the environment and reach Orb on
  descriptors as before. What it knows of a platform is its catalog row: `Env` and `Configure` (its
  section of the file mapped onto the environment it already reads, so the env contract is
  unchanged and an image without a file runs as before). The agent runs in its workspace, as the
  `agent` user, alone. A platform's `allow` is the shared sender allowlist. Owner, 2026-10-06.
- **Orb schedules wakes, the `wake` plugin.** A model wakes itself later, once or on a cron
  schedule, and the wake comes back as a message in the conversation that scheduled it, delivered
  once, a missed one once at restart. An ask no UI can answer is held the same way: the user's next
  message approves it (the plugin runs the call) or denies it, and without an answer in 30 minutes
  it does not run. The plugin has no goroutine or timer: its host advances it and delivers to
  sessions it has not open, `orb chat` for team agents (where it is on) or an SDK host. The wakes
  live in the host's own store (a file, a table, Redis), whose conditional claim keeps delivery
  once across replicas. It replaces the systemd timer that started turns through the ACP socket.
  Owner, 2026-11-14.
- **A browser is a capability of the agent's image, not of Orb.** The `browser` image adds
  agent-browser, which the agent drives from its shell with its skill, and Lightpanda, its default
  engine (small, no rendering, partial Web APIs); built with `CHROMIUM=1` it adds headless
  Chromium for screenshots and real-site fidelity, chosen per agent with `browser:`. Each browser
  runs in its agent's container as the agent's user with the tools' environment, its logins in the
  volume (Lightpanda's restored cookies and storage, Chromium's profile); Chromium runs without its
  own sandbox, which needs user namespaces containers deny, so the container is the sandbox.
  Rejected: a browser plugin in Orb and a browser shared between agents. Owner, 2026-10-06.
- **The base image keeps only what agents run.** Debian 13 with bash, git and curl, assembled into one layer without apt, Perl or documentation (copyright
  files kept); git's Perl-only commands (`send-email`, `svn`) go with Perl. Owner, 2026-10-06.
- **Memory is the agent's; each session is a view of it.** A session's prompt carries the memory
  profile as it stood when the session began, so the prefix stays cacheable. Whatever changed
  since, written by any other session of the agent on any front or process, joins the session at
  its next turn as one `orb.memory` message listing what was saved, replaced or forgotten; a
  session's own changes are already in its transcript, and a compaction reloads the snapshot.
  Rejected: a live system prompt (every write would void every live session's cache and rewrite
  what earlier turns saw), and relying on the model to call recall. Owner, 2026-10-06.
- **A team agent's tools never hold its credentials.** Tool processes (bash, MCP servers,
  extension hosts, external agents) inherit only the `ORB_TOOL_ENV` allowlist when it is set, and
  `orb chat` sets one (`PATH,HOME,USER,SHELL,LANG,LC_ALL,TERM,TZ,TMPDIR`) unless its operator
  widens it: an allowlist, because a deny-list misses the next secret an operator adds. An Orb
  with an allowlist marks itself non-dumpable, so its `/proc` entries, environment included, are
  unreadable to its same-user tools. Without an allowlist tools inherit everything, as pi's do.
- **Buzz is native.** `chat/buzz` is a platform like Telegram, Orb speaking the relay's protocol
  itself: buzz-acp's DM gate had to be patched, it dropped what arrived during a restart, it tied
  the image to Buzz Desktop's glibc 2.39 amd64 binaries, and Buzz 1.0 no longer ships it. The
  agent subscribes over the relay's WebSocket (NIP-42, its owner's NIP-OA tag in AUTH) to its
  mentions in the channels it is in, which also catches direct messages and Solo chats, since Buzz
  clients tag their participants, and to every message of its `channels`. The relay fans a
  channel's events out only to subscriptions naming the channel (a `#p` subscription alone gets
  stored events, never live ones; the live test found it), so it subscribes per batch of 128 of
  the channels its rosters (39002) list, and to each channel it is added to (44100) while
  connected. Everything else is the relay's HTTP API
  under NIP-98 with the tag in `x-auth-tag`: queries, its replies, its profile, media. Each thread
  is a conversation (a top-level message opens its own) and a direct message is one; the reply
  goes in the answered event's thread, with no `p` tag, so agents answering agents do not loop,
  and is not posted when the agent already posted in that conversation with its tool during the
  turn (the live test caught Gemini answering twice; a harness rule, not a prompt one). Presence
  (kind 20001 on the WebSocket) shows it online as Buzz's clients do.
  Mentioned mid-thread, the agent first gets the thread's messages since it last spoke there that
  never reached it. A cursor file in the gateway's data directory resumes the subscription where
  it stopped, and the turn ledger deduplicates what comes twice. `respond_to` is buzz-acp's
  (owner-only, the default, allowlist, anyone), applied to direct messages too; the owner is the
  auth tag's, and the allowlist the shared one's pubkeys. Not carried over: buzz-acp's sibling
  agents (list them), its `!shutdown`/`!cancel` (the processor's `/stop`, `/new`, `/status` and
  `/compact` work on Buzz), its heartbeat and its status reactions. The agent's tools are two,
  `buzz_query` (any filters) and `buzz_publish` (any event, signed as the agent, its owner's tag
  added as Buzz's own clients add it), with recipes in their prompt guidelines: everything on Buzz
  is an event, so two tools cover what the CLI's many commands did. Signing is BIP-340 over
  secp256k1 in `math/big`, checked against BIP-340's vectors: variable-time, and it only ever signs
  with the agent's own key. The Buzz app's own plugin for Orb, which has the app host an Orb agent
  over ACP, lives beside it in `chat/buzz/plugin`, so all of Orb's Buzz code is in one place.
  Owner, 2026-11-15, replacing "Buzz is reached the way Buzz designed it" (2026-10-06).
- **The agent signs its profile; its owner signs its record, on the owner's machine.** Buzz names
  an agent by its kind:0, and its agent directory lists one only when that kind:0 carries a valid
  NIP-OA tag from its owner and the owner has published a kind:30177 record of it (its name and
  whom it answers). `orb chat buzz` publishes the kind:0, once connected, from `BUZZ_NAME`,
  `BUZZ_ABOUT` and `BUZZ_AVATAR`, merged into the profile on the relay and only when they changed.
  The 30177 and the attestation need the owner's key, which never has to reach the agent: `orb chat
  setup buzz` (the catalog's `Setup` row, the only onboarding code, in `chat/buzz`) makes the
  agent's key and settings, and the owner approves it with the same command, here with the
  owner's key or on their own machine (`--attest`, which prints the `--auth` to give the agent);
  the agent checks the attestation's signature before keeping it. An agent of a closed relay needs
  no membership of its own: the relay admits it through its owner's tag. The 30177 is written as
  Buzz's app writes one (`parallelism`, `respond_to`, `name`, `picture`, tags `d` and `runtime`
  `orb`, a runtime no app runs, so the app lists it as running elsewhere). The settings land in
  `<data dir>/buzz.env` (0600), which `orb chat` reads under the environment, except as a team
  agent: its entrypoint hands it every credential, and its tools could write the file. The file is
  also a `docker --env-file`. No other owner record is needed: channel membership is the relay's,
  and team rosters resolve through 30177s. Owner, 2026-10-06; onboarding in Orb replacing
  `buzz-owner.py`, owner, 2026-11-15.
- **The Nostr key is a credential like the others.** It reaches Orb on `ORB_SECRETS_FD` with the
  model's key and the bot tokens, in a process its same-user tools cannot inspect, and no tool
  process inherits it. The `sidecar` user, its group socket and the execute-only CLI went with
  buzz-acp. Owner, 2026-11-15.
- **A team agent holds its credentials on descriptors, never by path.** Its in-process tools
  (read, edit, write) run as Orb itself, so no file Orb can open by name may hold a secret, and
  neither may `/proc/self/environ`. `orb-agent`, still root, passes every variable outside a
  plain list (`PATH`, `HOME`, locale, `ORB_*`) as KEY=VALUE lines on `ORB_SECRETS_FD`,
  which Orb loads into its environment (`/proc/self/environ` keeps the exec-time one), and opens
  `/agent/secrets/auth.json` (root, 0600) for OAuth logins as `ORB_AUTH_FD`, close-on-exec, which
  Orb reads and refreshes in place; reopening it through `/proc/self/fd` checks the file's own
  permissions and fails. A third user for tools was not enough: the read tool is Orb.
- **A team agent's settings.json and models.json stay its configuration of record**: `orb chat`
  reads them from the agent dir on every start instead of the copies the native store imported,
  so editing the mounted file and restarting changes the model.
### Storage, release and platforms

- **Native SQLite storage.** The CLI stores native state in SQLite with Pi JSONL import/export, so
  pi does not read a live native Orb session file. SDK defaults and the explicit `--pi-files` CLI
  entry point stay file-backed. A separate SQLite cache holds foreign summaries and bounded visible
  message excerpts: read-only offline, no reasoning/tool payloads, remote authority on reopen.
- **D24 — Live tests.** Merges are fixture-only and offline. Provider work runs an opt-in live
  smoke (`ORB_LIVE_TESTS=1`: one streamed tool-call round-trip per provider). A nightly capped
  suite (`make nightly-live`: OpenAI + Anthropic, three scripted tasks) reports failures without
  blocking merges. Live outputs are never recorded into fixtures.
- **Releases.** Independent semver; the upstream pin is in `UPSTREAM.lock`. `checksums.txt.sig` is
  an Ed25519 signature over `checksums.txt`, made by GoReleaser from the `ORB_RELEASE_SIGNING_KEY`
  secret; `orb update` and `host.update` verify it against `releaseKey` in the binary before
  trusting any checksum, and a key rotation ships in a release signed by the old key. `orb update`
  replaces the running binary after verification with an atomic swap, never elevates, and leaves
  Homebrew, Nix and Snap installs to their package managers. Every release carries checksummed
  source archives that must rebuild with `CGO_ENABLED=0 -buildvcs=false`.
- **Windows CI blocks every commit.** Windows artifacts join releases once the release workflow
  packages and verifies them.
- **Apps render `orb app`; their logic lives once, in Go.** `mitosis/view` follows every
  conversation on Mitosis and serves apps state and rows ready to draw; an app renders them and
  sends intents back, so Android, iOS, the desktop and the platforms that follow behave the same
  and a fix lands everywhere. Each app bundles its `orb` and talks to the machine's Mitosis, the
  CLI's own.
- **The desktop app is Go, drawn by MyGo, on macOS, Linux and Windows.** `platforms/desktop` renders
  `orb app` with MyGo's native UI (pure Go, the system's libraries reached through purego, no cgo),
  in the Android app's look, as a desktop app: a sidebar of conversations by machine, the
  conversation or a settings screen beside it. It is a Go module of its own, so the toolkit never
  reaches Orb's module or the `orb` binary; it links one Orb package, `internal/qr`, for the
  pairing code. `build.sh` builds each platform's `orb` into the app's resources and `mygo build`
  packages and signs the apps. Its terminal is MyGo's plugin over Ghostty's libghostty-vt, a
  native library pinned by SHA-256 that the build puts in the app, as SwiftTerm was the Mac app's.
  It replaced the SwiftUI Mac app: `platforms/apple` is iOS's alone. Owner, 2026-11-03.
- **The Android app runs the orb binary, not a gomobile library.** It executes the unmodified
  `CGO_ENABLED=0` CLI (`liborb.so`) as `orb app`, its own conversations included: the phone is a
  Mitosis peer of itself (the owner reaches its own machine's instances and `host.*` with the
  calls it makes on any peer), so one client follows every Orb, with long polls. Each phone
  conversation is an Orb its Mitosis launches, as on a server; the phone runs exactly what
  desktops run. P2's library embedding remains the path for iOS, which cannot spawn processes.
- **The Android app's agent works in a Linux the app carries.** Android 10+ refuses to execute
  files from app data, so the app ships proot and its loader as `lib*.so` (GPL-2.0, from Termux's
  package repository) and downloads Termux's `bootstrap-aarch64.zip` at first start, checked
  against GitHub's published sha256. proot maps the app's copy onto `/data/data/com.termux`, so
  `pkg`/`apt` work unmodified without the Termux app. The bash tool's `shellPath` and the in-app
  terminal start `liblinux.so`; the core stays the plain binary outside proot.
- **Mitosis profile sync.** A profile has at most one sync group: a Mitosis discovery scope with a
  set of owners among its members. Any owner admits (`orb mitosis sync add`, paired devices only),
  removes, promotes and demotes devices; a plain member cannot. Each change is a new version of the
  membership, signed into that owner's record; a version counts when its publisher owns the version
  it replaces, the highest wins, and of two of one version the smaller digest, so two owners
  removing each other at once leave one; a group never has no owner. A device follows a group with
  `sync join <peer>` through any member, and nothing derives membership from pairing. v1 shares two data classes: credentials (auth.json's API keys and OAuth
  logins, never a key that is a command, `!…`) and memory (the memory plugin's items, merged by ID
  with the same rules); a class is a registered Shareable/MergeShared pair, so configuration can be
  added later the same way, but none is built. Each class travels in a vault of its own: a device's
  signed peer record of the group scope (`orb-mitosis/1`) carries only each vault's digest and
  size, and any member holding a vault serves it by digest, in bounded chunks, only to its current,
  unblocked recipients; a vault is capped at 64 MiB, since every member, a browser tab included,
  holds it in memory. A vault is sealed with AES-256-GCM under a fresh 256-bit key on every publish, and that key is
  sealed to each recipient's X25519 key (the Montgomery form of its Ed25519 identity), stdlib only.
  Every owner publishes: per class, the group's set is the owners' vault of highest sequence. An
  owner takes each new one over its own, merges in the entries members hand it, keeping its own,
  and seals the result to every current, unblocked member at the next sequence unless the group's
  set already says it. A member applies the group's set and hands the owners alone what that set
  lacks or holds otherwise. Between two OAuth logins the one that expires later wins, so a token
  refreshed on any device replaces the stale copies; an entry removed on an owner is removed
  everywhere. Any member relays the owners' signed records, so the group lives on while some
  owners are off, and with one owner left nothing stops. Removing or blocking a device on an owner
  leaves it out of every later set, though it keeps what it already received, so revoking a key
  means rotating it; a device leaves its group by removing itself. Native devices run a round on every reconciliation, so a group converges with no
  command; `orb mitosis sync` runs one at once (a Worker object syncs that way). Sync is inert
  until a group exists. A device that boots unattended can decrypt its own vault, which is accepted
  as the cost of autonomy; a low-trust host belongs in a group of its own, with capped credentials.
  Owner, 2026-11-16; memory and vaults by digest added at the owner's request, 2026-11-17; several
  owners, 2026-11-17.

## Divergence ledger

| Divergence | Kind | Rationale |
|---|---|---|
| Gemini 3 cross-model tool replay | reliability adaptation | foreign-model function calls carry Google's documented `skip_thought_signature_validator` on the outgoing request only; same-model signatures and canonical history are untouched. Pi's unsigned replay is rejected by Vertex with HTTP 400 |
| Malformed and colliding provider tool-call recovery | reliability adaptation | when a provider declares tool use without emitting a call, orb retries at most three times with non-persisted recovery context; duplicate call IDs are deterministically suffixed. Session and event JSON shapes are unchanged |
| Anthropic sign-in holds both loopback addresses | security adaptation | with the default `127.0.0.1` callback host Orb also binds `[::1]` and refuses to start when another program answers there, since the OAuth state carries the PKCE verifier. Nothing on the wire changes |
| Bundled MCP extension | addition | owner requirement; kept out of core |
| No codemode | not yet adopted | no `codemode` tool, script `models` API, classifier models or Jev, and tool `exposure: "codemode"` is treated as `deferred` |
| Experimental harness is Orb-owned | ownership | upstream deleted `packages/agent/src/harness` (session v4, pico3, `AgentHarness`) in v1.0.0; `engine/harness` keeps only what Orb uses (JSONL v3 session storage, compaction, branch summaries, host ports), and the harness parts of F10 are carried from the committed tree instead of extracted. Session v4 is not supported |
| Steer and follow-up on an idle session start a run | behavior | upstream queues them and reports `queued`; Orb starts the run and reports `started`. Input queued as a run ends is never left waiting: a run ends only with an empty queue (session loops included), and what arrives as it settles, or while the TUI's submission is still returning, is the next run |
| `packages/server`, `packages/{client,protocol}`, `packages/session-backends` | removed | experimental remote-session server, client, CBOR protocol and sqlite backend; outside Orb's product boundary |
| `packages/telemetry`, telemetry/analytics settings | removed | unknown settings keys tolerated on parse, nothing sent, no plumbing |
| `packages/ai` Cloudflare `env.AI` gateway binding | excluded | needs a Workers environment object; not exported, and the provider registry (F2) is unchanged |
| Radius provider and OAuth, `/bug` | removed | pi.dev-coupled services; the generic `pi-messages` wire shape is ported. `/bug` stays captured in F8 with an explicit command-surface exclusion |
| Version/update checks | neutralized | OrdalieTech/orb GitHub releases, never pi.dev |
| Independent runtime configuration | isolation | Owner-approved: `.orb` roots and `ORB_*` controls replace Pi defaults without fallback or automatic import. Native SQLite remains `~/.orb/state/orb.db` with unchanged namespaces, preserving existing Orb sessions. Pi file/API formats remain compatible; conformance adapters translate only configuration-path components and the exact bash environment guideline. Existing Pi resources can be copied or imported explicitly, never moved or rewritten automatically |
| Native Herdr identity and restore | isolation | Orb owns `custom:orb` reports and its `orb --session` resume argv instead of lending Orb session IDs to the managed Pi integration; custom resume is supported in released Herdr 0.9.2+ |
| Public identity and executable | renamed | `orb` avoids colliding with an installed `pi`; local OAuth callback pages use Orb branding; exported pi HTML helpers keep their fixture contract |
| Default system-prompt identity | product positioning | Orb presents as a general-purpose problem-solving harness rather than a coding agent; tool lists, guidelines, context/skill injection, custom prompts and assembly order stay upstream-compatible, and F9 applies only the exact ledgered substitutions to upstream goldens |
| External Agent Skills discovery | compatibility addition | standard skill roots from Claude Code, Codex, OpenCode, Gemini CLI, Cursor and GitHub Copilot are imported. Pi-native and `.agents` roots keep precedence, project roots require trust, canonical files load once, first-name-wins diagnostics apply, and plugin/cache directories (including Claude Code's `skills/synced`) are never scanned. Interactive `@` completion offers badged skills at any token and inserts the canonical `/skill:name` token, drawn as an atomic `◆ name` chip. A message starting with one token keeps upstream's expansion; otherwise interactive mode prepends `/skill:name ` per distinct skill, and the kernel expands a leading run of distinct tokens into consecutive skill blocks before the message, so one message can invoke several skills. The transcript shows the chips inline and an expandable `◆ name · description` footer per skill; the HTML export template stays upstream-pinned (F6) |
| `/share` | neutralized | local HTML export instead of pi.dev upload |
| `/changelog`, `/arminsayshi`, `/dementedelves` | product identity | `/changelog` shows Orb's CHANGELOG.md; pi's easter eggs and the Earendil announcement image are removed |
| Model catalog runtime refresh | neutralized | models.dev directly, not pi.dev overlays; `openai-codex` models come from the signed-in ChatGPT account (D12) |
| darwin modifier-key native addon | gap | kitty keyboard protocol where possible |
| win32 console native addon | replaced | pure-Go console-mode calls (P2) |
| Bundled llama.cpp extension, `packages/storage/sqlite-node` | excluded | native Node integrations cannot satisfy P2 |
| `orb login` / `orb logout` subcommands | addition | headless auth lifecycle; bare `orb logout` lists stored credentials and requires an explicit provider |
| `orb models` subcommand | addition | the apps' model choice before any session runs: `--json` lists the models a new session can use with its default marked, `use` sets `defaultProvider`/`defaultModel`; `orb models …` is no longer a prompt |
| NVIDIA `qwen/qwen3.5-122b-a10b` denylist | addition | the live NIM endpoint advertises it, but its metadata cannot satisfy orb's chat-model contract |
| `CompleteSimple` and common simple tool choice | Go API adaptation | Go exposes the collection directly and the portable `auto`/`none`/`required` intersection; a named choice is one advertised tool plus `required`, so no provider shape leaks into embedders |
| No process-wide default stream | Go API adaptation | P10 forbids process globals: `engine.NewAgent` takes its stream explicitly, `NewAgentSession` defaults to `ai/api/all`, light assemblies build an `api.Registry` with only their providers, and `orb_nodefaultproviders` empties the default |
| Missing default stream error timing | Go API adaptation | `NewAgent` cannot return an error, so orb reports upstream's identical error on the first prompt or loop call |
| Single Ctrl-C exit at an empty prompt | usability adaptation | a nonempty draft still clears without exiting; focused selectors keep their cancel binding |
| Interrupting an unanswered turn takes the prompt back | usability adaptation | when the interrupted turn has shown nothing yet, orb rewinds the branch to before its prompt and returns the text to the editor, so an edited resend replaces it. Once any text, thinking or tool call has appeared, escape aborts exactly as upstream; the abandoned attempt stays in the tree |
| Compact built-in editor chrome | usability adaptation | a fitting one-line transient status moves into the editor's top border and the session name appears as a badge; dialogs, narrow statuses, scrolled drafts and extension editors keep upstream's status lane |
| Compact task and queue surfaces | usability adaptation | the tasks plugin keeps its schema and details but condenses rendering, adds a click-expandable widget and an unbound `/tasks` command; the queue adds a count to upstream's one-row truncation |
| `chat/` gateway and platforms | addition | D27/D28; kept out of core, one-way dependency on the SDK |
| ACP mode (`orb --mode acp`) and team agents (`orb chat buzz …`) | addition | pi has no ACP server (pi-acp is a separate Node adapter over `pi --mode rpc`, one process per session); Orb serves ACP itself, every session in one process. RPC frames are unchanged |
| Tool environment allowlist (`ORB_TOOL_ENV`) | addition | pi's tools inherit the whole environment; unset, Orb's do too. Set (always for `orb chat`), tool processes get only the listed variables |
| `AgentSessionOptions` tool-operations hook | addition | D27; seam for VFS/sandboxed tool operations |
| Streaming accumulation via append buffers | Go performance adaptation | `x += delta` is an amortized O(1) rope in V8 and an O(n) copy in Go; buffers preserve every byte. Do not restore `+=` |
| Tool-argument re-parse gated above 8 KB | Go performance adaptation | below the floor streamed `arguments` stay byte-identical; above it only the live preview lags, while `partialJson`/`partialArgs` and the end event stay exact |
| Footer metadata refreshed off the render thread | Go performance adaptation | Git and provider scans must not hold the render lock: refresh starts after 500 ms, the Git probe is capped at 250 ms |
| Print mode fails a prompted run that produced no assistant | usability adaptation | overflow recovery drops the failed assistant, so upstream exits 0 with empty stdout, indistinguishable from success |
| `exec.Cmd.WaitDelay` on the extension host | Go API adaptation | stderr is wrapped so extensions cannot put the terminal in cooked mode; without a delay `Wait` blocks on every grandchild holding the pipe |
| Goroutine panic guard inside `tui` | Go API adaptation | Go cannot observe another goroutine's panic, so every goroutine the package spawns recovers, runs the registered restores, writes the panic after restore and exits 1; `ProcessTerminal.Run` shares the same `Stop` path |
| Terminal widths from uniseg plus a three-class correction | measured divergence | `rivo/uniseg` corrected for keycap sequences, East-Asian-Wide text-presentation symbols, and U+FF9E/U+FF9F. Residual: standalone skin-tone modifiers, Yijing symbols, spacing combining marks, Hangul fillers, two/three-em dashes, emoji newer than x/text's tables, Wide-text-base plus halfwidth-mark clusters, noncharacters |
| Strict sampling with internal `$ref`, `$defs` and `definitions` | provider adaptation | pi sends such schemas non-strict although OpenAI, Anthropic, Bedrock and Gemini accept them in strict mode; Orb makes every definition strict and falls back only for external `$ref`; tool-argument validation then drops the optional nulls such a call returns behind a resolvable `$ref`, which pi-schema keeps (Orb-owned F1 validation cases) |
| Session-file locking | addition | upstream writes JSONL unlocked; orb uses `internal/filelock`'s proper-lockfile-compatible mkdir+heartbeat lock, which a concurrent pi can recognize and reclaim |
| Extension-host `cancel_request` and `provider_stream_event` frames | Go API adaptation | the host process boundary has no in-process `AbortSignal` or live provider streaming; these frames plus a per-request `AbortController` registry reproduce both, and the host exits on stdin close |
| `Request was aborted` for ctx-aborted stream failures | Go API adaptation | every adapter persists upstream's mid-stream abort text; request-phase texts follow upstream per adapter, while the anthropic/openai stream entry points collapse pre-stream ctx errors into the mid-stream text so the TUI's exact-match rendering holds |
| `extensions.Exec` bounded wait on inherited stdio | Go API adaptation | Go cannot see child exit while a detached grandchild holds the pipes, so orb waits up to 5 s and then reports the child's own exit code |
| RPC exits on SIGHUP without flushing a stalled consumer | behavior | pi 1.0 flushes stdout on every RPC shutdown but SIGTERM, so SIGHUP with an unread stdout hangs until a forced kill; Orb treats SIGHUP like SIGTERM and exits 129 without waiting on the consumer. Normal completion still flushes every frame |
| OpenRouter endpoint match | behavior | pi 1.5.0 takes OpenRouter's billed cost as the total for a model whose base URL merely contains `openrouter.ai`; Orb requires the endpoint host to be `openrouter.ai` or a subdomain, so a proxy path that names it cannot make its reported cost authoritative |
| No docs section | product adaptation | pi's system prompt points at README, docs and examples under its package directory because every pi distribution ships them; Orb ships only its binary and describes itself through the built-in `orb` skill (`orb skill`), which every CLI session lists, so its prompt has no docs section. F9 compares against pi's prompt with that section removed |
| No Go RPC client | omission | pi ships `rpc-client.ts`; Orb removed its unused Go port (`agent/modes.NewRPCClient`) in favor of driving `orb --mode rpc` over stdio or hosted RPC. The RPC server and wire stay pi-compatible |
| Higher-contrast muted borders | behavior | owner request: pi's `borderMuted`/`thinkingOff` (light `#b0b0b0`, dark `#505050`, about 2:1 against the background) made the input border and dialog rules hard to see; Orb uses light `#8a8a8a` and dark `#707070` (about 3.3:1). F12 keeps pi's values as the reference and skips those two roles |
| Named account display labels | addition | pi keeps one credential per provider; Orb lists several accounts and persists a label per account, ambient and runtime identities included, in the accounts store. Clearing restores the source label; credentials and selection are unchanged |
| RPC untyped-dispatch residual | gap | a known command with a mistyped member answers `success:false` with Go decoder text where upstream surfaces a JS `TypeError`; frame shape and the missing/non-string/null `type` path are byte-exact |
| `OAuthCredentials` extra members serialize sorted | Go API adaptation | Go maps have no insertion order; declared members keep upstream order and extras follow sorted |
| Markdown session export | addition | an `--export` path ending in `.md` uses the markdown exporter; every other path keeps upstream's HTML |
| Concurrent `AfterToolCall` hooks and `EventSink` in parallel tool mode | Go API adaptation | orb's parallel workers invoke both concurrently, so a hook mutating shared state needs its own locking (documented at `engine/types.go`) |
| Parallel tool fan-out bounded at 16 | Go resource adaptation | each Go tool call is a goroutine that may spawn subprocesses; result ordering is unchanged |
| Tool-update sink driven by one bounded queue (256) | Go API adaptation | a drain goroutine keeps ordering and never blocks the tool; a sink more than 256 updates behind backpressures rather than dropping |
| Native mouse and click support in interactive mode | addition | an optional `tui.MouseHandler` found by type assertion keeps D15's contract. SGR (1006) reports go to the component under the cursor before scrollbar, wheel and selection; drags and modifiers skip dispatch. Wired: `/tree`, `/resume`, `SelectList`, `SettingsList`, the extension option dialog and the editor |
| Embedded orb-extension-sdk | replacement | every `@earendil-works/pi-*`/`@mariozechner/pi-*` specifier resolves to the go:embed-ed `orb-extension-sdk` (`sdk/sdk.json`, materialized beside `host.mjs`, named by `ORB_EXTENSION_SDK_ROOT`): pure symbols ported from upstream, thin handles, and the `sdk_v1`, `agent_session_v1` and `model_runtime_v1` protocol services. Unsupported exports throw `OrbUnsupportedCapability`; a resolution landing in an installed real pi SDK is a per-extension load failure naming the import chain. Extension dependencies still materialize via `deps.go`; F13 gates the surface |
| Node capability floor 22.6, full capability 22.13 | measured divergence | 22.6–22.12 runs JavaScript and erasable TypeScript but cannot compile TypeScript inside `node_modules` (`module.stripTypeScriptTypes` arrives in 22.13); the loader names the missing capability. Node 26 removed TypeScript transformation, so enums, parameter properties and namespaces do not run there |
| Frontmatter YAML text keeps its terminating newline | dependency workaround | npm `yaml` treats end-of-input as a line break while `yaml.v3` does not; upstream pins the observable value in its tests. Do not restore the exclusive slice |
| Bun SDK aliases via `NODE_PATH` | Bun adaptation | `Bun.plugin` `onResolve` never sees nested imports; `NODE_PATH` is consulted after the `node_modules` walk and ignored by Node for ESM |
| Bun runs with `--no-install` | safety adaptation | dependencies come from an explicit audited install, never Bun's implicit mid-session fetch |
| Disposed sessions refuse work (`ErrSessionDisposed`) | SDK safety adaptation | upstream's `dispose()` refuses nothing, so a post-dispose prompt still calls the model with events silenced |
| Subagent parallel width capped at 32 | resource guard | each entry costs a goroutine, temp dir, child session and provider call; enforced in the tool and declared as `maxItems` |
| Unified-patch hunk headers derived locally | dependency workaround | `go-udiff` misreports the new-file start of later `@@` headers; orb recomputes them and matches `Diff.createTwoFilesPatch` across a 2,998-case differential fuzz |
| Azure serves the Responses API only | omission | pi 1.0.3 renamed the provider to `azure` and added Foundry Chat Completions deployments (DeepSeek V4 Pro). Orb takes the rename (the `auth.json`, `models.json` and `settings.json` keys) and keeps the Responses API; no agent runs on Azure, and F2's provider list is compared without `openai-completions` for `azure` |
| `models.json` keeps a provider's valid siblings | behavior | pi 1.4 validates the whole file against its published schema and rejects it when one provider is invalid; Orb validates per provider, so one typo costs that provider and not the others. The WP250 composition case is carried from the committed tree |
| No `/bug`; `/share` is a GitHub gist | omission | pi's `/bug` reports to pi's developers and its `/share` can upload to pi's Radius service. F8's and F12's built-in command lists drop `bug` and keep the gist description (`orbCommands`) |
| `samplingParamsByThinkingLevel` is ignored | gap | pi 1.0.2 reads it from `models.json`; Orb accepts the key without applying it, since no agent configures per-level sampling |
| `durationMs` goldens use a frozen clock | harness adaptation | pi records `durationMs` on assistant messages (from `Registry.StreamSimple`; faux streams time themselves), tool results and `tool_execution_end`. The extractor freezes `performance.now`, and `runner.ByteDiff` compares measured durations as zero, so presence, position and type stay checked |
| No cache warming | not yet adopted | pi keeps eligible prompt caches warm between requests (`cacheWarming`, idle by default since 1.5.1); Orb sends no request a turn did not ask for, since several Orbs idle under one Mitosis and every warm is billed. The pi-ai cache-warming exports are SDK stubs, and Anthropic message threads need no `disableContinuation` |
| Disposal waits five seconds for aborted tools | behavior | pi waits without bound for aborted tools to settle before extensions shut down; Orb waits five seconds, so a tool that ignores its abort cannot hold the exit |
| Extension overlays closed by a reload settle their call | Go API adaptation | pi leaves the `ctx.ui.custom()` promise of an overlay removed by `/reload` or a session switch pending; in Orb the call returns without a result, since a pending call is a goroutine and an open host request |
| MCP stdio servers keep their tree on Windows | platform adaptation | pi 1.5.1 ends a stdio server's process group on disconnect (POSIX); Windows has no group and reuses a waited PID at once, so Orb ends the tree only where a group exists |
