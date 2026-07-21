# Phase 1 — Walking skeleton

Goal: `pigo -p "fix the failing test"` works against OpenAI with read/bash/edit/write, records a
session, and the port can start assisting its own development (WP-170 gate).

## WP-110 — ai: unified types, streaming, Schema

**Upstream refs:** `packages/ai/src/types.ts` (whole file — the spec), `packages/ai/src/`
event/usage/stop-reason definitions; serialization behavior via pi-ai tests.

**Scope**
- `ai/`: Message union (User/Assistant/ToolResult), content blocks (Text/Thinking/ToolCall;
  ToolResult content Text|Image), Usage (cache fields, reasoning, cost), StopReason, ThinkingLevel,
  replay signatures, `addedToolNames` — JSON tags byte-identical to upstream (F1 enforces).
- `AssistantMessageEvent` protocol types (`start`, `text_/thinking_/toolcall_ start|delta|end`,
  `done`, `error`) + `StreamFn` signature + `Collect` fold; `Context`/`Options` request types.
- `internal/jsonschema`: `Schema` (raw JSON Schema value, marshals verbatim) + `FromStruct[T]()`
  reflection helper — **gate G1**: try `invopop/jsonschema`; if its output needs post-processing
  beyond ~50 lines to match what providers accept (typebox-style plain schemas, StringEnum pattern
  for Google), write the ~200-LOC internal reflector instead. Record the choice in the PR + dep table.
- `internal/partialjson`: incremental JSON parser for streaming tool args (port semantics of
  `partial-json` as used by `packages/ai`).

**Fixtures:** **F1** — extraction serializes a corpus of messages (all content kinds, edge cases:
empty content, images, signatures, errors) from TS; Go must unmarshal→remarshal byte-equal
(modulo key order — canonicalize in the differ).

**Acceptance:** F1 green; `FromStruct` covers nested structs/slices/enums/optionals with goldens;
partialjson handles the upstream test corpus (extracted).

## WP-120 — ai/api: OpenAI responses + completions

**Upstream refs:** `packages/ai/src/api/openai-responses.ts`, `openai-completions.ts`,
`openai-prompt-cache.ts`; provider compat flags in `packages/ai/src/providers/openai.ts`.

**Scope**
- `ai/api/openairesponses.go`, `openaicompletions.go` on `openai-go/v3`: request shaping from
  (Context, Options) — system/developer role handling, tools (schema passthrough), thinking/reasoning
  effort mapping, image inputs, `prompt_cache_key` + session-affinity headers; stream adaptation →
  AssistantMessageEvent (incl. tool-arg deltas via partialjson, usage extraction, stop reasons).
- baseURL override + compat-flag honoring (`supportsDeveloperRole`, `supportsReasoningEffort`, …) so
  WP-270 can enable compat providers with data only. API-key resolution from env/options.
- Minimal provider registry (id → api shape + baseURL + auth kind) with hardcoded `openai` entry
  (full catalog is WP-250).

**Fixtures:** **F2** — extraction captures upstream request payloads for a scripted matrix
(text/tools/images/thinking × both shapes) via upstream's request-building internals or a recording
fake; Go builds byte-equal payloads. Streaming: recorded SSE transcripts → identical event
sequences (F3-style trace for the api layer).

**Acceptance:** F2 green for both shapes; live smoke test behind env-var opt-in
(`PIGO_LIVE_TESTS=1`) runs one real streamed tool-call round-trip.

## WP-130 — agent: loop, Agent, faux provider

**Upstream refs:** `packages/agent/src/agent-loop.ts` (792 LOC — port faithfully),
`agent.ts`, `types.ts` (hooks, events, tool contract); `packages/ai/src/providers/faux`.

**Scope**
- `agent/loop.go`: RunLoop/RunLoopContinue with the never-fail contract (`stopReason:
  "error"|"aborted"`), turn structure, tool preflight (sequential) + execution (parallel default,
  per-tool override), steering/follow-up drain points, event emission order exactly as upstream.
- `agent/agent.go`: Agent with Prompt/Continue/Steer/FollowUp/Abort/WaitForIdle/Subscribe/Reset,
  State snapshot, all hook options (D13 list), context-cancellation abort.
- `agent/events.go`: AgentEvent taxonomy (names verbatim); subscriber ordering semantics.
- `ai/providers/faux`: port of the scripted faux provider (drives F3 and all loop tests without a network).
- AgentTool interface + error-to-error-result mapping + onUpdate streaming.

**Fixtures:** **F3** — extraction runs upstream loop scenarios (multi-turn, parallel tools, steering,
abort mid-stream, tool error, terminate) on faux, recording event JSONL; Go replays identical traces.

**Acceptance:** F3 green across the scenario matrix; race detector clean with parallel tools;
abort during stream yields `"aborted"` trace equal to upstream's.

## WP-140 — Tools wave 1: read/write/edit/ls

**Upstream refs:** `packages/coding-agent/src/core/tools/{read,write,edit,edit-diff,ls}.ts`,
`file-mutation-queue.ts`, `truncate.ts`; edit tests (the fuzzy corpus is the crown jewel).

**Scope**
- `internal/truncate` (implements F5, un-skip WP-002's tests), `codingagent/tools/` read (text only
  here: offset/limit paging, leading-`@` strip; images WP-440), write, ls, edit + editdiff (exact →
  normalized fuzzy match per ARCHITECTURE §5, multi-edit, udiff rendering), file-mutation queue
  (per-realpath serialization), Operations interfaces (the delegation seam — thin, no second impl).
- Tool schemas via `internal/jsonschema`; TUI render hooks stubbed (return plain text until Phase 4).

**Fixtures:** **F4** (edit fuzzy corpus — extract every upstream edit/edit-diff test case), F5 green.

**Acceptance:** F4 + F5 green; parallel writes to one file serialize correctly under `-race`.

## WP-150 — Tools wave 2: bash, grep, find

**Upstream refs:** `packages/coding-agent/src/core/tools/{bash,grep,find}.ts`,
`src/utils/{shell,output-accumulator,tools-manager}.ts`.

**Scope**
- bash: fresh shell spawn per call (command via stdin), streaming through output accumulator,
  50KB/2000-line truncation + full-output temp spill, process-group kill (unix), detached-child PID
  tracking, `shellCommandPrefix`, spawn-hook seam. `bash_unix.go` now; `bash_windows.go` is WP-670.
- grep (rg) + find (fd): system-binary discovery, else auto-download upstream-style into
  `~/.pi/agent/bin` (GitHub releases, checksum, correct platform asset), context lines, match
  limits, per-line truncation.

**Fixtures:** truncation/accumulator cases extend F5; grep/find behavior tested against a committed
mini-tree (deterministic, no network in CI — download path integration-tested behind env flag).

**Acceptance:** long-running command streams incrementally and truncates identically to upstream;
kill terminates the whole tree; grep/find output matches upstream shape on the mini-tree.

## WP-160 — codingagent skeleton: config, sessions, print mode

**Upstream refs:** `packages/coding-agent/src/core/{settings-manager,session-manager,
system-prompt}.ts`, `docs/{settings,session-format}.md`, `src/cli/args.ts` (subset), `docs/usage.md`.

**Scope**
- `codingagent/config`: settings load (global + project deep-merge, unknown keys tolerated),
  `PI_CODING_AGENT_DIR`, relevant env vars.
- `codingagent/session`: JSONL v3 tree read/write/append, v1→v3 migration, session dir naming
  (`--<cwd-dashed>--`), leaf tracking, flock locking.
- System-prompt assembly: base prompt, AGENTS.md/CLAUDE.md discovery (home → ancestors → cwd),
  `.pi/SYSTEM.md`/`APPEND_SYSTEM.md`, `--system-prompt`/`--append-system-prompt`, `--no-context-files`.
- `cmd/pigo` + print mode: `-p` (stdin merge), `@file` text attachments, core flags (`--provider
  --model --api-key --thinking -c/--continue --session-dir --no-session -t/--tools -xt`), session
  recording in print mode (upstream records in `-p` too).

**Fixtures:** **F6** (session format: parse/migrate/write goldens + cross-read: TS-written sessions
open in Go, Go-written in TS — extraction round-trips through upstream session-manager), **F9**
(system-prompt assembly goldens over a fixture tree).

**Acceptance:** F6 + F9 green; `pigo -c` resumes a session TS pi created in the same cwd and vice versa.

## WP-170 — Skeleton gate (integration)

**Scope:** wire 120+130+140+150+160 into a working `pigo -p`; fix integration fallout; write
`docs/plan/skeleton-gate-report.md` (what works, deviations found, perf snapshot: cold start, binary size).

**Acceptance (the gate)**
- `pigo -p "read main.go and add a comment"` performs a real OpenAI round-trip with tool calls on a
  sample repo, recording a v3 session TS pi can open.
- All fixture families landed so far green; CI matrix green; cold start < 50 ms; binary < 25 MB at this stage.
- Dogfood: one pigo WP task executed using pigo itself in print mode, transcript attached to the report.
