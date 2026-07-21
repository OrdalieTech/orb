# Upstream alignment — standing release gate

This audit keeps pi-go's layout, public API, test intent, documentation, and release practice close
to pinned TS pi without reopening settled decisions. It re-runs at every sprint close from Sprint 3
onward and once more at the release commit; any confirmed should-fix item blocks that close.

## Method

Compare `.upstream/` across six dimensions: source mapping, public SDK/extension API, upstream test
intent, user documentation, engineering conventions, and release/CI practice. Verify every
candidate against `docs/DECISIONS.md`, then regenerate the
[findings ledger](upstream-alignment-findings.md) rather than appending history.

## Current verdict

The 2026-07-20 full audit, refreshed for the release closure descending from `fbcabf9` on
2026-07-21, has **zero open should-fix findings**. Mechanical coverage remains 436/436 upstream
source files. The final delta makes session publication race-safe, restores immediate custom-message
and collapsible skill-invocation rendering from upstream, removes a quadratic Go-only ID-index copy,
and consolidates identical locale and branch code without changing wire or runtime behavior.

D29 keeps reusable harness primitives while dissolving the parallel `AgentHarness` facade into
`codingagent.AgentSession`; application-specific stream proxying remains available through
`agent.WithStreamFn`. Other intentional differences stay in the divergence ledger.

## Watches

Four non-blocking items remain: upstream's duplicate compaction/skills implementations are unified
in Go; `ModelRuntime` is absorbed into registry/auth/session services; upstream's standalone
`pi-ai` CLI has no Go command; and Go vulnerability scanning does not yet mirror upstream's
scheduled npm audit. Exact evidence and re-check conditions are in the findings ledger.

## Re-run rule

Run the six-dimension comparison against the final commit and pinned lock, regenerate the ledger,
and update the verdict date. A new should-fix item becomes tracked work before release; a watch is
promoted only when upstream behavior or an owner decision makes it product scope.
