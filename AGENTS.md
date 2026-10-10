# Orb — Agent execution contract

Orb is a modular pure-Go agent platform that keeps a tested pi-compatibility **kernel**
(DECISIONS.md "The compat kernel"). Inside the kernel, released pi is the interop target; outside
it, Orb evolves on its own judgment. Read this file before touching code. It applies to any coding
agent (Claude Code, Codex, or other).

## Ground truth

1. `docs/DECISIONS.md` — the constitution (P1–P11), the compat kernel, live decisions and the
   divergence ledger. Never contradict it silently; changing it is the owner's call.
2. `docs/ARCHITECTURE.md` — layout, contracts, dependency table (§8).
3. pi 1.0, the release pinned in `UPSTREAM.lock` (`make upstream` clones it into `.upstream/`).
   For kernel surfaces it is the spec; where a kernel behavior is ambiguous, read its code and tests
   for that area. Beyond the kernel it is the general reference, since it is a well-designed
   product: before building or changing a feature pi also has, read how pi 1.0 does it, then adopt
   it or diverge on purpose.

## Working mode

- **Trunk-based on `main`, plain Git.** Commit directly to main in coherent chunks.
- **`make check` before every commit, no exceptions** (build, vet + golangci-lint, race suite with
  fixtures, portability). Every commit on main builds and passes. Run `make fixtures-check` too
  when touching anything conformance-adjacent.
- **Kernel changes are fixtures-first** (P8): land the conformance surface red, then turn it green.
- **User-visible changes** append a line to `CHANGELOG.md` under `[Unreleased]`.
- **Git history is the record.** No plan, progress or report files. Decisions the work had to make
  on its own go in the commit body: decide slim and boring (and, in the kernel, interoperable) and
  keep moving; stop only for a genuine DECISIONS.md contradiction.
- **Blockers only the owner can clear** (credentials, remotes, hosts): surface them, work around
  them, never wait.

## Hard rules

- **Kernel surfaces interoperate with released pi.** Session JSONL, event JSON, RPC frames,
  settings/models/auth files, provider wire shapes, the JS extension surface: Orb reads what pi
  writes and runs what pi runs, verified by fixtures. Orb may improve on upstream, kernel included
  (P5): a deliberate improvement makes that fixture Orb-owned and adds a divergence-ledger line.
  Never change persisted or emitted JSON by accident.
- **No backward compatibility with Orb's own past.** Orb is deployed only inside Ordalie. No legacy
  formats, migrations, deprecated APIs or shims for earlier Orb; change interfaces in place and
  update every consumer (this repo, and Ordalie-back when it upgrades) in the same work. New
  capabilities follow P3: capability modules, never ad-hoc core widening.
- **Pure Go.** `CGO_ENABLED=0` must build. No cgo, no sidecar binaries except the upstream-sanctioned
  rg/fd auto-download.
- **Latest stable Go.** `go.mod` tracks the latest stable release from go.dev; CI and releases
  follow it, and the linter is rebuilt with the same toolchain.
- **Slim.** Stdlib first, internal helper next, dependency last and only via the ARCHITECTURE §8
  table. No speculative abstraction, no "for later" scaffolding.
- **Never weaken a criterion or a golden to pass it** (P9). No softened fixtures, skipped checks,
  lowered budgets or hand-edited goldens. A failing fixture means the code is wrong. A genuinely
  impossible criterion stops the work and goes to the owner.
- **Comments** state constraints the code can't (e.g. "field order matches upstream
  serialization"), never narration.

## Conformance and the upstream pin

Fixture families F1–F13 are defined in ARCHITECTURE §6. Extraction scripts live in
`conformance/extract/` and run with Node ≥22 inside `.upstream/` (Node is dev tooling only).
Wire, provider and algorithmic families are upstream-extracted parity gates. The render families
(`F12*` and the `WP450` replay/preview/UI-demo files) are Orb-owned snapshots of Orb's own TUI
(P6); their behavior-shaped values stay frozen upstream captures.

- `make upstream` — materialize the pinned pi checkout in `.upstream/`.
- `make fixtures` — regenerate the upstream-extracted goldens.
- `make fixtures-tui` — regenerate the Orb-owned render snapshots after a deliberate TUI change.
- `make fixtures-check` — regenerate into a temp tree and diff against the committed goldens
  (Linux in practice: F9 writes case-distinct files).
- `make upstream-rpc-tests` — run upstream's RPC suite against the orb binary.
- `make sdk-surface` — re-declare the embedded extension SDK's exports from the pinned sources.

`UPSTREAM.lock` pins the released pi version the fixtures are extracted from. Pin only the exact
commit of a published release tag, never `main` or unreleased work. To move it: update the lock,
run `make fixtures` and `make sdk-surface`, then `make check`; red conformance is the work list.
Only kernel paths carry port obligation; other upstream changes are cherry-picked on merit.

## Layout quick reference

`ai/` unified LLM layer · `engine/` loop, Agent, harness · `tui/` renderer and components ·
`agent/` tools, session, config, extensions, modes, assembly · `plugins/` capability modules ·
`bridge/` peer protocol · `host/` and `platforms/` ports and hosts · `chat/` gateway ·
`cmd/orb` CLI · `internal/` helpers · `conformance/` extract (TS, dev-only), fixtures, runner.
Full tree: ARCHITECTURE §1.

## Open work

- Bridge fully in the core (P4, P11): a host-supplied transport port and every session registered
  as an instance by default. Per-target gaps are in `docs/deployments.md`.
- Release the pi 1.0 adoption now under `[Unreleased]` in `CHANGELOG.md`.
- Upgrade Ordalie-back from Orb v0.7.0: filter system messages out of client SSE, and move
  `agentengine` from `shouldStopAfterTurn` to `finishTurn` and from `SetSystemPrompt` to a system
  message ahead of its replayed messages.
