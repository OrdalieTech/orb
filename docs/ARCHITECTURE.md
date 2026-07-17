# pi-go — Architecture

Companion to [DECISIONS.md](DECISIONS.md) (the *why*). This document is the *what and how*: layout,
per-package design, cross-cutting mechanics, conformance, sync, dependencies, build. The upstream
source at the pinned commit is the behavioral spec; this document tells you where to look and what
shape the Go side takes.

Upstream paths below are relative to the upstream repo (`earendil-works/pi` @ `UPSTREAM.lock`),
e.g. `packages/agent/src/agent-loop.ts`. The sync tool materializes that checkout at `.upstream/`
(gitignored).

## 1. Repository layout

```
pi-go/
├── go.mod                    module github.com/OrdalieTech/pi-go   (go ≥ 1.25)
├── cmd/pi/                   CLI entry point (thin: arg parsing → codingagent)
├── ai/                       port of packages/ai        — importable alone
│   ├── api/                  one file per API shape (openaresponses.go, anthropicmessages.go, …)
│   ├── providers/            provider registry + per-provider metadata (generated + hand corrections)
│   ├── auth/                 credential store, OAuth flows (PKCE, device-code)
│   └── models/               catalog: generated data, models.dev refresh, models.json overlay
├── agent/                    port of packages/agent     — loop, Agent, harness
│   └── harness/              session repo, compaction, skills, system-prompt, env abstraction
├── tui/                      port of packages/tui       — renderer + components, zero framework
├── codingagent/              port of packages/coding-agent — the product wiring
│   ├── tools/                read, bash, edit, write, grep, find, ls (+ operations interfaces)
│   ├── extensions/           Go-native extension API: types, registry, runner (event dispatch)
│   │   └── jsbridge/         sobek runtime, esbuild pipeline, node shims, JS ExtensionAPI bindings
│   ├── session/              session manager (JSONL v3 tree, migrations), export-html
│   ├── config/               settings manager, trust, keybindings, auth storage, models.json
│   ├── modes/                tui, print, json, rpc
│   └── mcp/                  bundled MCP extension (official go-sdk), built on extensions API
├── internal/
│   ├── jsonschema/           Schema type + reflection helper (gate G1)
│   ├── partialjson/          streaming tool-arg parser (port of `partial-json`)
│   ├── truncate/             shared output truncation (50KB / 2000-line rules)
│   └── sync/                 upstream sync tool (delta report, fixture regen driver)
├── conformance/
│   ├── extract/              TS scripts run inside .upstream/ to emit fixtures (dev-only Node)
│   ├── fixtures/             committed golden fixtures (F1–F12, see §6)
│   └── runner/               go test helpers consuming fixtures; RPC black-box adapter
├── docs/                     DECISIONS.md, ARCHITECTURE.md, MIRROR.md, plan/, sync/reports/
├── AGENTS.md                 execution contract for implementing agents
└── UPSTREAM.lock             pinned upstream commit + sync state
```

### Mirroring rules (normative)

1. Package ↔ package: `packages/ai` ↔ `ai/`, `packages/agent` ↔ `agent/`, `packages/tui` ↔ `tui/`,
   `packages/coding-agent` ↔ `codingagent/`.
2. File ↔ file where idiomatic: `agent-loop.ts` → `agent/loop.go`, `edit-diff.ts` →
   `codingagent/tools/editdiff.go`. Split only when Go conventions demand (e.g. `_test.go`,
   platform suffixes `bash_unix.go`/`bash_windows.go`).
3. Exported identifiers keep upstream names Go-cased: `AgentEvent`, `ToolResultMessage`,
   `runAgentLoop` → `RunLoop` (receiver-free), event name strings **unchanged** (`"message_update"`).
4. Wire/persisted JSON field names are **byte-identical** to upstream (session entries, events, RPC).
   Struct tags carry the exact upstream field names; fixtures enforce this.
5. `docs/MIRROR.md` maintains the table upstream-path → go-path; the sync tool consumes it to map
   upstream diffs to affected Go files. Every WP that creates files updates MIRROR.md.

## 2. `ai/` — unified LLM layer

Upstream spec: `packages/ai/src/types.ts` (message/streaming model), `packages/ai/src/api/*`
(API shapes), `packages/ai/src/providers/*` (catalog), `packages/ai/docs/`.

**Types.** `Message = UserMessage | AssistantMessage | ToolResultMessage` becomes a sealed interface
(`Message` with unexported marker; concrete structs). AssistantMessage content blocks
(`TextContent | ThinkingContent | ToolCall`) likewise. Preserve: `api/provider/model/usage/stopReason/
errorMessage` fields, opaque replay signatures (`thinkingSignature`, `textSignature`,
`thoughtSignature`), `ToolResultMessage.details/isError/addedToolNames`, `Usage` incl.
cacheRead/cacheWrite/cacheWrite1h/reasoning/cost, thinking levels `off|minimal|low|medium|high|xhigh|max`.

**Streaming.** The `AssistantMessageEvent` protocol (`start`, `text_/thinking_/toolcall_` ×
`start/delta/end`, `done`, `error`) is the universal stream contract. Go surface:

```go
type StreamFn func(ctx context.Context, req Request) (iter.Seq2[AssistantMessageEvent, error], error)
```

plus a `Collect` helper folding a stream into the final `AssistantMessage`. Tool-call args stream
through `internal/partialjson` exactly as upstream uses `partial-json`.

**API shapes** (one file each under `ai/api/`): openai-responses, openai-completions,
anthropic-messages, google-generative-ai, google-vertex, azure-openai-responses,
openai-codex-responses, mistral-conversations, bedrock-converse-stream, pi-messages (generic SSE
gateway shape — client side). Each adapts `(Context, Options)` → provider request and provider
stream → `AssistantMessageEvent`s. Implementation per D10: official SDK where sound, hand-rolled
`net/http` + SSE otherwise. Request-shaping is fixture-tested (F2) independent of transport.

**Providers & catalog.** A provider = metadata (id, api shape, baseURL, auth kind, compat flags,
models). Generated from models.dev by `go:generate` into `ai/models/generated.go` + hand-maintained
corrections (mirroring upstream `scripts/generate-models.ts` structure); runtime refresh writes
per-provider catalogs under `~/.pi/agent/` as upstream's models-store does. `models.json` overlay:
same semantics as upstream `docs/models.md`, including `$ENV` / `!command` apiKey interpolation and
compat flags (`supportsDeveloperRole`, `supportsCacheControlOnTools`, `supportsToolReferences`, …).

**Caching & headers.** Anthropic `cache_control` breakpoints (system/tools/last-user), TTL via
`PI_CACHE_RETENTION`; OpenAI `prompt_cache_key` + session-affinity header formats
(`packages/ai/src/api/openai-prompt-cache.ts`).

**Auth.** `ai/auth`: credential store interface (file impl lives in `codingagent/config`), API-key
env resolution, OAuth: anthropic PKCE (localhost :53692 callback + manual paste fallback),
openai-codex, github-copilot device flow, xai. Port from `packages/ai/src/auth/oauth/*`. Radius
excluded (ledger).

## 3. `agent/` — loop, Agent, harness

Upstream spec: `packages/agent/src/agent-loop.ts`, `agent.ts`, `types.ts`, `harness/`.

**Loop contract (load-bearing):** the loop never fails by panic/error-return for model-level
problems — failures are encoded as a final assistant message with `stopReason: "error"|"aborted"`.
`RunLoop(ctx, messages, agentCtx, config, sink)` + `RunLoopContinue`. Abort = context cancellation
mapped to `"aborted"`.

**Agent.** Mirrors upstream methods: `Prompt`, `Continue`, `Steer`, `FollowUp`, `Abort`,
`WaitForIdle`, `Subscribe`, `Reset`; `State()` snapshot (systemPrompt, model, thinkingLevel, tools,
messages, isStreaming, streamingMessage, pendingToolCalls, errorMessage). Hooks as functional
options: `ConvertToLLM`, `TransformContext`, `StreamFn`, `GetAPIKey`, `BeforeToolCall` (block+reason),
`AfterToolCall` (patch result / terminate), `PrepareNextTurn`, `ShouldStopAfterTurn`,
`GetSteeringMessages`/`GetFollowUpMessages`, `ToolExecution: sequential|parallel` (default parallel:
sequential preflight, concurrent execute), `SteeringMode`/`FollowUpMode: all|one-at-a-time`.

**Events.** `AgentEvent` taxonomy verbatim: `agent_start/end`, `turn_start/end`,
`message_start/update/end` (update carries the token-level `AssistantMessageEvent`),
`tool_execution_start/update/end`. Subscriber semantics: listeners invoked in order, their completion
awaited before idle (upstream awaits listener promises — Go: synchronous callbacks; the channel
adapter buffers). Steering drains after the current turn's tools; follow-ups drain when the agent
would stop.

**Tools.** `AgentTool`: name, label, JSON Schema params (`internal/jsonschema.Schema`),
`PrepareArguments` shim, `Execute(ctx, toolCallID, params, onUpdate) (ToolResult, error)` where a
returned error ⇒ error tool-result (upstream throw ⇒ error result), `Terminate`/`AddedToolNames`
result fields, per-tool execution-mode override.

**Harness.** Port of `packages/agent/src/harness/`: session repositories (JSONL + in-memory),
compaction + branch summarization, skills loading, prompt-template plumbing, system-prompt assembly,
execution-env abstraction (`env` interface — the seam later used by SSH/sandbox extensions). The
`codingagent` layer's `AgentSession` (upstream `packages/coding-agent/src/core/agent-session.ts`,
spec `packages/coding-agent/docs/sdk.md`) is the high-level embedding API and the thing the SDK
advertises; its 13 upstream SDK examples get Go ports under `codingagent/examples/`.

## 4. `tui/` — terminal UI

Upstream spec: `packages/tui/docs/tui.md` + `src/`. Differential line-based renderer; components
implement:

```go
type Component interface{ Render(width int) []string }
type Focusable interface{ Component; HandleInput(ev KeyEvent) }
```

Cursor placement via the zero-width marker convention (upstream `CURSOR_MARKER`). Components to
port: Editor (multi-line, undo stack, kill-ring, word nav, paste collapse), Input, Markdown,
SelectList, SettingsList, Box/Container, Text, TruncatedText, Loader/CancellableLoader, Image,
Spacer; autocomplete + fuzzy; configurable keybindings; kitty + iTerm2 image protocols; kitty
keyboard protocol (incl. key-release); East-Asian width via grapheme-aware width lib. Markdown:
goldmark AST → own ANSI renderer (upstream: marked + own renderer); code highlighting via chroma
(upstream: highlight.js). Native addons (darwin modifier keys, win32 console) are NOT ported —
kitty keyboard protocol covers modifier reporting where the terminal supports it (ledger gap).

`Render(width) []string` is pure → TUI components are golden-testable (F12) and JS-bridgeable.

## 5. `codingagent/` — the product

Upstream spec: `packages/coding-agent/src/`, `docs/` (usage, settings, extensions, sdk, rpc, json,
session-format, skills, prompt-templates, models, packages, themes).

**Tools** (`codingagent/tools/`, upstream `src/core/tools/`): read (text + images: decode via
stdlib/x/image, resize ≤2000×2000, EXIF orientation; omit image if model lacks vision), bash (fresh
`bash` spawn per call, command via stdin, streaming through the output accumulator, 50KB/2000-line
truncation with full spill to temp file, process-tree kill, detached-child PID tracking,
`shellCommandPrefix`, spawn-hook seam), edit (exact → fuzzy match: NFKC normalize + trailing-ws
strip + smart-quote/dash folding, normalized-space match mapped back line-by-line; multi-edit
arrays; udiff rendering), write, grep (ripgrep), find (fd), ls. `rg`/`fd`: prefer system binaries,
else auto-download upstream-style into `~/.pi/agent/bin` (`src/utils/tools-manager.ts`). Every tool:
Operations interface (delegation seam), TUI `RenderCall`/`RenderResult`, file-mutation queue
serializing writes per realpath (parallel execution default).

**Sessions** (`codingagent/session/`): JSONL v3 in-file tree (header line, 8-hex ids, parentId,
leaf = position; entry types `message`, `model_change`, `thinking_level_change`, `compaction`,
`branch_summary`, `custom`, `custom_message`, `label`, `session_info`), v1→v2→v3 auto-migration,
location `~/.pi/agent/sessions/--<cwd-dashed>--/<ts>_<uuid>.jsonl`, overrides
(`--session-dir` > `PI_CODING_AGENT_SESSION_DIR` > setting). Export to HTML (upstream
`src/core/export-html/`) and markdown. Byte-compatible with TS pi — cross-read fixtures (F6) prove it.

**Config** (`codingagent/config/`): settings manager (global deep-merged with project
`.pi/settings.json`; unknown keys tolerated), auth storage (0600, legacy `oauth.json` migration),
trust flow, keybindings, `PI_CODING_AGENT_DIR` override, models.json hot reload.

**Extensions — Go-native core** (`codingagent/extensions/`): the full ExtensionAPI as Go interfaces,
mirroring `docs/extensions.md` and `src/core/extensions/types.ts`:
- Event hooks: `project_trust`; `session_start/shutdown/before_switch/before_fork/before_compact/
  compact/before_tree/tree/info_changed`; `resources_discover`; `input`; `before_agent_start`;
  `agent_start/end/settled`; `turn_start/end`; `message_start/update/end`; `context`;
  `before_provider_headers/request`, `after_provider_response`; `model_select`,
  `thinking_level_select`; `tool_execution_start/update/end`; `tool_call` (block/mutate);
  `tool_result` (middleware chain); `user_bash`.
- Registrations: tools (incl. built-in override), commands (+argument completions), shortcuts,
  flags, providers (full, incl. OAuth + refreshModels), message/entry renderers.
- Messaging/state: `SendMessage` (deliverAs steer|followUp|nextTurn, triggerTurn), `SendUserMessage`,
  `AppendEntry`, session name/label, model + thinking setters, active-tools set (dynamic tool
  loading incl. deferred-loading passthrough), inter-extension event bus, `Exec`.
- `Ctx`: UI surface (dialogs select/confirm/input/editor with timeout+signal, notify, status/widget/
  footer/header/title, working indicator, editor text access, autocomplete providers, editor
  replacement, custom component takeover + overlays, theme), sessionManager, modelRegistry, signal,
  cwd, mode (`tui|rpc|json|print`), hasUI, isIdle/abort/hasPendingMessages, shutdown, compact,
  contextUsage, systemPrompt, trust.
Dispatch semantics ported from `runner.ts`: ordered middleware chains, error isolation (extension
errors logged, agent continues; `tool_call` handler error blocks the call fail-safe), per-mode UI
degradation (RPC bridges dialogs over the protocol; print/json = no-ops).

**Extensions — JS bridge** (`codingagent/extensions/jsbridge/`): sobek VM per extension + embedded
esbuild. Pipeline: discover (same paths as upstream: `~/.pi/agent/extensions/*.ts|*/index.ts`,
`.pi/extensions/…` trust-gated, settings `extensions[]`/`packages[]`, `-e`) → esbuild bundle
(entry = extension file; TS→`es2017`; async generators lowered; npm deps from the extension's
`node_modules` bundled if pure-JS; `pi`, `typebox`-equivalent, `pi-tui` marked external) → evaluate
in sobek → factory invoked with the JS ExtensionAPI object whose methods proxy to the Go API.
Node shims: `fs`, `path`, `os`, `process`, `url`, `util` implemented against Go host functions
(goja_nodejs reused where sobek-compatible: console, buffer, url); `child_process` subset routed via
the exec bridge; `fetch` via net/http binding. typebox: the real JS library bundled in-engine —
schemas surface as JSON Schema, which is all Go needs. pi-tui bridge: JS objects implementing
`render(width): string[]` wrapped as Go `Component`s; dialogs/status/widgets proxy directly; custom
editors and overlays are the last wave (gate G3). Hot `/reload` = rebuild + fresh VM.
Threading rule: **one goroutine per VM** — all calls into a sobek VM are serialized through its
event-loop goroutine; Go→JS callbacks post onto it (sobek/goja VMs are not goroutine-safe).

**MCP** (`codingagent/mcp/`): bundled extension registering MCP servers from settings as tool
sources via `modelcontextprotocol/go-sdk` (stdio + streamable HTTP), tools surfaced through the
normal registration API with dynamic tool loading. Off unless configured.

**Modes** (`codingagent/modes/`): TUI (default), print `-p` (stdin merge), json (AgentSessionEvent
JSONL out — adds `queue_update`, `compaction_start/end`, `auto_retry_start/end` per `docs/json.md`),
rpc (bidirectional JSONL stdin/stdout per `docs/rpc.md`: prompt/steer/follow-up/abort, session
mgmt, get_commands, extension-UI bridging; strict LF framing). RPC is a conformance surface —
upstream's RPC tests run against our binary (F7).

**Slash commands / skills / templates / themes:** resolution order extension → input hook →
`/skill:name` → template; built-in interactive commands (`/login /logout /model /resume /new /name
/session /tree /trust /fork /clone /compact /copy /export /import /reload /hotkeys /settings
/changelog /quit`; `/share` → local export per ledger); skills per agentskills.io with progressive
disclosure + trust gating (upstream `src/core/skills.ts`); prompt templates with bash-style arg
expansion (`$1`, `$@`, `${1:-default}`, `${@:N:L}`); themes as data (registerable via resources).

**pi packages:** `pi install/remove/update/list/config` for `npm:`/`git:` extension/skill/theme
packages — npm registry tarball fetch + extract (no node at runtime), git clone; storage
`~/.pi/agent/npm/` + project `.pi/npm/` (upstream `docs/packages.md`).

## 6. Conformance architecture

Fixture families (each = extraction script in `conformance/extract/`, goldens in
`conformance/fixtures/<family>/`, runner in `conformance/runner/`):

| ID | Family | Proves |
|---|---|---|
| F1 | message serialization | unified types marshal byte-identically |
| F2 | provider request shaping | (context, options) → provider payload per API shape |
| F3 | agent-loop event traces | scripted faux-provider runs → identical AgentEvent JSONL |
| F4 | edit fuzzy matching | upstream edit/edit-diff cases pass verbatim |
| F5 | truncation | 50KB/2000-line head/tail rules |
| F6 | session format | v1/v2/v3 parse, migrate, write; cross-read both directions |
| F7 | RPC transcripts | request/response conversations against the real binary |
| F8 | slash/template expansion | arg expansion + resolution order |
| F9 | system-prompt assembly | context files, SYSTEM/APPEND_SYSTEM, skills disclosure |
| F10 | compaction | summarization boundaries, firstKeptEntryId, token accounting |
| F11 | extension behavior | example-extension matrix under the bridge (per-example expected effects) |
| F12 | TUI render goldens | Component.Render(width) line snapshots |

Extraction runs Node/vitest **inside `.upstream/`** (dev-only), emitting JSON the Go tests consume.
Where upstream lacks a directly extractable test, the extractor drives upstream's own faux provider
(`packages/ai/src/providers/faux`) or public APIs to synthesize goldens. LLM-dependent behavior
(compaction summaries) is fixture-tested at the boundary (prompts + structure), not on model output.

**Black-box:** upstream RPC/CLI tests run unmodified against `pi-go --mode rpc` via a thin adapter
that swaps the spawned binary. The F11 matrix runner executes each upstream example extension
headlessly (json mode) and asserts its observable effects; results published to
`docs/sync/extension-matrix.md`.

## 7. Upstream sync

`UPSTREAM.lock` records `{repo, commit, syncedAt}`. `make sync` (also runnable by an agent as a
work package): clone/fetch upstream → diff `lock..HEAD` → map changed files through `docs/MIRROR.md`
→ classify (format-relevant? API-relevant? feature-only) → regenerate fixtures at the new commit →
run conformance → write `docs/sync/reports/<date>.md` (delta summary, fixture diffs, failing
conformance, proposed work items). Owner/agent triages; lock bumps when green. Cron automation is
deliberately deferred until conformance is stably green (D5).

## 8. Dependency policy

Rule: every direct dependency appears in this table with a justification; adding one without
updating the table fails review. Stdlib first; a few hundred lines of internal code beats a new
dependency; a well-maintained official SDK beats reinventing a provider.

| Dependency | Where | Why |
|---|---|---|
| grafana/sobek | jsbridge | JS engine (pure Go, ESM, k6-proven) |
| evanw/esbuild (pkg/api) | jsbridge | TS transpile + bundling (pure Go, official API) |
| dop251/goja_nodejs (selected) | jsbridge | console/buffer/url shims where sobek-compatible |
| openai/openai-go/v3 | ai/api | OpenAI responses+completions (D10) |
| anthropics/anthropic-sdk-go | ai/api | Anthropic messages + caching (D10) |
| google.golang.org/genai | ai/api | Gemini — **gate G2**, drop if tree too heavy |
| aws-sdk-go-v2 (bedrockruntime) | ai/api | Bedrock SigV4 + converse-stream (D10) |
| modelcontextprotocol/go-sdk | mcp | official MCP SDK v1.6+ |
| yuin/goldmark | tui | CommonMark parsing (render stays ours) |
| alecthomas/chroma/v2 | tui | syntax highlighting (upstream: highlight.js) |
| rivo/uniseg | tui | grapheme/East-Asian width |
| golang.org/x/{term,sys,image,text} | tui, tools | raw mode, signals, image decode/resize, encoding |
| bmatcuk/doublestar/v4 | tools, skills | `**` globbing (upstream: glob/minimatch) |
| gopkg.in/yaml.v3 | skills, config | frontmatter + YAML settings surfaces |
| aymanbagabas/go-udiff | tools | unified diff for edit rendering (upstream: `diff`) |
| gofrs/flock | session, config | file locking (upstream: proper-lockfile) |
| sabhiram/go-gitignore (or internal) | tools, harness | .gitignore semantics (upstream: `ignore`) |
| invopop/jsonschema (or internal) | internal/jsonschema | struct→schema reflection — **gate G1** |

Explicitly rejected: TUI frameworks (D15), langchaingo/fantasy-style unified LLM libs (D10),
v8go/quickjs CGo bindings (D7), sqlite (no need — sessions are JSONL).

## 9. Build, size, release

- `CGO_ENABLED=0`; targets `{linux,darwin} × {amd64,arm64}`; goreleaser for static binaries +
  checksums; install via curl script + Homebrew tap. Version check against GitHub releases only.
- Budgets: cold start < 50 ms; binary ≈ 25–35 MB (esbuild ≈ +9 MB, sobek ≈ +4 MB — accepted for
  extension parity); `go vet` + golangci-lint clean; race detector on in CI tests.
- Estimated final size: 60–90k LOC Go source (upstream ≈ 100k LOC TS) + fixtures. Slimness is
  enforced at the dependency table and by refusing speculative abstraction, not by dropping parity.

## 10. Risks & mitigations

| Risk | Mitigation |
|---|---|
| sobek ES gaps vs modern TS extensions | esbuild lowering to es2017; F11 matrix quantifies reality; qjs(WASM) is the researched fallback engine behind the same bridge interface |
| Extension API breadth (2,943-line spec) | phased bridge (hooks → registrations → UI → overlays); Go-native API first proves semantics; matrix tracks coverage honestly |
| TUI fidelity drift | F12 render goldens + side-by-side session comparison protocol in phase 4 |
| Provider SDK churn (openai v1→v3 history) | SDK usage confined to `ai/api/*` adapter files; unified types are ours; F2 pins request shapes |
| Upstream velocity (multi-release weeks) | pin + sync reports; formats-first tracking (D5); mirror layout keeps diffs mappable |
| Event/serialization drift breaking conformance | F1/F3/F6/F7 fixtures regenerate on every sync; wire-format struct tags reviewed against goldens |
| Parallel tool execution races | file-mutation queue per realpath (upstream semantics); race detector in CI |
| VM threading bugs in bridge | one-goroutine-per-VM rule; bridge API is message-passing only |
