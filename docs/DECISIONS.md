# pigo — Decision Record

Outcome of the planning interview (2026-07-17). Every architectural and product decision below is
settled and confirmed by the project owner. Changes to this record require owner sign-off; everything
else (implementation detail) is decided by whoever executes the work package, within these bounds.

## Provenance

| | |
|---|---|
| Upstream project | **pi** — https://pi.dev, repo `earendil-works/pi` (formerly `badlogic/pi-mono`) |
| Pinned reference | commit `20be4b18d4c57487f8993d2762bace129f0cf7c6`, version **0.81.1** (2026-07-21) |
| Upstream license | MIT, © 2025 Mario Zechner |
| This project | `github.com/OrdalieTech/pigo`, MIT, © Ordalie — with attribution to upstream in LICENSE and README |

pigo is a faithful Go port of pi, not a reimagining. Upstream's docs at the pinned commit
(`docs/*.md` in each package) are the specification; where this record is silent, upstream behavior wins.

## Product decisions

- **D1 — SDK-first.** pigo is a Go module first; the `pigo` CLI is one consumer of it. The `ai`
  layer must be importable on its own (as `@earendil-works/pi-ai` is upstream).
- **D2 — Full parity, no staged v1.** The whole of pi v0.81.1 is in scope: agent core, all tools,
  session tree + compaction, skills, prompt templates, themes, TUI, print/JSON/RPC modes, extension
  system, OAuth flows, HTML export, terminal images, pi packages, project trust. Exclusions are only
  those in the divergence ledger below. Sequencing exists (see plan phases); feature cuts do not.
- **D3 — Audience.** Ordalie production embedding + personal daily-driver + public OSS, simultaneously.
- **D4 — File-format compatibility.** pigo reads/writes pi's data formats and locations so both
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
  additionally run as-is against the pigo binary. Node/TS is permitted as *development tooling*
  (fixture extraction); the shipped product is pure Go.

## Architecture decisions

- **D7 — Strict pure-Go product (owner-amended 2026-07-20).** Every product and release build uses
  `CGO_ENABLED=0` and remains a single static binary; dependencies requiring CGo are disqualified.
  Development-only test binaries may enable CGo when the Go toolchain requires it for `-race`
  (ThreadSanitizer). That exception never ships. D31's optional user-provided Node/Bun process is
  not part of the shipped artifact and does not change the static pigo build.
- **D8 — Platforms.** linux + darwin, amd64 + arm64, from day one. Windows is a later parity wave
  (upstream supports it; we port its git-bash/console strategy then). Not dropped — deferred.
- **D9 — Single module, mirrored layout.** One `go.mod`. Packages mirror upstream packages
  (`ai/`, `agent/`, `tui/`, `codingagent/`, plus `cmd/pigo`, `internal/`); files track upstream files
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
- **D12 — Model catalog: direct authoritative sources.** Build-time generation uses
  `models.dev/api.json` for the baseline, intersects NVIDIA's manifest with the live NIM listing,
  and uses the live OpenRouter and Vercel AI Gateway APIs for those two catalogs. Runtime refresh
  remains a direct models.dev fetch into the `~/.pi` cache, never a pi.dev endpoint. `models.json`
  user overrides behave exactly as upstream (`docs/models.md`).
- **D13 — SDK style: mirror + Go idioms.** Same conceptual API and event taxonomy as upstream
  (`Agent`, `prompt/steer/followUp/abort/waitForIdle/subscribe/reset`; `AgentEvent` union as typed
  structs with upstream names) with Go-native mechanics: `context.Context`, error returns, functional
  options, and a channel/iterator adapter over subscribe. Event-shape parity is load-bearing for
  conformance trace comparison — do not "improve" event names or payloads.
- **D14 — Tool schemas.** JSON Schema is a first-class value on tools (raw schema type) — required
  anyway for extension/MCP-registered tools — plus a reflection helper deriving schemas from Go
  structs for ergonomic typed tools. JavaScript schema objects cross the extension-host protocol as JSON Schema.
- **D15 — TUI: faithful pi-tui port.** Hand-rolled differential line renderer mirroring pi-tui
  (Component contract: `Render(width) []string`), no TUI framework. The Component contract is what
  extension custom-UI rides on; preserving it is non-negotiable.
- **Interactive mode owns its viewport.** Pigo uses the alternate screen with a scrollable
  transcript and pins status, extension widgets, editor, and footer at the bottom. Mouse-wheel or
  `Ctrl+PageUp` scrolling detaches live follow; scrolling back down or `Ctrl+End` reattaches it, so
  loading and streaming frames cannot move the viewed history. The status spacer is collapsed and
  the right edge has a one-column proportional thumb with click-to-jump. Left-drag highlights the
  visible range, holds it stable during streaming, and copies it on release. The reusable TUI stays
  inline unless a caller opts into this viewport, and mode 1010 remains disabled while either renderer is live.
- **Huge transcripts use windowed layout.** Interactive chat caches per-child lines and renders only
  the visible range; steady frames are O(viewport + changed tail), while first render, resize, theme
  changes, and global expansion intentionally remain O(history).
- **Reachable clear-on-shrink updates stay differential.** When shorter content can be reconciled
  inside the renderer's active viewport, Pigo clears only the vacated rows and settles the tracked
  height instead of taking upstream's destructive full-transcript redraw. The inline renderer keeps
  the upstream fallback for true offscreen mutations; interactive mode avoids it by rendering only
  its owned viewport.

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
  **Superseded by D31 (2026-07-22): the embedded engine and shim ceiling were deleted.**
- **D18 — MCP: bundled first-party Go extension.** Built on `modelcontextprotocol/go-sdk`, compiled
  into the binary, enabled via settings. The core stays faithful to pi's no-MCP philosophy; this was
  our first philosophical addition (the second is the chat gateway, D27), and it doubles as the
  proof the Go extension API is real.

## Divergence ledger

| Divergence | Kind | Rationale |
|---|---|---|
| Bundled MCP extension | addition | owner requirement; kept out of core |
| `packages/server` (formerly `packages/orchestrator`) | removed | experimental upstream side product; the v0.81.0 rename does not change the D2 product boundary |
| Telemetry/analytics (`enableInstallTelemetry`, `enableAnalytics`, `trackingId`) | removed | owner decision; unknown settings keys tolerated on parse, nothing sent, no plumbing |
| Radius provider + Radius OAuth | removed | pi.dev-coupled service; the generic `pi-messages` SSE wire shape IS ported (usable by any backend, e.g. an Ordalie gateway) |
| Version/update checks | neutralized | point at OrdalieTech/pigo GitHub releases, never pi.dev |
| Public identity and executable | renamed | D30; `pigo` avoids colliding with an installed upstream `pi` |
| `/share` | neutralized | local HTML export instead of pi.dev upload |
| Model catalog runtime refresh | neutralized | models.dev directly, not pi.dev overlay endpoints |
| Windows support | deferred | later parity wave (D8) |
| darwin modifier-key native addon | gap | kitty keyboard protocol where possible; documented small parity gap |
| win32 console native addon | deferred | Windows wave |
| Bundled llama.cpp extension | excluded | v0.81.1 still ships this optional native Node/llama.cpp integration; it cannot satisfy the pure-Go, single-static-binary rule in D7 |
| `packages/storage/sqlite-node` | excluded | v0.81.1's optional Node SQLite storage package requires a native runtime; pigo retains the session repository interfaces and JSONL/memory implementations under D7 |
| `pigo login` / `pigo logout` CLI subcommands | addition | headless Go deployments need auth lifecycle commands; bare `pigo logout` deliberately lists stored credential names and requires an explicit provider instead of silently choosing one |
| NVIDIA `qwen/qwen3.5-122b-a10b` denylist | addition | the live NIM endpoint advertises it, but its current metadata cannot satisfy pigo's chat-model contract; keep the Go-only exclusion explicit until the live shape is usable |
| Missing default stream error timing | Go API adaptation | upstream throws in the JavaScript `Agent` constructor; Go's fixed `NewAgent` signature cannot return an error, so pigo reports the identical error on the first prompt or low-level loop call |
| Moonshot Kimi K3 compat metadata | resolved parity | `thinkingFormat: openai` and reasoning-effort support entered the pinned upstream in v0.81.1 and remain regression-tested |
| `AgentHarness` orchestration facade | dissolved | D29; harness primitives remain in `agent/harness`, while the high-level embedding lifecycle stays in `codingagent.AgentSession` |
| `streamProxy` `/api/stream` client | excluded | D29; application-specific proxy protocols use `agent.WithStreamFn` and the public streaming-JSON helper |
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

- **D29 — One high-level agent runtime (agent, 2026-07-20).** The pinned upstream exports a
  second `AgentHarness` orchestrator from `packages/agent`, but its own coding-agent still uses
  `AgentSession`; upstream documents that migration as pi 2.0 work. pigo keeps the already-ported
  session, repository, compaction, resource, and environment primitives in `agent/harness`, while
  `codingagent.AgentSession` remains the sole high-level embedding runtime. Reimplementing the
  1,029-line facade would duplicate queues, hooks, persistence ordering, and lifecycle state, and
  placing a wrapper in `agent` would invert the package dependency. The adjacent `streamProxy`
  client is also excluded: its `/api/stream` endpoint is an application protocol rather than agent
  behavior, and embedders already have `agent.WithStreamFn` plus `ai.ParseStreamingJSON`. Revisit
  either surface only when upstream's coding-agent adopts it or a real Go consumer requires it.

- **D30 — Public identity is pigo (owner, 2026-07-21).** The repository, Go module, executable,
  release artifacts, installer variables, terminal title, resume hints, and default RPC client
  command use `pigo`; no legacy `pi` executable or alias is shipped, so upstream pi can coexist on
  the same machine. Upstream compatibility names remain unchanged where they are the contract:
  `.pi`/`~/.pi`, upstream `PI_*` runtime variables, session and wire formats, pi package manifests,
  `pi-messages`, the JS extension `pi` API and `@earendil-works/pi-*` imports, embedded upstream
  assets, and extracted goldens. Conformance adapters may account only for exact public-name
  substitutions while separately asserting the `pigo` spelling.

- **D31 — Host-only JavaScript execution (owner, 2026-07-22).** All JavaScript and TypeScript
  extensions, including installed npm packages, project/global extension files, and explicit `-e`
  entries, run out of process in the extension host. Pigo selects local Node.js ≥22.6 (native type
  stripping) or Bun, with no embedded JavaScript engine, transpiler, Node shims, or runtime feature
  flag. When neither runtime is available, extension loading emits exactly `JS extensions require
  Node.js ≥22.6 or Bun; skills, prompt templates, MCP servers and built-in tools work without it`
  and the rest of the product remains available. The host owns real Node/Bun module, worker,
  top-level-await, WebAssembly, and native-addon semantics; pigo remains a static `CGO_ENABLED=0`
  binary and ships neither runtime.

- **D32 — First-party plugins: bundled-but-dormant (owner, 2026-07-22).** Tasks, websearch, and
  subagents ship in the binary but default off, preserving the upstream tool surface until a user
  opts in through the `plugins` settings object, `pigo plugins`, or the `/plugins` selector. The
  existing user/project settings overlay and runtime reload path own enablement; embedders bypass
  settings by selecting factories from `plugins.Catalog()`.

- **D33 — Permissions plugin (owner, 2026-07-22).** The dormant first-party permissions plugin uses the standard allow/deny/ask, ordered last-match-wins model and defaults to permissive log mode.

## 2026-07-21 parity-sync amendments

- Codex request compression uses `github.com/klauspost/compress/zstd` as a direct dependency. The
  upstream wire requires zstd request bodies, and the standard library has no zstd encoder.
- The v0.81.1 image catalog is checked in as deterministic Go data and pinned by an exact digest.
  Upstream's strict TypeScript model-data validator has no runtime Go analogue, so generation-time
  validation plus full-catalog tests enforce the same accepted shape.
- Remote-catalog freshness preserves upstream's `checkedAt`/`lastModified` semantics, while D12's
  single direct models.dev endpoint replaces pi.dev's provider-scoped service. The pigo identity in
  its User-Agent remains the D30 public-name substitution.

## 2026-07-22 v0.81.1 sync amendments

- `ai.RetryAssistantCall` is the shared retry policy for normal turns, compaction, and branch
  summaries. Coding-agent retry lifecycle events retain upstream names and payloads across the Go
  SDK, JSON, RPC, and interactive surfaces.
- The coding-agent package installs the default stream function during initialization, matching
  upstream extension compatibility. A missing fallback produces upstream's exact error text when
  execution begins; constructor-time error timing is the Go API adaptation ledgered above.
- Release source provenance maps upstream's source-archive feature onto GoReleaser. Every source
  archive is checksummed, excludes checkout/build state, and must rebuild with `CGO_ENABLED=0`
  and `-buildvcs=false` before the release is published; source archives intentionally omit the Git
  metadata Go would otherwise inspect for VCS stamping.

## Standing assumptions (owner-confirmed)

- Independent semver from `v0.1.0`; upstream snapshot recorded in `UPSTREAM.lock`.
- OAuth flows land with their provider's wave (ChatGPT/Codex OAuth with OpenAI wave, Claude Pro/Max
  with Anthropic wave, Copilot device-code later).
- The example-extension compatibility matrix (~69 upstream examples) is a standing conformance artifact.
- `rg`/`fd` auto-download into `~/.pi/agent/bin` ported as-is (system binaries preferred). This is
  upstream behavior, not a single-binary violation.
- Clipboard via OSC52 / shell-out (`pbcopy`/`xclip`/`wl-copy`), no native addon.
- Go ≥ 1.25 baseline; releases and CI pin Go 1.26.5.
- Node.js ≥22.6 or Bun is an optional runtime dependency for JavaScript/TypeScript extensions and
  Node remains development tooling for fixture extraction against the upstream clone.

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
  **Resolved (Sprint 4): notify-only.** The update check (already pointed at OrdalieTech/pigo
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
  pigo keeps the canonical-path visit stack and returns each skill once under its clean path.
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
  (pigo shim)") with an honest `has()` so `in`-feature-detection still works. True Node-ESM
  link-time failure is unreachable without a build-time export manifest; first-touch is the slim
  faithful approximation (question.ts-style examples now fail loudly at load instead of
  registering broken tools).
- **jsbridge runtime ceilings (superseded by D31 on 2026-07-22).** Native `.node` addons and WebAssembly are unsupported by
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
- **pi-* shim unknown-import failure is access-time, not link-time (superseded by D31 on 2026-07-22).** Node ESM would fail an
  unknown named import at link time; over esbuild-CJS bundling that requires a build-time export
  manifest, which pigo does not maintain. The shim instead throws on first *access* of an
  unexported name (with an honest `has()`), so `question.ts`/`questionnaire.ts`-style examples
  that touch the missing pi-tui `Editor`/`Key` surface only inside a TUI-only custom-UI factory
  still load silently in print mode (where that factory never runs, and the registered tool
  behaves upstream-identically) and throw clearly the moment the factory runs in interactive
  mode. Forcing load-time failure is not worth a build-time export manifest.
- **Package git subprocesses are quiet** (`clone -q`, `checkout -q` with
  `advice.detachedHead=false`, `fetch -q`) — a cosmetic deviation from upstream, which inherits
  git's stderr chatter.
- **Installed abbreviated Git commit pins resolve locally before fetch.** Git servers reject a
  short object ID such as `f2433d1` as an unadvertised remote ref even when the normal clone
  already contains that reachable commit. Pigo reconciles that detached object locally; branches,
  tags, missing commits, and fresh installs retain upstream's fetch/checkout behavior. This is a
  narrow usability divergence from upstream's failing `git fetch origin <short-sha>` path.
- **Ecosystem compatibility claims stay layered.** A locked 44-package corpus separately records
  stable loading, observable registration parity, line-grounded workflow feasibility, and executed
  offline command/workflow probes. A package that only loads is never labeled end-to-end compatible.
  The embedded CommonJS build preserves `import.meta.{url,filename,dirname}` per source module, but
  variable dynamic imports, top-level-await-only modules, real Node streams/sockets, and native
  addons remain explicit ceilings until their semantics can be implemented faithfully.
