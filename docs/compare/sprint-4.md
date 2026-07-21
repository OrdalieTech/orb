# Sprint 4 comparison — release closure (M5)

Status: **deterministic TS-pi parity is green**. Publication remains gated only by the live OAuth,
hosted-nightly, and real-terminal/macOS checks tracked in `docs/plan/PROGRESS.md`.

## Revisions and method

- Upstream: pi `0.80.10` at `3a40794ea14c6202586cc203d5b928eca9f6b673`.
- pi-go: release candidate containing this report, descending from `e46f671`.
- The same pinned TypeScript extractors and black-box RPC scenarios run against `.upstream/` and
  pi-go. Release-only Go surfaces are checked separately because TS pi has no GoReleaser,
  static-binary, or Homebrew counterpart.

## Results

| Surface | Result |
|---|---|
| F1–F12 extraction and Go runners | Regeneration is byte-clean; no golden was hand-edited |
| Black-box RPC | Upstream suite passes 28/28 against `pi --mode rpc` |
| CLI, session, provider, agent, TUI, and bridge behavior | Sprint 1–3 scripted comparisons remain green under the final gate |
| Source/API tail | 436/436 upstream source files mapped; zero open should-fix alignment findings |
| Release identity | ldflags version plus pinned upstream identity; update checks use OrdalieTech releases and never pi.dev |
| Go release additions | Four CGO-disabled targets, checksums, curl installer, and Homebrew formula generation configured |

## Ledgered differences

- Radius, telemetry, the removed-upstream llama extension, and pi.dev upload/update services remain
  excluded or neutralized by D2.
- The parallel `AgentHarness` facade and application-specific `streamProxy` protocol remain
  dissolved/excluded by D29; their reusable seams are present in the SDK.
- Go modules, static archives, and the Homebrew tap replace upstream's Node/Bun packaging under
  D1, D7, D8, and G4. These are release-platform differences, not wire or behavior drift.

The compact current ledger is `docs/compare/upstream-alignment-findings.md`; M5 measurements and
external blockers are in `docs/trim/M5.md`.

## Verification

```text
make fixtures-check
make check
.tools/bin/goreleaser check
```

The exact candidate tree ran those commands. The release generates the formula without a
persistent cross-repository credential; the authenticated release operator publishes it to the
tap. Proving the clean-macOS install still requires owner-controlled infrastructure.
