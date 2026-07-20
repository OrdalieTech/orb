# pi-go — Decision Record

Outcome of the planning interview (2026-07-17). Every architectural and product decision below is
settled and confirmed by the project owner. Changes to this record require owner sign-off; everything
else (implementation detail) is decided by whoever executes the work package, within these bounds.

## Provenance

| | |
|---|---|
| Upstream project | **pi** — https://pi.dev, repo `earendil-works/pi` (formerly `badlogic/pi-mono`) |
| Pinned reference | commit `3da591ab74ab9ab407e72ed882600b2c851fae21`, version **0.80.10** (2026-07-17) |
| Upstream license | MIT, © 2025 Mario Zechner |
| This project | `github.com/OrdalieTech/pi-go`, MIT, © Ordalie — with attribution to upstream in LICENSE and README |

pi-go is a faithful Go port of pi, not a reimagining. Upstream's docs at the pinned commit
(`docs/*.md` in each package) are the specification; where this record is silent, upstream behavior wins.

## Product decisions

- **D1 — SDK-first.** pi-go is a Go module first; the `pi` CLI is one consumer of it. The `ai`
  layer must be importable on its own (as `@earendil-works/pi-ai` is upstream).
- **D2 — Full parity, no staged v1.** The whole of pi v0.80.10 is in scope: agent core, all tools,
  session tree + compaction, skills, prompt templates, themes, TUI, print/JSON/RPC modes, extension
  system, OAuth flows, HTML export, terminal images, pi packages, project trust. Exclusions are only
  those in the divergence ledger below. Sequencing exists (see plan phases); feature cuts do not.
- **D3 — Audience.** Ordalie production embedding + personal daily-driver + public OSS, simultaneously.
- **D4 — File-format compatibility.** pi-go reads/writes pi's data formats and locations so both
  agents coexist on one machine: `~/.pi/agent/` layout, session JSONL **v3** tree format (with v1/v2
  migration), `settings.json` (global + `.pi/settings.json` project merge), `models.json`,
  `auth.json` (0600), `trust.json`, `keybindings.json`. CLI-flag parity is pursued but not contractual.

## Upstream relationship

- **D5 — Pin + agent-driven sync.** Port from the pinned commit. Afterward, coding agents run a
  manual-first `sync` workflow (fetch upstream delta → regenerate conformance fixtures → run suite →
  emit report + work items). Promote to scheduled automation only once conformance is stably green.
  Formats/behaviors we promised compat on are tracked; features diverge freely.
- **D6 — Upstream tests must run against the port.** Strategy: **fixtures + black-box**.
  Language-neutral golden fixtures are generated from the upstream repo by extraction scripts and
  consumed by both vitest (upstream side) and `go test` (our side). Upstream's RPC/CLI-level tests
  additionally run as-is against the pi-go binary. Node/TS is permitted as *development tooling*
  (fixture extraction); the shipped product is pure Go.

## Architecture decisions

- **D7 — Strict pure Go.** `CGO_ENABLED=0` everywhere, forever. Single static binary. Anything
  requiring CGo is disqualified by construction.
- **D8 — Platforms.** linux + darwin, amd64 + arm64, from day one. Windows is a later parity wave
  (upstream supports it; we port its git-bash/console strategy then). Not dropped — deferred.
- **D9 — Single module, mirrored layout.** One `go.mod`. Packages mirror upstream packages
  (`ai/`, `agent/`, `tui/`, `codingagent/`, plus `cmd/pi`, `internal/`); files track upstream files
  where idiomatic (`agent-loop.ts` → `loop.go`). Mirroring is what makes agent-driven upstream
  syncing and diff-mapping mechanical. A `MIRROR.md` map records the correspondence.
- **D10 — Provider layer: SDK-preferring hybrid.** Use official Go SDKs where they exist and are
  sound (`openai-go/v3`, `anthropic-sdk-go`, `aws-sdk-go-v2` bedrockruntime). G2 rejected
  `google.golang.org/genai` on measured weight, so Gemini and Vertex use hand-rolled JSON/SSE shapes.
  Hand-roll where no sound SDK exists (mistral-conversations, pi-messages wire shape, OAuth
  device/PKCE flows). Do not import kitchen sinks.
- **D11 — Provider order: OpenAI first.** openai-responses + openai-completions shapes first (this
  also unlocks Azure and the compat family — Groq, Cerebras, xAI, OpenRouter, DeepSeek, Fireworks,
  Together, etc. via baseURL + compat flags). Then Anthropic (+ prompt caching + Claude Pro/Max OAuth),
  then Gemini, Mistral, Bedrock/Vertex, Codex/Copilot, remainder of the ~34-provider catalog.
- **D12 — Model catalog: models.dev direct.** Build-time generation from `models.dev/api.json` into
  an embedded catalog + runtime refresh from models.dev into `~/.pi` cache. No dependence on pi.dev
  endpoints. `models.json` user overrides behave exactly as upstream (`docs/models.md`).
- **D13 — SDK style: mirror + Go idioms.** Same conceptual API and event taxonomy as upstream
  (`Agent`, `prompt/steer/followUp/abort/waitForIdle/subscribe/reset`; `AgentEvent` union as typed
  structs with upstream names) with Go-native mechanics: `context.Context`, error returns, functional
  options, and a channel/iterator adapter over subscribe. Event-shape parity is load-bearing for
  conformance trace comparison — do not "improve" event names or payloads.
- **D14 — Tool schemas.** JSON Schema is a first-class value on tools (raw schema type) — required
  anyway for extension/MCP-registered tools — plus a reflection helper deriving schemas from Go
  structs for ergonomic typed tools. In the JS bridge, typebox runs in-engine and hands us JSON Schema.
- **D15 — TUI: faithful pi-tui port.** Hand-rolled differential line renderer mirroring pi-tui
  (Component contract: `Render(width) []string`), no TUI framework. The Component contract is what
  extension custom-UI rides on; preserving it is non-negotiable.

## Extensibility decisions

- **D16 — Go-native extension API is the foundation.** The full ExtensionAPI surface (hooks,
  registrations, ctx.ui, session access — upstream `docs/extensions.md`) exists as Go interfaces
  first; internal features (built-in tools, MCP, slash commands) wire through it.
- **D17 — JS bridge: API-complete subset on sobek + esbuild.** TS extensions execute via
  grafana/sobek with embedded esbuild transpiling/bundling (k6's proven architecture). Fidelity
  target: full ExtensionAPI + typebox (in-engine) + pi-tui Component bridge + hand-built shims for
  common node builtins (`fs`, `path`, `os`, `process`, `url`, `util`; `child_process` routed through
  the exec bridge; `fetch` via Go http) + pure-JS npm deps via esbuild bundling. Native addons and
  exotic Node APIs are out of scope; the example-extension compatibility matrix documents reality.
- **D18 — MCP: bundled first-party Go extension.** Built on `modelcontextprotocol/go-sdk`, compiled
  into the binary, enabled via settings. The core stays faithful to pi's no-MCP philosophy; this was
  our first philosophical addition (the second is the chat gateway, D27), and it doubles as the
  proof the Go extension API is real.

## Divergence ledger

| Divergence | Kind | Rationale |
|---|---|---|
| Bundled MCP extension | addition | owner requirement; kept out of core |
| `packages/orchestrator` | removed | experimental upstream side product |
| Telemetry/analytics (`enableInstallTelemetry`, `enableAnalytics`, `trackingId`) | removed | owner decision; unknown settings keys tolerated on parse, nothing sent, no plumbing |
| Radius provider + Radius OAuth | removed | pi.dev-coupled service; the generic `pi-messages` SSE wire shape IS ported (usable by any backend, e.g. an Ordalie gateway) |
| Version/update checks | neutralized | point at OrdalieTech/pi-go GitHub releases, never pi.dev |
| `/share` | neutralized | local HTML export instead of pi.dev upload |
| Model catalog runtime refresh | neutralized | models.dev directly, not pi.dev overlay endpoints |
| Windows support | deferred | later parity wave (D8) |
| darwin modifier-key native addon | gap | kitty keyboard protocol where possible; documented small parity gap |
| win32 console native addon | deferred | Windows wave |
| Bundled llama.cpp extension | excluded | shipped at the pinned commit but deleted upstream immediately after; porting would be dead-on-arrival work (2026-07-19 alignment audit; owner may amend) |
| `chat/` gateway package (+ `chat/telegram`, `chat/whatsapp`) | addition | owner requirement (D27); kept out of core, strictly one-way dependency on the SDK |
| `AgentSessionOptions` tool-operations injection hook | addition | D27; ergonomic seam over the existing `NewSessionRuntime`/`BaseTools` path for VFS/sandboxed tool operations |
| `chat/` platform wave 2 (`slack`, `teams`, `discord`, `messenger`, `googlechat` + `chat/internal/` ws/webhook helpers) | addition | owner requirement (D28); official APIs only, stdlib-only clients incl. hand-rolled RFC 6455 |

## Execution decisions

- **D19 — Implemented by coding agents** (Claude Code or Codex). Work packages are therefore
  tool-agnostic, self-contained, sized for one agent session, with explicit acceptance checks.
  `AGENTS.md` at repo root is the execution contract.
- **D20 — Planning artifact.** This decision record + `ARCHITECTURE.md` + phased work packages under
  `docs/plan/`.
- **D21 — Sequencing: walking skeleton.** Thin end-to-end slice first (OpenAI + agent loop +
  read/bash/edit/write + print mode = a usable agent early, which then dogfoods its own development),
  then widen. Every package lands together with its conformance fixtures.
- **D22 — No hard deadline.** Quality and conformance gates govern pace.
- **D23 — Milestones + trim passes.** Success criteria are consolidated in
  `docs/RELEASE-CRITERIA.md` (M1–M5); agents work until every criterion checks. Each milestone ends
  with a mandatory trim pass (WP-180/390/470/560/650): dead code, dep audit, abstraction inlining,
  LOC-vs-upstream report — behavior-neutral, fixtures stay green. Slimness is a product goal.
- **D24 — Live-test policy.** Three tiers (RELEASE-CRITERIA): merges are fixture-only/no-network;
  provider WPs run opt-in live smoke; a nightly capped live suite (OpenAI + Anthropic, 3-task
  corpus) runs from M2 — failures file work items, and only the M5 7-day window blocks a release.

- **D25 — Sprint restructure (owner, 2026-07-18).** The WP system is retired as sequencing;
  `docs/plan/SPRINTS.md` is the active plan: four large sprints (Sprint 1 = M2 … Sprint 4 = M5),
  each opened fixtures-FIRST (red before port) and closed with a TS-pi comparison report
  (`docs/compare/sprint-N.md`), the trim checklist, and milestone verification. Trunk-based:
  single branch `main`, no GitButler lanes/worktrees/feature branches; commits are large coherent
  green chunks; every mainline commit builds and passes. Phase files demoted to spec sheets.
  M5 live burn-in shortened 7 days → 72 hours (owner-directed). Ambition: a working session aims
  to close a sprint.
- **D26 — Core first, expansion studied (owner, 2026-07-18).** Compatibility breadth is not pursued
  until the core is byte-right with all tests green vs TS pi. Core = engine + tools + sessions +
  modes + skills/templates + extension seams + SDK + TUI on the ALREADY-LANDED providers (openai,
  anthropic, google/vertex, mistral, azure, bedrock, pi-messages) with Anthropic OAuth. Expansion
  ring = codex shape + ChatGPT/Codex/Copilot/xAI OAuth, the compat provider family, MCP,
  pi-packages, and the JS extension bridge — all Sprint 3, which OPENS with an owner-reviewed
  expansion study (`docs/plan/expansion-study.md`); full parity remains the default v1.0 target
  unless the study amends this record. Work already landed for expansion surfaces is kept, not
  extended. No schedule estimates in plans, reports, or trackers — progress is stated as
  red-to-green movement only.

- **D27 — Chat gateway package (owner, 2026-07-19).** A top-level `chat/` package (with
  `chat/telegram/` and `chat/whatsapp/`) turns the SDK into a multi-user messaging agent: an
  at-least-once processor around `AgentSession` with normalized messages, a `SessionProvider`
  lease/hydration seam, and platform adapters. Dependency direction is strictly
  `chat → codingagent`; `codingagent` never imports `chat`. Both adapters are committed in the
  same arc, built in sequence: processor first (faux provider, in-memory sessions), then Telegram
  (webhook + long-poll, streamed preview edits), then WhatsApp Business Cloud API (typing + one
  final answer). Delivery state is recorded as `type:"custom"` session entries via
  `AppendCustomEntry` (a `pigo.chat.turn` started/settled/delivered ledger), keeping the session
  JSONL the single durable history; turn finalization keys off `AgentSettledEvent`, not
  `agent_end`; crash recovery reads raw session entries, never the built context. Tools are
  disabled by default — a deployment enabling them must inject an isolated workspace through its
  `SessionProvider`. The local JSONL provider is single-process; cluster deployments must supply
  partitioned or fenced conversation ownership (per-write flock cannot coordinate writers).
  Stdlib-only: both platform clients are hand-rolled HTTP/JSON per D10. Chat tests are plain
  `go test` goldens under `chat/` — never `conformance/`, whose F-families are
  upstream-extraction-only by contract. Includes one small SDK divergence, landed with this work:
  a tool-operations injection hook on `AgentSessionOptions` (previously reachable only via
  `NewSessionRuntime`/`BaseTools`). Second product-layer addition after the bundled MCP extension
  (D18's "one philosophical addition" phrasing is retired).

- **D28 — Chat platform wave 2 (owner, 2026-07-19).** Five adapters join the gateway: Slack
  (Events API, streamed previews via chat.update), Microsoft Teams (Bot Framework, final-only),
  Discord (Gateway over a hand-rolled RFC 6455 websocket client — zero new dependencies, the
  G1/G2 tradition), Facebook Messenger (Graph, shares the WhatsApp webhook idiom), and Google
  Chat (service-account JWT). Shared webhook-signature and websocket helpers are extracted to
  `chat/internal/` now that the third adapter triggers the extraction rule. Bridge-based
  platforms (Signal, iMessage, personal WhatsApp) and E2EE Matrix remain excluded per D27's
  official-API stance. Later waves ride the same seams and transport: Instagram DM, Line,
  Twilio SMS/RCS, Mattermost, Rocket.Chat, Zulip, IRC; KakaoTalk/WeChat noted as
  access-restricted. Zero new go.mod dependencies remains the rule for every wave.

## Standing assumptions (owner-confirmed)

- Independent semver from `v0.1.0`; upstream snapshot recorded in `UPSTREAM.lock`.
- OAuth flows land with their provider's wave (ChatGPT/Codex OAuth with OpenAI wave, Claude Pro/Max
  with Anthropic wave, Copilot device-code later).
- The example-extension compatibility matrix (~69 upstream examples) is a standing conformance artifact.
- `rg`/`fd` auto-download into `~/.pi/agent/bin` ported as-is (system binaries preferred). This is
  upstream behavior, not a single-binary violation.
- Clipboard via OSC52 / shell-out (`pbcopy`/`xclip`/`wl-copy`), no native addon.
- Go ≥ 1.25 baseline (sobek requirement); repo developed on Go 1.26.
- Node ≥ 22 is a *development-tooling* dependency only (fixture extraction against the upstream clone).

## Deferred decision gates (resolved inside the named work package)

- **G1 (WP-110, resolved):** use the stdlib-only internal JSON-Schema reflector. The invopop probe
  required provider-shape post-processing and added five transitive packages plus 640 KiB to a
  stripped binary; the internal helper emits the required TypeBox-style inline schemas directly.
- **G2 (WP-221, resolved):** use the stdlib REST/SSE Gemini adapter. The correctly stripped official
  SDK probe added 8,466,432 bytes (47.278%), 35 module entries, and 183 compiled packages; Vertex
  is completed by WP-222 with stdlib REST/SSE and pure-Go ADC, adding 393,216 bytes (2.177%) and no
  module or compiled-package entry against its consolidated parent. See `docs/plan/wp-221-g2-report.md`
  and `docs/plan/wp-222-vertex-report.md`.
- **G3 (WP-542):** pi-tui Component bridge overlay/experimental surfaces — bridge now vs documented gap.
  **Resolved (Sprint 3): bridge now.** `ctx.ui.custom` with overlay options (static and dynamic),
  `OverlayHandle` round-trips, focusable JS components, and editor replacement including the
  `CustomEditor` base class (JS class over the mode-registered real editor seam) are bridged; the
  modal-editor example runs unmodified. Remaining pi-tui component classes (Text/Container/Markdown
  construction from JS) are bridged on demand as the F11 matrix requires them.
- **G4 (WP-661):** self-update mechanism — notify-only vs in-place binary self-update.
  **Resolved (Sprint 4): notify-only.** The update check (already pointed at OrdalieTech/pi-go
  releases per the divergence ledger) surfaces new versions; installation goes through the install
  script or package manager. In-place binary self-replacement is a security and failure-mode
  liability a slim port does not need.

## Sprint 5 — ecosystem-compat sweep decisions

The July 2026 real-world compat sweep (six dimensions, real npm MCP servers, published pi
packages, upstream example extensions) fixed 41 findings. Decisions made while fixing, so they
are not re-litigated:

- **Skills ignore semantics are upstream-bug-for-bug.** `prefixIgnorePattern` semantics ported
  exactly: nested ignore-file patterns anchor to the ignore file's own directory, and a leading
  `/` is stripped (so root-level `/pattern` matches basenames at any depth). Correct gitignore
  behavior loses to parity with the pinned upstream.
- **Skills symlink-cycle guard stays.** Upstream has no visited-set and recurses cycles to
  ELOOP (~40 levels), leaking cycle-expanded paths into system-prompt `<location>` entries.
  pi-go keeps the canonical-path visit stack and returns each skill once under its clean path.
  Deliberate hardening divergence.
- **MCP `"disabled": true` is honored** as `"enabled": false` (portability with Cline/Roo/
  Claude Desktop configs). MCP config parsing is per-entry tolerant: invalid entries warn and
  are skipped, valid entries load.
- **Package dependency installs are Node-optional.** The package tarball is still fetched
  natively; `npmCommand` (default `npm install --omit=dev`) runs only when `package.json`
  declares dependencies that are not bundled, and a missing npm binary degrades to a warning.
  Supported `.npmrc` surface is deliberately minimal: `registry=` and nerf-darted `_authToken`
  (no `${VAR}` expansion, no per-scope registries, no `_auth`/username/password).
- **pi-* shim modules throw on unknown imports at first touch** ("'X' is not exported by ...
  (pi-go shim)") with an honest `has()` so `in`-feature-detection still works. True Node-ESM
  link-time failure is unreachable without a build-time export manifest; first-touch is the slim
  faithful approximation (question.ts-style examples now fail loudly at load instead of
  registering broken tools).
- **jsbridge runtime ceilings**: native `.node` addons and WebAssembly are unsupported by
  design (sobek); both fail with explicit one-line diagnostics. `node:net` raw sockets,
  `node:vm`, and `node:worker_threads` are not shimmed. `node:vm` is a rabbit hole with no slim
  faithful mapping onto sobek, and `worker_threads` (real threads sharing a JS heap) is
  fundamentally incompatible with sobek's single-threaded model. Consequences: the upstream
  `sandbox` example stays unsupported (needs `node:net` plus the unexported `createBashTool`
  factory surface), and the real npm package `pi-subagentura` stays unsupported (its
  `workflow-script`/`workflow-worker-thread` modules import `node:vm` and worker threads). The
  original sweep finding — `node:crypto`/`node:http`/`node:module`/`atob`/`btoa` — is fixed and
  verified; these three modules are a separate, deliberately-declined ceiling, each failing with
  a clear `unsupported external module "node:X"` diagnostic.
- **pi-* shim unknown-import failure is access-time, not link-time.** Node ESM would fail an
  unknown named import at link time; over esbuild-CJS bundling that requires a build-time export
  manifest, which pi-go does not maintain. The shim instead throws on first *access* of an
  unexported name (with an honest `has()`), so `question.ts`/`questionnaire.ts`-style examples
  that touch the missing pi-tui `Editor`/`Key` surface only inside a TUI-only custom-UI factory
  still load silently in print mode (where that factory never runs, and the registered tool
  behaves upstream-identically) and throw clearly the moment the factory runs in interactive
  mode. Forcing load-time failure is not worth a build-time export manifest.
- **Package git subprocesses are quiet** (`clone -q`, `checkout -q` with
  `advice.detachedHead=false`, `fetch -q`) — a cosmetic deviation from upstream, which inherits
  git's stderr chatter.
