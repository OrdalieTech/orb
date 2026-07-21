# Provider, catalog, and login parity verification — 2026-07-21

Target: pinned pi v0.80.10 for the original gap ledger, pi v0.81.0
(`9c480b6ad2c7419875a7a850fb4ad5f9232313b8`) for SYNC-2 through SYNC-5, and
upstream commit `959cc1897e` (released in v0.81.1) for the explicitly requested
SYNC-1 backport. Each verdict comes from reading the upstream implementation,
the current Go implementation, the regression itself, and the adjacent diff.

All 52 requested IDs are **CONFIRMED** in the final tree. The adversarial pass
initially refuted OT-M6 and exposed four adjacent defects; those were corrected
and re-tested before this report was closed. There are no unresolved
INSUFFICIENT or REGRESSION verdicts.

## Codex

| ID | Verdict | Upstream behavior | Go behavior | Non-tautological regression |
|---|---|---|---|---|
| B1 | CONFIRMED | `.upstream/packages/ai/src/utils/event-stream.ts:21-35`; `api/openai-codex-responses.ts:435-443,652-682` | `ai/api/openaicodexresponses.go:253-283`; `openaicodexresponses_websocket.go:399-451` | `openaicodexresponses_test.go:316-348` and websocket test `415-450` break consumption early and prove clean termination without producer panic. |
| CX-M3 | CONFIRMED | `openai-codex-responses.ts:202-217,342-349` | `ai/api/openaicodexresponses.go:238-247,439-449` | `openaicodexresponses_test.go:352-389` decodes the actual HTTP body as zstd and compares the payload. |
| CX-M4 | CONFIRMED | `openai-codex-responses.ts:519-529` | `ai/api/openaicodexresponses.go:390-404` | `openaicodexresponses_test.go:276-312` supplies a null thinking-level map entry and observes the fallback effort. |
| CX-m1 | CONFIRMED | `openai-codex-responses.ts:117-130` | `ai/api/openaicodexresponses.go:808-816` | `openaicodexresponses_test.go:393-406` exercises matching and non-matching retry messages. |
| CX-m2 | CONFIRMED after adjacent correction | `openai-codex-responses.ts:639-681` | `ai/api/openaicodexresponses.go:644-699,741-761` | `openaicodexresponses_test.go:411-448` sends string, object, and malformed error envelopes; malformed optional members are ignored like TS rather than hard-failing. |
| CX-m3 | CONFIRMED | `openai-codex-responses.ts:1523-1527` | `ai/api/codex_useragent.go:5-32`; platform suffix file | `openaicodexresponses_test.go:451-476` checks the Node-compatible OS/architecture spelling. |
| CX-m4 | CONFIRMED | `openai-codex-responses.ts:287-311,1438-1458` | `ai/api/openaicodexresponses.go:202-213`; websocket terminal handling | `openaicodexresponses_websocket_test.go:454-487` aborts after a terminal event and observes the upstream failure path. |

## OpenAI, Azure, and Anthropic

| ID | Verdict | Upstream behavior | Go behavior | Non-tautological regression |
|---|---|---|---|---|
| OA-M1 | CONFIRMED | `.upstream/packages/ai/src/api/openai-responses.ts:123-146`; `azure-openai-responses.ts:95-116` | `ai/api/openai_common.go:233-348`; `azureopenairesponses.go:371-383` | `openairesponses_test.go:483-702` and `azureopenairesponses_test.go:245-442` separately prove timeout-before-headers, disarm-after-headers, per-retry reset, hook ordering, and negative-value rejection. |
| OA-M2 | CONFIRMED | `openai-responses.ts:139-146,305-317`; `azure-openai-responses.ts:112-116`; `openai-responses-shared.ts:435-441` | `ai/api/openairesponses.go:844-858,1211-1219`; `azureopenairesponses.go:131-135` | `openairesponses_test.go:707-722` proves OpenAI applies service-tier pricing; `azureopenairesponses_test.go:448-482` proves Azure does not. |
| OA-m1 | CONFIRMED | `openai-completions.ts:489-505` | `ai/api/openaicompletions.go:107-116`; `openai_common.go:410-443` | `openaicompletions_test.go:487-538` streams OpenRouter `metadata.raw` and checks the final error text. |
| OA-m2 | CONFIRMED | `openai-responses.ts:41-45`; `openai-completions.ts:61-65` | `ai/api/openai_common.go:198-217` | `openai_common_test.go:71-94` proves explicit key then Authorization precedence and the absence of the invented API-layer environment fallback. |
| OA-m3 | CONFIRMED | `transform-messages.ts:100-116` | `ai/api/openai_messages.go:36-60` | `openai_messages_test.go:105-132` distinguishes empty and truthy signatures on same-model thinking blocks. |
| OA-m4 | CONFIRMED | `openai-completions.ts:794-804` | `ai/api/openaicompletions.go:1139-1149` | `openaicompletions_test.go:543-554` proves the cache anchor stops at the first system message. |
| OA-m5 | CONFIRMED | `openai-completions.ts:715-718` | `ai/api/openaicompletions.go:445-453,699-702`; `ai/model.go:175-260` | `openaicompletions_test.go:559-583` round-trips arbitrary `openRouterRouting` JSON rather than reconstructing selected fields. |
| OA-m6 | CONFIRMED | `anthropic-messages.ts:832-905` (Copilot returns before affinity) | `ai/api/anthropicmessages.go:1101-1105` | `anthropicmessages_test.go:367-381` proves the Copilot branch omits session affinity. |
| OA-m7 | CONFIRMED | `openai-responses-shared.ts:42-65,533-576` | `ai/api/openairesponses.go:688-710,810-820,1040-1100`; `openai_common.go:446-475` | `openairesponses_test.go:725-819` feeds the degenerate item/delta edges and asserts the upstream event/error outcomes. |

## Other provider shapes

| ID | Verdict | Upstream behavior | Go behavior | Non-tautological regression |
|---|---|---|---|---|
| OT-M5 | CONFIRMED | `.upstream/packages/ai/src/api/mistral-conversations.ts:78-86,213-238` installs no request deadline | `ai/api/mistralconversations.go:147-185` | `mistralconversations_test.go:140-176` inspects the request context and proves `timeoutMs` does not create a deadline. |
| OT-M6 | CONFIRMED after regression fix | `pi-messages.ts:176-263`, especially `:262` returns `{ ...event, partial }` | `ai/api/pimessages.go:416`; `ai/stream.go:94-113,361-386` | `pimessages_test.go:316-377` observes both unknown events in sequence; `ai/stream_test.go:13-48` checks exact object-spread wire output. The earlier implementation silently dropped them. |
| OT-M7 | CONFIRMED after adjacent correction | `bedrock-converse-stream.ts:223-239` passes the transformed payload to the SDK command | `ai/api/bedrockconversestream.go:98-108,407-422,1597-1668` | `bedrockconversestream_test.go:303-392` injects guardrail, performance, response-path, prompt-variable, inference, service-tier, and output-config fields and checks the SDK input. |
| OT-M8 | CONFIRMED | Google auth `build/src/auth/googleauth.js:302-305` converts unavailable metadata into the canonical no-ADC result | `ai/api/google_vertex_adc.go:33,174-208` | `google_vertex_adc_test.go:438-451` injects a dial failure and asserts the exact canonical message rather than the transport error. |
| OT-CF | CONFIRMED | `.upstream/packages/ai/src/providers/cloudflare-stream.ts:1-27`; `cloudflare-auth.ts:10-93` | `ai/api/cloudflare.go:14-38`; normal auth resolver | `cloudflare_test.go:55-96,102-137` proves only request-scoped env replaces URL placeholders and that resolved auth headers pass through unchanged. |
| OT-m1 | CONFIRMED | `google-shared.ts:309-335` throws on an unknown finish reason | `ai/api/google_shared.go:482-503`; stream propagation in `googlegenerativeai.go:572-590` | `google_shared_test.go:30-55` and `googlegenerativeai_test.go:201-236` assert the exact error and that prior deltas remain observable. |
| OT-m2 | CONFIRMED | `@google/genai` node runtime telemetry at `dist/node/index.mjs:7631,20769` | `ai/api/googlegenerativeai.go:50-54,386-405` | `googlegenerativeai_test.go:325-351` captures both `user-agent` and `x-goog-api-client` on the actual request. |
| OT-m3 | CONFIRMED | `packages/ai/src/types.ts` header-hook contract; `api/google-vertex.ts` dispatch path | `ai/api/googlevertex.go:147-166` | `googlevertex_test.go:572-606` mutates a header through `TransformHeaders` and observes it at the transport. |
| OT-m4 | CONFIRMED | Google auth `googleauth.js:302-305` checks GCP residency before metadata-detection policy | `ai/api/google_vertex_adc.go:184-204` | `google_vertex_adc_test.go:379-429` covers none/ping-only, serverless residency, probe counts, and unknown modes. |
| OT-m5 | CONFIRMED after adjacent correction | `bedrock-converse-stream.ts:463-518` creates only on an absent index and drops type-mismatched deltas | `ai/api/bedrockconversestream.go:979-1055` | `bedrockconversestream_test.go:397-463` proves mismatched text/reasoning are dropped and an empty initial reasoning delta emits only the start event. |
| OT-m6 | CONFIRMED | `bedrock-converse-stream.ts:166` exact ARN regex | `ai/api/bedrockconversestream.go:42-45` | `bedrockconversestream_test.go:467-486` checks commercial, GovCloud, China, malformed, uppercase, and wrong-service ARNs. |
| OT-m7 | CONFIRMED | `mistral-conversations.ts:274-292` nullish cached-token fallback order | `ai/api/mistralconversations.go:901-939` | `mistralconversations_test.go:181-223` distinguishes missing, null, camel/snake precedence, non-number termination, and clamping. |
| OT-m8 | CONFIRMED after adjacent correction | `mistral-conversations.ts:420-475` retains valid non-object streaming JSON and applies JS `arguments || {}` truthiness | `ai/api/mistralconversations.go:795-855`; `ai/json.go` raw argument storage | `mistralconversations_test.go:228-314` proves arrays survive and checks null/false/zero/string/number/array truthiness cases. |
| OT-m9 | CONFIRMED | `google-generative-ai.ts:436-469`; `google-vertex.ts:554-575` leave unmapped effort undefined | `ai/api/googlegenerativeai.go:709-773`; `googlevertex.go:284-329` | `googlegenerativeai_test.go:167-196` checks mldev and Vertex request payloads omit the unsupported budget/level rather than emitting zero. |

## Catalog and v0.81 sync

| ID | Verdict | Upstream behavior | Go behavior | Non-tautological regression |
|---|---|---|---|---|
| CAT-M1 | CONFIRMED | `.upstream/packages/ai/scripts/generate-models.ts:796-815,1414-1445` | `ai/models/internal/cataloggen/cataloggen.go:470-551` | `cataloggen_test.go:199-255` checks the exact ordered 19-ID NVIDIA manifest, live intersection, normalization, denylist, metadata, and fallback; the pinned and v0.81 manifests share digest `41696dd…fb0e8`. |
| CAT-M2 | CONFIRMED | `generate-models.ts:2281-2302` | `cataloggen.go:728-749` | `cataloggen_test.go:259-308` checks both Qwen providers, the complete model shape, and non-overwrite when models.dev already supplies it. |
| CAT-M3 | CONFIRMED | `generate-models.ts:818-938` | `cataloggen.go:558-682` | `cataloggen_test.go:312-405` checks source routing, filters, metadata, omission without captures, counts, and complete sorted-ID digests. The two post-tag live additions in each capture are `google/gemini-3.5-flash-lite` and `google/gemini-3.6-flash`. |
| CAT-m1 | CONFIRMED after correction | v0.81.0 `packages/coding-agent/src/core/remote-catalog-provider.ts:33-44,68-109` | `ai/models/store.go:29-36,67-97,134-302` | `store_test.go:229-598` exercises the four-hour gate, both freshness fields, 404/501, other HTTP failures with/without a store, Last-Modified, first-run persistence, and transport failure. |
| SYNC-1 | CONFIRMED ahead-of-pin backport | upstream commit `959cc1897e`, `generate-models.ts:1761-1764` (v0.81.1) | `ai/models/internal/cataloggen/metadata.go:353-362` | `metadata_test.go:141-165` checks Kimi K3 `thinkingFormat: openai` and reasoning-effort support. The ahead-of-v0.81.0 provenance is explicit in `DECISIONS.md`. |
| SYNC-2 | CONFIRMED | v0.81.0 `packages/ai/src/image-models.generated.ts:6-609` | `ai/models/images.go:6-45` | `images_test.go:16-64` compares all 39 serialized models by SHA-256 and spot-checks the new Krea/auto-beta shapes. |
| SYNC-3 | CONFIRMED | upstream commit `890b3547`, removals in `opencode.models.ts:173` and `openrouter.models.ts:957` | `cataloggen.go:378-384` | `cataloggen_test.go:340-343,409-419` proves both removals and the exact 53-model OpenCode count. |
| SYNC-4 | CONFIRMED | v0.81.0 `remote-catalog-provider.ts:33-44` | `ai/models/store.go:67-97`; generated timestamp in `generated.go:7` | `cataloggen_test.go:23-27` proves the build timestamp covers every capture; `store_test.go:180-225` proves stale built-in overlays lose while newer and extension overlays survive. |
| SYNC-5 | CONFIRMED | v0.81.0 `packages/ai/scripts/model-data.ts:146-190`; `generate-models.ts:2461-2568` | `cataloggen.go:188-270`; `cmd/genmodels/main.go:64-91` | `cataloggen_test.go:423-470` and `main_test.go:11-70` cover strict validation before output, same-directory atomic replacement, cleanup, and rollback on rename failure. |

## Login and auth

| ID | Verdict | Upstream behavior | Go behavior | Non-tautological regression |
|---|---|---|---|---|
| LOG-M1 | CONFIRMED | `.upstream/packages/coding-agent/src/modes/interactive/components/login-dialog.ts:96-112`; `utils/open-browser.ts:10-23` | `codingagent/modes/interactive.go:2660-2679`; `codingagent/open_browser_launch.go:12-40` plus platform files | `oauth_selector_test.go:573-588`, `open_browser_test.go:17-44`, and Linux process test `15-50` check notification wiring, commands, best-effort failure, and detached launch. |
| LOG-M2 | CONFIRMED | `components/oauth-selector.ts:102-161` | `codingagent/modes/oauth_selector.go:100-153` | `oauth_selector_test.go:49-113` exercises fuzzy fields, eight-row windowing, scrolling, method-name search, and all empty states. |
| LOG-M3 | CONFIRMED | `interactive-mode.ts:4838-4873,4943-4983`; `oauth-selector.ts:76-99` | `interactive.go:2257-2272,2340-2367`; `oauth_selector.go:83-97` | `oauth_selector_test.go:136-153,412-439` proves `/login <ref>` keeps an editable initial query and confirmation starts the match. |
| LOG-M4 | CONFIRMED after adjacent correction | `interactive-mode.ts:207-212,874,3722-3735,4293-4301,4336-4364,4417-4444,5033-5083` | `interactive.go:207,1099-1117,1488-1535,2503-2601` | `oauth_selector_test.go:242-392,524-569` and `interactive_lifecycle_test.go:22-55` cover every completion/default-model branch, direct and selector model changes, effective credentials, and an atomic once-only warning. |
| LOG-m1 | CONFIRMED | `oauth-selector.ts:71-73` | `oauth_selector.go:74-80` | `oauth_selector_test.go:34-44` checks exact login/logout titles. |
| LOG-m2 | CONFIRMED | `interactive-mode.ts:4992-5021` | `interactive.go:2227-2249,2463-2473` | `oauth_selector_test.go:443-465` distinguishes OAuth, API-key, and failure messages. |
| LOG-m3 | CONFIRMED | `oauth-selector.ts:164-180` | `oauth_selector.go:155-176` | `interactive_test.go:234-279` and `oauth_selector_test.go:117-132` check configured type/source labels in login and logout views. |
| LOG-m4 | CONFIRMED | `interactive-mode.ts:4875-4883,5086-5151` | `interactive.go:2426-2460`; `oauth_selector.go:267-330` | `oauth_selector_test.go:470-520,593-613` checks temporary Bedrock guidance, ambient dialog lifecycle, and no chat-history leakage. |
| LOG-m5 | CONFIRMED deliberate addition | no upstream CLI-subcommand counterpart | `cmd/pigo/auth.go:19-84,98-167` | `auth_test.go:17-66,105-135` checks explicit logout, bare-logout credential listing/no deletion, empty-store text, and numbered or literal headless selection. The divergence is ledgered. |
| LOG-m6 | CONFIRMED | `core/auth-guidance.ts:6-16`; `cli/list-models.ts:29-39` | `codingagent/session_rpc.go:645-668`; `cmd/pigo/models.go:20-30` | `cmd/pigo/models_test.go:67-84` asserts the provider-specific guidance exposed by model listing. |
| LOG-m7 | CONFIRMED | `packages/ai/src/auth/oauth/anthropic.ts:82-97` | `ai/auth/oauth/anthropic.go:269-285` | `anthropic_test.go:242-255` exercises errno, wrapped cause, and plain-error formatting. |
| LOG-m8 | CONFIRMED after correction | `packages/ai/src/auth/types.ts:23-34`; `auth/resolve.ts:84-117` | `ai/auth/types.go:17-30`; `credential_json.go:60-76,170-191`; `resolve.go:131-151` | `credential_json_test.go:76-126` proves exact fractional JSON preservation, cloning, mutation override, integer public compatibility, JS-number expiry boundary, and invalid rejection. |
| LOG-m9 | CONFIRMED | `packages/coding-agent/src/migrations.ts:47-70` | `codingagent/config/auth_migrate.go:37-80` | `config/auth_test.go:197-227` proves the source settings mode is preserved and `auth.json` remains `0600`. |

## Ranked adversarial findings

No unresolved finding remains. The defects found during verification, ranked by
impact before correction, were:

1. **OT-M6:** unknown pi-message events were dropped instead of re-emitted with
   the evolving partial message. This would break forward-compatible clients.
2. **OT-M7 / OT-m5:** Bedrock hook fields and two delta edge cases were still
   lossy. The SDK input and stream event sequence now match upstream.
3. **CX-m2 / OT-m8:** malformed Codex optional error members and non-object
   Mistral tool arguments did not preserve JavaScript's permissive runtime
   semantics. Both now have shape- and truthiness-focused regressions.
4. **CAT-m1 / LOG-m8 / LOG-M4:** catalog error freshness, fractional OAuth
   expiry values, and the Anthropic warning's concurrent once guard needed
   correction; focused race coverage now passes.

Adjacent v0.81.0 review also confirmed required stream injection, public versus
coding compaction result separation, exact retained-tail identity, the new
session checkpoint path API, strict catalog/image validation, product assets,
and action versions. `packages/server`, `packages/storage/sqlite-node`, and the
native llama extension remain explicit D2/D7 exclusions rather than silent gaps.
