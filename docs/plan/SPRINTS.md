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
   scripted scenarios through TS pi (`.upstream/`) and orb, every difference fixed or ledgered.
4. **Close = trim + criteria.** Sprint close: trim checklist (RELEASE-CRITERIA), milestone boxes
   checked, comparison report committed. No separate trim WPs.
5. Hard rules from AGENTS.md unchanged: byte-compat kernel formats, dependency table, never weaken
   a golden, pure Go.

## Sprint 0 — Consolidate (first, before anything else)

Integrate every existing side ref (wp352, wp360, wp370, wp390*, wp450*, wp520, wp530, wp661, and
any GitButler lane state) into `main`, verifying each integrated commit builds; delete the refs;
note the two historical non-building snapshots (2a8ac08, 68c3af) in PROGRESS.md. Work already done
for expansion surfaces (e.g. MCP, bridge, packages) is integrated and kept — it simply isn't
*extended* until Sprint 3. Rewrite PROGRESS.md as a sprint checklist. Commit the restructured plan
docs. From then on: single branch.

## Sprint 1 — Core headless correct (closes M2)

**Definition of done:** upstream's RPC test suite passes against `orb --mode rpc`; F7/F8 green;
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

## Orb Bridge v1 — native delivery completed

Delivered the full native v1 contract in ARCHITECTURE, including scoped discovery and agent calls,
with one executable and TUI administration. SDK APIs and unrelated progress entries are preserved.
Verification and release measurements are recorded in PROGRESS (2026-09-21).

1. Measure the pinned stream-only Tailcat assembly on all four release targets against the
   55 MB and 50 ms budgets; record dependencies and retain the binding limits. Define versioned
   method schemas and protocol vectors independently of Pi fixtures.
2. Add non-owning runtime attachment and shared local/extension/remote control boundaries.
3. Add durable native storage, separate admin/attachment IPC, in-process registration, fencing,
   credential isolation, profile locks, and bounded reconnects.
4. Add strict framed RPC, TLS identity pinning, recoverable pairing, grants, and revocation.
5. Map safe commands with durable receipts, crash recovery, atomic snapshots, and bounded replay.
6. Add signed scoped reconciliation and separately opt-in agent calls with both grants required.
7. Assemble `orb bridge`, built-in Settings/Ctrl+P management with explicit service activation,
   and the remote conversation view. Agent calls are an option inside Bridge, not a second UI plugin.
   The owner-requested simplified pairing confirms full mutual conversation trust; SSH setup
   locates or installs a compatible Orb and uses Bridge after bootstrap.

Each slice starts with failing tests and commits only after `make check`. Closure requires
20-instance restart/grant tests; local/remote races; persistence fault injection and quotas;
pairing/authority/revocation/parser tests; snapshot/backpressure/fairness tests; A-B-C scoped
reconciliation and agent-grant tests; real CLI/TUI exercise; native direct/relay transport checks;
unchanged SDK consumers with no Tailcat imports; portable core Wasm compilation; Pi/RPC gates;
four static builds and final size/startup measurements. Exclude the restricted launcher, mobile
apps, browser transport, and platform-specific hosting. Record evidence in PROGRESS, SDK and
architecture docs, and CHANGELOG; no per-change report files.

## Unified conversations and native SQLite — implementation plan (2026-09-21)

Owner requested a single session picker grouped by machine, including creation/resumption on
remote hosts, and a full native SQLite migration. Owner explicitly accepted SQLite-native storage
with Pi JSONL import/export instead of live shared Pi files. This is a new delivery after Bridge
v1: the restricted native launcher, previously excluded, is necessary here. This section is a
plan, not a claim of implemented behavior. No release budgets or SDK contracts are relaxed.

### Product contract

- One Sessions surface for startup, `/resume`, and the command palette: This computer, then
  connected machines by human-readable name. Identity uses PeerID, never the display name.
  Show saved and active conversations with title, workspace, activity, and execution/connection
  state; current-project/all-project filtering and search work across authorized sources.
- Local results arrive immediately; remote sources load independently with bounded concurrency,
  pagination, cancellation, and visible per-machine failures. Keep selection stable by identity.
  Offline machines remain visible; never claim an unavailable runtime has stopped.
- Each machine offers New conversation. Select a configured workspace and server-side preset;
  use the last valid selection as the default. Credentials and tools belong to the destination.
  An unconfigured host explains what is missing rather than opening an unusable conversation.
- Opening an active conversation attaches to its existing runtime. Opening a saved conversation
  acquires exclusive ownership and starts a runtime if needed. Switching the viewed conversation
  never switches another user's runtime or interrupts background work. Multiple views share the
  same execution; separate conversations can execute concurrently.
- Managed workers stay bound to one conversation. New/fork creates another conversation; switch
  changes the client's view. Keep existing instance-level new/switch/fork semantics for standalone
  and SDK callers; do not silently change `orb.instance/1`.
- Closing a view only detaches. Cancel stops the current execution. Idle managed workers may exit
  after a defined idle period, provided no execution, approval, queued work, or viewer remains.
  Archive removes a conversation from the default list without deleting history. Deletion is an
  explicit confirmed action, refused while execution/ownership makes it unsafe.
- Bridge settings manage devices, pairing and hosting configuration. Conversation navigation
  leaves that menu. Preserve the focused remote view, destination/path footer, keyboard/mouse
  navigation, and operation without local provider credentials.

### Layer and storage decisions

| Layer | Responsibility |
|---|---|
| Existing `agent/session` and harness | Session tree, compaction, forks, replay, codecs; reuse the runtime |
| SQLite adapter | Explicit DB handle, schema/migrations, transactional repositories; no networking or daemon startup |
| Native host capability | Catalog, ownership and managed worker lifecycle; composed beside Bridge, not inside its router |
| `connect` / `connect/agent` | Versioned conversation service plus existing non-owning runtime control/observations |
| `bridge` | Authenticate, authorize and route; no session engine, SQL or process construction |
| CLI/TUI assembly | Open storage, assemble capabilities and select local/remote views |

Use one `orb.db` per explicit local state root (normally one per OS user per machine), with
profile namespaces. Different security domains use separate roots/OS identities; a namespace
is not a sandbox. Never share this file across machines or replicate complete remote transcripts. The owner
approved a separate disposable cache of remote summaries and bounded user/assistant excerpts
(2026-09-21); origin remains explicit and reopening always revalidates against the destination.
The picker aggregates catalogs over Bridge; the destination remains authoritative. WAL/SHM files,
IPC sockets, process locks, logs, and explicit backups are not competing databases.

SQLite owns native mutable state: conversations and entries; names, branches and working
directories; runtime ownership; operation receipts/tombstones and launch intents; Bridge identities,
transport keys, peers, grants, contacts and withdrawal floors; attachment state; Orb-managed
global settings, model overrides, auth, trust and keybindings; memory and enabled chat-gateway
delivery state. Keep layer ownership through narrow existing store contracts even when their
data shares a database. Do not replace typed data with one ever-growing serialized state blob.

Source resources stay files: project configuration, AGENTS.md, skills, extensions, themes,
workspace files, package artifacts and exports. Project overrides keep existing precedence and
trust behavior. Native global preference writes go to SQLite; project-scoped edits remain in the
project. Global file interoperability becomes explicit import/export, never silent two-way sync.
External secret providers remain references rather than copied credentials.

Use indexed session metadata plus canonical JSON entry payloads with explicit order, tree links,
leaf and revision. Preserve unknown fields and supported v3/v4 behavior. Avoid normalizing every
provider payload into SQL tables. Reuse existing repository seams; add only the product-session
persistence seam actually needed by the existing file and SQLite implementations. Preserve public
constructors, method sets and file-backed SDK defaults; CLI assembly explicitly selects SQLite.
File-oriented API fields/methods must retain truthful semantics: audit sessionFile/parentSession,
extension session managers, exports, --session/--session-dir and environment overrides before
cutover. Never pass a fabricated SQLite URI to a caller expecting a real file. Retained explicit
file-backed compatibility entry points are adapters, not a mirrored native source of truth.

### Ordered implementation slices and acceptance gates

1. **Record contracts and prove packaging feasibility.** Amend DECISIONS and ARCHITECTURE for the
   owner-approved storage boundary; inventory every persistent writer and every public file-path
   assumption. Specify additive service methods for catalog/list, create, open, rename, fork,
   archive/delete and ownership/status, with identity, paging, authorization and retry vectors.
   Identify conversations by destination identity + storage namespace + SessionID; keep runtime,
   generation and execution IDs distinct. Probe a maintained CGo-free SQLite driver (start with
   modernc.org/sqlite) in the actual Tailcat-enabled binary before choosing/pinning it. Check its
   embedded SQLite release for known WAL/corruption fixes. Build darwin/linux × amd64/arm64;
   retain 55 MB and 50 ms release gates. Measure populated-database startup separately from
   --version. If no supported assembly meets the gates, surface the measured blocker before
   committing to that driver, without changing the limits.
2. **Land transactional storage and recovery tests.** Add versioned schema migrations, indexes,
   foreign keys, WAL, FULL durability for admitted work, bounded busy waits and short transactions.
   Configure/verify connection pragmas on every connection. No SQL transaction spans a model
   stream, tool, approval or network wait. Use revision checks and fenced ownership updates rather
   than last-write-wins. Keep one Bridge service owner per profile while allowing independent
   runtime processes to use SQLite concurrently. Test two-process contention, stale ownership,
   quota/disk-full failures, corruption, migration crashes and unsupported schema versions.
   Checkpoint and backup through SQLite-supported operations; never copy a live DB file alone.
3. **Migrate the existing native persistence completely.** Import session headers/trees, memory,
   settings/auth and Bridge/attachment/operation state while preserving IDs, keys, grants,
   counters and deduplication tombstones. Enumerate configured custom roots, not just defaults.
   Require old Orb writers to quiesce, lock sources and verify fingerprints so a changing source
   cannot be declared migrated. Journal source identity/digest and progress for restartable,
   bounded-batch import; validate counts, payloads and tree references before publishing the
   cutover marker. Report duplicate IDs/conflicting sources and damaged files without overwriting
   or silently skipping them. Keep original files as untouched recovery backups; don't delete
   them automatically. A failed import leaves the previous storage usable; after native writes,
   rollback requires export of new data, not reopening a stale backup. Old binaries must not be
   used against the migrated native root. Provide verified backup/restore and JSONL import/export;
   account for Bridge revision/receipt rollback so restoring stale authority never replays work.
   Private DB, WAL, backup and parent-directory permissions protect included secrets; redact them
   from diagnostics. Migrate optional capability state when enabled without resetting it.
4. **Introduce the native conversation host.** Reuse AgentSessionRuntime in workers of the same
   Orb executable; no second agent engine or general scheduler. Assemble host management with
   the existing native service and keep workers independent of Bridge/client lifetimes. The host
   can start headless without a provider locally on the viewing client. Configure workspace roots,
   server-side presets, concurrency/resource limits and provider availability locally. Never accept
   remote shell strings, arbitrary argv/environment or unvalidated filesystem paths. Existing
   foreground instances register their ownership so opening their active session attaches rather
   than spawning a second writer. Legacy session switches atomically update ownership/catalog.
   Persist create/open intent before spawn, correlate authenticated worker registration with a
   stable launch token, and fence every session mutation. Reconcile an uncertain spawn before
   retrying; a bare PID or expired heartbeat never authorizes a duplicate active executor. On lost
   ownership the old worker must stop dispatching work. After a crash mark interrupted effects
   outcome_unknown; do not replay tools or prompts automatically. Test concurrent opens, crash
   windows around spawn/registration, worker exit, host restart and Bridge restart during work.
5. **Expose authorized conversation operations.** Advertise the additive host service through
   existing negotiation; old peers keep active-instance access and show hosting as unavailable.
   Catalogs filter before paging; opaque cursors must not leak unauthorized sessions. Resolve all
   session/workspace IDs on the destination. All mutations carry operation IDs and target
   preconditions, persist acceptance before dispatch and retain retry protection. Launch/manage
   permission is separate internally from instance control and agent-call authority. New full-trust
   pairing can include hosting when locally enabled and disclosed; existing trust needs a local
   explicit enablement, not a silent grant expansion. Bridge administration remains local. Revoking
   access immediately terminates visibility/control without killing destination-owned work.
   Persist approvals against exact execution/action identity; reconnecting must not approve them.
6. **Unify the session UI.** Extend the existing selector and loaders; don't create a second session
   browser. Add machine sections, progressive loading/search, stable selection, pagination and
   per-source errors. Route selection to local attachment or the existing remote view using typed
   references, not string-encoded paths. Wire create/open/fork/rename/archive/delete through the
   same destination service and permission checks. The picker remains usable while another
   conversation streams. Keep drafts/view state per conversation, and resnapshot after cursor
   expiry or ownership changes. Remove duplicate conversation navigation in Bridge settings.
   Exercise empty hosts, offline hosts, missing credentials/workspaces, old peers, duplicate names,
   multiple viewers, narrow terminals, and reconnects; regenerate Orb-owned render snapshots.
7. **Verify delivery and remove superseded native paths.** Run migration fixtures for all supported
   session versions and unknown entries; Pi JSONL round-trips, SDK consumer builds, RPC/provider/
   extension conformance; negative privilege/catalog tests; receipt and launch fault injection;
   concurrent local/remote create/open races and bounded observation tests. Exercise at least 20
   active conversations plus a large saved catalog with pagination. On lab-3 and edge use isolated
   test roots to verify real TUI creation/resume, background execution after disconnect, device
   revocation, direct/relay transport and restarts without touching personal sessions. Run
   `make check`, four static release builds, startup/size/memory and WAL-growth measurements;
   confirm SDK-only builds import no SQLite driver/Bridge/Tailcat unless explicitly selected and
   portable core still compiles for Wasm. Remove obsolete native JSON writers and scans after
   cutover; keep only explicit compatibility codecs/backends and migration readers. Update
   ARCHITECTURE, SDK documentation, CHANGELOG and PROGRESS with verified evidence. Every commit
   is coherent and green on main; no separate report files, automatic release or deployment.

**Definition of done:** starting with an empty paired server, a user creates and resumes remote
conversations entirely from Sessions, runs multiple conversations concurrently, reconnects without
losing ownership/history, and upgrades existing data without re-pairing or losing deduplication.
Native application state has one SQLite authority per local root; there is no live JSON shadow
store, mandatory local daemon for offline Orb, or cross-machine database. SDK/file compatibility
continues through explicit existing entry points and verified import/export.

SQLite design references: https://sqlite.org/wal.html (single local writer, WAL sidecars and
durability), https://sqlite.org/backup.html (consistent backups), and
https://pkg.go.dev/modernc.org/sqlite (candidate CGo-free driver; selection remains measurement-gated).

## Ambition setting

Each working session aims to CLOSE a sprint, and must at minimum leave main green, fixtures green,
and the sprint's RED surface measurably smaller. No schedule estimates anywhere — progress is
measured only by red-to-green movement and closed milestones. Blockers only the owner can clear
(credentials, remotes, hosts) are surfaced in PROGRESS.md and worked around, never waited on.

## Claude Sessions — owner-requested isolated executor, 2026-09-21

Implement the native session adapter described in ARCHITECTURE, preserving existing SDK defaults.
Hermes is a research reference, not a transport/authentication implementation to port. Acceptance:

- Whole-turn executor seam preserves event ordering, cancellation, concurrency and SDK isolation.
- Official SDK and unmodified Claude executable own login, sessions, tools and native permissions.
- Explicit native IDs/checkpoints cover create, restart/resume and fork without history replay.
- Generic input replies use the same execution fences and durable Bridge receipts as other controls.
- `/claude` management and explicit provider selection work without Orb provider credentials.
- Hermetic process/Bridge tests, opt-in native account checks, race gate, static builds and docs.

## Portable core — plan (owner direction, 2026-09-22)

Constitution: P2 (tier-1 targets) and P10 (portable core behind host ports). Reference study:
`deepseek-ai/deepseek-harness` validates provider seams for fs/exec and a layered tool-permission
pipeline, and shows what to avoid: string-keyed services, implicit load order, dynamic loading,
and emulating Node in the browser instead of supplying native providers. Each slice opens RED on
its gate and closes GREEN; `make check` stays green between slices because ratchets only shrink.

1. **Gates.** `make portability` (part of `make check`): `go build` + `go vet` for every P2
   target, test binaries compiled for each, `js/wasm` tests executed under Node and `wasip1`
   tests under a pure-Go runtime. CI adds a `windows-latest` job running the full suite. A
   core-purity test in `internal/layering` lists the core packages and forbids `os/exec`,
   `os/signal`, `syscall`, `net`, `http.DefaultClient`/`DefaultTransport` and implicit
   `os.Getenv`/`LookupEnv`/`Environ`/`UserHomeDir`/`Getwd`, with today's violations recorded as a
   ratchet that may only shrink. Size budgets per target, including the Worker bundle.
2. **Native everywhere.** Windows (Git Bash discovery, process-tree kill, console modes),
   32-bit linux (iSH), android/arm64 (Termux) build and pass; Windows runs the whole suite in CI.
3. **Ports.** One `host` seam: `FS` (the upstream-shaped `harness.FileSystem`), `Exec`
   (`harness.Shell`), `Store` (documents, append-only session logs, locks), `Net` (HTTP client,
   optional listener), `Env` (variables, agent/session directories, clock, randomness). The
   per-tool upstream `*Operations` become adapters over `FS`/`Exec`, so a platform implements
   ports only once. Remove process globals: `http.DefaultClient` package variables, the
   `init()` default stream, the closed provider `switch` (becomes a registry, which also lets a
   light assembly link only the providers it selects), the global extension host. Settings,
   auth, sessions and resource discovery read documents and directories through `Store`/`Env`.
   `platforms/native` implements every port for unix and windows. A shared port conformance
   suite (the `fstest.TestFS` pattern) runs against every implementation on its own target.
   Status 2026-09-22: `host.Host` (AgentDir, FS, Exec, Store, Sessions) is accepted by
   `NewAgentSession` and `CreateAgentSessionServices`; settings, credentials, model catalogs and
   session journals then come from it. `platforms/scenario` runs one scripted turn through a Host
   natively, in `js/wasm` without a host filesystem and under WASI without mounts, and requires
   identical output. Measured weight: a full `AgentSession` is 12.0 MB gzip on `js/wasm` against
   6.4 MB for the engine-only browser runtime. The two structural causes are the closed provider
   switch in `ai/api` (every provider SDK links) and `agent` → `agent/modes/theme` (syntax
   highlighting and CJK segmentation tables in the core). The theme edge is cut (theme files
   parse in `internal/themefile`; `TestProductCoreIsHeadless` guards it): a full AgentSession is
   now 9.46 MB gzip, gated at 10 MB. Providers register through `api.Registry` (`ai/api/all`
   is the full set; Bedrock's AWS SDK links only when selected; `orb_nodefaultproviders` brings a
   stream-supplying session to 7.33 MB gzip). RPC mode is the headless `agent/rpc` and runs over
   in-memory pipes on both Wasm runtimes. Still open: project settings, resources and theme
   discovery over `FS`, grep without ripgrep, and the Worker assembly with its own budget.
4. **One assembly.** `agent/assembly` takes a host and resolves rows by the ports each needs;
   `cmd/orb`, `cmd/orb-wasm`, `chat`, subagents and Claude sessions all compose through it. The
   Wasm runtime's hand-written model selection disappears in favor of the catalog.
5. **Hosts.**
   - Browser (`js/wasm` worker): OPFS files, IndexedDB store, Fetch network, no `Exec`.
   - Worker (`js/wasm`, one host for Cloudflare Workers, Durable Objects and Celld): a module
     shim exports a Durable Object class that owns one Orb instance; Durable Object storage is the
     `Store`, a virtual `FS` lives on it, Fetch is the network, no `Exec`. Light assembly under a
     measured compressed-size budget.
   - WASI (`wasip1`): preopened directories as `FS`; outbound HTTP through a host import when the
     runtime provides one, otherwise no `Net`. Proven under a pure-Go runtime in tests.
   - Android: the static `android/arm64` CLI under Termux; the standalone app embeds the core as a
     gomobile library and runs `Exec` natively (binaries shipped in the app's native library
     directory). Termux itself stays external (GPLv3): interop, not embedding.
   - iOS: the standalone app embeds the core as a gomobile library; `Exec` is optional and is
     backed by WASI commands run in-process (a-Shell's model, pure Go), since App Store apps cannot
     spawn processes. The `linux/386` CLI runs inside iSH today; embedding iSH (GPLv3, x86
     emulation) is not the app's execution model.
6. **Cross-host conformance.** Scripted scenarios over the faux provider run on every host and
   must produce identical event JSON and session JSONL; kernel fixtures are reused, never forked.

## Deployments — plan (owner direction P11, 2026-09-23)

Orb deploys anywhere as a full runtime and every deployment is a Bridge peer. The catalogue and
its evidence rules live in `docs/deployments.md`; a target moves up only with its gate green.

1. **Durable Objects and Celld** (in progress): `platforms/worker` host on Durable Object
   storage, RPC frames over WebSocket/HTTP, end-to-end under `cf dev` and `celld dev` in CI, one
   deployed test instance, one-command deploy and removal.
2. **Durable Object as a full Bridge peer**: accept the WebSocket Bridge transport on `/bridge`,
   so laptop Orbs pair with a cloud Orb and grants work in both directions.
3. **Windows parity**: the `windows-latest` job green and blocking again, Windows release
   artifacts.
4. **Richer hosted tools**: project settings, skills and context files over `FS`; grep without
   ripgrep (a pure-Go search over the FS port), so hosts without `Exec` keep search.
5. **Android (Termux) and iSH**: validate on devices, then add `android/arm64` and `linux/386`
   release artifacts.
6. **WASI**: an entry program and an HTTP host import.
7. **iOS and Android apps**: gomobile library driven by RPC frames; iOS `Exec` through in-process
   WASI commands.
