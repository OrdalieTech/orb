# Phase 3 — Harness & headless parity

Lane B: everything that makes pigo a complete headless product and embeddable SDK.

## WP-310 — Compaction, branch summaries, auto-retry, queue

**Upstream refs:** `packages/agent/src/harness/` (compaction, branch summarization),
`packages/coding-agent/src/core/agent-session.ts` (orchestration, auto-retry, queue events),
settings `compaction.*`, `branchSummary.*`, `retry.*`.

**Scope:** compaction (reserveTokens/keepRecentTokens boundaries, summary entry +
firstKeptEntryId + token accounting), branch summarization on tree navigation, auto-retry with
backoff (provider timeout/retry settings), queue events (`queue_update`,
`compaction_start/end`, `auto_retry_start/end`), context-usage accounting.

**Fixtures:** **F10** — boundary/selection goldens (which entries kept, summary insertion point,
token math) on synthetic sessions; summarization prompts asserted structurally, not on LLM output.

**Acceptance:** F10 green; a long faux-driven session compacts at the same entry boundaries as upstream.

## WP-320 — Session tree ops + export

**Upstream refs:** `session-manager.ts` tree navigation, `docs/session-format.md`;
`src/core/export-html/`.

**Scope:** fork, `--fork`, resume `-r`, `--session <path|id>`, `-c` semantics, tree navigation
primitives (used by `/tree` TUI later and RPC), labels/naming (`-n`, session_info entries),
`--export <in> [out]` → self-contained HTML (port export-html renderer) + markdown export
(`/share`'s local replacement per ledger).

**Acceptance:** F6 extended with tree/fork goldens; exported HTML for a fixture session matches
upstream's structurally (DOM-level comparison with tolerances documented).

## WP-330 — JSON mode

**Upstream refs:** `packages/coding-agent/docs/json.md`, `src/modes/` json entry.

**Scope:** `--mode json`: AgentSessionEvent JSONL to stdout (AgentEvents + queue/compaction/retry
extras), stdin prompt handling, clean shutdown semantics.

**Fixtures:** **F3-session** traces (json-mode event streams for scripted faux runs) — byte-equal
lines modulo documented nondeterminism (timestamps, ids — canonicalized by the differ).

**Acceptance:** trace fixtures green; `pigo --mode json` drives a faux session identically to upstream.

## WP-331 — RPC mode

**Upstream refs:** `packages/coding-agent/docs/rpc.md` (1,480 lines — the spec),
`src/modes/rpc/`, `src/rpc-entry.ts`; upstream RPC tests (the black-box suite).

**Scope:** bidirectional JSONL over stdin/stdout, strict LF framing: commands (prompt, steer,
follow-up, abort, session mgmt, get_commands, state queries), extension-UI protocol bridging
(dialog/notify round-trips surface as RPC exchanges), error frames. This is the embedding surface
for non-Go hosts and the conformance crown jewel.

**Fixtures:** **F7** — recorded upstream RPC transcripts replayed against `pigo --mode rpc`;
PLUS upstream's own RPC test files executed unmodified against the Go binary via the WP-002 adapter.

**Acceptance:** F7 green; upstream RPC test suite passes against pigo (exclusions documented
one-by-one with reasons).

## WP-340 — Skills, prompt templates, slash resolution

**Upstream refs:** `docs/skills.md`, `docs/prompt-templates.md`, `src/core/{skills,
prompt-templates,slash-commands}.ts`, `packages/agent/src/harness/skills.ts`.

**Scope:** agentskills.io SKILL.md parsing (lenient validation, frontmatter incl. allowed-tools,
disable-model-invocation), discovery across all locations (global/`.agents`/project walked to git
root, packages, settings, `--skill`), trust gating, progressive disclosure into system prompt
(XML per spec), `/skill:name` commands; prompt templates (frontmatter, bash-style args `$1 $@
${1:-d} ${@:N:L}`); slash resolution order (extension → input hook → skill → template).

**Fixtures:** **F8** (expansion + resolution goldens), F9 extended (skills disclosure block).

**Acceptance:** F8/F9 green; an upstream skills dir + prompts dir load with identical system-prompt
output and command lists.

## WP-350 — Go-native ExtensionAPI core

**Upstream refs:** `docs/extensions.md` (the 2,943-line spec), `src/core/extensions/types.ts`,
`runner.ts` (dispatch semantics).

**Scope:** `codingagent/extensions/`: the full API as Go interfaces — every hook, registration,
messaging/state call and Ctx surface listed in ARCHITECTURE §5; registry; runner with upstream
dispatch semantics (ordered handlers, middleware chaining for `tool_result`, error isolation,
`tool_call` fail-safe blocking, per-mode UI degradation no-ops). Ctx.ui interfaces defined now,
TUI-backed implementations arrive Phase 4 (headless impls: RPC-bridged + no-op).

**Acceptance:** unit tests for dispatch semantics extracted from upstream runner tests (F-series
addition); a native Go demo extension (port of upstream `examples/extensions/permission-gate.ts`)
compiles against the API and blocks a tool call in a faux session.

## WP-351 — Extension wire-through

**Upstream refs:** call sites across `packages/coding-agent/src/` (loader/runner integration,
tool pipeline, provider request path, input path, `user_bash`).

**Scope:** thread the runner through: agent lifecycle events, context rewriting, provider
header/request/response hooks in `ai` call path, tool_call/tool_result middleware around tool
execution, input interception, user_bash, resources_discover, built-in tool override, dynamic
active-tools (deferred-loading passthrough per upstream), registerFlag/Command into CLI, event bus,
`Exec` helper. Extension discovery/loading order for *Go* extensions (compiled-in registry;
settings-driven enable/disable) — TS loading is Phase 5.

**Acceptance:** ported permission-gate + pirate + status-line Go demo extensions behave as their
upstream docs describe in print/json modes; F3 traces unchanged when no extensions registered
(zero-cost when unused).

## WP-352 — Bundled MCP extension

**Upstream refs:** none (divergence D18) — design doc required in-package.

**Scope:** `codingagent/mcp/`: settings-declared MCP servers (stdio + streamable HTTP via official
go-sdk), tools registered through WP-350/351 API with dynamic tool loading, tool-result mapping
(text/image), server lifecycle (start/stop/reconnect), `/mcp` status command (TUI later), off unless
configured. Settings schema documented in the package README (kept out of upstream-mirrored settings docs).

**Acceptance:** against the go-sdk example server: tools appear, execute, stream results in a faux
session; disabling removes them; core binary without config performs zero MCP work.

## WP-360 — pi packages + project trust

**Upstream refs:** `docs/packages.md`, `src/core/` package manager + trust flow
(`project_trust` hook, `trust.json`), settings `packages[]`.

**Scope:** `pigo install/remove/update/list/config` for `npm:` (registry tarball fetch + extract,
integrity check, no node) and `git:` sources into `~/.pi/agent/npm/` + project `.pi/npm/`;
resource contribution (extensions/skills/prompts/themes) into discovery; project trust flow +
`defaultProjectTrust` + trust-gated project resources.

**Acceptance:** installing an upstream-published pi package (e.g. a skills pack) yields the same
resources TS pi sees (cross-check fixture); trust prompts/gating match upstream behavior in json mode.

## WP-370 — SDK surface + examples + docs

**Upstream refs:** `packages/coding-agent/docs/sdk.md` (1,183 lines), `examples/sdk/` (13 examples),
`agent-session.ts` (`createAgentSession`, runtime variant).

**Scope:** public embedding API polish: `codingagent.NewAgentSession` (options struct mirroring
upstream createAgentSession), channel/iterator adapter over Subscribe, godoc pass on all exported
surfaces, port all 13 SDK examples to `codingagent/examples/` as runnable Go programs, `docs/sdk.md`
for Go (structure mirroring upstream's doc).

**Acceptance:** all 13 examples compile and run against faux; godoc renders clean; an external
`go get` smoke module builds against the tagged pre-release.
