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
  into the binary, enabled via settings. The core stays faithful to pi's no-MCP philosophy; this is
  our one philosophical addition, and it doubles as the proof the Go extension API is real.

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
- **G4 (WP-661):** self-update mechanism — notify-only vs in-place binary self-update.
