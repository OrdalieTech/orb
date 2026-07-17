# Phase 6 — Ops, release, future

## WP-610 — Sync tooling (full)

**Upstream refs:** none (our machinery); consumes `docs/MIRROR.md`, `UPSTREAM.lock`, conformance.

**Scope:** `internal/sync` + `make sync`: fetch upstream, diff lock..HEAD, map changed files via
MIRROR.md to affected Go files/WPs, classify changes (wire-format / API-surface / feature-only /
docs), regenerate all fixture families at the new commit, run conformance, emit
`docs/sync/reports/<date>.md` (delta summary, fixture diffs, red tests, proposed work items),
lock-bump command gated on green. Designed to be run by a coding agent as a routine WP; cron
promotion is a one-line CI addition deliberately left to the owner (D5).

**Acceptance:** dry-run against a known newer upstream commit produces a correct, readable report;
a wire-format change upstream (simulated) is flagged as such; lock bump refuses on red.

## WP-620 — Documentation

**Scope:** README (what/why, provenance + credit to Mario Zechner's pi, install, quickstart,
divergence ledger link, dimetron/pi-go disambiguation note), Go SDK guide (mirroring upstream
`docs/sdk.md` structure), extension-author guide for the bridge (what works, shims table, matrix
link), CONTRIBUTING (points at AGENTS.md + plan), doc comments sweep.

**Acceptance:** a newcomer path: install → first session → embed SDK → run an upstream extension,
each verified by following the docs literally.

## WP-661 — Release pipeline — gate G4

**Scope:** goreleaser: static `CGO_ENABLED=0` binaries for `{linux,darwin}×{amd64,arm64}`,
checksums, GitHub Releases; curl install script + Homebrew tap; version embedding; update check
against GitHub releases only (never pi.dev), honoring `PI_SKIP_VERSION_CHECK`; **gate G4**: notify-
only vs in-place self-update for `pi update` — decide by weighing supply-chain surface vs
convenience, record in DECISIONS.
**Acceptance:** `v0.1.0` pre-release cut from CI; install script verified on clean linux + macOS
VMs; binary size and cold-start budgets (ARCHITECTURE §9) measured and recorded in release notes.

## WP-670 — Windows wave (future, unscheduled)

**Upstream refs:** `src/utils/shell.ts` (git-bash discovery), bash tool Windows paths (`taskkill
/F /T`), tui win32 console addon behavior, path/terminal quirks across packages.

**Scope (when scheduled):** `bash_windows.go` (git-bash discovery, taskkill trees), console mode
handling via x/sys/windows (replacing the native addon), path normalization sweep, rg/fd windows
assets, CI windows runner, fixture families re-run on windows.

**Acceptance:** deferred — defined at scheduling time; entry criterion: phases 1–5 conformance
stable for a full sync cycle.
