# WP-610 sync tooling report

Status: **complete**. `make sync` now analyzes an upstream target without changing the lock or
committed fixtures, while `make sync-bump` promotes only a green descendant revision from clean
lock and fixture paths.

## Workflow

The driver materializes and fetches `.upstream`, resolves the target once, diffs it against
`UPSTREAM.lock`, and restores the original checkout after extraction. It parses the actual
`docs/MIRROR.md` tables, including directory, wildcard, and brace patterns, then reports affected Go
targets and WPs for each added, modified, deleted, copied, or renamed upstream path. Classification
prioritizes compatibility-sensitive wire and persistence surfaces, then public APIs, docs, and
feature-only changes.

All registered extractors write to a temporary fixture tree. The driver compares that tree to the
committed goldens by path, byte count, and SHA-256, overlays it onto a temporary source copy, and
runs `go test -race ./...`; this makes red candidate behavior visible without overwriting the source
of truth. A promotion additionally requires descendant ancestry and clean `UPSTREAM.lock` plus
`conformance/fixtures` paths, then installs the generated tree and restores the prior fixtures if
the atomic lock rewrite fails.

## Acceptance evidence

At the time of verification, `origin/main` was still the pinned `3da591ab`. The dry run therefore
used the later-dated upstream branch commit `42a16ee5098df32396f47a2e00b9ca194c5af778`
(`origin/add-usage-to-more-entry-types`, 2026-07-17 20:02 UTC). The generated
[`2026-07-18` sync report](../sync/reports/2026-07-18.md) maps 46 changed paths, identifies five
wire-format changes and four API-surface changes, records all candidate fixture deltas, and reports
the genuine F10 incompatibility caused by the branch's changed summary-output shape. It also marks
the branch divergence and refuses promotion; `.upstream` returned to the pin, while
`UPSTREAM.lock` and committed fixtures remained unchanged.

Ordinary tests create a local base/newer upstream pair and prove the same readable dry-run report,
including explicit wire-format classification. Separate promotion tests prove that red conformance
returns both the red and promotion-refusal sentinels without changing either lock or fixtures, and
that a green descendant installs fixtures and advances the lock.

## Verification

The final candidate passes:

```text
make fixtures-check
make build test lint
go mod verify
go mod tidy -diff
CGO_ENABLED=0 GOOS={linux,darwin} GOARCH={amd64,arm64} go build ./...
```

No dependency was added, no committed golden changed, and the real red dry run remains committed as
evidence rather than being weakened or regenerated away.
