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
| `packages/coding-agent/src/core/tools/truncate.ts` | `internal/truncate/` |
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
| `packages/agent/src/types.ts` | `agent/types.go`, `agent/events.go`, `agent/types_test.go`, `agent/events_test.go` | WP-130 |
| `packages/agent/src/agent-loop.ts` | `agent/loop.go`, `agent/loop_test.go` | WP-130 |
| `packages/agent/src/agent.ts` | `agent/agent.go`, `agent/clone.go`, `agent/agent_test.go` | WP-130 |
| `packages/ai/src/utils/validation.ts` | `internal/jsonschema/validate.go`, `internal/jsonschema/validate_test.go` | WP-130 |
| `packages/ai/src/providers/faux.ts` | `ai/providers/faux/` | WP-130 |
| `packages/ai/src/compat.ts` (`streamSimple` dispatch for landed API shapes) | `ai/api/stream_simple.go` | WP-130 |
| `packages/ai/src/providers/faux.ts` (UTF-16 streaming and surrogate wire behavior) | `ai/stream.go`, `ai/json.go`, `internal/jsonwire/marshal.go` | WP-130 |
| Agent-loop scripted behavior | `conformance/extract/f3-agent.ts`, `conformance/fixtures/F3/`, `conformance/runner/f3_agent_test.go` | WP-130 |
| `packages/coding-agent/src/core/tools/path-utils.ts`, `packages/coding-agent/src/utils/paths.ts` | `codingagent/tools/path.go`, `codingagent/tools/path_test.go` | WP-140 |
| `packages/coding-agent/src/core/tools/read.ts` | `codingagent/tools/read.go`, `codingagent/tools/read_test.go`, `codingagent/tools/common.go` | WP-140 |
| `packages/coding-agent/src/core/tools/write.ts` | `codingagent/tools/write.go`, `codingagent/tools/write_test.go` | WP-140 |
| `packages/coding-agent/src/core/tools/edit.ts` | `codingagent/tools/edit.go`, `codingagent/tools/edit_test.go` | WP-140 |
| `packages/coding-agent/src/core/tools/edit-diff.ts`, npm `diff@8.0.4` | `codingagent/tools/editdiff.go`, `codingagent/tools/editdiff_test.go`, `conformance/extract/f4-edit.ts`, `conformance/fixtures/F4/`, `conformance/runner/f4_edit_test.go` | WP-140 |
| `packages/coding-agent/src/core/tools/ls.ts` | `codingagent/tools/ls.go`, `codingagent/tools/ls_test.go` | WP-140 |
| `packages/coding-agent/src/core/tools/{read,write,edit,ls}.ts` (`renderCall`/`renderResult` stubs) | `codingagent/tools/render.go`, `codingagent/tools/render_test.go` | WP-140 |
| `packages/coding-agent/src/core/tools/file-mutation-queue.ts` | `codingagent/tools/mutation_queue.go`, `codingagent/tools/mutation_queue_test.go` | WP-140 |
| `packages/agent/src/agent-loop.ts` (parallel invocation-order reservation) | `agent/types.go`, `agent/loop.go`, `agent/loop_test.go` | WP-140 |
| `packages/coding-agent/src/core/tools/truncate.ts` | `internal/truncate/truncate.go`, `internal/truncate/truncate_test.go`, `conformance/runner/f5_truncation_test.go` | WP-140 |
| `packages/coding-agent/src/core/tools/bash.ts` | `codingagent/tools/bash.go`, `codingagent/tools/bash_unix.go`, `codingagent/tools/bash_test.go`, `codingagent/tools/bash_unix_test.go` | WP-150 |
| `packages/coding-agent/src/core/tools/output-accumulator.ts` | `codingagent/tools/output_accumulator.go`, `codingagent/tools/output_accumulator_test.go`, `conformance/extract/f5-truncation.ts`, `conformance/fixtures/F5/accumulator.json`, `conformance/runner/f5_truncation_test.go` | WP-150 |
| `packages/coding-agent/src/utils/shell.ts`, `packages/coding-agent/src/utils/child-process.ts` | `codingagent/tools/bash_unix.go`, `codingagent/tools/bash_unix_test.go` | WP-150 |
| `packages/coding-agent/src/core/tools/grep.ts`, `packages/coding-agent/src/core/tools/find.ts` | `codingagent/tools/grep.go`, `codingagent/tools/find.go`, `codingagent/tools/grep_test.go`, `codingagent/tools/find_test.go`, `codingagent/tools/search_test.go`, `codingagent/tools/search_minitree_test.go`, `codingagent/tools/testdata/search/tree/` | WP-150 |
| `packages/coding-agent/src/utils/tools-manager.ts` | `codingagent/tools/toolmanager.go`, `codingagent/tools/toolmanager_test.go` | WP-150 |
| `packages/coding-agent/src/core/settings-manager.ts`, `packages/coding-agent/docs/settings.md` | `codingagent/config/settings.go`, `codingagent/config/settings_test.go` | WP-160 |
| `packages/coding-agent/src/core/session-manager.ts`, `packages/coding-agent/src/core/session-cwd.ts`, `packages/coding-agent/docs/session-format.md` | `codingagent/session/`, `conformance/extract/f6-session.ts`, `conformance/extract/f6-verify.ts`, `conformance/fixtures/F6/`, `conformance/runner/f6_session_test.go` | WP-160 |
| `packages/coding-agent/src/core/system-prompt.ts` | `codingagent/system_prompt.go`, `codingagent/system_prompt_test.go` | WP-160 |
| `packages/coding-agent/src/core/resource-loader.ts` | `codingagent/resources.go`, `codingagent/resources_test.go` | WP-160 |
| `packages/coding-agent/src/core/messages.ts`, `packages/agent/src/agent.ts` (custom-message state preservation) | `codingagent/messages.go`, `codingagent/messages_test.go`, `agent/clone.go`, `agent/agent_test.go` | WP-160 |
| `packages/coding-agent/src/cli/{args,file-processor,initial-message}.ts`, `packages/coding-agent/src/main.ts` (phase-1 subset) | `cmd/pi/` | WP-160 |
| `packages/coding-agent/src/modes/print-mode.ts` (text subset) | `codingagent/modes/print.go`, `codingagent/modes/signals_unix.go`, `codingagent/modes/print_test.go`, `codingagent/modes/print_signal_unix_test.go` | WP-160 |
| `packages/coding-agent/src/core/{system-prompt,resource-loader,settings-manager}.ts` | `conformance/extract/f9-system-prompt.ts`, `conformance/extract/upstream-model-data.ts`, `conformance/fixtures/F9/`, `conformance/runner/f9_system_prompt_test.go` | WP-160 |
| `packages/coding-agent/src/core/session-manager.ts` (`JSON.stringify` migration rewrite) | `ai/json.go`, `internal/jsonwire/marshal.go` | WP-160 |
| `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/modes/print-mode.ts` (live integration evidence) | `docs/plan/skeleton-gate-report.md`, `docs/plan/artifacts/wp-170-dogfood.jsonl`, `docs/plan/artifacts/wp-170-self-dogfood.jsonl` | WP-170 |
| M1 recurring trim checklist and WP-170 gate evidence | `docs/trim/M1.md` | WP-180 |
| `packages/ai/scripts/generate-models.ts`, `packages/ai/src/models.ts`, provider model catalogs | `ai/models/internal/cataloggen/`, `ai/models/cmd/genmodels/`, `ai/models/catalog.go`, `ai/models/corrections.go`, `ai/models/generated.go`, `ai/models/testdata/` | WP-250 |
| `packages/ai/src/models-store.ts`, `packages/coding-agent/src/core/models-store.ts`, `remote-catalog-provider.ts` | `ai/models/store.go`, `ai/models/store_test.go` | WP-250 |
| `packages/coding-agent/src/core/model-config.ts`, `provider-composer.ts`, `resolve-config-value.ts`, `packages/coding-agent/docs/models.md` | `codingagent/config/model_config.go`, `codingagent/config/model_config_schema.go`, `codingagent/config/model_config_test.go`, `codingagent/config/model_registry.go`, `codingagent/config/model_registry_env.go`, `codingagent/config/model_registry_test.go` | WP-250 |
| `packages/coding-agent/src/core/model-resolver.ts` | `codingagent/model_resolver.go`, `codingagent/model_resolver_test.go` | WP-250 |
| `packages/coding-agent/src/core/model-resolver.ts`, `packages/coding-agent/src/cli/list-models.ts` (`localeCompare`) | `internal/localecompare/localecompare.go` | WP-250 |
| `packages/coding-agent/src/cli/list-models.ts`, `args.ts`, `packages/coding-agent/src/package-manager-cli.ts` | `cmd/pi/models.go`, `cmd/pi/models_test.go`, `cmd/pi/args.go`, `cmd/pi/main.go`, `cmd/pi/runtime.go` | WP-250 |
| `packages/coding-agent/src/core/model-runtime.ts` (request-time configured model headers) | `agent/types.go`, `agent/agent.go`, `agent/loop.go`, `agent/loop_test.go`, `codingagent/config/model_config.go`, `cmd/pi/runtime.go` | WP-250 |
| Model pattern, list-table, numeric model limits, and docs-example behavior | `conformance/extract/wp250-models.ts`, `conformance/fixtures/WP250/`, `conformance/runner/wp250_fractional_numbers_test.go` | WP-250 |
| `packages/ai/src/api/anthropic-messages.ts` | `ai/api/anthropicmessages.go`, `ai/api/anthropicmessages_test.go`, `ai/api/anthropic_live_test.go`, `ai/api/simple_options.go`, `ai/api/stream_simple.go` | WP-210 |
| `packages/ai/src/types.ts` (Anthropic streaming indexes and usage insertion order) | `ai/types.go`, `ai/json.go` | WP-210 |
| `packages/ai/src/providers/anthropic.ts`, `packages/ai/src/auth/helpers.ts` | `ai/providers/anthropic.go`, `ai/providers/anthropic_test.go`, `ai/providers/openai.go` | WP-210 |
| `packages/ai/test/anthropic-sse-parsing.test.ts`, `packages/ai/src/utils/json-parse.ts` | `internal/partialjson/stringify.go`, `internal/partialjson/partialjson_test.go` | WP-210 |
| `packages/ai/src/utils/headers.ts` (provider response header records) | `ai/api/openai_common.go`, `ai/api/openai_common_test.go` | WP-210 |
| Anthropic adapter request/stream behavior and agent-loop signed-thinking replay | `conformance/extract/f2-anthropic.ts`, `conformance/extract/f2-openai.ts`, `conformance/extract/f3-agent.ts`, `conformance/fixtures/F2/`, `conformance/fixtures/F3/anthropic-thinking-signature-round-trip.jsonl`, `ai/api/conformance_test.go`, `conformance/runner/f3_agent_test.go` | WP-210 |
| `packages/agent/src/harness/messages.ts`, `packages/agent/src/harness/compaction/{compaction,branch-summarization,utils}.ts` | `agent/harness/types.go`, `agent/harness/compaction.go`, `agent/harness/branch_summary.go`, `agent/harness/compaction_test.go` | WP-310 |
| `packages/ai/src/utils/{retry,overflow}.ts` | `agent/harness/retry.go`, `agent/harness/compaction_test.go` | WP-310 |
| `packages/coding-agent/src/core/agent-session.ts`, `packages/coding-agent/src/core/settings-manager.ts`, `packages/coding-agent/src/modes/print-mode.ts` | `codingagent/session_runtime.go`, `codingagent/session_events.go`, `codingagent/session_runtime_test.go`, `codingagent/config/settings.go`, `codingagent/config/settings_test.go`, `codingagent/modes/print.go`, `cmd/pi/main.go`, `cmd/pi/runtime.go` | WP-310 |
| Compaction boundaries, selection, token accounting, and prompt structure | `conformance/extract/f10-compaction.ts`, `conformance/fixtures/F10/`, `conformance/runner/f10_compaction_test.go` | WP-310 |
