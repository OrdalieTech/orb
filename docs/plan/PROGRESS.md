# Implementation progress

## Release 0.9.0 preparation — 2026-09-22

Minor version for optional Claude Sessions, shared questions, rule-based permission auto mode and
moved Go capability imports, alongside streaming/allocation optimizations. Pi stays pinned to
0.86.0; go.dev confirms Go 1.27.1 remains the latest stable toolchain. Release notes explicitly call
out auto versus manual approval and import-path migration; existing explicit permission modes and
stored plugin IDs are retained.

Linux CI run 35713302768 exposed a callback response/connection-close race and an unreliable tiny
allocation used by the selector finalizer test. OAuth now drains active responses with a bounded
graceful shutdown; its regression reads the full success page. The lifetime probe exceeds Go's
tiny allocation size without changing its deadline or upstream expectations. Both regressions pass
50 race repetitions. The full Linux amd64 suite then reproduced a separate retention path in
CI run 35719032801: stopped AfterFunc callbacks can remain rooted until runtime timer-heap cleanup.
A standard-library weak pointer keeps the status callback from owning the selector; the identical
full Linux amd64 race suite passes with that change. The lifetime assertion and 750 ms deadline
remain intact. No fixtures or budgets were weakened.

Static versioned candidates: darwin/amd64 54,508,256 B, darwin/arm64 52,053,154 B,
linux/amd64 53,510,304 B, linux/arm64 50,725,024 B. All are below 55 MB. On Apple M4,
20 warm-cache samples give 12.04 ms median `--version` and 15.40 ms `--help`. An isolated macOS
candidate passes native migration, original preservation, JSONL export, private backup, restore,
SQLite integrity and legacy-root refusal. Release automation checks all packaged sizes and Linux
startup, archive checksums, a source rebuild without Git, and successful hosted CI before publishing.
Provider/OAuth and real-terminal live-test deferrals remain unchanged.

## Permission hardening — 2026-09-22

Owner-requested review reproduced cross-tool approval reuse, allow-on-dismissal/headless requests,
authorizer bypasses of guards, missing operation details, symlink/multi-target rule escapes and
passive native-hook approval. Regression tests now cover each. Enabled policy defaults to enforce;
explicit consent is distinct from a nonblocking Go hook without changing extension JSON. Approvals
are scoped, bounded and cleared on mode changes; audit entries omit duplicate tool payloads.

Native containment uses existing tool-operation injection and stdlib `os.Root`, covers built-in
write/edit plus integrated and external subagents, and survives disabled extensions. No new dependency
or host framework. SDK hosts select the native adapter explicitly; VFS hosts retain their own operations.
Claude Sessions refuses Orb sandbox configurations it cannot enforce. Command rules remain textual;
reads/network and trusted extension or direct user shell operations are outside these filesystem limits.

Validation: targeted race regressions, live macOS bash/file/external-child containment, static Linux
CLI and selected JS/Wasm compilation. Real Claude SDK 0.3.278 through paired in-process Bridge passed
explicit/automatic consent, denied Read, questions, resume, fork and cancellation (606 ms). A separate
real Claude test confirmed audit mode preserves native Write approval and dismissal creates no file.
Its initial fixture prompt caused the model to decline before invoking a tool; the final prompt explicitly
authorizes the owned temporary test path, with approval/no-write assertions unchanged. This is not a
fresh Linux kernel or Tailcat network test. Full `make check` passes build, vet/lint, race and conformance.

The active sequence is `SPRINTS.md`; the old work-package numbers are historical spec references
only. Progress is measured by conformance surfaces moving from red to green and by milestone
criteria closing.

## Inline questions and mouse control — 2026-09-22

Hover now highlights choices, tabs and Continue/Submit without changing answers or moving the
layout. Leaving clears hover; repeated motion over one target avoids redundant invalidations.
The hover regression and full `make check` pass.

Questions now use the existing non-overlay `UI.Custom` composer replacement, which restores the
editor and draft on completion or cancellation. The plugin panel keeps space for the transcript;
choices, tabs and Continue/Submit accept a single click, while drag gestures cannot answer.
Bridge forwards mouse events and handles history paging before question input. No core API changed.
Targeted question/native-tool/remote-view race tests pass. A real Claude PTY produced a long
transcript, displayed the inline question, repainted history on PageUp while it remained pending,
and resumed after the answer into the saved transcript. The full `make check` passes, including
lint, race tests and conformance.

## OpenCode-inspired question panel — 2026-09-22

Restyled the shared `plugins/questions` panel with an accent rail, numbered choices, descriptions
beneath each option, custom input shown on demand, question tabs and a review step for batched or
multiple-choice answers. Number keys, arrows, Tab, custom-edit Escape and mouse selection remain
plugin-local. Existing runtime and Bridge contracts are unchanged. Regression checks cover review
before submission, custom answers, back navigation, narrow layouts and visible remote controls.
The Questions, Claude Sessions and CLI race suites pass. A real Claude PTY displayed the new panel,
accepted a selection and resumed into the saved transcript. `make check` built and vetted but was
blocked by concurrent reorganization lint errors in oauth_selector, bridge imports and unused
subagents/tasks test helpers; those unrelated edits were preserved.

## Shared Questions and Claude permissions — 2026-09-22

Added the default-off Questions tool and one shared panel for Orb, Claude and remote views, with
validated structured replies, custom answers, multiple selections and back navigation. Claude's
former question UI was removed. Core carries opaque presentation data through existing runtime
input; Bridge transport has no provider-specific behavior. Claude native tool hooks now consult
the existing optional Permissions plugin, including rules, approval reuse and audit.

Verified the native Orb tool with a faux provider, invalid/stale replies and narrow panel rendering.
A real Claude SDK 0.3.278 run over paired in-process Bridge covered native Write approval, automatic
allow, denied Read, question/reply, resume, fork and cancellation (505 ms). An isolated real PTY
confirmed the shared choices display and selecting one resumes Claude and persists its transcript.
This is not fresh Tailcat network proof. The final `make check` passes, including race and
conformance. Remote viewport regression keeps the panel visible below long transcripts; oversized
answers remain editable. The static Darwin arm64 release-shaped build is 51,952,930 bytes.

## Native resume verification — 2026-09-22

The reported conversation remains in SQLite with 19 messages / 56 entries. The installed 0.8.0
binary found it during verification but lacked the Claude executor and attempted provider fallback;
the earlier legacy lookup error was not reproducible with that installed binary. Updated the local
executable to the checked current build, retaining the previous binary. Actual PTY startup restored
the conversation as Claude using both its full ID and prefix, without sending a prompt or changing
any saved entry. Extended the migration/restart regression to verify both forms still use SQLite
when the corresponding legacy backup is corrupt. `make check` passes; no lookup fallback or new
storage path was added.

## Claude interactive questions — 2026-09-22

The TUI bound its extension runner directly and left the runtime's stored UI at its placeholder.
Runtime input therefore canceled without showing a dialog. Both initial and replacement TUI
binding now use the existing `BindExtensionUI` seam; no Claude condition or schema entered core.
The plugin preserves question descriptions/previews, sequential questions, custom answers and
multi-selection, using the same generic input/reply path locally and through Bridge. Dismissal
returns a native denial; global cancellation still interrupts the SDK. Native question, plan,
task and file/command summaries use the existing plain-text tool renderer, owned by the plugin.
The CLI does not modify the extension registry, so presentation cannot leak into ordinary sessions.
SDK UI hosts can supply their own text component through the plugin options.
A generic visible-runtime-input regression and a real Node SDK-host question test cover the lost
UI binding, custom text, selection/deselection, answer mapping and renderer registration.

Verified `make check` (build, vet, lint, race, conformance, layering and pure-Go checks). A real
Claude Sonnet PTY session displayed the question, accepted a selection, resumed with the expected
answer, and saved the tool/result projection in SQLite. A live native SDK test also paired two
in-process bridges, answered `AskUserQuestion` through authenticated `input.reply`, then verified
native Write approval, model switching, resume/fork and cancellation (706 ms). This exercises
Bridge authorization/routing and the real SDK, not a fresh Tailcat network traversal.

## Claude model discovery and session identity — 2026-09-22

Replaced the one-model closure and hardcoded Sonnet default with the official SDK `supportedModels()`
control, queried without a prompt or persistent native session. The executing CLI supplies model
aliases, resolved IDs and effort capabilities; Orb's normal picker receives their adapted forms.
Unknown explicit IDs remain explicit, existing session selections are retained, and changing the
executor in place is rejected. Native effort/thinking options now follow the selected Orb controls.
The page has New Claude session, Model and (while in Claude) Switch to Orb; launch-default
toggles are removed. Exit preserves the native conversation and opens a regular session, including
when no Orb provider is configured. A plugin-owned marker handles explicit executor exit without
changing Pi metadata-only session restoration. The existing extension status hook labels Claude
sessions, including the native model reported
on completion. Generic Bridge descriptions expose safe display metadata; `/models` and
`/model <provider/id> [effort]` use `session.model` under existing session-management authority,
serialized admission and durable receipts. No Claude-specific Bridge dependency was added.

The native account probe returned five models with Opus as the default, then exercised a Sonnet
turn with low effort, a switch to Haiku, resume/fork, explicit Write approval and cancellation.
Hermetic tests cover catalog adaptation, explicit canonical IDs, switching without Orb credentials,
effort delivery, executor isolation and remote selection through the operation ledger. The actual
PTY TUI exposed the native catalog, showed the Claude footer and switched to a saved regular
session with no Orb provider credentials. The final live model-switch probe verified the returned
assistant model was Haiku, retained fork context, and cancelled in 608 ms. `make check` passed,
including normal RPC behavior and unchanged metadata-only session conformance. Four final static
builds passed the 55 MB cap: darwin/arm64 51,901,186 B, darwin/amd64 54,345,168 B,
linux/arm64 50,528,416 B and linux/amd64 53,346,464 B.

## Claude first-use setup — 2026-09-22

Starting a Claude session now installs the pinned SDK automatically on the execution host; the
same preparation path serves TUI, direct CLI and Bridge-hosted runtimes. Setup uses the existing
cross-process lock, has a two-minute installer deadline, and retries failed installations without
mistaking a partial SDK entry point for success. Custom SDK paths remain explicit. The management
page keeps New session, Model and one current default toggle; no separate Install SDK action.
Node/npm and native Claude sign-in remain host prerequisites. Regression covers failure, retry,
reuse and a missing custom SDK path. A clean temporary profile also downloaded the official SDK
and completed two runtime configurations, proving first-use setup and subsequent reuse.
`make check` passed, including lint, the full race suite and conformance.

## Claude Sessions — isolated optional SDK loop, 2026-09-21

Reviewed Hermes DirectSDK at c92c27c9f919178a58974a72333b473c6cb2e71d and current Anthropic
SDK, sessions, permission and legal documentation. Hermes adapts model calls with transcript replay
and an admission proxy; Orb instead delegates complete native sessions to official SDK 0.3.278 and
the user's unmodified Claude executable. Authentication remains entirely in that executable.

`claudesessions` owns the Node host, native checkpoints, event translation, configuration and
permission adapter. The optional engine session-loop seam and execution-bound input requests are
vendor-neutral; Bridge routes ordinary controls and input replies without a Claude dependency.
The native Settings/palette page is available while execution remains opt-in. SQLite stores the
Orb projection; native transcripts remain necessary for resume. No new Go dependency was added.

Verification: full `make check` passed (build, vet, lint, race and conformance), ordinary SDK examples
and an external unchanged SDK consumer built without Claude/Bridge/Tailcat imports, and portable
connect/protocol/Bridge compiled for Wasm. Tests cover stream/tool translation, native checkpoints,
new/switch/fork, stale and single-use input, cancellation and independent instance execution.
Live SDK tests with Claude Code 2.1.273 exercised create/resume, paired in-process Bridge routing,
an explicitly approved native Write, fork recall and cancellation (612 ms). This proves the plugin
through Bridge contracts, not a fresh Tailcat network traversal. An actual CLI print run and PTY
command-palette → new Claude session produced verified assistant messages in isolated SQLite.
The PTY harness's second cleanup interrupt hit an already-closed terminal; the persisted response
was checked separately, and a fresh Orb process resumed the SQLite session and recalled its exact
previous token. No existing user sessions or credentials were copied or modified.

Four static release-target builds remained below 55 MB (50.53–54.33 MB). Warm darwin/arm64
`--version` startup measured 21.74 ms median over 22 samples; this is local process startup, not
native Claude cold-start latency. The optional SDK starts no subprocess while disabled or idle.

## Unified conversations and SQLite — native cutover, 2026-09-21

The owner requested unified local/remote Sessions navigation and native SQLite, explicitly
accepting Pi JSONL import/export instead of live file sharing. SPRINTS now contains the complete
sequence: compatibility/driver feasibility, transactional storage, resumable migration, native
conversation hosting, authorized service contracts, unified selector, and fault/live/release gates.
DECISIONS records the approved storage direction. Native launching extends the previously
completed Bridge v1 scope without moving runtime ownership into Bridge.

- [x] Probe actual SQLite use in the Tailcat-enabled CLI. modernc v1.59.0 embeds SQLite 3.53.4.
      Ordinary darwin/amd64 builds reached 55,370,400 B; ncruces v0.35.5 reached 57,308,464 B.
      Disabling inlining only for Orb's packages (not dependencies) gives modernc probe sizes
      darwin/arm64 51,461,842 B, darwin/amd64 53,855,888 B, linux/amd64 52,879,520 B,
      linux/arm64 50,135,200 B with the existing Tailcat tags and stripped release flags.
      Local M4 process-start medians: --version 12.22 ms; existing WAL database open/write
      13.11 ms. These are feasibility probes, not final release measurements or native x86 timings.
- [x] Add explicit SQLite documents/session repositories, namespace isolation, FULL/WAL connections,
      schema rejection, consistent backups, durable journals and stale-writer rejection. Settings,
      auth and trust accept caller-owned transactional documents without changing file defaults.
      The full `make check` gate passed after concurrent work settled; final cache verification
      is below. SDK examples build without SQLite/Bridge/Tailcat imports. Portable `connect` and
      `bridge` build for Wasm; harness retains an existing Unix `Setpgid` portability failure.
- [x] Measure a 100,000-session catalog on M4: 128-row pages about 0.11–0.12 ms; targeted FTS
      prefix search 0.16 ms after fixing the query plan (previously 16.37 ms). Immutable keyset
      pagination avoids OFFSET scans and changes of order while messages stream. A native journal
      append after 10,000 entries fell from 28.19 ms / 50.4 MB allocations to 0.25 ms / 0.55 MB
      in the 10-operation probe by refreshing only the new tail; a 1,000-operation run measures
      0.091 ms / 23.5 KB per append. Unchanged reads no longer advance its revision. Creation,
      fork and import share one insert transaction. These are storage microbenchmarks, not
      end-to-end throughput guarantees.
- [x] Add a separate SQLite cache for visited foreign sessions, keyed by profile, pinned peer,
      namespace and SessionID. Legacy instance services use instance-scoped namespaces; cached
      entries never enter owned session repositories. Store only eight visible user/assistant
      messages, 4 KiB per message / 32 KiB encoded per preview, expiring after seven days, capped
      at 128 sessions per peer and 1,024 per profile. Streaming fragments, reasoning, tool payloads
      and attachments are excluded. Idle polling does not rewrite the cache. Old refreshes are
      fenced after revocation, superseded refreshes resnapshot, and expired previews are hidden.
      Bridge lists show cached entries immediately and fall back to them offline; cached views
      stay read-only until the destination confirms the same session. No cached-to-local fallback.
      This does not yet implement discovery of every saved conversation on remote hosts.
- [x] Run native SQLite checks on lab-3 and edge using isolated static test binaries: concurrent
      writer processes, restart/replay protection, backup, corruption/schema checks and namespace
      isolation pass. 100k-session page/search queries: lab-3 ~0.50–0.60 ms; edge ~0.22–0.28 ms.
      Durable append after 10k entries: lab-3 ~5.95 ms; edge ~0.26 ms (100-operation microbenchmarks).
      Preserve FULL durability; storage hardware dominates write latency on lab-3.
- [x] Pair an isolated local Bridge to lab-3 over SSH, attach 20 faux-model runtimes on lab-3,
      then verify a real Bridge prompt, completed-message preview persistence and local-block
      purge. Hermetic stream tests also verify offline command rejection, reconnect, remote
      revocation and refusal to retarget a cached view after an instance changes sessions.
- [x] Final native cutover release builds (`CGO_ENABLED=0`, existing Tailcat tags, Orb-only
      inlining disabled): darwin/amd64 54,252,560 B; darwin/arm64 51,816,130 B;
      linux/amd64 53,256,352 B; linux/arm64 50,397,344 B. All stay below 55 MB.
      M4 candidate `--version` over 30 warm processes: median 13.34 ms / maximum 15.35 ms.
      Opening a populated 100k-session database and printing CLI help: median 16.86 ms /
      maximum 19.48 ms over 20 warm runs. The full `make check` passes: build, vet/lint,
      race suite and pure-Go wire/conformance gates. All 29 unchanged upstream RPC tests pass
      through explicit Pi-file compatibility. All SDK examples and an external consumer build
      without SQLite/Bridge/Tailcat dependencies; portable `connect`/`bridge` compile for Wasm.
- [x] Select SQLite explicitly in native CLI assembly for conversations, global configuration,
      accounts/model catalogs/keybindings, memory, chat spool, Bridge state, attachments and receipts.
      Migrate configured legacy roots before cutover; preserve original bytes, IDs, tree links and
      capability state. Imports checkpoint source digests and reject changed inventories, damaged
      trees, symlinks and unsupported versions. A process exit immediately before cutover resumes
      correctly; changed sources are rejected. Empty initial database creation is recoverable.
      Native session ownership is acquired before runtime teardown, and rejects concurrent opens
      and deletion. Chat reset atomically carries delivery markers into its new native journal.
- [x] Keep existing SDK defaults and unchanged Pi-file tests through explicit `--pi-files` assembly;
      refuse that assembly on a migrated root. Native paths stay empty; resume, new/switch/fork,
      HTML/Markdown/JSONL export and global configuration import/export use database state.
      `storage backup` creates a private consistent snapshot; `storage restore` recovers conversation
      journals and rejects conflicts, without rolling back authority, credentials or delivery state.
      This is deliberately conversation recovery, not unrestricted whole-database rollback.
      Product v1/v2/v3 imports are covered; the separate harness v4 SDK API remains unchanged and
      is not a native v3-adapter input. Migration cannot fence arbitrary third-party file writers;
      they must remain stopped after cutover, and old originals are recovery material only.
- [x] Run current native migration/capability/chat-reset and SQLite tests on both lab-3 and edge
      in temporary roots, including four writer processes, full-database write rollback, crash
      recovery, conflict rejection, backup, foreign preview limits and revocation fencing.
      Live SSH pairing to lab-3 exercised 20 SQLite sessions, remote faux-model prompting, completed
      preview caching and local-block purge. That live check found and fixed an IPC-context bug
      that purged the compatibility cache instead of the native database. Restarting Bridge
      reconnected all 20 instances with stable identities; restarting the runtime process reopened
      the same 20 session IDs and preserved operation receipts byte-for-byte. SQLite integrity
      checks pass. Production installations and personal session roots were not migrated.
- [x] Recheck 100k-session catalogs: M4 128-row page 0.141 ms / FTS 0.186 ms; lab-3 page
      0.542 ms / FTS 0.604 ms; edge page 0.311 ms / FTS 0.460 ms. Durable append after 10k
      entries: M4 0.155 ms, lab-3 6.568 ms, edge 0.216 ms. These are bounded microbenchmarks
      (100 local / 20 server iterations), not claims about end-to-end saturation throughput.
      FULL durability remains enabled; the slower lab disk is not hidden by relaxed sync.
- [x] Repeat the full macOS gate after release hardening. The lab contention run exposed two
      distinct cases: simultaneous schema creation and normal FULL-durability writer starvation.
      First opens now serialize schema initialization and recheck it; existing WAL databases open
      without a write lock. SQLite writer admission waits up to 30 seconds rather than five,
      without reducing durability or replaying transactions. Three consecutive complete SQLite
      suites plus native migration/capability/chat-reset suites pass on both lab-3 and edge.
      A vanished Orb process no longer blocks the legacy-writer check; a deterministic regression
      retains refusal for a live process that cannot be inspected. RPC subprocess failures now
      capture stderr and clean up the child; 30 repeated binary transcript runs pass, including
      20 with concurrent short-lived Orb processes. The full local gate passes again.
- [x] Regenerate canonical fixtures on an isolated Linux filesystem: TS-to-Go comparisons and
      reciprocal session/auth reads pass without golden changes. A 20-second protocol fuzz run
      executed 884,148 inputs without failure. Module checksums verify, all SDK examples build
      without SQLite/Bridge/Tailcat dependencies, and portable connect/bridge still build for Wasm.
      The existing viewport benchmark at one million lines uses 2,593 B / 49 allocations per
      complete frame (15.6 microseconds in the 100-iteration M4 run), the same allocation budget
      as 100k lines. This measures rendered frames, not total transcript memory.
- [x] Package `0.8.0-rc.1` with the pinned GoReleaser 2.18.2 configuration: four platform
      archives, source archive, checksums and Homebrew cask. Check every SHA-256, exclude generated
      and private state from the source archive, and build all packages from the extracted source.
      The actual installer succeeds against candidate archives with local download substitution.
      Both Darwin binaries, Linux/amd64 on both authorized servers, and Linux/arm64 in the Linux
      container pass packaged-CLI migration, preserved originals, export, backup/recovery, integrity
      and migrated-root compatibility refusal. Full `make check` passes on macOS and Linux;
      Linux canonical fixtures and all 29 upstream RPC tests pass. The packaged Mac/Linux Bridge
      services pair through SSH and pass remote prompt, preview persistence and revocation with
      20 attached lab runtimes. Isolated services and server test roots are cleaned up.
- [x] Final M4 measurements: native `--help` median 16.58 ms / maximum 17.56 ms across 30 warm
      processes, with 37,076,992 B peak RSS in a separate help process. At 1,000 benchmark iterations,
      durable append after 10k entries is 0.096 ms / 23,548 B allocated per append; 100k-session
      page/search are 0.138/0.199 ms. No additional dependency was introduced by the native cutover;
      lint/unused checks pass. These are bounded measurements on the named hardware, not universal
      latency, memory or throughput guarantees. Live providers/OAuth, desktop clipboard and real
      terminal image checks remain the already-recorded owner-deferred coverage, not new claims.
      Candidate artifacts were prepared locally without publication. The owner subsequently authorized
      promotion to stable v0.8.0, with upgrade documentation and the normal green-CI publication gate.
- [ ] Implement managed hosting, conversation service and the unified multi-Bridge Sessions UI;
      the native-storage cutover does not imply completion of those separate plan slices.

## Orb/OpenCode evaluation — 2026-09-20

External runner lives in `../orb-evals/` (Harbor 0.23.0, OpenCode V2.0.11).
All four offline Linux CLI/model checks pass, including a tool round trip and outgoing routing,
reasoning and token-limit assertions. Harbor installation in the real DeepSWE image passes;
the unmodified grader passes negative (`nop=0`) and positive (`oracle=1`) controls on
`abs-module-cache-flags`. The owner stopped the initial DeepSWE pilot after four complete pairs
because the desired coverage is newer and broader; its provisional Orb 1/4 versus OpenCode 2/4
result is explicitly non-conclusive. The replacement panel uses Terminal-Bench 4.0 and DocOps at
their pinned August/July 2026 revisions: 10 seeded CPU terminal tasks plus 16 document tasks
stratified across L1-L4 and Word/Excel/PowerPoint/PDF. Cost is recomputed from per-request
persisted tokens and captured provider prices; key-wide usage deltas only enforce the campaign
budget because the credential may also serve backend traffic. Both official verifiers passed
negative and positive controls; the 52-attempt Luna campaign is running from
`../orb-evals/results/recent-20260920-221716/`.

## Pi v0.86.0 adoption — 2026-09-20

Owner-approved implementation with production headless compatibility required: retain existing
exported Go entry points and interface method sets, legacy session reads, and RPC command shapes.
Target release: `ecac0a9c4edad3dac5d9f8b40e0c7db7a56471fc` (`v0.86.0`).

The owner explicitly chose Pi's default system-message events after the real production consumer
audit found that Ordalie's broad `message_end` SSE forwarding would expose system prompts. Before
deployment, that consumer must filter system messages; no backend files are changed by this work.
The owner also requires upgrading to the latest stable Go on every update: this adoption now uses
Go 1.27.1, with matching CI/release version-file selection and golangci-lint 2.13.2.

- [x] Extract released transcript/provider/session regression fixtures before implementation.
- [x] Implement provider, retry, compaction, extension, and RPC fixes with additive Go APIs.
- [x] Integrate transcript-backed prompt/tool replay and legacy session compatibility.
- [x] Improve session discovery, progressive resume, autocomplete, and clipboard reliability;
      focused race suites pass, including mutation/close cancellation lifetime regressions.
- [x] Audit the former product name: no genuine references remain in tracked files.
- [x] Compare exported Go APIs against the pre-change production revision: no incompatible APIs.
- [x] Verify headless, host, upstream RPC, and canonical Linux fixture/race/pure-Go gates.
- [x] Record sync evidence and advance the pin only after complete conformance is green.
- [x] Run final post-pin `make check`: build, vet, lint, full race suite, and pure-Go wire suite pass.
- [x] Commit the verified adoption on `main`.

Prompt-cache warming and diagnostic-upload features remain deferred per the reviewed adoption plan.
Tier-2 live provider smokes currently lack their required credential environment variables;
deterministic provider fixtures and local fake-server tests remain the integration gate. No live
provider verification is claimed.

Production replay now passes all three selected Ordalie tests (multiple tool rounds, stable legacy
system prompts, and session persistence/native SSE). Linux TS↔Go session/auth checks pass, and
`make check` passes on Go 1.27.1. Released TS Pi and Orb now both pass the same 29/29 unmodified
upstream RPC tests, including fresh-session counts. The legacy prompt setter is preserved without
memory-specific transcript rewriting. All fixture dependencies now follow the released Pi versions,
including OpenAI SDK 6.40.0.

Trim: removed obsolete provider split/deferred helpers, extension schema wrappers, active-tool
resolution, session-listing helpers, and clipboard spawning code. No Go dependency was added;
`go mod tidy -diff` is clean. Source LOC (excluding Go tests and generated filenames): ai 32,220 /
23,894 TS (1.35×), engine 13,658 / 33,353 (0.41×), agent 70,019 / 73,689 (0.95×), tui 13,866 /
18,267 (0.76×). The ai count includes Orb's direct-source catalog generator under `ai/`, whereas
upstream's generator lives outside `src/`; excluding those 2,025 dev-tool lines gives 1.26× for ai.
Removed dead helpers rather than widening shared abstractions.

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
- [x] Publish `docs/compare/sprint-2.md`, complete trim pass #3 (M3, retired), and check every
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

Status: **implementation and deterministic verification complete; only subscribed-account OAuth
evidence remains owner-blocked**. The owner retained the full-parity scope and adopted the study's
55 MB bridged-binary recommendation on 2026-07-20.

- [x] Publish `docs/plan/expansion-study.md` for owner review before extending breadth. The owner
      retained the full-parity defaults and adopted its 55 MB decimal binary-size recommendation
      on 2026-07-20.
- [x] Audit the frozen expansion ring: providers (35/36 + codex), OAuth (all four flows), MCP,
      packages/trust, and the bridge runtime/non-UI/shims layers are already landed and green.
- [x] Land the RED `ctx.ui` F11 surface first (ui-dependent upstream examples wired and failing),
      then turn WP-541 (ctx.ui bridge) green — seventeen examples, full dialog/status/widget/theme/
      autocomplete surface, AbortController, pi-tui helper shim.
- [x] WP-542: custom components, editors, overlays over the bridge (gate G3 resolved: bridge now) —
      `ctx.ui.custom` with overlay options and handles, editor replacement, the `CustomEditor` base
      over the registered real editor; modal-editor end-to-end plus six more custom-UI examples.
- [x] WP-550: F11 matrix at 61/69 (88%) unmodified with the 69-example matrix published (superseded by the ecosystem matrix);
      six named extensions end-to-end; bridge wired into the product (`--extension`, settings and
      project paths, `/reload` per-path VM replacement) with a real-binary smoke.
- [x] Port the openrouter-images generation client (only unported API shape).
- [x] Alignment-audit work items closed this sprint: MIRROR triage (21 verified rows),
      `settings.httpProxy` implemented with environment precedence, SDK convenience surface
      (tool bundles, public ai model helpers with duplicates deleted).
- [x] Publish `docs/compare/sprint-3.md`, complete trim pass #4 (M4, retired), and check
      every locally provable M4 criterion.
- [x] Pre-release parity tail (Sprint 4): the six numbered upstream regression tests; typed
      tool-event accessors, public streaming-JSON entry, UI component kit exports; the five small
      gaps found by MIRROR verification (`/session` cache-waste totals, opencode session-affinity
      headers, live-export ToolHTMLRenderer, `/settings` idle-timeout entry, footer/tool-header
      cosmetics).

## Sprint 4 — Ship (M5)

Status: **v0.1.0 was published on 2026-07-21 from `600198b` after the owner explicitly authorized
the deterministic green candidate for release. GitHub Actions run `29875158999` passed the full
gate and published all four archives; subscribed OAuth, hosted-nightly, and real-terminal/macOS
smokes remain post-release follow-up** (see the retired M5 checklist §Release closure in history).

- [x] Land the release machinery: goreleaser (4 targets, snapshot verified), tag-triggered
      workflow re-running the gate, checksum-verifying install script, Homebrew formula generation,
      ldflags version, CI on `make check`, README newcomer path, G4 resolved
      notify-only.
- [x] Close the parity tail: six upstream regression tests ported, five MIRROR-verification gaps
      fixed, three real defects found and fixed (CLI stream SessionID, live custom messages,
      select-list theme). The broader startup skills/prompts/extensions/themes listing and its
      diagnostics remain deferred; the v0.83 file-backed system-prompt context slice is covered.
- [x] Close the alignment should-fix remainder: typed tool-event accessors, ai.ParseStreamingJSON,
      UI component exports (absentees documented), unit-test tails including the 28 missing
      app.* keybinding migrations found and fixed.
- [x] Re-run current alignment: the 2026-07-21 release-closure refresh descending from `fbcabf9`
      keeps 436/436 upstream files mapped and zero open should-fix findings; it also closes the
      previously unwired skill-invocation renderer, with no new MIRROR row needed.
- [x] Close the final context-lifecycle audit: next-turn state refresh, custom/branch compaction
      weight, unified model/thinking mutation effects, and provider-header hook ordering now match
      upstream with focused regressions; production code is 69 raw lines smaller.
- [x] Publish `docs/compare/sprint-4.md` with the final deterministic TS/Go comparison and every
      release-platform difference fixed or ledgered.
- [x] Re-run the candidate trim: 111,431 / 99,172 = 1.124x mirror LOC, 19 reviewed clone groups,
      clean module audit, 52,236,720 B largest bridged artifact across four targets, and 42.1 ± 0.9
      ms no-prompt cold start on one CPU. The owner-set size and 50 ms mean caps are green.
- [x] Resolve the two binding-rule conflicts (owner, 2026-07-20): retain D17 and set the bridged
      artifact cap to 55 MB decimal; clarify D7 so shipped builds remain static `CGO_ENABLED=0`
      while development-only `-race` binaries may link Go's CGo-backed ThreadSanitizer runtime.
- [x] Pin releases and CI to Go 1.26.5. An identical-source comparison records 2.8% gains in the
      in-memory 1,000-turn Processor core and F12 rendering, with the startup, session-creation,
      compaction, and binary-size regressions retained in the M5 checklist (retired) rather than hidden by
      an aggregate.
- [x] Re-verify the deterministic M1–M4 criteria at the release commit. The v0.81.0 lock is green,
      436/436 upstream files are mapped, and the owner deferred subscribed OAuth, hosted-nightly,
      and real-terminal checks to post-release follow-up.
- [x] Create the final annotated `v0.1.0` tag at release commit `600198b` and publish the GitHub
      release with checksum-verified Linux and macOS archives.
- [x] Owner authorized publication on 2026-07-21 without waiting for the subscribed OAuth,
      trailing-72-hour nightly, and clean-macOS/terminal smokes; those remain explicit follow-up.

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
      table); complete the eight-point S5 trim (retired).
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
- [x] Complete the S6 trim (retired): remove 1,068 net lines from the inherited candidate, record zero
      new dependencies and zero duplicate groups, and prove the SDK-only additions add zero
      linked bytes to `cmd/orb`.
- [x] Commit the sprint arc as one green mainline chunk and close it per D25; exact `make check`,
      fixture regeneration, module verification, static analysis, and four CGO-disabled
      cross-builds are green.

## Upstream v0.81.0 sync — 2026-07-21

Status: **green; closes with the commit containing this record**.

- [x] Independently adversarially verify all 52 provider, catalog, and login gap IDs against their
      TypeScript implementations and regression tests; the final evidence table is
      the provider-login parity audit (retired): 51 CONFIRMED, LOG-m7 INSUFFICIENT only
      because ordinary Go errors lack JavaScript's creation stack, and zero REGRESSION verdicts.
- [x] Port the complete in-scope v0.81.0 delta: required stream injection, public compaction/session
      contracts, retained-tail identity, catalog/image generation and freshness, version/product
      assets, and renamed fixture APIs. Ledger server, native SQLite, and llama as D2/D7 exclusions.
- [x] Regenerate every fixture from exact tag `9c480b6a`; all 30 manifests are byte-clean, F10
      payloads are unchanged, and only the expected changelog/version product payloads differ.
- [x] Verify the NVIDIA 19-ID manifest exactly, full OpenRouter/Vercel ID-set digests, all 39 image
      models, `go mod tidy -diff`, direct zstd dependency placement, and the protected WIP surfaces.
- [x] Run `make check` (static product build, vet, zero lint findings, tree-wide race suite) and
      `make fixtures-check` against the read-only exact-tag checkout; both are green.
- [x] GitHub CLI confirmed v0.81.1 was published while this exact-tag sync was running. Only the
      requested SYNC-1 Kimi K3 change is retained as an explicit ahead-of-pin backport; the other
      v0.81.1 commits are not silently attributed to v0.81.0 and belong to the next sync cycle.

## Upstream v0.81.1 sync — 2026-07-22

Status: **green; closes with the commit containing this record**.

- [x] Diff all 14 commits from v0.81.0 (`9c480b6a`) through v0.81.1 (`20be4b18`) and classify every
      changed upstream path against the mirror and divergence ledger.
- [x] Port shared assistant retry behavior, compaction/branch-summary retry events, the restored
      default stream fallback, deferred interactive catalog refresh, Kimi K3 metadata, and the
      published 1,098-model catalog.
- [x] Map upstream source-archive publication onto deterministic GoReleaser output with checksum,
      content-exclusion, clean static rebuild, and two-pass byte-stability checks.
- [x] Regenerate all conformance families from the exact v0.81.1 checkout; F10 payloads remain
      byte-identical and only its manifest provenance changes.
- [x] Run the complete race/lint/fixture/tidy/static-cross-build/release-config gate and commit the
      green sync.
- [x] Rehearse GoReleaser v2.17 twice from the clean commit: checksums pass, the source archive is
      byte-deterministic, excludes checkout/build state, and rebuilds the full module and product
      with `CGO_ENABLED=0 -buildvcs=false`.

## Public extension ecosystem matrix — 2026-07-22

Status: **complete for the locked offline surface; package-specific external-service workflows remain explicit**.

- [x] Lock the 44 most-downloaded valid Pi package manifests from the public gallery snapshot,
      including exact versions, top-level integrity hashes, and the complete npm dependency graph.
- [x] Run every package under pinned Pi 0.81.1 and Orb 0.1.2 with one cold attempt, two warm-ups,
      and eleven interleaved measured samples in a credential-free, network-isolated container.
- [x] Compare canonical tools, parameter schemas, prompt guidance, and commands after subtracting
      each runtime's observer baseline; retain all attempts and diagnostics in the raw artifact.
- [x] Inspect the defining workflow of all 44 packages with package-local file-and-line evidence,
      then execute seven safe command handlers and Piolium's real knowledge-base staging workflow.
- [x] Fix the unambiguous runtime gaps found by those workflows: Piolium Node/SDK surfaces,
      `Buffer.byteLength`, `process.getuid()`, and per-source-module `import.meta` resource paths.
- [x] Publish `docs/sync/ecosystem-extension-matrix.md` and the machine-readable compact report
      with the complete result, performance limits, blocker families, remediation, and hashes.

## Ponytail trim — 2026-08-11

Status: **green; net shrink with no fixture drift**.

- [x] Delete 1,349 net implementation lines before this report (441 added, 1,790 removed):
      production Go is down 951 lines and Go tests are down 400. Tracked non-test Go now totals
      155,085 lines.
- [x] Remove the persistent extension metadata cache and its fingerprint/write-through plumbing,
      collapse the snapshot and message-order parsers onto `encoding/json`, restore `x/text`'s
      charset index, and retire one-caller/allocation-only helpers without changing wire or TUI
      output.
- [x] Keep the dependency graph unchanged; `go mod tidy -diff` is empty and `go mod verify` passes.
      Raw deadcode/staticcheck findings remain supported public SDK/test-only roots and existing
      parity/vendored-style diagnostics; the configured vet plus golangci-lint gate reports zero
      issues.
- [x] Pass `make fixtures-check` and `make check`, including the complete race suite, CGO-disabled
      byte-checked provider/conformance suites, live Node-backed F13 replay, and hermetic F8/WP360
      resource discovery. No golden was edited.
- [x] Measure the CGO-disabled `cmd/orb` binary at 50,579,863 bytes, up 406,989 bytes (0.81%) from
      restoring the full HTML charset tables. `orb --version` moves from 6.8 ms to 9.4 ms over 30
      runs after restoring eager direct regexp variables; both remain inside the 55 MB and 50 ms
      release limits. Metadata commands now pay live extension-host startup instead of owning a
      stale-prone cache.

## Resolved owner decisions

- **M5 binary-size cap (2026-07-20)** — the owner adopted the expansion study's recommended 55 MB
  decimal bridged cap while retaining D17 and the 50 ms cold-start cap. A clean snapshot of
  `37d9ab7` built all four targets; the largest is darwin/amd64 at 52,187,808 B.
- **D7 versus the mandatory race gate (2026-07-20)** — D7 now governs shipped product/release
  binaries, which remain static `CGO_ENABLED=0`. Development-only `go test -race` binaries may
  enable CGo solely because the Go race runtime links ThreadSanitizer; the exception never ships.
- **Canonical release repositories (2026-07-20)** — public `OrdalieTech/orb` and
  `OrdalieTech/homebrew-tap` repositories now exist; this checkout's `origin` points to the
  canonical source repository and preserves the former personal remote as `legacy`.

- **Review-fix round (2026-07-28)** — full live audit at 2a5af4a produced 33 confirmed findings;
  all fixed in one wave (ai timeouts/abort text, extension-host callback semantics + streaming
  providers + live AbortSignal, TUI panic guard + width corrections + stdin race, auth self-heal,
  session locks via internal/filelock, jsonwire persisted-file parity, CLI parity: --verbose,
  @file-in-RPC, untyped RPC dispatch, ctx.shutdown, project_trust, diagnostics colors) plus the
  verified trim list (corrections.go, dual-source provider metadata, test-only wrappers, jstrim
  and ctxsleep consolidation, markdown exporter wired). `upstream-rpc-tests` added to CI.
- **ai/ LOC budget justification (2026-07-28, per RELEASE-CRITERIA trim rule)** — ai/ measures
  29.5k non-test LOC vs 21.1k upstream TS = 1.40x, over the 1.3x per-package budget. The driver is
  `ai/api` at 1.78x: the G2/WP-222 decision to hand-roll Gemini/Vertex (and SSE plumbing generally)
  on stdlib `net/http` instead of official SDKs trades LOC for zero native deps and is considered
  paid for; the rest of ai/ sits at ~1.06x. Other mirrored packages are in budget (agent 0.85x,
  tui 1.01x, codingagent 1.13x; aggregate 1.15x).

- **Upstream sync v0.84.1 (2026-08-10)** — lock and fixtures promoted to `53fa77c`; extraction
  scripts made dual-revision safe; conformance-gated surfaces ported (session JSONL v4 write +
  v1–v3 migration, delta-only JSON/RPC `message_update`, OpenAI incomplete-reason errors, Bedrock
  failure diagnostics, Gemini-3 tool-call ids, Baseten + Qwen Token Plan Individual catalogs,
  scoped-models selector, `scrollbarThumb`, iTerm image size). Deferred to follow-up ports, per
  the 2026-08-10 sync report: fullscreen TUI mode + Mermaid/LaTeX rendering, the v4 lane-based
  harness Session API surface beyond the storage codec, auth/models-store concurrency overhaul,
  `AGENTS.override.md`, `pi auth check`, markdown transformers, `samplingParams`/vLLM
  `thinking_token_budget`, and deferred-response provider contracts.

- **agent/harness is supported SDK surface (owner, 2026-08-10)** — the 2026-08-10 trim audit
  flagged the harness session stack as having zero callers; that measurement only sees this
  repository. Orb is a Go module first (D1) and downstream embedders import `agent/harness`
  directly. The stack and its F6/F8 conformance families are load-bearing SDK surface: do not
  retire, and treat harness API changes as embedder-visible.

## Upstream sync — 2026-09-05, pi v0.85.0

Pinned: published release `107d79f11072bbc8a3a757ed7fd69596bee7d68c` (2026-09-04).
Provider payloads and streams, catalog metadata, extension lifecycle, session controls, transactional
v4 storage, and product compaction are covered by regenerated upstream fixtures. No Go dependencies
were added. The removed v4 lane writer and v4-to-v3 bridge were deleted; published method sets remain
source-compatible, with explicit errors for operations the released format no longer supports.
The standalone Go ExecutionEnv retains its existing regression corpus outside the wire fixture family.
Orb-owned selector frames are preserved while their search/callback behavior stays upstream-extracted.

Candidate sync and its complete race suite are GREEN, reciprocal fixture regeneration is byte-clean,
and the unmodified released upstream RPC suite passes 29/29 tests. The pin was promoted only after
these gates passed. Final `make check` is GREEN: CGO-disabled build, vet, lint (zero issues), complete race suite,
and the shipped-build provider/conformance rerun. `go mod verify` also passes. Existing account/host
blockers below remain unchanged.


Trim and measurements: the obsolete v4 JSONL bridge/writer and unused truthiness helper were
removed; no new module dependency or speculative default-on capability was introduced.
All four `CGO_ENABLED=0` builds pass:

| Target | Bytes |
|---|---:|
| linux-amd64 | 51,888,132 |
| linux-arm64 | 48,739,807 |
| darwin-amd64 | 53,359,552 |
| darwin-arm64 | 50,606,514 |

The largest build remains under 55 MB. Linux/amd64 grows 2.59% from the last recorded
50,579,863-byte baseline. Final `--version` over 30 runs: mean 22.1 ms,
median 21.97 ms, maximum 36.91 ms (50 ms cap). Concurrent build load
made wall time variable: an alternating 30-run comparison against the existing August 18 binary
measured 14.45 ms old / 14.69 ms candidate; the larger historical timing delta is consistent with this host's
load, so those cross-date timings are not a controlled regression measurement. This comparison artifact predates the
last small serializer/render fixes; the final absolute measurements above use the final sources.

Raw source-line comparison (non-test Go, excluding assets/testdata; upstream src TypeScript):

| Package | Go lines | Upstream TS lines | Ratio |
|---|---:|---:|---:|
| ai | 31,321 | 24,268 | 1.29 |
| engine | 13,391 | 24,295 | 0.55 |
| agent | 69,214 | 70,065 | 0.99 |
| tui | 13,865 | 18,108 | 0.77 |

## Orb Bridge v1 — 2026-09-21

Implemented the owner-approved one-executable native delivery in separate `connect`,
`connect/agent`, `bridge`, native IPC/storage, Tailcat transport, and CLI/TUI assemblies.
The existing runtime remains the execution/session owner. Bridge and agent-call capabilities
are separately default-off; closing an attachment or bridge never disposes its runtime.
The active-plan packaging supersedes the Downloads specification's separate executable.
Restricted launching, mobile applications, browser transport, and other platform adapters
remain outside this delivery.

The initial stream-only feasibility gate used released Tailcat v0.7.0 at
`15ab9e68bfc6534a61797d7af28cedd42b54a3a5`, its documented omission tags, and the stream-only
SSH/C2N/DBus/Android omissions now in `.goreleaser.yml`. The four probe binaries measured
42,401,952–45,948,064 bytes; the interleaved Darwin/arm64 startup probe measured 20.24 ms baseline
and 24.46 ms with Tailcat. The probe module graph grew from 89 to 636 modules and the compiled
CLI graph from 519 to 773 packages, while the SDK graph contained no bridge/Tailcat imports.
Concurrent dependency maintenance subsequently changed the baseline. The final graph contains
643 modules, 859 CLI packages, and 483 SDK packages; the SDK still imports neither `connect`,
`bridge`, nor Tailcat. The reviewed Tailcat release pins its Tailscale dependency exactly.

Verification:

- Hermetic race tests cover pairing claim recovery and claimant binding, fixed versus future
  grants with twenty instances, separate owner/attachment credentials, duplicate registration,
  generation fencing, durable grant revocation, pinned TLS, malformed frames/JCS, bounded RPC
  results, receipt conflicts/quotas, ambiguous storage acknowledgment, and unknown crash outcomes.
- Runtime tests cover local transition fences, execution identity, reentrant queue callbacks,
  preserved owner rebind callbacks, durable retry after reconnect, local transcript resets,
  bounded replay/cursor expiry, and accepted work surviving connection and attachment closure.
- Three-bridge tests cover scope isolation, same-revision conflict detection, persisted withdrawal
  floors, and both directional grants for instance-subject calls. Discovery grants no execution
  authority; routing cannot transparently forward execution through a third bridge.
- On the explicitly authorized `ordalie@ordalie-lab-3` and `ordalie@ordalie-edge`, isolated
  profiles paired and approved once, attached twenty faux-provider runtimes, delivered remote
  prompts/transcripts, retained identical receipts across bridge restart and generation change,
  and switched sessions. Reopening the runtimes retained their enrolled identities.
- Native direct traversal and separately forced DERP relay (region 303) passed pinned-TLS stream
  checks. Live A–B–C reconciliation across three isolated profiles learned C through B, allowed
  A to authenticate C using the signed locator without granting instance access, and propagated
  C's withdrawal. A real SSH terminal ran the focused view without provider credentials, rendered
  status, submitted `/new`, verified the remote session change, and exited with Escape.
- `make check` passes with the repository's Node 24.18.0 runtime on PATH: CGO-disabled build,
  vet, zero lint issues, complete race suite, and shipped-build provider/Pi conformance rerun.
  Node 26's documented lack of TypeScript enum transformation cannot run the F13 dependency;
  no test, fixture, or budget was weakened. The unmodified upstream RPC suite passes 29/29.
- Parser fuzzing passed 494,184 executions in the recorded 10-second run. Existing SDK examples
  and an external module containing the unchanged minimal example build; `go mod verify` passes.
  `GOOS=js GOARCH=wasm CGO_ENABLED=0 go build ./connect ./connect/protocol ./bridge` passes.
  Existing Orb TUI snapshots pass without bridge-specific golden edits.

Final static release builds use `CGO_ENABLED=0`, the release tags, `-trimpath`, and
`-ldflags='-s -w -funcalign=4'`:

| Target | Bytes |
|---|---:|
| linux-amd64 | 50,557,088 |
| linux-arm64 | 47,710,368 |
| darwin-amd64 | 51,586,992 |
| darwin-arm64 | 49,096,482 |

Refreshed after Bridge navigation simplification: all remain below 55 MB decimal. Forty warm-cache
Darwin/arm64 `--version` runs after five warmups measured 9.48 ms mean, 9.43 ms median, and
10.31 ms maximum (50 ms budget), with output redirected to `/dev/null`.
Bridge-only protocol schemas are in ARCHITECTURE and tests in `connect`/`bridge`; SDK assembly
and user commands are documented in `docs/sdk.md`. Unrelated concurrent progress and UI changes
were preserved. Isolated remote test services and scratch profiles were removed after validation.

## 2026-09-21 — Built-in Bridge settings and direct page navigation

Bridge management is always assembled in the CLI and opens directly from Settings, Ctrl+P,
or `/bridge`. Its single service switch starts and attaches this Orb, including when a service
is already running; stopping saves the disabled preference and retains the explicit stop marker.
Devices and pending pairing requests, sharing/access, optional agent calls, and advanced controls
use the existing framed lists and dialogs. Both Bridge toggles are removed from the Plugins UI;
existing configuration keys, assembly IDs, SDK factories, and authority checks are preserved.
Plugins also opens directly from Ctrl+P, preserving the composer draft.

Regression checks started red for direct navigation and built-in management. Coverage includes
inactive startup without profile/network side effects, project overrides, native IPC enable/stop,
agent-tool reload, submenu return, focus restoration, and narrow-terminal layout. A real PTY
caught a completion/focus deadlock; the regression now passes with callbacks dispatched after
unlocking. Isolated built-binary PTY checks passed Ctrl+P Bridge, Ctrl+P Plugins, Settings → Bridge,
and service enable → attached runtime → persistent stop. Test services and profiles were removed.
`make check` passes with the documented Node 24 fixture runner: static build, vet, zero lint
issues, complete race suite, layering checks, and shipped-build Pi/provider conformance.
Existing Orb-owned snapshots remain green; no golden changes or new dependencies were needed.

## 2026-09-21 — Guided pairing and SSH setup

The Bridge home page exposes Share this Orb, Connect to a device, and Connect using SSH before
activation. Sharing chooses view/control and current/future access, copies a bounded versioned
invitation, waits for a claim, and asks the owner to approve the exact device and grants. Joining
waits for approval and opens the shared conversation picker. The home-page hint says Choose
access; only the actual clipboard action says Copy invitation. Legacy JSON invitations still work.
Saved devices show Connected, Not connected, or Blocked from live connection state; pairings
persist when stopped. CLI stop now waits for disconnection, fixing an immediate stop/start race.

The optional native SSH shortcut uses the existing system client and verified host keys to start
an installed remote Orb and pair through its local-owner commands. Conversation traffic then uses
Bridge. The TUI targets the personal profile; `orb bridge connect-ssh` additionally accepts a
remote profile and executable. Future-instance access remains explicit, and the server receives
no reciprocal grant. No new source files, dependencies, exported SDK changes, or core imports.

Regression tests cover clipboard payload equality, invitation parsing/expiry/bounds, approval
binding and refusal, polling cancellation, SSH host validation/shell quoting, live peer states,
and stop completion. A built-binary PTY exercised Share → access → Copy → paste → fingerprint
verification → automatic approval → shared picker → idle remote view → Escape. Its clipboard
command was replaced with an isolated capture, preserving the user's real clipboard. Both
`ordalie-lab-3` and `ordalie-edge` passed automatic SSH pairing and authorized Bridge conversation
inspection with isolated test installations. Three immediate stop/start cycles retained pairings,
changed the boot identity, and cleared live connections. Scratch profiles, installations, and
processes were removed; remote process checks confirmed none remained.

`make check` passes using the documented Node 24 runner, including vet/lint, the full race suite,
layering checks, and shipped-build Pi/provider conformance. All four static release builds and
the refreshed measurements above pass. Existing TUI snapshots remain green without regeneration.

## 2026-09-21 — Full conversation trust and SSH installation

The owner simplified new pairings to full mutual access to current and future conversations.
Share now opens its invitation immediately; each device confirms the other identity once.
The existing grant layer implements this with a reserved all-groups selector, including future
groups, while keeping controller subjects, agent-call authority, and local administration separate.
Existing restricted grants are retained. Empty remote catalogs now confirm successful pairing
instead of showing an error. Bridge details wrap into their two reserved lines, so SSH diagnostics
remain readable without increasing modal height or changing other lists' default rendering.

SSH setup checks both PATH and the user install directory. When necessary it reuses the updater's
HTTPS/checksum-verified platform downloads, uploads over SSH, checks the uploaded checksum and
Bridge support, and atomically replaces the user-owned Orb executable. Login failure, invalid
archives/checksums, interrupted uploads, and unsupported candidates leave the old executable
intact. An explicitly selected remote executable is validated without replacing it. No additional
dependency, source file, sudo requirement, shell configuration change, or SDK import was added.

The published v0.7.1 release predates Bridge; automatic installation correctly rejects it instead
of replacing a working binary with an incompatible release. On the owner's requested `lab-3`,
the tested Linux/amd64 development build was installed at `~/.local/bin/orb`, replacing v0.6.0.
The installed binary remains there; test profiles, conversations, and processes were removed.
Live default-path SSH pairing passed before a remote conversation existed, then a newly created
conversation became accessible and authorized inspection passed in both directions.

Hermetic checks cover missing/old installations, reuse behind an older PATH entry, refused custom executables, checksum
failure, truncated transfers, login diagnostics, wildcard grants, and blocked peers. Built-binary
PTY checks passed invitation copy/paste without permission menus, both identity confirmations,
mutual full grants, remote viewing, Escape restoration, and the complete two-line SSH error.
The clipboard command was isolated; the user's clipboard was preserved. `make check` and all
four static release builds pass; release measurements above are refreshed for this change.

## 2026-09-21 — Replace stale Bridge daemons before pairing

Reproduced the owner's `not_found`: a still-running older local daemon rejected the new
all-groups grant after the remote device had already approved pairing. Local admin status now
advertises full-access support; startup and pairing replace a daemon lacking it through the
existing stop/start path. The profile, grants, and non-owning runtime attachment survive, and
an existing deliberate Stop still requires explicit activation. Enabling pairing on an already
enabled Bridge does not rewrite project-owned plugin settings.

A regression test first failed without the compatibility helper, then passed for both older
and current daemons, including waiting for disconnection before replacement. An isolated live
check with the exact historical executable verified automatic replacement, persistent identity
and invitations, new full-access invitation acceptance, stop-marker cleanup, and reuse of an
already compatible daemon. Both personal daemons on this Mac and `ordalie-lab-3` now run the
updated build; identities and grants survived, the active local conversation reattached with
its original InstanceID, catalogs succeeded in both directions, and the server inspected the
local conversation over Bridge. The server currently has no attached conversations.

`make check` passes, including race and Pi conformance checks, and all four static release builds
remain below 55 MB. No SDK interface, runtime ownership, or peer protocol changed.

## 2026-09-21 — Minimal, live Bridge navigation

Bridge now opens on its service switch, Add device, and paired devices. Add device contains SSH
setup and invitation exchange; selecting a device opens its conversations directly. Advanced
keeps only agent-call opt-in and the fingerprint. Removed the duplicate device page and the TUI
forms for groups, grants, scopes, instance assignment, and operation receipts; the CLI retains
those operations. Opening configuration still starts no service, and trust remains explicit.

Home and conversation panels refresh bounded reads in the background, retain selection by ID,
and cancel outstanding requests on close. Actions re-read local status before dispatch instead
of using the snapshot from when the panel opened. Remote catalogs follow pagination within the
existing instance bound, omit unavailable runtimes, and use InstanceIDs independently of display
names. An empty device stays open until a conversation appears; interrupted catalogs reconnect
without replaying commands. Error details retain the two-line layout.

Regression checks started red for the simplified navigation and live refresh. Race checks cover
selection, cancellation, native status updates while the page stays open, and paginated catalogs
with duplicate separator-containing aliases and stale entries. Built-binary PTY checks verified
actual invitation copying, both trust confirmations, direct remote viewing, a newly attached
conversation appearing automatically, recovery after remote daemon restart, Escape navigation,
and the complete SSH login diagnostic. Profiles, clipboard interception, and test runtimes were
isolated and cleaned. No core runtime, SDK contract, dependency, or source file was added.
Production code shrank by 45 lines. `make check` passes, including race and Pi conformance; an
initial F13 background-lifecycle mismatch passed both its isolated rerun and the subsequent
complete gate without fixture changes. All four static release builds and startup measurements
above remain within their budgets. Concurrent footer changes were preserved.

## 2026-09-21 — Current session directory in the footer

The compact footer now reads the active session directory on each render, abbreviates the home
prefix, and keeps the final directory visible when space is tight. Healthy Bridge clears its
old `Bridge · personal` status; startup failures still show a disconnected warning. Targeted
tests cover directory changes, narrow layouts, and Bridge activation. Orb-owned WP450 footer
snapshots were regenerated with `make fixtures-tui`; `make check` passes, and all four static
release builds remain below 55 MB. Concurrent autocomplete work was left out of this commit.

## Owner-blocked evidence
- Anthropic Pro/Max end-to-end OAuth requires an interactive subscribed account.
- ChatGPT/Codex, Copilot, and xAI OAuth end-to-end runs likewise require subscribed accounts.
- Tier-2/Tier-3 provider live tests require repository/API credentials and CI secrets.
- Real Kitty and iTerm2 image emission plus native Darwin/X11/Wayland clipboard smoke require those
  terminal and desktop environments.
- Off-machine clean macOS release validation and the 72-hour burn-in require owner-provided hosts
  plus publication/CI access; clean Linux install paths are already verified hermetically.
