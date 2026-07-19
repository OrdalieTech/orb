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

Status: **next active deterministic surface; Sprint 1 owner-run evidence remains open in parallel**.

- [x] Land the RED F12 component, editor, Markdown, overlay, terminal-color, and primitive-composite corpus.
- [x] Turn core components, overlays, terminal colors, ICU navigation, stress/fuzz, and primitive frame budget green.
- [ ] Land the session-selector lifetime fixture and stop status timers on confirm, cancel, and runner exit.
- [ ] Complete ResourceLoader theme-object/source-info installation into the interactive registry.
- [ ] Reach application-level byte-reviewed frame parity and complete commands plus image/clipboard checks.
- [ ] Publish `docs/compare/sprint-2.md`, complete trim pass #3, and check every M3 criterion.

## Sprint 3 — Expansion (M4)

Status: **pending**.

- [ ] Publish `docs/plan/expansion-study.md` for owner review before extending breadth.
- [ ] Land the RED provider, OAuth, MCP, package/trust, and F11 extension-matrix surfaces first.
- [ ] Complete the full-parity defaults unless the owner records an amended decision.
- [ ] Publish `docs/compare/sprint-3.md`, complete trim pass #4, and check every M4 criterion.

## Sprint 4 — Ship (M5)

Status: **pending**.

- [ ] Re-verify M1–M4, execute a green upstream sync, and build/verify all release artifacts.
- [ ] Complete the 72-hour live-suite window, newcomer docs walk-through, final size/LOC/dep audit.
- [ ] Complete trim pass #5 and tag `v0.1.0` only when every M5 criterion is checked.

## Owner-blocked evidence

- Anthropic Pro/Max end-to-end OAuth requires an interactive subscribed account.
- Tier-2/Tier-3 provider live tests require repository/API credentials and CI secrets.
- Real Kitty and iTerm2 image emission plus native Darwin/X11/Wayland clipboard smoke require those
  terminal and desktop environments.
- Off-machine clean macOS/Linux release validation and the 72-hour burn-in require owner-provided
  hosts/remotes; all local and fixture work continues independently.
