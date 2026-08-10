# Orb — Decision Record

Outcome of the planning interview (2026-07-17). Every architectural and product decision below is
settled and confirmed by the project owner. Changes to this record require owner sign-off; everything
else (implementation detail) is decided by whoever executes the work package, within these bounds.

## Provenance

| | |
|---|---|
| Upstream project | **pi** — https://pi.dev, repo `earendil-works/pi` (formerly `badlogic/pi-mono`) |
| Pinned reference | commit `53fa77ccd8a279eb87e92294ef3687b03ff80112`, version **0.84.1** (2026-08-07) |
| Upstream license | MIT, © 2025 Mario Zechner |
| This project | `github.com/OrdalieTech/orb`, MIT, © Ordalie — with attribution to upstream in LICENSE and README |

Orb is a faithful Go port of pi, not a reimagining. Upstream's docs at the pinned commit
(`docs/*.md` in each package) are the specification; where this record is silent, upstream behavior wins.

## Product decisions

- **D1 — SDK-first.** orb is a Go module first; the `orb` CLI is one consumer of it. The `ai`
  layer must be importable on its own (as `@earendil-works/pi-ai` is upstream).
- **D2 — Full parity, no staged v1.** The whole of the pinned pi release (see `UPSTREAM.lock`; amended by D35 for the TUI surface) is in scope: agent core, all tools,
  session tree + compaction, skills, prompt templates, themes, TUI, print/JSON/RPC modes, extension
  system, OAuth flows, HTML export, terminal images, pi packages, project trust. Exclusions are only
  those in the divergence ledger below. Sequencing exists (see plan phases); feature cuts do not.
- **D3 — Audience.** Ordalie production embedding + personal daily-driver + public OSS, simultaneously.
- **D4 — File-format compatibility.** orb reads/writes pi's data formats and locations so both
  agents coexist on one machine: `~/.pi/agent/` layout, session JSONL **v4** tree format (with
  v1/v2/v3 migration), `settings.json` (global + `.pi/settings.json` project merge), `models.json`,
  `auth.json` (0600), `trust.json`, `keybindings.json`. CLI-flag parity is pursued but not contractual.

## Upstream relationship

- **D5 — Pin + agent-driven sync.** Port from the pinned commit. Afterward, coding agents run a
  manual-first `sync` workflow (fetch upstream delta → regenerate conformance fixtures → run suite →
  emit report + work items). Promote to scheduled automation only once conformance is stably green.
  Formats/behaviors we promised compat on are tracked; features diverge freely.
- **D6 — Upstream tests must run against the port.** Strategy: **fixtures + black-box**.
  Language-neutral golden fixtures are generated from the upstream repo by extraction scripts and
  consumed by both vitest (upstream side) and `go test` (our side). Upstream's RPC/CLI-level tests
  additionally run as-is against the orb binary. Node/TS is permitted as *development tooling*
  (fixture extraction); the shipped product is pure Go.

## Architecture decisions

- **D7 — Strict pure-Go product (owner-amended 2026-07-20).** Every product and release build uses
  `CGO_ENABLED=0` and remains a single static binary; dependencies requiring CGo are disqualified.
  Development-only test binaries may enable CGo when the Go toolchain requires it for `-race`
  (ThreadSanitizer). That exception never ships. D31's optional user-provided Node/Bun process is
  not part of the shipped artifact and does not change the static orb build.
- **D8 — Platforms.** linux + darwin, amd64 + arm64, from day one. Windows is a later parity wave
  (upstream supports it; we port its git-bash/console strategy then). Not dropped — deferred.
- **D9 — Single module, mirrored layout.** One `go.mod`. Packages mirror upstream packages
  (`ai/`, `agent/`, `tui/`, `codingagent/`, plus `cmd/orb`, `internal/`); files track upstream files
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
- **Interactive mode owns its viewport.** Orb uses the alternate screen with a scrollable
  transcript and pins status, extension widgets, editor, and footer at the bottom. Mouse-wheel or
  `Ctrl+PageUp` scrolling detaches live follow; scrolling back down or `Ctrl+End` reattaches it, so
  loading and streaming frames cannot move the viewed history. Sending a message reattaches it too:
  submitting is an explicit request to watch what happens next, unlike an arriving frame. The status spacer is collapsed and
  the right edge has a one-column proportional thumb with click-to-jump. Left-drag highlights the
  visible range, holds it stable during streaming, and copies it on release. The reusable TUI stays
  inline unless a caller opts into this viewport, and mode 1010 remains disabled while either renderer is live.
- **Huge transcripts use windowed layout.** Interactive chat caches per-child lines and renders only
  the visible range; steady frames are O(viewport + changed tail), while first render, resize, theme
  changes, and global expansion intentionally remain O(history).
- **Reachable clear-on-shrink updates stay differential.** When shorter content can be reconciled
  inside the renderer's active viewport, Orb clears only the vacated rows and settles the tracked
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
| Malformed and colliding provider tool-call recovery | reliability adaptation | owner-directed Hermes-inspired hardening: when a provider declares tool use without emitting a call, orb retries at most three times with non-persisted recovery context; duplicate call pairing IDs are deterministically suffixed before execution so every result remains unambiguous. Canonical pi session and event JSON shapes stay unchanged |
| Bundled MCP extension | addition | owner requirement; kept out of core |
| `packages/server` (formerly `packages/orchestrator`) | removed | experimental upstream side product; the v0.81.0 rename does not change the D2 product boundary |
| `packages/{client,protocol}` (v0.84.0) | removed | experimental remote-session client and CBOR protocol for the excluded server product; same D2 boundary |
| `packages/telemetry` (v0.84.0) | removed | vendor-neutral telemetry contracts; consistent with the existing telemetry-gated attribution removal |
| `packages/session-backends` (renamed from `packages/storage` in v0.84.0) | removed | optional sqlite session backend for the excluded server product; orb's harness keeps the JSONL repo only |
| Telemetry/analytics (`enableInstallTelemetry`, `enableAnalytics`, `trackingId`) | removed | owner decision; unknown settings keys tolerated on parse, nothing sent, no plumbing |
| Radius provider + Radius OAuth | removed | pi.dev-coupled service; the generic `pi-messages` SSE wire shape IS ported (usable by any backend, e.g. an Ordalie gateway) |
| Version/update checks | neutralized | point at OrdalieTech/orb GitHub releases, never pi.dev |
| Public identity and executable | renamed | D30; `orb` avoids colliding with an installed upstream `pi` |
| Default system-prompt identity | product positioning adaptation | Orb presents as a general-purpose problem-solving harness for work and software development rather than using pi's coding-agent identity; tool lists, guidelines, context/skill injection, custom prompts, and assembly order remain upstream-compatible, with F9 applying only the exact ledgered text substitutions to generated upstream goldens |
| External Agent Skills discovery | compatibility addition | Orb automatically imports standard skill roots from Claude Code, Codex, OpenCode, Gemini CLI, Cursor, and GitHub Copilot. Pi-native and `.agents` roots keep precedence, project roots require trust, canonical files load once, existing first-name-wins collision diagnostics apply, and plugin/cache directories are never scanned. |
| `/share` | neutralized | local HTML export instead of pi.dev upload |
| Model catalog runtime refresh | neutralized | models.dev directly, not pi.dev overlay endpoints |
| Windows support | deferred | later parity wave (D8) |
| darwin modifier-key native addon | gap | kitty keyboard protocol where possible; documented small parity gap |
| win32 console native addon | deferred | Windows wave |
| Bundled llama.cpp extension | excluded | v0.81.1 still ships this optional native Node/llama.cpp integration; it cannot satisfy the pure-Go, single-static-binary rule in D7 |
| `packages/storage/sqlite-node` | excluded | v0.81.1's optional Node SQLite storage package requires a native runtime; orb retains the session repository interfaces and JSONL/memory implementations under D7 |
| `orb login` / `orb logout` CLI subcommands | addition | headless Go deployments need auth lifecycle commands; bare `orb logout` deliberately lists stored credential names and requires an explicit provider instead of silently choosing one |
| NVIDIA `qwen/qwen3.5-122b-a10b` denylist | addition | the live NIM endpoint advertises it, but its current metadata cannot satisfy orb's chat-model contract; keep the Go-only exclusion explicit until the live shape is usable |
| `CompleteSimple` and common simple tool choice | Go API adaptation | upstream `Models.completeSimple` only collects `streamSimple`, while TypeScript callers can smuggle provider-specific `toolChoice` fields through structural casts. Go exposes the same collection directly and the portable `auto`/`none`/`required` intersection; a named choice is one advertised tool plus `required`, so no provider shape leaks into embedders |
| Missing default stream error timing | Go API adaptation | upstream throws in the JavaScript `Agent` constructor; Go's fixed `NewAgent` signature cannot return an error, so orb reports the identical error on the first prompt or low-level loop call |
| Single Ctrl-C exit at an empty prompt | usability adaptation | owner requirement; a nonempty draft still clears without exiting, and focused selectors retain their cancel binding |
| Interrupting an unanswered turn takes the prompt back | usability adaptation | owner requirement; upstream's escape restores only queued messages, which orb now does too. When the interrupted turn has shown nothing yet, orb additionally rewinds the branch to before its prompt (the existing tree-navigation path) and returns the text to the editor, so an edited resend replaces the prompt instead of stacking after it. Once any text, thinking, or tool call has appeared, escape aborts exactly as upstream does; the abandoned attempt stays reachable in the tree |
| Compact built-in editor chrome | usability adaptation | while the built-in editor is mounted, a one-line transient status that fits moves into its top border and the explicit session name appears as a truncated themed badge at the right; dialogs, narrow statuses, scrolled drafts, and extension editors keep upstream's standard status lane, with keybindings and extension UI contracts unchanged |
| Compact task and queue surfaces | usability adaptation | the bundled tasks plugin keeps its replacement schema and full branch-aware details but condenses persistent/collapsed rendering, makes the native TUI widget click-expandable with dimmed inset details, and adds an unbound `/tasks` full-list command; queued messages retain the configured dequeue binding and every queued item while adding a count to upstream's one-row truncation and edit hint |
| Moonshot Kimi K3 compat metadata | resolved parity | `thinkingFormat: openai` and reasoning-effort support entered the pinned upstream in v0.81.1 and remain regression-tested |
| `AgentHarness` orchestration facade | dissolved | D29; harness primitives remain in `agent/harness`, while the high-level embedding lifecycle stays in `codingagent.AgentSession` |
| `streamProxy` `/api/stream` client | excluded | D29; application-specific proxy protocols use `agent.WithStreamFn` and the public streaming-JSON helper |
| `chat/` gateway package (+ `chat/telegram`, `chat/whatsapp`) | addition | owner requirement (D27); kept out of core, strictly one-way dependency on the SDK |
| `AgentSessionOptions` tool-operations injection hook | addition | D27; ergonomic seam over the existing `NewSessionRuntime`/`BaseTools` path for VFS/sandboxed tool operations |
| `chat/` platform wave 2 (`slack`, `teams`, `discord`, `messenger`, `googlechat` + `chat/internal/` ws/webhook helpers) | addition | owner requirement (D28); official APIs only, stdlib-only clients incl. hand-rolled RFC 6455 |
| Streaming accumulation via append buffers | Go performance adaptation | upstream's `x += delta` is an O(1) amortized rope in V8 and an O(n) copy in Go, so the transliterated idiom made the port asymptotically slower than its source; buffers preserve every emitted byte. Do not "restore" `+=` on a future sync |
| Tool-argument re-parse gated above 8 KB | Go performance adaptation | rebuilding the argument map is O(len(buffer)) per delta in both runtimes; below the floor — every fixture and every human-sized call — streamed `arguments` stay byte-identical, above it only the live preview lags while `partialJson`/`partialArgs` and the end event remain exact |
| Footer metadata refreshed off the render thread | Go performance adaptation | Git process startup and available-provider scans must not hold the TUI render lock during loader frames or first paint; refresh starts after 500 ms, the Git probe is capped at 250 ms, and the footer invalidates when the asynchronous result arrives |
| Print mode fails a prompted run that produced no assistant | usability adaptation | overflow recovery drops the failed assistant from agent state (upstream `agent-session.ts:2006`), so upstream's text mode exits 0 with empty stdout; scripts cannot distinguish that from success |
| `exec.Cmd.WaitDelay` on the extension host | Go API adaptation | stderr is deliberately wrapped so extensions cannot put the parent terminal in cooked mode, which forces a pipe; without a delay `Wait` blocks until every grandchild that inherited it closes, which Node's `child_process` does not do |
| Goroutine panic guard inside `tui` | Go API adaptation | upstream restores the terminal from a process-wide `uncaughtException` handler (`interactive-mode.ts` `uncaughtCrash`); Go cannot observe another goroutine's panic, so every goroutine the package spawns recovers, runs the registered restores, writes the panic and stack to stderr after restore, and exits 1. `ProcessTerminal.Run` stays the synchronous-body guard for embedders and shares the same `Stop` restore path |
| Terminal widths from uniseg plus a three-class correction | measured divergence | upstream widths come from get-east-asian-width plus an RGI-emoji regex; orb keeps `rivo/uniseg` and corrects the classes measured against upstream — keycap sequences, East-Asian-Wide text-presentation symbols, and halfwidth voiced sound marks U+FF9E/U+FF9F. Residual known divergences: standalone skin-tone modifiers, Yijing symbols, spacing combining marks, Hangul fillers, two/three-em dashes, emoji newer than x/text's Unicode tables, clusters mixing a Wide text-presentation base with a halfwidth voiced mark, and noncharacters x/text tables class as Wide |
| Session-file locking | addition | upstream `session-manager.ts` writes JSONL with no locking at all; orb keeps cross-process locking and takes it through `internal/filelock`'s proper-lockfile-compatible mkdir+heartbeat lock (self-cleaning, stale-steal), replacing a `gofrs/flock` sidecar that left permanent `.jsonl.lock` files a concurrent upstream pi could neither recognize nor reclaim |
| Extension-host `cancel_request` and `provider_stream_event` frames | Go API adaptation | D31's host process boundary has no counterpart to upstream's in-process positional `AbortSignal` (`tool-definition-wrapper.ts`) or its live provider streaming (`loader.ts` `streamSimple`); these two Orb-internal frames plus a per-request `AbortController` registry reproduce both, and the host self-exits on stdin close so an Orb hard-crash cannot orphan it |
| `Request was aborted` for ctx-aborted stream failures | Go API adaptation | every adapter persists upstream's mid-stream abort text `Request was aborted` on a ctx-aborted stream failure (Google's and Mistral's stream paths now included); request-phase texts follow upstream per adapter — `retryProviderRequest` and aborted OpenRouter image body reads return `Request aborted` verbatim, Google's request phase keeps upstream's `Request aborted` — while the anthropic/openai-family stream entry points collapse pre-stream ctx errors into the mid-stream text so the TUI's exact-match "Operation aborted" rendering holds |
| `extensions.Exec` bounded wait on inherited stdio | Go API adaptation | upstream's `waitForChildProcess` resolves the moment the child exits even when a detached grandchild holds the stdio pipes; Go's `os/exec` cannot observe that without `WaitDelay`, so orb waits up to 5s for the pipes and then reports the child's own exit code (never a synthetic failure) |
| RPC untyped-dispatch residual | gap | frames with a known command but a mistyped member (e.g. `{"type":"prompt","message":5}`) answer `success:false` with Go decoder text where upstream surfaces the JS `TypeError` of untyped dispatch; byte parity would mean emulating per-command V8 runtime errors. Frame shape and the missing/non-string/null `type` path are byte-exact |
| `OAuthCredentials` extra members serialize sorted | Go API adaptation | upstream builds the credentials object in JS, so extra members keep insertion order; orb carries extras in a Go map, which has none. Declared members match upstream's declaration order and extras follow, sorted for determinism |
| Markdown session export | addition | upstream `--export` emits HTML only; an output path ending in `.md` routes to the WP-320 markdown exporter, every other path keeps upstream's HTML behavior and the help text is unchanged |
| Concurrent `AfterToolCall` hooks and `EventSink` in parallel tool mode | Go API adaptation | upstream's promises interleave at await points but never run simultaneously; orb's parallel workers invoke both concurrently, so an embedder porting a hook that mutates shared state without locks must add its own synchronization (documented at `agent/types.go`) |
| Parallel tool fan-out bounded at 16 | Go resource adaptation | upstream's `Promise.all` (`agent-loop.ts:539`) is unbounded because JS tool calls are cooperative; in Go each call is a goroutine that may spawn subprocesses, so a model-controlled fan-out is a resource risk. Result ordering is unchanged |
| Tool-update sink driven by one bounded queue (256) | Go API adaptation | upstream's sink is an event-loop callback, so `emit` is a synchronous call that still never blocks the tool and is awaited per call at `agent-loop.ts:694`; Go's `EventSink` blocks, so a drain goroutine reproduces both ordering and non-blocking. A sink more than 256 updates behind backpressures rather than dropping an event |
| Native mouse and click support in interactive mode | addition | owner requirement (2026-07-25). Upstream consumes mouse reports only for transcript selection and the scrollbar. orb adds an optional `tui.MouseHandler` found by type assertion, so D15's `Render(width int) []string` contract is unchanged and every existing component and extension UI keeps working. SGR (1006) reports are offered to the component under the cursor before falling back to the existing scrollbar, wheel and selection behavior; an in-flight drag or any modifier skips dispatch, preserving native drag-select and shift-bypass. Wired: `/tree`, `/resume`, `SelectList`, `SettingsList`, the extension option dialog, and the editor. Excluded: the transcript body and inline TUIs, which have no known screen origin |
| Extension SDK comes from orb's own npm root | independence | orb searched `PATH` for the `pi` executable and handed extensions the SDK bundled in the npm package owning it, so behaviour depended on a third-party install being present. Reading pi's config files is the D4 compatibility promise; executing its code is not. The SDK now resolves from orb's managed roots only, and `ORB_PI_SDK_ROOT` is a deliberate user override rather than a fallback |
| Node capability floor is 22.6, full capability is 22.13 | measured divergence | 22.6-22.12 runs plain JavaScript and erasable TypeScript but cannot compile TypeScript published inside `node_modules`, because `module.stripTypeScriptTypes` arrives in 22.13. Refusing to start on 22.12 would refuse extensions that do run, so the floor stays at 22.6 and the loader names the missing capability at the file that needs it. Node 26 removed TypeScript transformation entirely: enums, parameter properties and namespaces do not run there on any path |
| Staged extension entries removed | simplification | the mechanism gave an entry under `node_modules` a `node_modules`-free path and required `--preserve-symlinks` to keep its links opaque, which broke pnpm store resolution on every Node version. Supplying transpiled source from the load hook covers entry and dependencies at any nesting depth, so the package manager's own layout governs. Cost: an npm-installed TypeScript extension on Node 22.7-22.12 now reports that it needs Node >= 22.13 rather than working |
| Frontmatter YAML text keeps its terminating newline | dependency workaround | upstream slices it off too (`utils/frontmatter.ts:23`), but npm `yaml` treats end-of-input as a line break so clip chomping still yields the newline, while `gopkg.in/yaml.v3` synthesizes none; upstream pins the observable value in `test/frontmatter.test.ts:25-30`. Do not "restore" the exclusive slice on a future sync |
| Bun SDK aliases via `NODE_PATH` | Bun adaptation | Bun's runtime `Bun.plugin` `onResolve` never observes a nested import, so Node's resolve hook has no counterpart. `NODE_PATH` is consulted after the `node_modules` walk, which enforces "alias only when absent" in the resolver itself; Node ignores it for ESM, so the primary path is unaffected. The root-to-`/compat` redirect remains Node-only |
| Pinned SDK auto-provisioning into orb's own npm root | addition | upstream never needs this because pi ships with its SDK; orb is a static binary, so the first loose extension with a bare SDK import triggers one `npm install --ignore-scripts` of the version matching orb's parity target into the user npm root, under the same mkdir lock discipline as settings writes. The SDK is public MIT code from the npm registry; no installed pi is consulted, keeping the independence rule intact |
| Bun runs with `--no-install` | safety adaptation | dependencies are materialized by an explicit audited install step; Bun's implicit auto-install otherwise fetches unresolved specifiers from npm mid-session, which Node never does |
| Disposed sessions refuse work (`ErrSessionDisposed`) | SDK safety adaptation | upstream `agent-session.ts:835` `dispose()` sets no flag and no method refuses afterwards, so a post-dispose prompt still called the model and persisted the turn with its events silenced. An embedder that disposes on client disconnect spends money invisibly; the guard sits at `runPolicies` plus the queue-only entry points, so a doomed request still reports its specific reason |
| Subagent parallel width capped at 32 | resource guard | `tasks` is model-controlled and each entry costs a goroutine, a temp dir, a child session, and a real provider call; uncapped, one tool call fanned out to 2,000 children in measurement. Enforced in the tool and declared as `maxItems` in the schema |
| Unified-patch hunk headers derived locally | dependency workaround | `go-udiff` advances `ToLine` only in its new-hunk branch (`unified.go:143`), so the second and later `@@` headers report the wrong new-file start; orb recomputes them and matches `Diff.createTwoFilesPatch` across a 2,998-case differential fuzz |

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
  `AppendCustomEntry` (a `orb.chat.turn` started/settled/delivered ledger), keeping the session
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
  `AgentSession`; upstream documents that migration as pi 2.0 work. orb keeps the already-ported
  session, repository, compaction, resource, and environment primitives in `agent/harness`, while
  `codingagent.AgentSession` remains the sole high-level embedding runtime. Reimplementing the
  1,029-line facade would duplicate queues, hooks, persistence ordering, and lifecycle state, and
  placing a wrapper in `agent` would invert the package dependency. The adjacent `streamProxy`
  client is also excluded: its `/api/stream` endpoint is an application protocol rather than agent
  behavior, and embedders already have `agent.WithStreamFn` plus `ai.ParseStreamingJSON`. Revisit
  either surface only when upstream's coding-agent adopts it or a real Go consumer requires it.

- **D30 — Public identity is Orb (owner-amended, 2026-08-10).** The repository, Go module, executable,
  release artifacts, installer variables, terminal title, resume hints, and default RPC client
  command use `orb`; no legacy `pi` executable or alias is shipped, so upstream pi can coexist on
  the same machine. Upstream compatibility names remain unchanged where they are the contract:
  `.pi`/`~/.pi`, upstream `PI_*` runtime variables, session and wire formats, pi package manifests,
  `pi-messages`, the JS extension `pi` API and `@earendil-works/pi-*` imports, embedded upstream
  assets, and extracted goldens. The default system prompt identifies Orb as a general-purpose
  problem-solving harness for work and software development; coding remains a core capability rather
  than the exclusive role. Conformance adapters may account only for exact public-name substitutions
  and this ledgered default-prompt identity and documentation wording while separately asserting the
  `orb`/`Orb` spelling.

- **D31 — Host-only JavaScript execution (owner, 2026-07-22).** All JavaScript and TypeScript
  extensions, including installed npm packages, project/global extension files, and explicit `-e`
  entries, run out of process in the extension host. Orb selects local Node.js ≥22.6 (native type
  stripping) or Bun, with no embedded JavaScript engine, transpiler, Node shims, or runtime feature
  flag. When neither runtime is available, extension loading names the missing runtime and points at
  `ORB_NODE`, and the rest of the product remains available. The 22.6 floor is a capability floor,
  not a uniform one: see the version-capability rows in the divergence ledger, measured across
  22.6-26.5. The host owns real Node/Bun module, worker,
  top-level-await, WebAssembly, and native-addon semantics; orb remains a static `CGO_ENABLED=0`
  binary and ships neither runtime.

- **D32 — First-party plugins: bundled-but-dormant (owner, 2026-07-22).** Tasks, websearch, and
  subagents ship in the binary but default off, preserving the upstream tool surface until a user
  opts in through the `plugins` settings object, `orb plugins`, or the `/plugins` selector. The
  existing user/project settings overlay and runtime reload path own enablement; embedders bypass
  settings by selecting factories from `plugins.Catalog()`.

- **D33 — Permissions plugin (owner, 2026-07-22).** The dormant first-party permissions plugin uses the standard allow/deny/ask, ordered last-match-wins model and defaults to permissive log mode.

- **D34 — MemoryStore seam + memory plugin (owner, 2026-07-23; amended 2026-07-30).** This
  Orb-original addition gives the dormant memory plugin one storage seam shared by per-profile
  JSONL stores and per-tenant database implementations, because three ecosystem memory packages
  otherwise reinvent storage. The seam lives at root-level `memory/`, following the Orb-original
  `chat/` precedent, so embedders can import it standalone. `memory/agent` owns the shared agent
  behavior: plain `agent.Agent` SDK users attach it directly, while the coding-agent plugin is a
  thin adapter over the same runtime. Enablement is the only mode: local and SDK users get the same
  frozen `USER PROFILE`/`MEMORY` prompt, fixed 1,375/2,200-character budgets, and
  `remember`/`recall`/`replace`/`forget`; capacity pressure drives model-led consolidation. Stores
  are tenant-scoped and concurrent, with an optional transaction seam for compound mutations
  shared by multiple sessions or processes. There is no separate injection or shutdown-distillation
  configuration. The profile uses the Store's bounded 100-item query window and append-before-delete
  replacement. V1 has no per-turn RAG, session search, secret scanning, widget, or subagent inheritance.

- **D35 — TUI presentation is Orb-owned (owner, 2026-08-10).** Byte-parity with pi is retained for
  wire and data formats (D4), provider request shaping, and tool/session/RPC behavior — but not for
  TUI rendering. The F12-family render goldens (themes, component frames, visible-command frames,
  replay/UI demos) convert from upstream-extracted fixtures to Orb-owned snapshots regenerated from
  Orb's own renderer; until that conversion lands they continue to regenerate from upstream.
  Upstream TUI features (e.g. fullscreen mode) are cherry-picked on merit rather than ported
  wholesale. This amends D2's full-parity scope for the TUI surface only.

## 2026-07-21 parity-sync amendments

- Codex request compression uses `github.com/klauspost/compress/zstd` as a direct dependency. The
  upstream wire requires zstd request bodies, and the standard library has no zstd encoder.
- The v0.81.1 image catalog is checked in as deterministic Go data and pinned by an exact digest.
  Upstream's strict TypeScript model-data validator has no runtime Go analogue, so generation-time
  validation plus full-catalog tests enforce the same accepted shape.
- Remote-catalog freshness preserves upstream's `checkedAt`/`lastModified` semantics, while D12's
  single direct models.dev endpoint replaces pi.dev's provider-scoped service. The orb identity in
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
  module or compiled-package entry against its consolidated parent (WP-221, WP-222).
- **G3 (WP-542):** pi-tui Component bridge overlay/experimental surfaces — bridge now vs documented gap.
  **Resolved (Sprint 3): bridge now.** `ctx.ui.custom` with overlay options (static and dynamic),
  `OverlayHandle` round-trips, focusable JS components, and editor replacement including the
  `CustomEditor` base class (JS class over the mode-registered real editor seam) are bridged; the
  modal-editor example runs unmodified. Remaining pi-tui component classes (Text/Container/Markdown
  construction from JS) are bridged on demand as the F11 matrix requires them.
- **G4 (WP-661):** self-update mechanism — notify-only vs in-place binary self-update.
  **Resolved (Sprint 4): notify-only.** The update check (already pointed at OrdalieTech/orb
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
  orb keeps the canonical-path visit stack and returns each skill once under its clean path.
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
  (orb shim)") with an honest `has()` so `in`-feature-detection still works. True Node-ESM
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
  manifest, which orb does not maintain. The shim instead throws on first *access* of an
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
  already contains that reachable commit. Orb reconciles that detached object locally; branches,
  tags, missing commits, and fresh installs retain upstream's fetch/checkout behavior. This is a
  narrow usability divergence from upstream's failing `git fetch origin <short-sha>` path.
- **Ecosystem compatibility claims stay layered.** A locked 44-package corpus separately records
  stable loading, observable registration parity, line-grounded workflow feasibility, and executed
  offline command/workflow probes. A package that only loads is never labeled end-to-end compatible.
  The embedded CommonJS build preserves `import.meta.{url,filename,dirname}` per source module, but
  variable dynamic imports, top-level-await-only modules, real Node streams/sockets, and native
  addons remain explicit ceilings until their semantics can be implemented faithfully.
