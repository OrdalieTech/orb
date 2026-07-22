# pigo — Agent execution contract

You are implementing pigo, a faithful Go port of the pi coding agent (pi.dev). Read this file
fully before touching code. It applies to any coding agent (Claude Code, Codex, or other).

## Ground truth, in order

1. `docs/DECISIONS.md` — settled decisions. Never contradict them; never re-litigate them.
2. `docs/ARCHITECTURE.md` — layout, contracts, dependency policy.
3. `docs/RELEASE-CRITERIA.md` — milestone success criteria (M1–M5), live-test policy, trim checklist.
4. `docs/plan/SPRINTS.md` — the ACTIVE plan: four large test-defined sprints. The old
   `docs/plan/phase-*.md` files are spec sheets (upstream refs, per-surface detail), not sequencing.
5. Upstream source at the pinned commit — the behavioral spec. Materialize it with `make upstream`
   (clones `earendil-works/pi` at the commit in `UPSTREAM.lock` into `.upstream/`). When the spec
   sheet and upstream disagree on behavior, upstream wins; when either is ambiguous, read the
   upstream tests for that area.

## Working mode (trunk-based, fixtures-first — replaces the old per-WP protocol)

1. **One branch: `main`.** No GitButler lanes, no worktrees, no feature branches. Commit directly
   to main in coherent green chunks — a chunk may span what used to be several WPs.
2. **Every commit on main builds and passes.** `make check` (build + vet/lint + race suite,
   fixtures included) is THE pre-commit gate — before every commit, no exceptions. Bigger steps
   are welcome; broken mainline commits are not. User-visible changes append a line to
   `CHANGELOG.md` under `[Unreleased]`.
3. **Fixtures first.** Open each sprint by landing its conformance surface (extraction scripts,
   goldens, runners, black-box adapters) so the sprint starts RED; implementation turns it GREEN.
   Never write the port first and the fixtures after.
4. **Compare to TS pi.** Each sprint closes with `docs/compare/sprint-N.md`: identical scripted
   scenarios through TS pi (`.upstream/`) and pigo, every difference fixed or ledgered.
5. **Close the sprint**: trim checklist (RELEASE-CRITERIA), milestone boxes checked with evidence,
   comparison report committed, `docs/plan/PROGRESS.md` updated. Commit messages: `Sprint N: <what>`
   with verified checks in the body.
6. Read the relevant spec sheet AND every upstream file it cites before porting a surface — port
   what pi actually does, not what agents usually do. Update `docs/MIRROR.md` for every new file.

## Hard rules

- **Wire formats are byte-compatible with upstream.** Session JSONL, event JSON, RPC frames,
  settings/models/auth files: field names and shapes come from upstream, verified by fixtures.
  Never rename, "clean up", or reorder persisted/emitted JSON.
- **Do not improve upstream behavior.** Quirks are spec. Note suspected upstream bugs in the commit
  body; port them faithfully unless DECISIONS.md diverges explicitly.
- **Pure Go.** `CGO_ENABLED=0` must build. No cgo, no sidecar binaries except the upstream-sanctioned
  rg/fd auto-download.
- **Slim.** Stdlib first; internal helper next; dependency last and only via the ARCHITECTURE §8
  table. No speculative abstraction, no "for later" scaffolding.
- **Never weaken a criterion or a golden to pass it.** No softened fixtures, no skipped checks, no
  lowered budgets, no hand-edited goldens. A failing fixture means the code is wrong. If a criterion
  is genuinely impossible, stop and surface it.
- **Scope.** Surprises the plan didn't anticipate: decide the way pi's own author would (faithful,
  slim, boring), note the decision and rationale in the commit body, keep moving. Only genuine
  DECISIONS.md contradictions warrant stopping.
- **Comments** state constraints the code can't (e.g. "field order matches upstream serialization"),
  never narration.
- **Blockers needing the owner** (credentials, remotes, hosts): surface in `docs/plan/PROGRESS.md`,
  work around, never wait.

## Layout quick reference

`ai/` unified LLM layer · `agent/` loop+Agent+harness · `tui/` renderer/components ·
`codingagent/` tools, session, config, extensions (+`host/`, `mcp/`), modes · `cmd/pigo` CLI ·
`internal/` jsonschema, jsonwire, partialjson, truncate, sync · `conformance/` extract (TS,
dev-only), fixtures, runner. Full tree: ARCHITECTURE §1.

## Conformance

Fixture families F1–F12 are defined in ARCHITECTURE §6. Extraction scripts live in
`conformance/extract/` and run with Node ≥22 inside `.upstream/` (Node is dev tooling only — the
product is pure Go). Never hand-edit goldens; regenerate them. A failing fixture after your change
means your change is wrong, not the fixture.

## Upstream sync

`make sync` produces `docs/sync/reports/<date>.md`; turn red conformance into follow-up work items
in the report; bump `UPSTREAM.lock` only when green.
