# Contributing to pigo

pigo is a faithful Go port of [pi](https://pi.dev) (MIT, Mario Zechner). The port's contract:

- **Upstream behavior is the spec.** The pinned commit in `UPSTREAM.lock` (materialize with
  `make upstream`) defines correct behavior, including quirks. Do not improve upstream; divergences
  require an entry in `docs/DECISIONS.md`.
- **Fixtures are the gate.** Conformance goldens are generated from real upstream code
  (`make fixtures`), never hand-edited. A failing fixture means the change is wrong.
- **`make check` before every commit** (build, vet + golangci-lint, full race suite), and
  `make fixtures-check` when touching anything conformance-adjacent. Every mainline commit is green.
- **Slim.** Stdlib first, dependency last and only via the table in `docs/ARCHITECTURE.md` §8.
  Trim passes close every sprint; mirrored packages stay ≤ 1.3× upstream TS lines.
- **Wire formats are byte-compatible.** Session JSONL, event JSON, RPC frames, settings/models/auth
  files: never rename, reorder, or "clean up" persisted or emitted JSON.
- User-visible changes append a line to `CHANGELOG.md` under `[Unreleased]`.

Execution contract for coding agents: `AGENTS.md`. Architecture and layout:
`docs/ARCHITECTURE.md`. Milestones and release criteria: `docs/RELEASE-CRITERIA.md`.
