# Sprint plan — ACTIVE (supersedes phase-file sequencing)

Owner restructure, 2026-07-18 (D25 + D26 in DECISIONS.md). Remaining work is four large sprints,
each defined by the tests that must pass against TS pi — not by scope bullets. **Core-first
ordering (D26): get the engine byte-right with every test green before spending any time on
compatibility breadth.** Providers beyond those already landed, MCP, pi-packages, and the JS
extension bridge are the EXPANSION ring — deferred to Sprint 3, which opens with a study the owner
reviews. The old `phase-*.md` files are **spec sheets** (upstream refs, per-surface detail): read
them for *what to port*, ignore their sequencing.

## Rules of the road (replace the WP protocol)

1. **One branch: `main`.** No GitButler lanes, no worktrees, no feature branches. A commit is a
   coherent, green chunk — as large as three old WPs, as small as a fix. Every mainline commit
   builds (`CGO_ENABLED=0 go build ./...`) and passes `make test`.
2. **Fixtures first, then port.** Open each sprint by landing its conformance surface so the sprint
   starts RED and is done when it is GREEN. Never the reverse order again.
3. **Compare to TS pi, continuously.** Each sprint closes with `docs/compare/sprint-N.md`: the same
   scripted scenarios through TS pi (`.upstream/`) and pi-go, every difference fixed or ledgered.
4. **Close = trim + criteria.** Sprint close: trim checklist (RELEASE-CRITERIA), milestone boxes
   checked, comparison report committed. No separate trim WPs.
5. Hard rules from AGENTS.md unchanged: byte-compat wire formats, dependency table, never weaken a
   golden, pure Go, MIRROR.md updated.

## Sprint 0 — Consolidate (first, before anything else)

Integrate every existing side ref (wp352, wp360, wp370, wp390*, wp450*, wp520, wp530, wp661, and
any GitButler lane state) into `main`, verifying each integrated commit builds; delete the refs;
note the two historical non-building snapshots (2a8ac08, 68c3af) in PROGRESS.md. Work already done
for expansion surfaces (e.g. MCP, bridge, packages) is integrated and kept — it simply isn't
*extended* until Sprint 3. Rewrite PROGRESS.md as a sprint checklist. Commit the restructured plan
docs. From then on: single branch.

## Sprint 1 — Core headless correct (closes M2)

**Definition of done:** upstream's RPC test suite passes against `pi-go --mode rpc`; F7/F8 green;
F2 green for the LANDED shapes (openai-responses/completions, anthropic, google, vertex, mistral,
azure, bedrock, pi-messages); Anthropic OAuth verified with `auth.json` cross-compat green; skills
and prompt templates conformant; extension-API seams wired internally; SDK examples run; harness
`SessionRepo`/`FileSystem` parity landed with rehydrate-from-bytes; nightly live suite wired;
`docs/compare/sprint-1.md` proves TS-vs-Go parity on scripted print/json/rpc sessions.
**Explicitly deferred to Sprint 3:** codex/copilot/xai OAuth + codex shape, the ~20-provider compat
family, MCP, pi-packages (npm:/git:).
**Scope (spec sheets):** old WPs 331, 340, 350, 351, 370, plus the harness env/SessionRepo addition
(upstream `packages/agent/src/harness/` — FileSystem/ExecutionEnv/SessionStorage/SessionRepo,
jsonl-repo, memory-repo, wired into SessionRuntime). 350/351 land the seams because internal
features ride them; MCP/bridge/packages consume them later.
Open with: F7 RPC transcript extraction + the adapter running upstream's RPC suite + F8 goldens, RED.

## Sprint 2 — TUI complete (closes M3)

**Definition of done:** F12 green (components, editor wide-char, markdown corpus, composites);
side-by-side frame replay vs TS pi with every deviation fixed or ledgered; all built-in interactive
commands work; <16 ms/frame; `docs/compare/sprint-2.md` = the frame-diff report.
**Scope (spec sheets):** old WPs 410–460 (integrate existing side-ref work first).
Open with: F12 render-golden extraction for every component the sprint touches, RED.

**M1 + M2 + M3 together = CORE COMPLETE: pi, correct, all tests passing. Everything after this is
breadth.**

## Sprint 3 — Expansion: study, then build (closes M4)

**Opens with `docs/plan/expansion-study.md`** — a decision memo for the owner: which of the ~20
compat providers actually matter (usage data, effort each), MCP scope (settings surface, transports),
pi-packages value, bridge fidelity targets vs the example matrix, with a recommended cut. Surface it
in PROGRESS.md. While the owner reviews, proceed with the parts certain under any outcome: the JS
bridge core (old WPs 510, 520, 530 — runtime, API bindings, node shims). If no owner amendment
arrives by the time bridge core is green, continue with full-parity defaults.
**Definition of done (full-parity defaults, study may amend via DECISIONS):** every upstream
provider except Radius resolves; codex shape + ChatGPT/Codex, Copilot, xAI OAuth verified; MCP
round-trips; packages + trust work; ≥80% of upstream single-file extensions run unmodified (F11
matrix); hello, todo, pirate, permission-gate, status-line, modal-editor end-to-end;
`docs/compare/sprint-3.md` = the matrix + per-provider comparison.
**Scope (spec sheets):** old WPs 241, 270, 352, 360, 510, 520, 530, 541, 542 (G3), 550.

## Sprint 4 — Ship (closes M5)

**Definition of done:** all M1–M4 re-verified at the release commit; one full sync cycle against a
fresher upstream commit with green lock bump; goreleaser artifacts for 4 targets + install script
verified; live suite ≥90% over trailing **72 hours**; docs newcomer path verified; final trim with
LOC/dep audit; `v0.1.0` tagged.
**Scope (spec sheets):** old WPs 610, 620, 650, 661 (G4).

## Sprint 5 — Chat gateway (D27)

**Definition of done:** `chat/`, `chat/telegram/`, `chat/whatsapp/` land per D27 with plain
`go test` coverage (never `conformance/` — F-families are upstream-extraction-only): the
processor's turn ledger green across every crash boundary (replay tests for
started/settled/delivered markers, duplicate inbound events, orphaned-branch recovery, resend
without re-prompting after settled-not-delivered); Telegram adapter verified against a
deterministic fake Bot API server (webhook secret-token auth + long-poll ingress, coalesced
preview edits with flood-control backoff, 4096-unit chunking, media, group mention gating,
`/new` `/stop` `/status` `/compact`); WhatsApp adapter verified against a fake Cloud API server
(hub challenge, HMAC signature validation, mark-read + typing, media, delivery-status
reconciliation); the `AgentSessionOptions` tool-operations hook landed with tests; tools off by
default; `CGO_ENABLED=0 go build ./...` and `go test -race ./...` green with zero new
dependencies; 1,000 concurrent faux turns pass race-clean and idle conversations retain no
resident actors or goroutines. Behavior is cross-checked against the reference implementations
(earendil-works/pi-chat, Hermes gateway docs); deliberate differences noted in
`docs/compare/sprint-5.md`.
**Scope:** D27 only. No new deps; both platform clients stdlib HTTP/JSON per D10.
Open with: processor + ledger recovery tests against the faux provider and in-memory sessions, RED.

## Sprint 6 — Chat platform wave 2 (D28)

**Definition of done:** `chat/slack/`, `chat/teams/`, `chat/discord/`, `chat/messenger/`,
`chat/googlechat/` land against the existing `chat` contracts with deterministic fake-server
tests per adapter (signature/auth rejection, ingress normalization, delivery sequences,
chunking goldens, error-code policy); `chat/internal/wsclient` (hand-rolled RFC 6455 client:
handshake, masking, fragmentation, ping/pong, close codes) with protocol tests against a fake
websocket server, plus Discord Gateway session logic (hello/heartbeat/identify/resume,
message_content intent) tested against a scripted gateway; shared webhook-signature helpers
extracted to `chat/internal/` and adopted by the existing adapters without behavior change;
streamed previews where the platform supports edits (Slack, Discord), final-only elsewhere
(Teams, Messenger, Google Chat); zero new go.mod dependencies; `go test -race ./chat/...`
green; docs/chat.md extended and `docs/compare/sprint-6.md` records reference cross-checks and
deliberate exclusions (bridge platforms, E2EE Matrix). Behavior referenced against Hermes
per-platform docs and pi-chat's Discord implementation.
**Later waves (recorded, not scheduled):** Instagram DM (Graph, near-free after Messenger),
Line, Twilio SMS/RCS, Mattermost, Rocket.Chat, Zulip, IRC (stdlib TCP, trivial);
KakaoTalk/WeChat access-restricted; Signal/iMessage/personal-WhatsApp/E2EE-Matrix stay out per
D27/D28.
Open with: the wire briefs + wsclient protocol tests against the fake server, RED.

## Ambition setting

Each working session aims to CLOSE a sprint, and must at minimum leave main green, fixtures green,
and the sprint's RED surface measurably smaller. No schedule estimates anywhere — progress is
measured only by red-to-green movement and closed milestones. Blockers only the owner can clear
(credentials, remotes, hosts) are surfaced in PROGRESS.md and worked around, never waited on.
