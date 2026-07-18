# Implementation plan — overview

**The active plan is [SPRINTS.md](SPRINTS.md)** (owner restructure 2026-07-18, recorded as D25):
four large test-defined sprints on a single branch, fixtures-first, each closing with a TS-pi
comparison report. The WP system below is retired as sequencing.

This file and the `phase-*.md` files remain as **spec sheets**: they carry the upstream file
references, fixture families, and acceptance detail per surface. Use them to know *what to port
and where it lives upstream*; ignore their ordering, dependency columns, and one-WP-per-commit
ceremony.

## Delivered under the old WP system (historical)

001–002 bootstrap+conformance · 110–180 walking skeleton + **M1** (`docs/trim/M1.md`) ·
210/211 Anthropic + OAuth · 221/222 Gemini + Vertex (G2: stdlib REST/SSE) · 231/232 Mistral/Azure +
Bedrock · 250 catalog · 260 pi-messages · 310/320 compaction + session tree · 330 JSON mode.
Live status: `docs/plan/PROGRESS.md` (sprint checklist).

## Spec-sheet index (detail in phase files; sequencing superseded)

| Surface | Spec sheet | Old WP refs |
|---|---|---|
| Remaining providers + OAuth (codex/copilot/xai), compat family — **expansion, Sprint 3** | [phase-2-providers.md](phase-2-providers.md) | 241, 270 |
| RPC mode, skills/templates, extension API + wire-through, SDK facade — core, Sprint 1 | [phase-3-harness.md](phase-3-harness.md) | 331, 340, 350, 351, 370 |
| MCP + pi packages/trust — **expansion, Sprint 3** | [phase-3-harness.md](phase-3-harness.md) | 352, 360 |
| Harness SessionRepo/FileSystem parity (Sprint-1 addition) | upstream `packages/agent/src/harness/` (`types.ts`, `session/`, `env/`) | — |
| TUI | [phase-4-tui.md](phase-4-tui.md) | 410–460 |
| JS extension bridge | [phase-5-jsbridge.md](phase-5-jsbridge.md) | 510–550 (G3 in 542) |
| Sync tooling, docs, release | [phase-6-ops.md](phase-6-ops.md) | 610, 620, 661 (G4) |
| Windows (future, unscheduled) | [phase-6-ops.md](phase-6-ops.md) | 670 |

Trim is no longer a WP: every sprint closes with the trim checklist in
[../RELEASE-CRITERIA.md](../RELEASE-CRITERIA.md). Execution contract: [../../AGENTS.md](../../AGENTS.md).
Decisions: [../DECISIONS.md](../DECISIONS.md).
