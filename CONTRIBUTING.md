# Contributing to orb

orb is a pure-Go agent platform that began as a Go port of [pi](https://pi.dev) (MIT, Mario
Zechner) and keeps a tested pi-compatibility kernel:

- **Kernel surfaces interoperate with released pi.** Session JSONL, event JSON, RPC frames,
  settings/models/auth files, provider wire shapes and the JS extension surface follow the pi
  release pinned in `UPSTREAM.lock` (materialize it with `make upstream`). A deliberate difference
  gets its fixture made Orb-owned and a line in the divergence ledger of `docs/DECISIONS.md`.
- **Fixtures are the gate.** Conformance goldens are generated from upstream (`make fixtures`) or,
  for the TUI, from Orb's renderer (`make fixtures-tui`), never hand-edited. A failing fixture means
  the change is wrong.
- **`make check` before every commit**, and `make fixtures-check` when touching anything
  conformance-adjacent. Every commit on `main` is green.
- **Slim.** Stdlib first, a dependency last and only via the table in `docs/ARCHITECTURE.md` §8.
- **No backward compatibility with orb's own past.** Change interfaces in place and update their
  consumers.
- User-visible changes append a line to `CHANGELOG.md` under `[Unreleased]`.

Execution contract for coding agents: `AGENTS.md`. Architecture and layout: `docs/ARCHITECTURE.md`.
