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
| `packages/coding-agent/src/core/session-manager.ts` (tree extraction, fork, listing, labels, lazy persistence) | `codingagent/session/tree_ops.go`, `codingagent/session/list.go`, `codingagent/session/wp320_audit_test.go` | WP-320 |
| `packages/coding-agent/src/main.ts`, `packages/coding-agent/src/cli/{args,session-picker}.ts` (fork/resume/session/name integration) | `cmd/pi/args.go`, `cmd/pi/args_test.go`, `cmd/pi/main.go`, `cmd/pi/session_cli.go`, `cmd/pi/session_cli_test.go` | WP-320 |
| `packages/coding-agent/src/core/export-html/{index,tool-renderer}.ts`, `template.{html,css,js}`, vendored Marked and Highlight.js assets | `codingagent/session/exporthtml/export.go`, `codingagent/session/exporthtml/renderer.go`, `codingagent/session/exporthtml/renderer_test.go`, `codingagent/session/exporthtml/assets/`, HTML tests and testdata | WP-320 |
| `packages/coding-agent/src/modes/interactive/theme/{theme.ts,dark.json,light.json}` (built-in export colors and terminal fallback) | `codingagent/session/exporthtml/theme.go` | WP-320 |
| WP-320 Markdown export divergence (no upstream equivalent) | `codingagent/session/exporthtml/markdown.go`, `codingagent/session/exporthtml/markdown_test.go`, `codingagent/session/exporthtml/testdata/session.md` | WP-320 |
| Session tree, fork, listing, and HTML-export behavior | `conformance/extract/f6-session.ts`, `conformance/fixtures/F6/tree-export.json`, `conformance/runner/f6_tree_export_test.go` | WP-320 |
| `packages/ai/src/auth/{types,context,credential-store,helpers,resolve}.ts` | `ai/auth/{types,context,credential_json,store,resolve}.go` and tests | WP-211 |
| `packages/ai/src/auth/oauth/{anthropic,pkce,oauth-page}.ts` | `ai/auth/oauth/{anthropic,pkce,oauth_page}.go` and tests | WP-211 |
| `packages/coding-agent/src/core/auth-storage.ts`, `packages/coding-agent/src/migrations.ts` | `codingagent/config/{auth,auth_lock,auth_migrate}.go` and tests | WP-211 |
| `packages/coding-agent/src/core/resolve-config-value.ts` | `codingagent/config/resolve_config_value.go` and tests | WP-211 |
| Headless provider authentication over the upstream auth interaction core | `cmd/pi/auth.go`, `cmd/pi/args.go`, `cmd/pi/main.go`, `cmd/pi/runtime.go` and tests | WP-211 |
| Auth storage, migration, OAuth-page, and provider-auth cross-read behavior | `conformance/extract/f2-auth.ts`, `conformance/extract/f2-auth-verify.ts`, `conformance/fixtures/F2/auth-storage.json`, `codingagent/config/auth_conformance_test.go` | WP-211 |
| WP-211 automated and credential-gated manual verification evidence | `docs/plan/wp-211-auth-report.md` | WP-211 |
| `packages/ai/src/api/google-generative-ai.ts`, `packages/ai/src/api/google-shared.ts`, `packages/ai/src/api/simple-options.ts`, `@google/genai` Mldev request transforms | `ai/api/googlegenerativeai.go`, `ai/api/google_shared.go`, `ai/api/google_mldev.go`, `ai/api/google_schema.go`, `ai/api/googlegenerativeai_test.go`, `ai/api/google_shared_test.go`, `ai/api/google_schema_test.go`, `ai/api/google_live_test.go`, `ai/api/stream_simple.go` | WP-221 |
| `packages/ai/src/providers/google.ts`, `packages/ai/src/auth/helpers.ts` | `ai/providers/google.go`, `ai/providers/google_test.go`, `ai/providers/openai.go`, `codingagent/config/model_registry.go` | WP-221 |
| Google adapter request, stream, provider, signed-thought, StringEnum, header, URL, UTF-16, image, and multiline-SSE behavior | `conformance/extract/f2-google.ts`, `conformance/extract/f2-openai.ts`, `conformance/fixtures/F2/google-provider.json`, `conformance/fixtures/F2/google-requests.json`, `conformance/fixtures/F2/google-streams.json`, `ai/api/conformance_test.go` | WP-221 |
| `packages/ai/src/api/google-vertex.ts` (G2 deferral) | `docs/plan/wp-221-g2-report.md`, `docs/plan/phase-2-providers.md` (WP-222) | WP-221 |
| `packages/ai/src/api/mistral-conversations.ts` | `ai/api/mistralconversations.go`, `ai/api/mistralconversations_test.go`, `ai/api/mistral_live_test.go`, `ai/api/stream_simple.go` | WP-231 |
| `packages/ai/src/api/azure-openai-responses.ts` | `ai/api/azureopenairesponses.go`, `ai/api/azureopenairesponses_test.go`, `ai/api/azure_openai_live_test.go`, `ai/api/openairesponses.go`, `ai/api/stream_simple.go` | WP-231 |
| `packages/ai/src/providers/{mistral,azure-openai-responses}.ts` | `ai/providers/mistral.go`, `ai/providers/mistral_test.go`, `ai/providers/azure_openai.go`, `ai/providers/azure_openai_test.go`, `ai/providers/openai.go` | WP-231 |
| Mistral and Azure request/recorded-stream behavior | `conformance/extract/f2-mistral-azure.ts`, `conformance/extract/f2-openai.ts`, `conformance/fixtures/F2/{mistral,azure}-{requests,streams}.json`, `ai/api/conformance_test.go` | WP-231 |
| WP-231 acceptance and credential-gated live evidence | `docs/plan/wp-231-report.md` | WP-231 |
| `packages/ai/src/api/google-vertex.ts`, `packages/ai/src/api/google-shared.ts`, `@google/genai` Vertex request transforms | `ai/api/googlevertex.go`, `ai/api/google_vertex_wire.go`, `ai/api/googlevertex_test.go`, `ai/api/google_vertex_live_test.go`, `ai/api/google_shared.go`, `ai/api/googlegenerativeai.go`, `ai/api/stream_simple.go` | WP-222 |
| `packages/ai/src/api/google-vertex.ts`; `@google/genai` Node auth; `google-auth-library` ADC, external-account, impersonation, executable, certificate, and AWS credential behavior | `ai/api/google_vertex_adc.go`, `ai/api/google_vertex_adc_test.go`, `ai/api/google_vertex_adc_external_user.go`, `ai/api/google_vertex_adc_external_user_test.go`, `ai/api/google_vertex_adc_impersonated.go`, `ai/api/google_vertex_adc_impersonated_test.go`, `ai/api/google_vertex_adc_external_account.go`, `ai/api/google_vertex_adc_external_account_test.go`, `ai/api/google_vertex_adc_external_account_identity.go`, `ai/api/google_vertex_adc_external_account_executable.go`, `ai/api/google_vertex_adc_external_account_aws.go` | WP-222 |
| `packages/ai/src/providers/google-vertex.ts`, `packages/ai/src/env-api-keys.ts`, `packages/ai/test/google-vertex-api-key-resolution.test.ts` | `ai/providers/google_vertex.go`, `ai/providers/google_vertex_test.go`, `ai/providers/openai.go`, `ai/auth/types.go`, `codingagent/config/model_registry.go` | WP-222 |
| `packages/coding-agent/src/core/{agent-session,model-runtime,auth-storage}.ts` (request-scoped provider auth environment) | `agent/types.go`, `agent/agent.go`, `agent/loop.go`, `agent/loop_test.go`, `codingagent/session_runtime.go`, `codingagent/session_runtime_test.go`, `cmd/pi/main.go`, `cmd/pi/runtime.go`, `cmd/pi/runtime_test.go` | WP-222 |
| Google Vertex adapter request, stream, provider-login, auth-resolution, URL, schema, thinking, header, ADC-token, and metadata behavior | `conformance/extract/f2-google-vertex.ts`, `conformance/extract/f2-openai.ts`, `conformance/fixtures/F2/google-vertex-provider.json`, `conformance/fixtures/F2/google-vertex-requests.json`, `conformance/fixtures/F2/google-vertex-streams.json`, `ai/api/conformance_test.go` | WP-222 |
| WP-222 dependency, binary-impact, fixture, and live-test evidence | `docs/plan/wp-222-vertex-report.md` | WP-222 |
| `packages/ai/src/api/bedrock-converse-stream.ts`, `packages/ai/src/utils/node-http-proxy.ts` | `ai/api/bedrockconversestream.go`, `ai/api/bedrockconversestream_test.go`, `ai/api/bedrock_live_test.go`, `ai/api/stream_simple.go` | WP-232 |
| `packages/ai/src/providers/amazon-bedrock.ts`, `packages/ai/src/env-api-keys.ts` | `ai/providers/bedrock.go`, `ai/providers/bedrock_test.go`, `ai/providers/openai.go`, `codingagent/config/model_registry.go`, `codingagent/config/model_registry_test.go` | WP-232 |
| Bedrock adapter request/stream behavior and provider credential resolution | `conformance/extract/f2-bedrock.ts`, `conformance/extract/f2-openai.ts`, `conformance/fixtures/F2/bedrock-provider.json`, `conformance/fixtures/F2/bedrock-requests.json`, `conformance/fixtures/F2/bedrock-streams.json`, `ai/api/conformance_test.go` | WP-232 |
| WP-232 acceptance evidence | `docs/plan/wp-232-report.md` | WP-232 |
| `packages/ai/src/api/pi-messages.ts` | `ai/api/pimessages.go`, `ai/api/pimessages_test.go`, `ai/api/pimessages_live_test.go`, `ai/api/stream_simple.go` | WP-260 |
| `packages/ai/src/types.ts` (pi-messages transport event fields and server-error member insertion order) | `ai/stream.go`, `ai/types.go`, `ai/json.go` | WP-260 |
| pi-messages request and serialized-event stream behavior | `conformance/extract/f2-pi-messages.ts`, `conformance/extract/f2-openai.ts`, `conformance/fixtures/F2/pi-messages-{requests,streams}.json`, `ai/api/conformance_test.go` | WP-260 |
| WP-260 acceptance and credential-gated live evidence | `docs/plan/wp-260-report.md` | WP-260 |
| `packages/coding-agent/src/modes/print-mode.ts`, `packages/coding-agent/docs/json.md` | `codingagent/modes/print.go`, `codingagent/modes/print_test.go`, `cmd/pi/args.go`, `cmd/pi/main.go`, `cmd/pi/json_mode_test.go` | WP-330 |
| `packages/coding-agent/src/core/agent-session.ts` (JSON-mode session event extras) | `codingagent/session_events.go`, `codingagent/session_runtime.go`, `codingagent/session_runtime_test.go` | WP-330 |
| `packages/coding-agent/src/modes/print-mode.ts`, `packages/coding-agent/src/core/agent-session.ts`, `packages/ai/src/providers/faux.ts` | `conformance/extract/f3-session.ts`, `conformance/fixtures/F3-session/`, `cmd/pi/json_mode_test.go` | WP-330 |
| JSON-mode LF framing consumed through EOF | `conformance/runner/fixture.go`, `conformance/runner/fixture_test.go` | WP-330 |
