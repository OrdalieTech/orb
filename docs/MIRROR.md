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
| `packages/ai/src/types.ts` | `ai/types.go`, `ai/model.go`, `ai/json.go`, `ai/conformance_test.go`, `ai/model_test.go`, `ai/json_test.go` | WP-110 |
| `packages/ai/src/utils/event-stream.ts` | `ai/stream.go`, `ai/stream_test.go` | WP-110 |
| `packages/ai/src/utils/diagnostics.ts` | `ai/types.go` | WP-110 |
| `packages/ai/src/` (`JSON.stringify` wire semantics) | `internal/jsonwire/` | WP-110 |
| `packages/ai/src/types.ts` (serialization corpus) | `conformance/extract/f1-messages.ts`, `conformance/fixtures/F1/cases.json` | WP-110 |
| `packages/ai/src/utils/json-parse.ts`, npm `partial-json@0.1.7` | `internal/partialjson/`, `conformance/extract/f1-partialjson.ts`, `conformance/fixtures/F1/partialjson.json` | WP-110 |
| `packages/ai/src/utils/typebox-helpers.ts`, npm `typebox@1.1.38` | `internal/jsonschema/`, `conformance/extract/f1-schema.ts`, `conformance/fixtures/F1/schema.json` | WP-110 |
| `packages/ai/src/api/openai-responses.ts`, `openai-responses-shared.ts` | `ai/api/openairesponses.go`, `ai/api/openairesponses_test.go`, `ai/api/openai_live_test.go` | WP-120 |
| `packages/ai/src/api/openai-completions.ts` | `ai/api/openaicompletions.go`, `ai/api/openaicompletions_test.go` | WP-120 |
| `packages/ai/src/api/transform-messages.ts` | `ai/api/openai_messages.go`, `ai/api/openai_messages_test.go` | WP-120 |
| `packages/ai/src/api/simple-options.ts`, `packages/ai/src/utils/estimate.ts`, `packages/ai/src/models.ts` (cost and thinking clamps) | `ai/api/simple_options.go`, `ai/api/simple_options_test.go`, `ai/api/openai_common.go`, `ai/api/openai_common_test.go` | WP-120 |
| `packages/ai/src/api/openai-prompt-cache.ts`, `packages/ai/src/utils/provider-env.ts`, `packages/ai/src/utils/headers.ts` | `ai/api/openai_common.go`, `ai/api/openai_common_test.go` | WP-120 |
| `packages/ai/src/utils/error-body.ts`, `packages/ai/src/utils/sanitize-unicode.ts`, `packages/ai/src/api/github-copilot-headers.ts` | `ai/api/openai_common.go`, `ai/api/openai_common_test.go`, `ai/api/openaicompletions.go`, `ai/api/openairesponses.go`, `internal/jsonwire/marshal.go`, `internal/jsonwire/marshal_test.go` | WP-120 |
| `packages/ai/src/utils/deferred-tools.ts`, `packages/ai/src/utils/hash.ts` | `ai/api/openairesponses.go`, `ai/api/openairesponses_test.go`, `ai/api/openaicompletions.go`, `ai/api/openaicompletions_test.go` | WP-120 |
| `packages/ai/src/types.ts` (streaming ToolCall scratch and JSON.stringify replay) | `ai/types.go`, `ai/json.go`, `ai/json_test.go` | WP-120 |
| `packages/ai/src/utils/json-parse.ts`, npm `partial-json@0.1.7` (streaming argument stringify order) | `internal/partialjson/partialjson.go`, `internal/partialjson/stringify.go`, `internal/partialjson/partialjson_test.go` | WP-120 |
| `packages/ai/src/providers/openai.ts`, `packages/ai/src/auth/helpers.ts` | `ai/providers/openai.go`, `ai/providers/openai_test.go` | WP-120 |
| OpenAI adapter and provider request/stream behavior | `conformance/extract/f2-openai.ts`, `conformance/fixtures/F2/`, `ai/api/conformance_test.go` | WP-120 |
