# Upstream alignment — audit and standing pre-ship gate

Owner-requested (2026-07-19): keep pi-go's structure, API, tests, docs, and process reasonably
close to the real pi repo, and watch the gap closely before shipping anything. This document is the
instrument: the audit method, the current verdict, the work items it produced, and the rule that it
re-runs before any release.

## Method

Six read-only comparison dimensions against `.upstream/` at the pinned commit — mirror coverage,
public API surface (SDK users + extension authors), upstream test intent, user-facing docs and
packaging, engineering conventions, release/CI practice — each finding adversarially verified
against the full DECISIONS ledger so settled divergences are never re-litigated. Full verified
detail: [upstream-alignment-findings.md](upstream-alignment-findings.md).

**Standing rule: this audit re-runs at every sprint close from Sprint 3 on, and a fresh run at the
release commit is an M5 gate.** New confirmed should-fix findings become work items before the
sprint closes; the findings ledger is regenerated, not hand-edited.

## Current verdict (re-run 2026-07-19 at the release candidate, pi-go post-`529035c`)

**All 22 confirmed should-fix items are closed except one owner decision.** Closed across Sprints
3–4: MIRROR triage (21 verified rows), llama ledgered, `httpProxy` implemented, the SDK surface
(tool bundles, public model helpers, typed tool-event accessors, `ai.ParseStreamingJSON`, the
existing UI component exports — absent components documented as method-based flows), the six
numbered regression tests, the unit-test tails (tool-name normalization, Copilot effort overrides,
keybindings migration — which surfaced and fixed 28 missing `app.*` legacy migrations —
footer/branch detection), the five MIRROR-verification parity gaps, `make check`/CHANGELOG/
CONTRIBUTING/SECURITY process items, and the release pipeline (goreleaser, tag workflow,
ldflags version, install script). **Open: the AgentHarness facade decision (owner,
PROGRESS.md).** Watch list (36 items) unchanged.

### Original verdict (2026-07-19, pi-go `edaa772`)

No ship-blockers. 22 confirmed should-fix items, 36 watch items, 10 candidate findings refuted or
already covered by DECISIONS. Calibration from the auditors: the extension API core, the four
package boundaries, wire formats, and LICENSE/attribution are an unusually faithful mirror; the
drift is concentrated in bookkeeping (MIRROR coverage), SDK convenience surface, test-intent tails,
and process scaffolding — exactly the things conformance fixtures cannot see.

### Confirmed drift, grouped into work items

1. **MIRROR bookkeeping (Sprint 3):** sixteen live `core/*.ts` modules, the `packages/agent` public
   facade (`agent-harness.ts`, `proxy.ts` + 3 helpers), and the bundled llama.cpp extension have no
   file-level MIRROR row or ledger entry. Actions: one triage pass adding a row per file (absorbed →
   name the Go file; excluded → say so); a llama ledger row (deleted upstream post-pin — exclude,
   no port); an explicit owner-visible decision for the AgentHarness facade (port for SDK parity vs
   record "dissolved into codingagent runtime"); implement-or-ledger the `httpProxy` settings key
   (currently silently swallowed).
2. **SDK/API convenience surface (Sprint 3, small):** `tools.NewCodingTools`/`NewReadOnlyTools`
   bundles; export `ai.CalculateCost`/`ClampThinkingLevel`/`SupportedThinkingLevels`/`ModelsAreEqual`
   (deleting three private clamp copies); typed per-tool event accessors mirroring upstream's
   `isBashToolResult`-family guards; a public streaming-JSON parse entry point; the extension UI
   component kit exports. Each is the first symbol an upstream SDK user would look for.
3. **Test-intent debt (Sprint 3/4):** of 326 upstream test files, the uncovered tail is small but
   real — six numbered upstream regression tests with no Go counterpart (port before v0.1.0; dropped
   regression tests are how old bugs return), Anthropic tool-name normalization, Copilot
   thinking-effort overrides, cache-waste arithmetic, and five interactive-mode niceties. The
   findings ledger is the authoritative list; each becomes a Go test or a fixture case.
4. **Process alignment (done in this commit):** a single canonical `make check` gate (upstream's
   `npm run check` norm) referenced from AGENTS.md; a root `CHANGELOG.md` in upstream's format with
   an `[Unreleased]` section that sprints append to (the embedded upstream changelog stays a product
   asset for `/changelog` parity); `CONTRIBUTING.md` and `SECURITY.md` for public-repo hygiene.
5. **Release practice (Sprint 4 spec, recorded now):** the release workflow must re-run the full
   gate at the release commit, extract notes from `CHANGELOG.md`, inject the version via ldflags
   (no more hardcoded `0.1.0-dev`), and print the upstream pin alongside it —
   `pi-go v0.1.0 (upstream pi v0.80.10 @ 3da591ab)`.

### Watch list

36 items — notably: upstream's dual-package compaction/skills duplication ported once (keep, but
sync diffs need mapping), the RPC *client* half and `streamProxy` absent, `ModelRuntime` unnamed in
Go, commit-message and pre-commit-hook conventions, npm-audit-equivalent dependency scanning, and
the `pi-ai` standalone CLI. None blocks a ship; all re-checked at each audit.

## How to re-run

Re-issue the six-dimension audit (the workflow definition lives with the session tooling; the
prompt contract is: compare, verify against DECISIONS, regenerate the findings ledger) and update
the verdict section here. A run that surfaces a new should-fix finding blocks the sprint close
until the item is a tracked work item; a run at the release commit with open should-fix items
blocks the tag.
