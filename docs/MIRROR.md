# MIRROR — upstream ↔ pi-go correspondence

Consumed by the sync tool (WP-610) to map upstream diffs to affected Go code. Every WP that adds
files appends rows. Package-level baseline:

| Upstream (at UPSTREAM.lock) | pi-go |
|---|---|
| `packages/ai/src/` | `ai/` |
| `packages/ai/src/api/` | `ai/api/` |
| `packages/ai/src/auth/` | `ai/auth/` |
| `packages/ai/src/providers/` + `models.generated.ts` | `ai/providers/`, `ai/models/` |
| `packages/agent/src/` | `agent/` |
| `packages/agent/src/harness/` | `agent/harness/` |
| `packages/tui/src/` | `tui/` |
| `packages/coding-agent/src/core/tools/` | `codingagent/tools/` |
| `packages/coding-agent/src/core/extensions/` | `codingagent/extensions/` (+ `jsbridge/`) |
| `packages/coding-agent/src/core/session-manager.ts`, `export-html/` | `codingagent/session/` |
| `packages/coding-agent/src/core/{settings-manager,auth-storage}.ts`, trust | `codingagent/config/` |
| `packages/coding-agent/src/modes/` | `codingagent/modes/` |
| `packages/coding-agent/src/cli/` | `cmd/pi/` |
| `packages/coding-agent/src/utils/truncate.ts` | `internal/truncate/` |
| (npm `partial-json`) | `internal/partialjson/` |
| `packages/orchestrator/` | — excluded (DECISIONS ledger) |

File-level rows are appended beneath this line as WPs land.

| Upstream file | pi-go file | WP |
|---|---|---|
| (project bootstrap) | `go.mod`, `Makefile`, `.github/workflows/ci.yml` | WP-001 |
| `packages/coding-agent/src/cli.ts` (placeholder only) | `cmd/pi/main.go` | WP-001 |
| `packages/coding-agent/src/core/tools/truncate.ts` | `conformance/extract/f5-truncation.ts`, `conformance/fixtures/F5/` | WP-002 |
| `packages/*/test/` (fixture conventions) | `conformance/runner/`, `conformance/README.md` | WP-002 |
