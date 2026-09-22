# Orb — Architecture

Companion to [DECISIONS.md](DECISIONS.md) (the *why*). This document is the *what and how*: layout,
per-package design, cross-cutting mechanics, conformance, sync, dependencies, build. The upstream
source at the pinned commit is the behavioral spec; this document tells you where to look and what
shape the Go side takes.

Upstream paths below are relative to the upstream repo (`earendil-works/pi` @ `UPSTREAM.lock`),
e.g. `packages/agent/src/agent-loop.ts`. The sync tool materializes that checkout at `.upstream/`
(gitignored).

## 1. Repository layout

```
orb/
├── go.mod                    module github.com/OrdalieTech/orb   (go ≥ 1.27.1)
├── cmd/orb/                   CLI entry point (thin: arg parsing → agent)
├── ai/                       port of packages/ai        — importable alone
│   ├── api/                  one file per API shape (openairesponses.go, anthropicmessages.go, …)
│   ├── providers/            provider registry + per-provider metadata (generated + hand corrections)
│   ├── auth/                 credential store, OAuth flows (PKCE, device-code)
│   └── models/               catalog: generated data, models.dev refresh, models.json overlay
├── storage/                  transactional document seam; sqlite/ is an explicitly opened adapter
├── accounts/                 named credential store over ai/auth; explicit sidecar and base store
├── engine/                    port of packages/agent     — loop, Agent, harness
│   └── harness/              session repo, compaction, skills, system-prompt, env abstraction
├── tui/                      port of packages/tui       — renderer + components, zero framework
├── agent/              port of packages/coding-agent — the product wiring
│   ├── tools/                read, bash, edit, write, grep, find, ls (+ operations interfaces)
│   ├── extensions/           Go-native extension API: types, registry, runner (event dispatch)
│   │   └── host/             Node/Bun child host, protocol, JS ExtensionAPI bindings
│   ├── session/              session manager (JSONL v3 tree, migrations), export-html
│   ├── config/               settings manager, trust, keybindings, auth storage, models.json
│   ├── modes/                tui, print, json, rpc
│   └── assembly/             product catalog, enablement and plugin management UI
├── chat/                     chat gateway + platform adapters (D27/D28 additions; chat → agent only)
├── plugins/                  optional capabilities, independently importable Go packages
│   ├── memory/               storage contract only
│   │   ├── agent/            engine attachment and bounded memory tools
│   │   ├── extension/        public extension API adapter; explicit Store
│   │   └── filestore/        native JSONL storage and migration reader
│   ├── usage/                quota client + cache; ai/auth and stdlib only
│   │   └── footer/           optional extension status display
│   ├── bridge/               portable routing, identity, grants
│   │   ├── agent/            agent-facing bridge tools
│   │   ├── hosts/native/     IPC and persistence
│   │   └── transports/tailcat/ native network transport
│   ├── tasks/                task tool and its optional TUI rendering
│   ├── websearch/            HTTP search/fetch, native credential and DNS defaults
│   ├── subagents/            child agents and native CLI execution
│   ├── permissions/          policy, hooks and configuration UI
│   │   └── native/           native bash/file containment through tool-operation options
│   ├── mcp/                  configured MCP integration
│   └── herdr/                explicitly selected external integration
├── internal/
│   ├── jsonschema/           Schema type + reflection helper (gate G1)
│   ├── jsonwire/             JSON.stringify-compatible wire encoder
│   ├── partialjson/          streaming tool-arg parser (port of `partial-json`)
│   ├── truncate/             shared output truncation (50KB / 2000-line rules)
│   ├── jstrim/               ECMAScript String.prototype.trim whitespace predicate
│   ├── filelock/             proper-lockfile-compatible mkdir+heartbeat lock
│   ├── cjksegment/           CJK segmentation helper
│   ├── ignorerules/          gitignore-style matching
│   ├── localecompare/        JS localeCompare ordering
│   ├── skilllocations/       external Agent Skills compatibility roots
│   ├── semver/               semver range matching (packages)
│   ├── uuidv7/               uuidv7 generation (upstream sequence scheme)
│   └── sync/                 upstream sync tool (delta report, fixture regen driver)
├── conformance/
│   ├── extract/              TS scripts run inside .upstream/ to emit fixtures (dev-only Node)
│   ├── fixtures/             committed golden fixtures (F1–F13, see §6)
│   └── runner/               go test helpers consuming fixtures; RPC black-box adapter
├── docs/                     DECISIONS.md, ARCHITECTURE.md, plan/, sync/reports/
├── AGENTS.md                 execution contract for implementing agents
└── UPSTREAM.lock             pinned upstream commit + sync state
```

### Capability packaging and host boundaries

Owner-directed regrouping (2026-09-22): `plugins/` is an organizational namespace, not a
runtime loader or a single Go package. Each capability can be imported independently. The
product catalog and `/plugins` UI live in `agent/assembly`; custom hosts register only their
chosen extension factories. Runtime disablement does not remove compiled dependencies.

Memory keeps three concrete boundaries: its Store contract, the `engine.Agent` attachment,
and the product extension adapter. File storage lives in `plugins/memory/filestore`; only
product assembly selects the default home-directory backend, lazily when enabled. SDK callers
pass an explicit Store. `plugins/usage` stays usable without the optional `footer` adapter.
Bridge keeps native IPC and Tailcat in separate packages; the protocol stays in `connect`.
Existing plugin IDs, settings keys, tools and persisted formats are unchanged. Go consumers
must update old `memory`, `usage`, `bridge`, `agent/mcp`, `agent/extensions/herdr` and
`agent/plugins` imports to the new packages; no duplicate forwarding packages are retained.

`internal/layering` checks both source edges and transitive dependencies: memory and usage
cannot pull in presentation, memory attachments cannot pull in file/SQLite backends, and
portable Bridge cannot pull in native hosts/transports. The Wasm probe executes injected
filesystem/shell tools plus memory, tasks and quota fetching without a host filesystem on
`js/wasm`; `wasip1/wasm` is compile-checked. This proves selected compositions, not a complete
browser application. Native subagents, process-backed MCP, Herdr and native transports still
require a suitable host. Web search retains native credential/DNS defaults; browser networking
and storage must be explicitly adapted. Tasks and permissions currently include TUI adapters,
and the product agent still has presentation dependencies. No browser/mobile application,
Worker lifecycle, universal platform manifest or speculative host framework is introduced.

### Permission boundaries

`plugins/permissions` enforces rules by default and requests explicit consent through the existing
runtime input seam. Guards cannot be bypassed by an authorizer asking a question. Headless requests deny
by default; cancellation always denies. Session approvals bind the session, tool, working directory,
canonical file targets, arguments and matched rule; the bounded cache resets when mode changes.
Approval prompts show the arguments and working directory. Audits omit duplicate argument payloads.
A passive Go hook does not authorize an external executor: the Go-only `Approved` flag carries
positive consent, while `Block` always wins. Neither flag addition changes pi's JSON contracts.

`plugins/permissions/native.ToolOptions` supplies the existing SDK `ToolOptions` seam with shell
sandboxing and edit/write operations backed by `sandbox.Files` and `os.Root`. The CLI and built-in
subagents use it; SDK hosts choose it explicitly instead of expecting a policy object to alter
injected tools. Browser/VFS hosts keep their own operations. Linux native hosts must implement the
existing `__sandbox` launcher; unsupported platforms refuse restricted shell execution. File writes
stay within the configured workspace (when writable) or temporary directory; reads and network
remain unrestricted. Agent bash also retains its `/dev` write exception. Direct human/RPC shell
commands and extension code remain trusted host operations. These are filesystem limits, not
isolation of plugins, credentials or network traffic. External subagent executables inherit
filesystem containment while retaining their own internal tool policy. Claude Sessions currently
refuses restricted Orb sandbox modes because it cannot enforce that boundary for its native runtime.

Configured host containment survives `--no-extensions` and disabling the permissions plugin. To
remove it, explicitly select `danger-full-access`. Command globs remain textual rules, not a shell
parser or an analysis of programs invoked by a command. Compound/expanding shell syntax with scoped
rules requires approval unless a matching whole-tool or exact-command allow explicitly authorizes it.
Path rules support recursive `**`; an allow must cover every target, including canonical destinations.

### Upstream package correspondence

1. Package ↔ package: `packages/ai` ↔ `ai/`, `packages/agent` ↔ `engine/`, `packages/tui` ↔ `tui/`,
   `packages/coding-agent` ↔ `agent/`.
2. File ↔ file where idiomatic: `agent-loop.ts` → `engine/loop.go`, `edit-diff.ts` →
   `agent/tools/editdiff.go`. Split only when Go conventions demand (e.g. `_test.go`,
   platform suffixes `bash_unix.go`/`bash_windows.go`).
3. Exported identifiers keep upstream names Go-cased: `AgentEvent`, `ToolResultMessage`,
   `runAgentLoop` → `RunLoop` (receiver-free), event name strings **unchanged** (`"message_update"`).
4. Wire/persisted JSON field names are **byte-identical** to upstream (session entries, events, RPC).
   Struct tags carry the exact upstream field names; fixtures enforce this.
5. The conformance fixtures are the upstream-sync ground truth: red after a pin bump is the work
   list. There is no upstream-path → go-path mapping table to maintain.

## 2. `ai/` — unified LLM layer

Upstream spec: `packages/ai/src/types.ts` (message/streaming model), `packages/ai/src/api/*`
(API shapes), `packages/ai/src/providers/*` (catalog), `packages/ai/docs/`.

**Types.** `Message = SystemMessage | UserMessage | AssistantMessage | ToolResultMessage` becomes a sealed interface
(`Message` with unexported marker; concrete structs). AssistantMessage content blocks
(`TextContent | ThinkingContent | ToolCall`) likewise. Preserve: `api/provider/model/usage/stopReason/
errorMessage` fields, opaque replay signatures (`thinkingSignature`, `textSignature`,
`thoughtSignature`), `ToolResultMessage.details/isError/addedToolNames`, `Usage` incl.
cacheRead/cacheWrite/cacheWrite1h/reasoning/cost, thinking levels `off|minimal|low|medium|high|xhigh|max`.
Wire emission goes through `ai.Marshal`, which matches `JSON.stringify` escaping and non-finite
tool-argument behavior; protocol code must not call `encoding/json.Marshal` directly.
System messages carry prompt sections and tool-state changes in the transcript. Existing Go
`Context.SystemPrompt`/`Context.Tools` fields remain available to legacy stream functions; provider
adapters project the transcript into their native request format.

**Streaming.** The `AssistantMessageEvent` protocol (`start`, `text_/thinking_/toolcall_` ×
`start/delta/end`, `done`, `error`) is the universal stream contract. Go surface:

```go
type StreamFn func(ctx context.Context, req Request) (iter.Seq2[AssistantMessageEvent, error], error)
```

plus a `Collect` helper folding a stream into the final `AssistantMessage`. `ai/api.StreamSimple`
adapts the common options, including `auto|none|required` tool choice, and `CompleteSimple` is its
collected form. Tool-call args stream through `internal/partialjson` exactly as upstream uses
`partial-json`.

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

**Auth.** `ai/auth`: credential store interface (file impl lives in `agent/config`), API-key
env resolution, OAuth: anthropic PKCE (localhost :53692 callback + manual paste fallback),
openai-codex, github-copilot device flow, xai. Port from `packages/ai/src/auth/oauth/*`. Radius
excluded (ledger).

## 3. `engine/` — loop, Agent, harness

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
`agent` layer's `AgentSession` (upstream `packages/coding-agent/src/core/agent-session.ts`,
spec `packages/coding-agent/docs/sdk.md`) is the high-level embedding API and the thing the SDK
advertises; its 13 upstream SDK examples get Go ports under `agent/examples/`. D29 deliberately
dissolves upstream's parallel `AgentHarness` facade into this runtime; the underlying harness
primitives remain public without duplicating orchestration or making `engine` depend on `agent`.

### Optional native session executors

`engine.SessionLoop` is an optional whole-turn callback, selected at construction through
`WithSessionLoop` or `AgentSessionOptions.SessionLoop`. The existing Agent still owns admission,
execution identity, cancellation, queue storage, ordered subscriptions and state snapshots. The
callback owns model/tool iteration and emits existing engine events. SessionRuntime skips its
provider-auth preflight, retry and automatic compaction for these sessions; explicit Orb compaction
refuses rather than rewriting a foreign engine's context. Default constructors and event shapes
remain unchanged. This seam has two real implementations: Orb's existing loop and Claude Sessions.

`plugins/claudesessions/` owns the official SDK host, native session checkpoints, event translation,
configuration and `/claude` management. Only CLI assembly imports it. Its embedded JavaScript runs
on user-provided Node with `@anthropic-ai/claude-agent-sdk@0.3.278` and a user-installed, unmodified
Claude executable. No Go dependency, vendor binary, SDK bundle or credential is embedded in Orb.
Importing the package performs no I/O. The capability defaults off; its management command can be
available before activation, like Bridge management.

Native Claude transcripts are authoritative for resume, tools and compaction. Orb stores its display
projection and plugin-owned `claude-sessions` checkpoint entries in its normal session journal
(SQLite in native CLI, unchanged caller-selected storage in the SDK). Native session IDs are
explicit, never directory-wide `continue`. A copied checkpoint forks to the new Orb session UUID
at the last confirmed native message; it never appends to the original conversation. Native errors
and list-price accounting remain native metadata, not asserted subscription invoices. Each active
turn owns one subprocess; idle sessions own none. Independent instances have independent drivers.
Steer/follow-up queues are delivered between complete native turns. Orb extension tools and context
rewrites are not injected into Claude; Claude's own tools, skills, settings and MCP remain native.

`SessionRuntime.RequestInput` is the small, vendor-neutral approval/input seam. A question has an
unguessable, single-use ID, bounded title and choices, and a cancellation context. The owning TUI
and authorized controllers race to provide the first valid answer. Bridge describes pending input
and routes `input.reply` through the existing durable operation ledger and session/execution fences;
its new `instance.input.reply` grant is explicit. Native model discovery uses the official SDK's
`supportedModels()` control without a prompt or persisted session. The plugin adapts names, resolved
IDs and effort capabilities into Orb's existing model picker; no provider registry or token endpoint
is impersonated. `session.model` uses generic model IDs and optional thinking levels, resolves only
the runtime's advertised catalog, and requires an idle revision-fenced session-management grant.
Descriptions include display-only model metadata, never model headers or credentials. No Claude event, tool name, account or session type
enters Bridge. Disconnect does not answer, cancel or broaden a pending permission request.

Research baseline (2026-09-21): [Hermes DirectSDK](https://hermes-agent.nousresearch.com/docs/plugins/claude-subscription-directsdk),
source `NousResearch/hermes-plugin-claude-subscription-directsdk@c92c27c9f919178a58974a72333b473c6cb2e71d`,
uses native stream-json as a *model provider*: replay via `shouldQuery:false`, inert tools, extra-body
injection, disabled native compaction and a localhost HTTP admission proxy. Those mechanisms are
not used here. Orb uses the [public SDK session API](https://code.claude.com/docs/en/agent-sdk/sessions)
and [permission callbacks](https://code.claude.com/docs/en/agent-sdk/user-input).
[Anthropic's current terms](https://code.claude.com/docs/en/legal-and-compliance) distinguish a product
offering Claude.ai login or intermediating credentials from an end user authenticating directly in
an unmodified native binary. Orb offers no custom login or subscription-token provider. Users sign
in through `claude auth login` on the execution host, under their own agreement; native API-key and
cloud authentication remain available. The [subscription SDK notice](https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan)
says the June 15 billing change is paused; allowances, entitlements and terms remain Anthropic's.

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

`Render(width) []string` is pure → TUI components are golden-testable (F12) and host-proxyable.

## 5. `agent/` — the product

Upstream spec: `packages/coding-agent/src/`, `docs/` (usage, settings, extensions, sdk, rpc, json,
session-format, skills, prompt-templates, models, packages, themes).

**Tools** (`agent/tools/`, upstream `src/core/tools/`): read (text + images: decode via
stdlib/x/image, resize ≤2000×2000, EXIF orientation; retain the image block on successful reads,
including upstream's contradictory non-vision note claiming omission), bash (fresh
`bash` spawn per call, command via stdin, streaming through the output accumulator, 50KB/2000-line
truncation with full spill to temp file, process-tree kill, detached-child PID tracking,
`shellCommandPrefix`, spawn-hook seam), edit (exact → fuzzy match: NFKC normalize + trailing-ws
strip + smart-quote/dash folding, normalized-space match mapped back line-by-line; multi-edit
arrays; udiff rendering), write, grep (ripgrep), find (fd), ls. `rg`/`fd`: prefer system binaries,
else auto-download upstream-style into `~/.pi/agent/bin` (`src/utils/tools-manager.ts`). Every tool:
Operations interface (delegation seam), TUI `RenderCall`/`RenderResult`, file-mutation queue
serializing writes per realpath (parallel execution default).

Native shell execution, rg/fd discovery/downloads and Unix permission checks are selected by Go
build constraints. Wasm builds retain the existing tool Operations contracts, refuse unavailable
native operations explicitly, and use delegated path identities for mutation ordering. The Wasm
probe runs read/write/edit and injected bash with no host filesystem. This establishes tool and
agent-package portability, not a complete browser/Worker host or Windows support. Demo extensions
remain in examples and conformance tests; the CLI no longer imports them.

**Sessions** (`agent/session/`): JSONL v3 in-file tree (header line, 8-hex ids, parentId,
leaf = position; entry types `message`, `model_change`, `thinking_level_change`, `compaction`,
`branch_summary`, `custom`, `custom_message`, `label`, `session_info`), v1→v2→v3 auto-migration,
location `~/.pi/agent/sessions/--<cwd-dashed>--/<ts>_<uuid>.jsonl`, overrides
(`--session-dir` > `PI_CODING_AGENT_SESSION_DIR` > setting). Export to HTML (upstream
`src/core/export-html/`) and markdown. Byte-compatible with TS pi — cross-read fixtures (F6) prove it.

**Config** (`agent/config/`): settings manager (global deep-merged with project
`.pi/settings.json`; unknown keys tolerated), auth storage (0600, legacy `oauth.json` migration),
trust flow, keybindings, `PI_CODING_AGENT_DIR` override, models.json hot reload.

**Extensions — Go-native core** (`agent/extensions/`): the full ExtensionAPI as Go interfaces,
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

**Extensions — JavaScript host** (`agent/extensions/host/`): all JavaScript and TypeScript
extensions run in one owned local Node.js ≥22.6 or Bun child process. Discovery covers the
trust-gated project directory, global directory, configured paths, resolved npm/git package paths,
and explicit `-e` entries in upstream order. Node strips TypeScript natively and Bun executes it
directly; where Node refuses type stripping under `node_modules`, the loader supplies transpiled
source from its load hook so the package manager's own layout keeps governing resolution. Every
`@earendil-works/pi-*` (and legacy `@mariozechner/pi-*`) specifier resolves to the embedded
`orb-extension-sdk` (`host/sdk/`, versioned by `sdk.json`, go:embed-ed and materialized
content-addressed beside `host.mjs`): pure ports of the exercised upstream symbols, thin
session/settings/resource handles, and capability-negotiated services (`sdk_v1`,
`agent_session_v1`, `model_runtime_v1`) that bridge `createAgentSession`, `ModelRuntime`, and
`ModelRegistry` onto the Go runtime (`agent.ExtensionAgentSessionService`); every other
upstream export throws a precise `OrbUnsupportedCapability` diagnostic. The loader refuses — as a
per-extension resolve-time load failure — any resolution reaching a real installed pi SDK.
Missing declared dependencies are materialized with npm or Bun before load.

The embedded `host.mjs` dynamically imports each entry and proxies the complete ExtensionAPI over
versioned bidirectional JSONL. Registrations, events, tools, commands, providers and auth callbacks,
state snapshots/deltas, and the full `ctx.ui` surface terminate in the normal Go registry and UI
interfaces. Component rendering is push-based so Go's synchronous `Render(width)` reads the latest
host-provided frame. A PATH-prepended `pi` symlink points subagent processes back to orb. Hot
`/reload` stops the current generation, starts a fresh child, imports every entry again, and rebinds
stable Go wrappers. Unexpected exits use bounded restart/backoff; shutdown cancels pending UI and
in-flight requests. If neither supported runtime exists, orb emits the D31 diagnostic once and
continues without JavaScript extensions.

**MCP** (`plugins/mcp/`): bundled extension registering MCP servers from settings as tool
sources via `modelcontextprotocol/go-sdk` (stdio + streamable HTTP), tools surfaced through the
normal registration API with dynamic tool loading. Off unless configured.

**Modes** (`agent/modes/`): TUI (default), print `-p` (stdin merge), json (AgentSessionEvent
JSONL out — adds `queue_update`, `compaction_start/end`, `auto_retry_start/end` per `docs/json.md`),
rpc (bidirectional JSONL stdin/stdout per `docs/rpc.md`: prompt/steer/follow-up/abort, session
mgmt, get_commands, extension-UI bridging; strict LF framing). RPC is a conformance surface —
upstream's RPC tests run against our binary (F7).

**Interactive discovery:** Ctrl+P opens a searchable palette of actions, individual settings, templates, and
extension commands. Ctrl+M selects models when the terminal disambiguates it from Enter; the
palette also exposes model selection on legacy terminals. Ctrl+N starts a session and Ctrl+R renames it. Existing explicit
keybindings win over these new defaults. Native composer slash suggestions appear above the input with commands, skills, and templates;
extension editors keep the complete pi completion surface. Selecting a resource
inserts its canonical invocation into the draft for arguments and explicit submission. Plugins
and Bridge are page destinations: selecting them opens their UI immediately and preserves the draft. Floating
modals dim the background; non-capturing extension overlays keep their opt-in backdrop. The native
composer reserves Shift+Enter for newlines, including ambiguous legacy Escape+Return input;
explicit protocol Alt+Enter continues to queue follow-ups.

**Provider accounts and usage:** Ctrl+P → Providers groups connected accounts with Add account
under every provider. `accounts.Store` wraps an explicit `ai/auth.CredentialStore`, keeping the
provider-keyed `auth.json` unchanged when adding accounts and recording extra credentials and
selection in an atomic, locked, 0600 `accounts.json` sidecar. The CLI attaches it; importing the
engine or auth package alone does not pull it in. `BindCredentialStore` pins one account through
OAuth read/refresh/write, and `NewModelRegistryWithCredentials` supplies the same source to model
availability and request resolution. Explicit CLI keys retain precedence; switching that provider
requires restarting without the override. No account file is created until an account action.

The independent `usage.Client` reads Codex's `backend-api/wham/usage` and OpenCode Go's
`zen/go/v1/usage` endpoints with bounded requests and no credential-bearing redirects. It reports
remaining quota from provider data; missing data stays unavailable. Its cache holds at most 64
account identities. Footer and account reads share a bounded request cache keyed by provider,
endpoint and credential digest; the account UI retains its nonblocking snapshot cache.
`plugins.ProviderUsage` attaches through extension lifecycle events and footer
statuses as the default-off `provider-usage` assembly row, enabled by Show usage in footer in
Providers; it is hidden from the general Plugins menu. It polls once per minute and cancels on
account/model changes and shutdown. Clicking
that footer status opens a native account switcher with cached percentages and at most four
concurrent refreshes; closing it cancels requests. Switching providers keeps an identical model
when available, otherwise opens the model picker. Neither accounts nor usage imports agent/TUI
code, and no quota network request blocks startup or rendering. The compact footer keeps the model
and reasoning level on the left, with the current session working directory, the most limited
remaining quota, and a rounded context percentage on the right. Healthy Bridge adds no redundant
footer label; startup disconnection stays visible. Detailed windows and resets stay in the account menu; compact rendering
does not probe Git metadata.

**Slash commands / skills / templates / themes:** resolution order extension → input hook →
`/skill:name` → template. Orb also discovers the standard project/user skill roots of Claude Code,
Codex, OpenCode, Gemini CLI, Cursor, and GitHub Copilot; project roots are trust-gated, native roots
win collisions, and external aliases deduplicate without scanning plugin caches. At any prompt
token, `@` autocomplete mixes clearly badged skills with files and inserts the canonical
`/skill:name` path when a skill is accepted. Built-in interactive commands (`/login /logout /model /resume /new /name
/session /tree /trust /fork /clone /compact /copy /export /import /reload /hotkeys /settings
/changelog /quit`; `/share` → local export per ledger); skills per agentskills.io with progressive
disclosure + trust gating (upstream `src/core/skills.ts`); prompt templates with bash-style arg
expansion (`$1`, `$@`, `${1:-default}`, `${@:N:L}`); themes as data (registerable via resources).

**pi packages:** `orb install/remove/update/list/config` for `npm:`/`git:` extension/skill/theme
packages — npm registry tarball fetch + extract (no node at runtime), git clone; storage
`~/.pi/agent/npm/` + project `.pi/npm/` (upstream `docs/packages.md`). Package installation itself
is native Go; executing package-provided JavaScript requires the D31 Node/Bun runtime.

## Native persistence

`cmd/orb/storage.go` selects one explicitly opened SQLite database for native CLI state.
`storage/sqlite` supplies transactional documents, Pi v3 journals, memory and chat-spool
repositories; existing SDK constructors keep file defaults and import no SQLite driver.
Global settings, credentials/accounts, trust, model catalogs, keybindings, Bridge identity/grants,
attachment credentials/receipts and foreign previews share the database. Project configuration,
installed resources, exports, sockets, process locks and diagnostic logs remain ordinary files.

The first native startup inventories legacy roots, refuses active legacy Orb writers, journals
source hashes and imports each source transactionally before publishing cutover. Interrupted
imports resume; changed/conflicting sources, unsupported session versions and damaged trees
fail closed. Source files remain untouched recovery copies, never live shadows. Native sessions
have real IDs and empty file paths; process locks protect local ownership and journal revisions
reject stale writes. Session replacement claims the destination before aborting the current turn.
No database transaction spans a provider request, approval or model stream.
Initialized databases open without taking a write lock; first opens serialize schema creation.
WAL readers remain independent of writers. FULL durability is retained with a 30-second SQLite
busy wait for contention on slower disks; exhaustion returns an error, never replays a transaction.

`orb --pi-files ...` explicitly selects the original file assembly for compatibility consumers;
it must use a separate legacy root after native cutover. The unchanged upstream RPC tests run
through this entry point. Native exports use the same Pi JSONL/HTML/Markdown codecs. Backup uses
SQLite `VACUUM INTO`, with private permissions and durability barriers. `storage restore` merges
conversation journals from a backup and rejects conflicting histories; it never rolls back
credentials, Bridge authority, receipts or chat delivery state. Whole-database authority rollback
is deliberately not exposed. Managed remote hosting and the unified multi-Bridge selector remain
separate, pending slices of the active plan.

## Orb Bridge v1 — implementation contract

The owner-approved native delivery is one `orb` executable: `orb bridge` administers an
explicit profile, while `orb --bridge <profile> --instance <alias>` attaches a runtime.
Bridge management is built into Settings, Ctrl+P, and `/bridge`; opening it starts no service.
Its home page contains the service switch, Add device, pending approvals, and a live device list.
Add device offers SSH setup or invitation exchange; selecting a device opens its live conversations
directly. Advanced contains only agent-call opt-in and the local fingerprint. Groups, grants,
scopes, and receipts remain CLI administration. Bridge has no duplicate toggles in Plugins.
Pairing explicitly activates the service when needed. Invitations are bounded,
versioned copy/paste codes; each owner confirms trust in the other identity. New TUI/SSH pairings
grant full controller access to all current and future conversations in both directions, including
new groups. The owner approved this simpler default on 2026-09-21; existing restricted grants
are unchanged. The reserved grant selector `group_id: "*"` means all groups, with `include_future`
retaining its existing snapshot-versus-future meaning. Trust does not grant remote administration,
discovery scopes, or agent-subject authority. Saved pairings survive service restarts; connection badges derive from
live streams, and stopping waits for the admin connection to close. Open panels refresh bounded
reads once per second, preserve selection by identity, and cancel work on close. The conversation
picker reads bounded catalog pages, excludes disconnected instances, and reconnects after daemon
restarts without replaying any commands. Local admin status advertises
`supports_full_access`; start and pairing replace a daemon missing that capability through the
normal stop/start path before issuing grants. The replacement preserves profile state and lets
non-owning runtime attachments reconnect. A previously deliberate Stop remains effective until
explicit activation; an upgrade's temporary stop marker is removed before starting the replacement.
Networking remains explicitly enabled, and its focused remote conversation view requires no local model credentials. The 2026-09-21 Bridge v1
specification governs the protocol; this section supersedes its two-executable packaging.
Restricted launching, mobile UIs, browser transports, and platform hosting adapters are excluded.

The owner-requested SSH pairing shortcut lives entirely in `cmd/orb`. It invokes the host's
existing OpenSSH client with strict host-key verification and noninteractive authentication.
Setup checks PATH and the user's install directory, reuses a compatible Orb, or downloads the
server's release through the existing checksum-verifying updater. It streams the verified binary
over SSH, validates Bridge support in a temporary executable, then atomically installs it in
`~/.local/bin` without elevation or shell edits. An incompatible published release leaves the
existing binary intact. It then starts Orb and approves a one-use invitation through the owner's
SSH session. Invitations never enter command-line arguments. SSH ends after setup; conversation
traffic uses the same pinned Bridge transport and two directional grants.
No SSH service, helper binary, key copying, or additional Go dependency is shipped.

| Layer | Responsibility |
|---|---|
| `connect` | Versioned calls, receipts, observations, and non-owning attachment contracts |
| `connect/agent` | Existing `AgentSessionRuntime` adaptation; operation ledger and bounded observations |
| `plugins/bridge` | Identity, pairing, directional grants, registration, routing, scoped contacts |
| Native/Tailcat adapters | Explicit persistence, profile locks, IPC roles/credentials, network streams |
| `cmd/orb` and UI assemblies | Lifecycle, management, capability selection, remote conversation view |

Existing SDK constructors, interfaces, subscription semantics, and defaults remain unchanged.
Neither `ai`, `engine`, nor `agent` imports Bridge, Tailcat, or new UI dependencies. Closing an
attachment, view, or bridge never disposes a runtime. Stores, credentials, transports,
and lifetimes are explicit; imports create no files or network activity. Multiple independent
bridges and runtimes can coexist in one process. Only interchangeable attachment and persistence
boundaries need interfaces. Native storage stays outside Pi-managed files and keeps bridge
metadata, instance attachment credentials, and operation ledgers separately owned.

Native IPC separates local-owner administration from scoped instance attachment, checks OS peer
credentials, fences registrations with persistent generations, and refuses simultaneous writers.
Bridge loss marks instances unavailable while their local work continues. Pairing pins Ed25519
PeerIDs and requires a recoverable one-use invitation claim plus local-owner approval of exact
grants. Group membership is administrative; fixed selectors snapshot InstanceIDs, while future
membership requires explicit selection. Discovery scopes grant no execution authority. Agent
calls require source and destination grants, derive subjects from attachment credentials, and
never transparently forward execution through a third bridge.

The transport is pinned TLS 1.3 with ALPN `orb-bridge/1` over Tailcat streams, without TLS
resumption or Tailcat shell/file/proxy/exit-node services. Persist transport keys and PSKs.
JSON-RPC frames use four-byte big-endian lengths, at most 1 MiB each, depth 64, 64 in-flight
requests, pages of at most 128 items, and 4 MiB queued output. Reject duplicate keys, invalid
UTF-8, malformed IDs, unknown behavioral arguments, and noncanonical decimal counters.
Signed contact payloads use JCS and are bounded at 16 KiB; reconciliation preserves conflicting
revision diagnostics and durable withdrawal floors within explicit scopes.

`orb.instance/1` maps inspection, prompt, steer, follow-up, cancel, and session list/new/switch/fork.
Session IDs resolve inside the adapter. Local, extension, and remote work share transition
ordering and execution identity; control locks never span model streams or interactive approvals.
Durably record acceptance before acknowledgment or dispatch. Retain compact deduplication
records for the instance identity's lifetime; quota exhaustion refuses new acceptance. Identical
retries retrieve authorized receipts before stale-generation checks; conflicting payloads fail.
Recheck authorization and target preconditions at dispatch, never retarget, and reconcile crash
ambiguity as `outcome_unknown` rather than replaying effects. Accepted work belongs to the runtime.
Remote prompts cannot invoke bridge administration slash commands.

Snapshot state and its continuation cursor are established atomically. Paginated transcript
snapshots have stable identities and explicit expiry. Replay and subscriptions are bounded;
slow consumers receive a resnapshot signal and cannot stall runtimes or other instances.
Use bounded callbacks, never the existing lossless `SubscribeChan` adapter. No transcript replica,
hidden command queue, compression, arbitrary file transfer, or general plugin framework.

Explicit enable starts a background `orb bridge run` process that survives TUI exit. Enabled
launches restore it if needed; deliberate Stop persists until Start or re-enable. No login service
or implicit SDK startup. Begin with a personal profile/group, with all state profile-scoped.
Bridge protocol tests are Orb-owned checks, separate from Pi conformance families.

### Native v1 method schemas

Every request is one JSON-RPC 2.0 object with a string ID; batches and notifications are
unsupported. `bridge.hello` exchanges `peer_id`, `bridge_boot_id`, `protocol`, `max_frame`,
and `max_page` before application methods. Frame limits are the smaller advertised limit
(minimum 1 KiB); oversized results fail explicitly, and pages respect the smaller item limit.
Counters use canonical unsigned decimal strings; operation, instance, group, scope, and boot
IDs use 16 random bytes encoded as unpadded base64url. Session/entry IDs remain Orb session IDs.

| Method | Parameters and result |
|---|---|
| `bridge.ping` | `{}` → `{}` |
| `pair.claim` | `invitation_id`, secret `token`, optional claimant `locator` → recoverable invitation status |
| `pair.status` | `invitation_id` → status for its authenticated claimant |
| `instances.list` | optional `cursor` → authorized `items`, optional continuation `cursor` |
| `instances.describe` | `instance_id` → current generation, session/revision/execution target, optional name/workspace/input request, permitted methods |
| `instances.call` | `instance_id`, `service`, `method`, `args`; mutations additionally require `session_id`, `expected`, `operation_id` → inspection/list result or durable receipt |
| `operations.get` | `instance_id`, `operation_id` → caller-scoped receipt |
| `events.subscribe` | optional `instance_id` (absent means catalog); either replay `cursor`, or optional `snapshot_id` and page `offset` → replay events or frozen transcript page plus cursor and partial message |
| `events.unsubscribe` | `instance_id`, `snapshot_id` → release retained snapshot |
| `peers.list` | `scope_id`, optional `cursor` → scoped signed records |
| `peers.publish` | bounded `records` array → validated, durably merged revisions |

`expected` contains `registration_generation` and `session_revision`. Prompt arguments are
`{text}`; steer/follow-up are `{text, execution_id}`; cancel is `{execution_id}`; input replies
are `{execution_id, id, value}` and require `instance.input.reply`; session new
is `{}`, switch is `{session_id}`, fork is `{entry_id}`, and model selection is
`{provider, model, thinking?}`. Session list accepts an optional
`offset`. Read-only inspection needs no operation ID. An optional `subject` on remote calls
is restricted to an instance subject and comes from the source bridge's credential-bound
outbound route; administrative methods never appear in this routing table.

The CLI caches visited remote session summaries and visible message excerpts through
`storage/sqlite`, separately from owned sessions. Cache keys include the profile, pinned peer,
namespace and SessionID; legacy services use an instance-scoped namespace. Limits are eight
completed user/assistant messages, 4 KiB per message / 32 KiB encoded per preview, seven days,
128 sessions per peer and 1,024 per profile. Offline previews are explicitly stale and read-only;
reopening revalidates the destination, and revocation fences delayed refreshes and purges content.
Reasoning, tool payloads and attachments never enter this cache.

Receipts retain the operation ID, target, expected revision/generation, method, acceptance time,
canonical payload digest, status, and bounded result/error. Terminal deduplication entries are
never evicted. A failed storage barrier makes the writer unavailable until reopened; unfinished
receipts recover as `outcome_unknown`. Bridge profiles and ledgers each cap storage at 1 MiB.
Snapshot retention is four snapshots per attachment for one minute, with an 8 MiB/16,384-message
transcript mirror. Replay retains 2,048 events within 4 MiB. `cursor_expired` explicitly requests
resnapshotting; oversized individual messages fail with `resource_exhausted`. Catalog/contact
cursors bind the complete ordered content digest and expire if that content changes. Catalog
observers return an empty replay while unchanged and explicitly request a fresh snapshot after
a catalog or visibility change; the bridge retains no per-client catalog history.

The native adapter serves separate `admin.sock` and `attach.sock` endpoints with same-UID checks.
Its stores use an exclusive file lock, private permissions, atomic replacement, file fsync,
and directory fsync. `ORB_BRIDGE_HOME` explicitly overrides the profile root for isolated hosts.
Transport metadata contains the pinned server key, PSK, relay region, and per-peer client keys.
No remote-supplied relay map or embedded relay definition is accepted. Contact changes trigger
reconciliation, with a 15-second retry sweep; signed recovery locators must authenticate the
already-pinned PeerID. Native direct and forced-relay tests are separate from the hermetic gate.


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
| F6 | session format | v1/v2/v3 parse, migrate, write; cross-read both directions; tree/fork/list/export goldens |
| F7 | RPC transcripts | request/response conversations against the real binary |
| F8 | slash/template expansion | arg expansion + resolution order |
| F9 | system-prompt assembly | context files, SYSTEM/APPEND_SYSTEM, skills disclosure |
| F10 | compaction | summarization boundaries, firstKeptEntryId, token accounting |
| F11 | extension behavior | Go-native extension runner and product-wiring effects |
| F12 | TUI render goldens | Component.Render(width) line snapshots |
| F13 | dynamic-workflows ecosystem replay | `@quintinshaw/pi-dynamic-workflows@3.5.1` behavior goldens from real pinned pi, replayed through the extension host + orb-extension-sdk bridges |

Extraction runs Node/vitest **inside `.upstream/`** (dev-only), emitting JSON the Go tests consume.
Where upstream lacks a directly extractable test, the extractor drives upstream's own faux provider
(`packages/ai/src/providers/faux`) or public APIs to synthesize goldens. LLM-dependent behavior
(compaction summaries) is fixture-tested at the boundary (prompts + structure), not on model output.

**Black-box:** upstream RPC/CLI tests run unmodified against `orb --mode rpc` via a thin adapter
that swaps the spawned binary. Host behavior is covered by real Node/Bun end-to-end tests and the
locked 44-package harness under `conformance/extensions/`; F11 remains the extracted Go-native
runner and wiring surface.

## 7. Upstream sync

`UPSTREAM.lock` records `{repo, commit, syncedAt}`. `make sync` (also runnable by an agent as a
work package): clone/fetch upstream → diff `lock..HEAD` → classify each path by pattern
(kernel: wire-format / API-surface → obligations; feature-only / docs → optional cherry-picks) →
regenerate fixtures at the new commit → run conformance → write `docs/sync/reports/<date>.md`
(delta summary, fixture diffs, failing conformance, proposed work items). Only kernel paths carry
port obligation (P5). Owner/agent triages; lock bumps when green. Cron automation is deliberately
deferred until conformance is stably green.

## 8. Dependency policy

Rule: every direct dependency appears in this table with a justification; adding one without
updating the table fails review. Stdlib first; a few hundred lines of internal code beats a new
dependency; a well-maintained official SDK beats reinventing a provider.

| Dependency | Where | Why |
|---|---|---|
| openai/openai-go/v3 | ai/api | OpenAI responses+completions (D10) |
| anthropics/anthropic-sdk-go | ai/api | Anthropic messages + caching (D10) |
| klauspost/compress | ai/api | zstd request compression required by the OpenAI Codex Responses wire |
| aws-sdk-go-v2, aws-sdk-go-v2/{config,credentials,service/bedrockruntime}, smithy-go | ai/api | Official Bedrock client, credential chain, SigV4/bearer auth, and converse-stream (D10) |
| modelcontextprotocol/go-sdk | mcp | official MCP SDK v1.6+ |
| yuin/goldmark | tui, chat | CommonMark parsing (render stays ours) |
| alecthomas/chroma/v2 | tui | syntax highlighting (upstream: highlight.js) |
| rivo/uniseg | tui | grapheme/East-Asian width |
| golang.org/x/{term,sys,image,text} | cli, tui, tools | terminal detection/raw mode, signals, image decode/resize, encoding |
| bmatcuk/doublestar/v4 | tools, skills | `**` globbing (upstream: glob/minimatch) |
| gopkg.in/yaml.v3 | skills, config | frontmatter + YAML settings surfaces |
| aymanbagabas/go-udiff | tools | unified diff for edit rendering (upstream: `diff`) |
| tailscale/tailcat v0.7.0 | CLI transport assembly | Stream-only WireGuard/NAT traversal and DERP; tested below the existing size/startup budgets with upstream omission tags; no SDK dependency |
| gofrs/flock | memory, native bridge storage | file locking for the JSONL memory store (session/config use internal/filelock) |
| @anthropic-ai/claude-agent-sdk 0.3.278 | optional `plugins/claudesessions` Node host | Official native session, permission and cancellation API; installed automatically on first Claude session, outside Go module and release binary |
| modernc.org/sqlite v1.59.0 | native CLI / opt-in SDK `storage/sqlite` adapter | CGo-free SQLite 3.53.4; WAL/FULL durability, transactional documents, indexed session journals and FTS5 catalogs, memory, chat spool and bounded foreign previews; CLI explicitly owns the database lifetime |

**G1 resolution (WP-110):** `internal/jsonschema` uses a stdlib-only reflector. The evaluated
`invopop/jsonschema` output required stripping `$schema`/`$defs`/`$ref` and undoing closed-object
defaults to match TypeBox's inline provider-facing schemas, while adding five transitive packages
and 640 KiB to a stripped probe binary. No direct dependency was added.

**G2 resolution (WP-221):** Gemini uses a stdlib REST/SSE adapter. Against consolidated commit
`813da39`, a `google.golang.org/genai@v1.64.0` probe grew the correctly stripped binary from
17,907,874 to 26,374,306 bytes (+8,466,432, 47.278%), expanded the module graph from 67 to 102
entries, and grew the compiled package graph from 294 to 477 packages. The final hand-rolled adapter
adds 155,648 bytes (0.869%) and no modules. WP-222 completes Vertex with stdlib REST/SSE and
request-scoped pure-Go ADC; against its consolidated parent it adds 393,216 bytes (2.177%), no
module, and no compiled package (WP-222).

Explicitly rejected: TUI frameworks (D15), langchaingo/fantasy-style unified LLM libs (D10),
v8go/quickjs CGo bindings (D7), and native SQLite bindings (the v0.81.0 storage package is ledgered;
sessions remain JSONL or memory-backed).

## 9. Build, size, release

- `CGO_ENABLED=0` for every product/release target `{linux,darwin} × {amd64,arm64}`; goreleaser for
  static binaries + checksums; install via curl script + Homebrew tap. Development race-test
  binaries may enable CGo only for the Go race runtime (D7). Version checks use GitHub releases.
- Budgets: cold start < 50 ms; release binary ≤ 55 MB decimal; `go vet` + golangci-lint clean;
  race detector on in CI tests. JavaScript startup belongs to the optional external host, while
  > 10% orb binary-size growth still triggers investigation.

## 10. Risks & mitigations

| Risk | Mitigation |
|---|---|
| Node/Bun version or module-resolution drift | minimum runtime probe, protocol handshake, real-host end-to-end tests, and the locked ecosystem matrix |
| Extension API breadth (2,943-line spec) | Go-native API proves semantics; protocol/API inventory and host end-to-end tests cover callbacks and snapshots |
| TUI fidelity drift | F12 render goldens + side-by-side session comparison protocol in phase 4 |
| Provider SDK churn (openai v1→v3 history) | SDK usage confined to `ai/api/*` adapter files; unified types are ours; F2 pins request shapes |
| Upstream velocity (multi-release weeks) | pin + sync reports; formats-first tracking (D5); path-classified sync reports keep kernel obligations triaged |
| Event/serialization drift breaking conformance | F1/F3/F6/F7 fixtures regenerate on every sync; wire-format struct tags reviewed against goldens |
| Parallel tool execution races | file-mutation queue per realpath (upstream semantics); race detector in CI |
| Host lifecycle and request races | generation-scoped correlation, bounded restart/backoff, typed UI cancellation, and race tests |

The optional `plugins/questions` module owns the question schema, validation, native tool
and shared TUI panel. Runtime input carries only bounded opaque presentation data and an optional
reply validator. Claude maps native questions inside `plugins/claudesessions`; the CLI renders the shared
panel for remote views, while Bridge remains unaware of Claude and question schemas. Native Claude
tool permissions adapt through the existing extension `BeforeToolCall` hook, keeping policy in the
Permissions plugin and execution in the native SDK.
