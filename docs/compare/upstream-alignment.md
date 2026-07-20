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

## Current verdict (fresh re-run 2026-07-20)

**Zero open should-fix findings.** The mechanical source audit maps 436/436 files across upstream
AI, agent, coding-agent, and TUI (including 185/185 coding-agent source files). The fresh public-API
and wire pass closed the post-sync tail: public retry/overflow and skill-block parsing, custom-theme
HTML export, notify-only update/version identity, the complete typed RPC client, and ImagesModels
with the exact 35-model OpenRouter catalog (SHA-256
`70b2efa95213e55d83092d94f5753c5affe509ee3543f66ec7451eb404378d11`). Adversarial review caught
and fixed exact-pattern over-acceptance, custom-theme precedence/source reload, image-registry data
races, and RPC listener reentrancy/panic handling; normal and race-focused suites are green.

The original 22 confirmed items also remain closed. D29 retains the harness primitives while
dissolving the unused parallel `AgentHarness` facade into `codingagent.AgentSession`, and the
application-specific `streamProxy` protocol remains ledgered behind `agent.WithStreamFn`.

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
   `pi-go 0.1.0 (upstream pi 0.80.10 @ 3a40794e)`.

### Watch list

The historical watch list remains non-blocking. RPC client and `streamProxy` are now closed; the
remaining examples are upstream's duplicated compaction/skills implementations ported once,
`ModelRuntime` absorbed without its TS class name, npm-audit-equivalent scanning, and the `pi-ai`
standalone CLI. Each is re-checked at the next audit rather than expanded speculatively.

## How to re-run

Re-issue the six-dimension audit (the workflow definition lives with the session tooling; the
prompt contract is: compare, verify against DECISIONS, regenerate the findings ledger) and update
the verdict section here. A run that surfaces a new should-fix finding blocks the sprint close
until the item is a tracked work item; a run at the release commit with open should-fix items
blocks the tag.
