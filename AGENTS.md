# pi-go — Agent execution contract

You are implementing a work package (WP) of pi-go, a faithful Go port of the pi coding agent
(pi.dev). Read this file fully before touching code. It applies to any coding agent (Claude Code,
Codex, or other).

## Ground truth, in order

1. `docs/DECISIONS.md` — settled decisions. Never contradict them; never re-litigate them.
2. `docs/ARCHITECTURE.md` — layout, contracts, dependency policy.
3. `docs/RELEASE-CRITERIA.md` — milestone success criteria (M1–M5), live-test policy, and the
   recurring trim-pass checklist. A milestone WP is done when every criterion checks, not before.
4. Your WP spec in `docs/plan/phase-*.md` — scope, upstream refs, acceptance checks.
5. Upstream source at the pinned commit — the behavioral spec. Materialize it with `make upstream`
   (clones `earendil-works/pi` at the commit in `UPSTREAM.lock` into `.upstream/`). When the WP spec
   and upstream disagree on behavior, upstream wins; when either is ambiguous, read the upstream
   tests for that area.

## Workflow for one WP

1. Read the WP spec and every upstream file it references. Do not start from memory of "how agents
   usually work" — port what pi actually does.
2. Check `docs/plan/OVERVIEW.md` for dependencies; confirm prerequisite WPs are merged.
3. Implement in the mirrored location (ARCHITECTURE §1 mirroring rules). Update `docs/MIRROR.md`
   for every new file.
4. Land the WP's conformance fixtures WITH the code (extraction script + goldens + Go test), plus
   ordinary unit tests for Go-side logic.
5. Definition of done — all of: `CGO_ENABLED=0 go build ./...` for linux+darwin/amd64+arm64;
   `go vet ./...` and golangci-lint clean; `go test -race ./...` green including fixtures; every
   acceptance check in the WP spec demonstrably true; MIRROR.md updated; no new dependency outside
   ARCHITECTURE §8 (if you genuinely need one, STOP and surface it — do not just add it).
6. Commit with message `WP-XXX: <title>` and a body listing acceptance checks verified. One WP per
   branch/PR unless the spec says otherwise.

## Hard rules

- **Wire formats are byte-compatible with upstream.** Session JSONL, event JSON, RPC frames,
  settings/models/auth files: field names and shapes come from upstream, verified by fixtures.
  Never rename, "clean up", or reorder persisted/emitted JSON.
- **Do not improve upstream behavior.** Quirks are spec. File a note in the WP report if something
  looks like an upstream bug; port it faithfully unless DECISIONS.md diverges explicitly.
- **Pure Go.** `CGO_ENABLED=0` must build. No cgo, no sidecar binaries except the upstream-sanctioned
  rg/fd auto-download.
- **Slim.** Stdlib first; internal helper next; dependency last and only via the §8 table. No
  speculative abstraction, no "for later" scaffolding, no interface with one implementation unless
  upstream's design demands the seam (Operations interfaces, env abstraction do).
- **Scope.** Implement your WP, not the next one. Seams the plan marks for later get a TODO
  referencing the WP id, nothing more.
- **Comments** state constraints the code can't (e.g. "field order matches upstream serialization"),
  never narration.
- **Never weaken a criterion to pass it.** No softened fixtures, no skipped checks, no lowered
  budgets. If a RELEASE-CRITERIA item is genuinely impossible, stop and surface it.
- **Trim passes are real WPs.** At every milestone (WP-180/390/470/560/650) run the trim checklist
  in RELEASE-CRITERIA: delete dead code, audit deps, inline unearned abstractions, report LOC vs
  upstream. Slimness is a product goal, not a style preference — and a trim never changes behavior.

## Layout quick reference

`ai/` unified LLM layer · `agent/` loop+Agent+harness · `tui/` renderer/components ·
`codingagent/` tools, session, config, extensions (+`jsbridge/`, `mcp/`), modes · `cmd/pi` CLI ·
`internal/` jsonschema, partialjson, truncate, sync · `conformance/` extract (TS, dev-only),
fixtures, runner. Full tree: ARCHITECTURE §1.

## Conformance

Fixture families F1–F12 are defined in ARCHITECTURE §6. Extraction scripts live in
`conformance/extract/` and run with Node ≥22 inside `.upstream/` (Node is dev tooling only — the
product is pure Go). Never hand-edit goldens; regenerate them. A failing fixture after your change
means your change is wrong, not the fixture.

## Upstream sync WPs

Sync runs are ordinary WPs: `make sync` produces `docs/sync/reports/<date>.md`; turn red conformance
into follow-up work items in the report; bump `UPSTREAM.lock` only when green.
