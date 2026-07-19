# Implementation progress

The active sequence is `SPRINTS.md`; the old work-package numbers are historical spec references
only. Progress is measured by conformance surfaces moving from red to green and by milestone
criteria closing.

## Sprint 0 — Consolidate

Status: **closed** at `68d7229`.

- [x] Record the owner-directed trunk, fixtures-first, core-first plan as D25/D26.
- [x] Leave the GitButler workspace and move the shared checkout to plain `main`.
- [x] Integrate historical WP-351 extension wire-through and its F11-wire conformance fixture.
- [x] Integrate the historical SDK facade and 13 example packages into green `main`.
- [x] Integrate historical WP-360 package management and project trust with upstream fixtures.
- [x] Integrate historical WP-410 TUI core and its upstream F12 primitive goldens.
- [x] Integrate historical WP-430 Markdown, syntax highlighting, themes, and F12 goldens.
- [x] Integrate historical WP-440 terminal-image, read-image, and clipboard foundations with
      pinned-upstream F12, WP440, and WP440Read fixtures.
- [x] Integrate the replaceable AgentSessionRuntime, reloadable extension state, SDK provider
      settings, and the generated WP370Runtime lifecycle fixture.
- [x] Integrate every former GitButler lane and side ref, reconciling overlapping implementations.
- [x] Verify `CGO_ENABLED=0 go build ./...` and `make test` at every integrated commit.
- [x] Delete merged side refs and temporary consolidation stashes.
- [x] Finish on plain `main` in the primary checkout with no linked worktree, lane, or feature branch.

Current red-to-green evidence: RPC/resources/native extensions moved from merge conflicts and six
lint failures to green fixtures and the pinned 27-test upstream RPC run. The SDK candidate moved
from home-directory writes and missing persisted-message/settings/session propagation to green
focused tests, all 13 faux examples, an isolated external-module build, deterministic fixtures,
and four CGO-free Linux/Darwin amd64/arm64 builds. Replaceable runtime behavior moved from a
compile-time RED fixture to an upstream-generated green lifecycle matrix for cancellation,
teardown-first replacement, setup, rebind, `withSession`, and quit ordering. TUI primitives,
Markdown/themes, terminal images, image reads, settings-backed image width/visibility, and `/copy`
are integrated and green on their deterministic surfaces; real terminal and desktop smoke remains
owner-blocked evidence rather than a local substitute.

The last side overlays are one resolved plain-main candidate: F2 provider/catalog regeneration,
Cloudflare nullable auth headers, JS bridge/F11, MCP, WP-450, and interactive-mode surfaces compile
and their focused suites are green. Restoring the one omitted later WP-530 object moved eleven
streaming fetch, Headers, Response body, path, and Buffer regressions to green. MCP moved from a
real-agent settlement failure and reused-call-ID cross-wires to deterministic draining of progress
notifications observed before tool settlement across stdio/in-memory and Streamable HTTP; later
standalone SSE notifications are deliberately ignored once the call is sealed. Interactive auth
moved from full-session reloads, collapsed status, duplicate-name routing, and an unusable fresh
install to in-place registry refresh, exact status sources, stable provider identity, an upstream
unknown-model sentinel, and first-login default-model selection. The recovered F12 scratch corpus
moved missing overlay/color APIs and three supplementary-Han cursor failures to 45 byte-exact
overlay frames, 44 focus traces, terminal-color traces, four primitive full-screen composites, and
266 green CJK navigation cases without replacing the validated pure-Go ICU 78.2 implementation.
The consolidated candidate passes the full repository race suite, byte-clean pinned fixture
regeneration, all 27 upstream RPC tests, vet plus golangci-lint, module verification and tidy diff,
and CGO-disabled builds for Linux and Darwin on amd64 and arm64.

Historical note: `2a8ac08` and `68c3afa` were intermediate snapshots that did not build by
themselves; their corrected descendants are already represented in the consolidated history and
they are not valid integration points.

Expansion work already landed before D26, including Codex/Copilot/xAI and related fixtures, is kept
but will not be extended until Sprint 3.

## Sprint 1 — Core headless correct (M2)

Status: **deterministic core GREEN; M2 awaits two owner-run checks**.

- [x] Land the RED F7 RPC transcript/upstream-suite adapter and the retained F8 resource goldens first.
- [x] Turn the upstream RPC suite, F7, retained F8 cases, F9, and F10 green.
- [x] Expand F8 to the remaining upstream resource precedence, dedupe, diagnostics, and command cases,
      then turn that surface green.
- [x] Keep F2 green for all landed core API shapes and `auth.json` cross-compatible.
- [x] Port harness `SessionRepo`/`FileSystem`, JSONL/memory repositories, and rehydrate-from-bytes.
- [x] Keep all 13 SDK examples and the retained skills/templates/native-extension fixtures green.
- [x] Land exact pinned-upstream missing-model diagnostics for print/json/RPC and turn every byte green.
- [x] Land auth lifecycle/isolation tests, then bind login to mode cancellation and refresh only
      credential-dependent projections without reloading unrelated model configuration.
- [x] Close the uncovered resource, native-extension seam, and public SDK-facade cases from the RED audit.
- [x] Build an external `go get` SDK smoke module and wire the nightly live suite.
- [x] Publish `docs/compare/sprint-1.md` with identical scripted print/json/rpc evidence and complete trim pass #2.
- [ ] Record one subscribed Anthropic Pro/Max browser login plus streamed request.
- [ ] Record the first hosted nightly live-suite run with repository secrets.

Current red-to-green evidence: F7-cli moved real text/JSON/RPC missing-model output to exact pinned
TypeScript bytes and exposed an EOF race that previously returned `Session is unavailable`; the
RPC dispatcher now retains the active session and the upstream prompt diagnostic. F8 now proves
resource precedence, metadata, ordered diagnostics, immediate extension resources, command
collisions, and harness substitution against TypeScript. Six native extension gaps moved green,
including nil handlers, panic origin, provider queue/post-bind behavior, trust ordering, and input
identity. Auth lifecycle, the public loader/service/session SDK controls, provider registration,
all 13 isolated faux examples, and the external consumer smoke are green. Trim pass #2 removes 301
net lines and cuts the accidental Copilot catalog startup cost, bringing the no-prompt mean from
48.7 ms to 40.0 ms. The exact candidate passes byte-clean regeneration, 27/27 upstream RPC tests,
the full race suite, lint/vet, module verification/tidy diff, and four CGO-disabled cross-builds.

## Sprint 2 — TUI complete (M3)

Status: **closed for deterministic parity; real kitty/iTerm2 and native-desktop clipboard smoke
remain owner-blocked evidence (not waived)**.

- [x] Land the RED F12 component, editor, Markdown, overlay, terminal-color, and primitive-composite corpus.
- [x] Turn core components, overlays, terminal colors, ICU navigation, stress/fuzz, and primitive frame budget green.
- [x] Land the session-selector lifetime fixture and stop status timers on confirm, cancel, and runner exit.
- [x] Complete ResourceLoader theme-object/source-info installation into the interactive registry.
- [x] Reach application-level byte-reviewed frame parity and complete commands plus image/clipboard checks
      (deterministic surfaces; real-terminal smoke owner-blocked).
- [x] Publish `docs/compare/sprint-2.md`, complete trim pass #3 (`docs/trim/M3.md`), and check every
      locally provable M3 criterion.

The selector lifetime trace is green for selection, cancellation, runner exit, and every emitted
timer/render field. ResourceLoader package filters and theme accents now match pinned TS output,
including negated package resources, exact object identity, source metadata, and replacement reloads.
Application autocomplete, all visible and hidden command dispatch/behavior, branched JSONL export,
and ordinary plus signal shutdown are green against executable upstream fixtures. Exact raw ANSI,
padding, and line-count assertions are also green for every hidden frame and all 22 visible commands,
including the full changelog. Pinned F12-app lifecycle transcripts made the remaining `ctx.ui` RED
surface explicit: editor replacement and bracketed
paste, terminal-input presence and reset cleanup, persisted working state, custom-UI transactions,
dialog timers and zero-width borders, header/footer disposal, Theme-object switching, and ordinary
error spacing all compare directly to executable TS pi behavior and are now green.
The separate generated `F12-ui-lifecycle` family now pins reset ordering/state, widget ownership and
layout, historical plus streaming hidden-thinking labels, tools-expanded propagation, and custom
overlay transactions. Reset cleanup/disposal, widget caps/placement/reentrant factories, thinking
label reset, and header/resource/chat expansion are green. The custom-overlay trace is green too:
options resolve once, margins and component-width fallback survive, and temporary visibility stays
distinct from permanent `OverlayHandle.Hide` removal with accurate focus restoration.
Landing the lifecycle family exposed and closed two real races: extension-editor swaps and
working-indicator mutations are now atomic against reset/dispose teardown. Golden extraction is
also environment-independent now — the invoking terminal's `COLORTERM` is stripped before driving
upstream, the visible-commands test pins 256-color mode, and the session-selector fixture uses a
deterministic cwd (a random mkdtemp suffix could previously satisfy fuzzy queries), proven by a
byte-clean full `make fixtures-check`.

## Sprint 3 — Expansion (M4)

Status: **open; study surfaced to owner; proceeding on full-parity defaults**.

- [x] Publish `docs/plan/expansion-study.md` for owner review before extending breadth.
      **OWNER: please review — it contains a binary-size cap decision (M5) and confirms the
      full-parity defaults; silence = defaults stand.**
- [x] Audit the frozen expansion ring: providers (35/36 + codex), OAuth (all four flows), MCP,
      packages/trust, and the bridge runtime/non-UI/shims layers are already landed and green.
- [x] Land the RED `ctx.ui` F11 surface first (ui-dependent upstream examples wired and failing),
      then turn WP-541 (ctx.ui bridge) green — seventeen examples, full dialog/status/widget/theme/
      autocomplete surface, AbortController, pi-tui helper shim.
- [x] WP-542: custom components, editors, overlays over the bridge (gate G3 resolved: bridge now) —
      `ctx.ui.custom` with overlay options and handles, editor replacement, the `CustomEditor` base
      over the registered real editor; modal-editor end-to-end plus six more custom-UI examples.
- [ ] WP-550: F11 matrix ≥80% of the 69 single-file upstream examples unmodified;
      publish `docs/sync/extension-matrix.md`; six named extensions end-to-end.
- [ ] Port the openrouter-images generation client (only unported API shape).
- [ ] Alignment-audit work items (docs/compare/upstream-alignment.md): MIRROR triage rows for the
      16 unmapped core modules and the agent facade files; implement-or-ledger `settings.httpProxy`;
      SDK convenience surface (tool bundles, public ai model helpers, typed tool-event accessors,
      public streaming-JSON entry point, UI component kit exports); port the six numbered upstream
      regression tests and the small uncovered unit-test tail.
- [ ] Publish `docs/compare/sprint-3.md`, complete trim pass #4, and check every M4 criterion.

## Sprint 4 — Ship (M5)

Status: **pending**.

- [ ] Re-verify M1–M4, execute a green upstream sync, and build/verify all release artifacts.
- [ ] Complete the 72-hour live-suite window, newcomer docs walk-through, final size/LOC/dep audit.
- [ ] Complete trim pass #5 and tag `v0.1.0` only when every M5 criterion is checked.

## Owner-blocked evidence

- **Decision pending: M5 binary-size cap** (`docs/plan/expansion-study.md`) — stripped binary is
  102,882 B over the decimal 35 MB cap before the bridge links sobek/esbuild; recommended: 45 MB
  cap + one bounded size pass in Sprint 4.
- **Decision pending: AgentHarness facade** (alignment audit) — upstream's primary
  `packages/agent` export has no Go equivalent; port it for SDK parity or record "dissolved into
  the codingagent runtime" in DECISIONS. Recommended: record the dissolution; port only if an SDK
  consumer asks.
- **Recorded for review: llama.cpp extension excluded** (divergence ledger) — shipped at the pin
  but deleted upstream right after; amend if you want it ported anyway.
- Anthropic Pro/Max end-to-end OAuth requires an interactive subscribed account.
- ChatGPT/Codex, Copilot, and xAI OAuth end-to-end runs likewise require subscribed accounts.
- Tier-2/Tier-3 provider live tests require repository/API credentials and CI secrets.
- Real Kitty and iTerm2 image emission plus native Darwin/X11/Wayland clipboard smoke require those
  terminal and desktop environments.
- Off-machine clean macOS/Linux release validation and the 72-hour burn-in require owner-provided
  hosts/remotes; all local and fixture work continues independently.
