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
- [x] WP-550: F11 matrix at 61/69 (88%) unmodified with `docs/sync/extension-matrix.md` published;
      six named extensions end-to-end; bridge wired into the product (`--extension`, settings and
      project paths, `/reload` per-path VM replacement) with a real-binary smoke.
- [x] Port the openrouter-images generation client (only unported API shape).
- [x] Alignment-audit work items closed this sprint: MIRROR triage (21 verified rows),
      `settings.httpProxy` implemented with environment precedence, SDK convenience surface
      (tool bundles, public ai model helpers with duplicates deleted).
- [x] Publish `docs/compare/sprint-3.md`, complete trim pass #4 (`docs/trim/M4.md`), and check
      every locally provable M4 criterion.
- [x] Pre-release parity tail (Sprint 4): the six numbered upstream regression tests; typed
      tool-event accessors, public streaming-JSON entry, UI component kit exports; the five small
      gaps found by MIRROR verification (`/session` cache-waste totals, opencode session-affinity
      headers, live-export ToolHTMLRenderer, `/settings` idle-timeout entry, footer/tool-header
      cosmetics).

## Sprint 4 — Ship (M5)

Status: **release candidate reached; every locally provable M1–M5 criterion green; remainder
owner-gated** (see docs/trim/M5.md §Owner-gated remainder).

- [x] Land the release machinery: goreleaser (4 targets, snapshot verified), tag-triggered
      workflow re-running the gate, checksum-verifying install script, ldflags version, CI on
      `make check`, README newcomer path, G4 resolved notify-only.
- [x] Close the parity tail: six upstream regression tests ported, five MIRROR-verification gaps
      fixed, three real defects found and fixed (CLI stream SessionID, live custom messages,
      select-list theme), startup loaded-resources listing deferred with a MIRROR note.
- [x] Close the alignment should-fix remainder: typed tool-event accessors, ai.ParseStreamingJSON,
      UI component exports (absentees documented), unit-test tails including the 28 missing
      app.* keybinding migrations found and fixed.
- [x] Re-verify M1–M4 at the release candidate; run the sync cycle GREEN end-to-end (lock bump
      blocked: upstream unmoved past the pin); final trim pass #5 with LOC (1.089x) and dep audit
      (docs/trim/M5.md).
- [x] Tag `v0.1.0` (annotated, remainder recorded in the tag message per the owner's goal
      directive; it remains unpublished, so publication stays an owner act).
- [ ] Owner-gated before publishing the tag: size-cap decision, OAuth live runs, CI secrets
      (nightly 72h window), clean-VM install/docs verification, a green lock bump once upstream
      publishes past the pin.

## Sprint 5 — Chat gateway (D27)

Status: **closed in `43e5863`**.

- [x] Land the chat core RED-first: processor + turn ledger recovery tests against the faux
      provider and in-memory sessions, then turn them green (`chat/`: message, adapter, provider,
      ledger, processor, coalescer, local spool runner).
- [x] Turn every crash boundary green: replay after `started`, after the user message (orphaned
      branch via `Manager.Branch`), after `settled` (resend with the `♻ recovered reply` prefix,
      no re-prompt), after send-before-`delivered`; duplicate `EventID` no-op; resume edits the
      recorded preview id.
- [x] Land the Telegram adapter against a deterministic fake Bot API server: webhook
      secret-token auth + long-poll ingress with durable-enqueue offset semantics, coalesced
      preview edits with flood-control backoff, HTML formatting with plain-text fallback,
      fence-aware 4096-UTF-16-unit chunk goldens, media groups and download, group mention
      gating by entities, `/stop` `/new` `/status` `/compact`.
- [x] Land the WhatsApp Cloud adapter against a fake Graph server: hub challenge, HMAC
      signature validation before parsing (constructor refuses to build unsigned), mark-read +
      typing, final-message-only delivery with wamid threading, media download with URL-expiry
      refetch, out-of-order status reconciliation (`StatusRank`).
- [x] Land the `AgentSessionOptions.ToolOptions` hook with tests proving injected operations are
      used and survive `RebuildBaseTools`; default behavior unchanged when nil.
- [x] Keep tools off by default (`NoTools: "all"` in `NewLocalProvider`); enable only via the
      explicit `WithSessionOptions` hook.
- [x] 1,000 concurrent faux turns over 100 keys race-clean; idle conversations retain zero
      keyed-mutex entries and goroutines return to baseline. `CGO_ENABLED=0 go build ./...` and
      `go test -race ./...` green, zero new dependencies, `conformance/` untouched.
- [x] Land `chat/examples/localbot` (runnable Telegram long-poll gateway over the local spool).
- [x] Publish `docs/chat.md` (embedding guide), the MIRROR.md D27 addition row, and
      `docs/compare/sprint-5.md` (pi-chat/Hermes cross-check with the deliberate-difference
      table).
- [x] Commit the sprint arc as green mainline chunks and close the sprint per D25
      (`43e5863`, exact `make check` green).

## Sprint 6 — Chat platform wave 2 (D28)

Status: **closed by the Sprint 6 commit containing this record**.

- [x] Land `chat/internal/wsclient` RED-first (hand-rolled RFC 6455 client, stdlib-only) and
      turn its protocol suite green against a fake hijacked server: handshake, masking,
      16/64-bit lengths, fragmentation reassembly, ping auto-pong, clean close vs synthesized
      1006, oversize rejection.
- [x] Extract `chat/internal/graphhook` (hub.challenge handshake + `X-Hub-Signature-256`
      raw-body HMAC) from `chat/whatsapp` with zero behavior change — the existing WhatsApp
      tests pass unmodified — and reuse it in Messenger.
- [x] Land the Slack adapter against a fake Web/Events API server: v0 signing with replay
      window, url_verification, publish-and-ack within the 3s deadline, bot-echo drops,
      `sl:<channel>:<ts>` dedupe collapsing the app_mention/message.channels double delivery,
      preview streaming via `chat.update` with the edit-refused fallback, mrkdwn transcoding
      and fence-aware 4,000-char chunk goldens.
- [x] Land the Teams adapter against a fake JWKS/connector: the full inbound JWT validation
      matrix (RS256, issuer, audience, skew, serviceUrl claim — constructor-enforced, never
      skippable), typing + final-only delivery in every conversation type, 28,000-UTF-16-unit
      chunking with pacing and recursive 413 halving.
- [x] Land the Discord adapter: gateway session over wsclient against a scripted fake gateway
      (hello→identify→READY→dispatch→heartbeat-ack loss→resume), resume-first reconnects with
      capped backoff, fatal 4004/4012/4013/4014 with the actionable 4014 intent hint,
      DIRECT_MESSAGES included in the intents, typing refresh, PATCH preview edits, 2,000-rune
      chunking, `allowed_mentions: {"parse": []}` on every send, 429 retry_after honored.
- [x] Land the Messenger adapter against a fake Graph server: graphhook-verified webhook,
      is_echo drops, (page id, PSID) conversation keys, final-only delivery with typing_on
      refresh and 1,900-rune chunks, 24h-window/policy errors never retried, watermark
      callbacks; `subscribed_apps` step documented on the constructor.
- [x] Land the Google Chat adapter against a fake JWKS/Chat API: inbound bearer-JWT
      verification (project-number audience), stdlib RS256 service-account assertion, async
      replies only, argumentText preference, final-only deterministic client-assigned ids
      (create-conflict-to-PATCH crash idempotence, 1 write/s/space serialization), Chat-dialect
      transcoding and chunk goldens.
- [x] All five adapters: `Message.Account` consistent with `Account()`, group mention gating
      with mention stripping, `/cmd` normalization, sent-chunk resume on Finalize retry, token
      redaction. `CGO_ENABLED=0 go build ./...` and `go test -race ./chat/...` green, zero new
      dependencies, `conformance/` untouched.
- [x] Extend `docs/chat.md` with the Platforms section (five adapters + the three internal
      helpers) and the MIRROR.md D28 addition row.
- [x] Publish `docs/compare/sprint-6.md` (per-platform Hermes/pi-chat cross-check with the
      deliberate-difference table, incl. the bridge/E2EE exclusions).
- [x] Complete `docs/trim/S6.md`: remove 1,068 net lines from the inherited candidate, record zero
      new dependencies and zero duplicate groups, and prove the SDK-only additions add zero
      linked bytes to `cmd/pi`.
- [x] Commit the sprint arc as one green mainline chunk and close it per D25; exact `make check`,
      fixture regeneration, module verification, static analysis, and four CGO-disabled
      cross-builds are green.

## Owner-blocked evidence

- **Decision pending: M5 binary-size AND cold-start caps** (`docs/plan/expansion-study.md`,
  decision 3; measured in `docs/trim/M4.md`) — bridged stripped binary 51,425,442 B vs the 35 MB
  cap, and no-prompt cold start 50.4–50.9 ± 6-7 ms vs the 50 ms cap (pure binary-load cost; init
  is clean). Options: raise caps for the bridged artifact, ship `pi` + bridge-less `pi-slim`
  (35.1 MB / ~44 ms at M3), or fund deeper size work. Neither criterion is weakened pending your
  call.
- **Recorded for review: llama.cpp extension excluded** (divergence ledger) — shipped at the pin
  but deleted upstream right after; amend if you want it ported anyway.
- Anthropic Pro/Max end-to-end OAuth requires an interactive subscribed account.
- ChatGPT/Codex, Copilot, and xAI OAuth end-to-end runs likewise require subscribed accounts.
- Tier-2/Tier-3 provider live tests require repository/API credentials and CI secrets.
- Real Kitty and iTerm2 image emission plus native Darwin/X11/Wayland clipboard smoke require those
  terminal and desktop environments.
- Off-machine clean macOS/Linux release validation and the 72-hour burn-in require owner-provided
  hosts plus publication/CI access; all local and fixture work continues independently.
