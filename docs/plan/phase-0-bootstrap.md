# Phase 0 — Bootstrap

## WP-001 — Repo bootstrap

**Objective:** a building, linting, CI-green empty module ready for WPs.

**Scope**
- `go.mod` (`github.com/OrdalieTech/pi-go`, go ≥ 1.25), directory skeleton per ARCHITECTURE §1
  (empty packages with doc.go only where needed — no speculative code).
- `LICENSE`: MIT, dual copyright (© 2025 Mario Zechner — upstream pi; © 2026 Ordalie — this port).
- `README.md` stub: one paragraph — what pi-go is, provenance + credit to pi (pi.dev), status: under construction.
- `Makefile`: `build`, `test`, `lint`, `upstream` (clone `earendil-works/pi` at `UPSTREAM.lock` commit
  into `.upstream/`, gitignored), `sync` (placeholder until WP-610).
- CI (GitHub Actions): matrix `{linux,darwin} × {amd64,arm64}` cross-build with `CGO_ENABLED=0`,
  `go vet`, golangci-lint, `go test -race` on linux+darwin runners.
- `.gitignore` (`.upstream/`, build artifacts), golangci-lint config (modest ruleset, no vanity linters).

**Out of scope:** any product code; goreleaser (WP-661).

**Acceptance**
- Fresh clone: `make build test lint` green; CI green on a PR.
- `make upstream` materializes the pinned commit and `git -C .upstream rev-parse HEAD` matches UPSTREAM.lock.

## WP-002 — Conformance harness + fixture conventions

**Objective:** the machinery every later WP uses to land fixtures.

**Upstream refs:** `packages/*/test/` layout; `packages/ai/src/providers/faux` (faux provider —
port target lives in WP-130, but extraction must be able to drive the TS one).

**Scope**
- `conformance/` layout per ARCHITECTURE §6: `extract/` (TS project, Node ≥22, runs inside
  `.upstream/` via its installed deps; package.json with tsx or vitest as runner), `fixtures/<family>/`
  (committed JSON goldens + `manifest.json` recording upstream commit + generator), `runner/`
  (Go helpers: load fixture dir, table-drive tests, byte-diff reporting).
- Implement family **F5 (truncation)** end-to-end as the proving example: extraction script reads
  upstream `packages/coding-agent/src/core/tools/truncate.ts` test cases / drives the function,
  emits goldens; `internal/truncate` gets a placeholder failing test wired to the goldens
  (implementation arrives in WP-140 — mark skipped until then).
- `make fixtures` regenerates all families from `.upstream/`; CI asserts regeneration is clean
  (goldens committed == regenerated) once families exist.

**Out of scope:** the other fixture families (land with their WPs); sync reporting (WP-610).

**Acceptance**
- `make fixtures` runs extraction inside `.upstream/` and rewrites `conformance/fixtures/F5/` deterministically (stable ordering, no timestamps).
- `go test ./conformance/...` consumes F5 goldens (skipped-pending status is visible, not silently green).
- A `conformance/README.md` documents: adding a family, regenerating, the no-hand-edits rule.
