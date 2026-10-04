# Plugins, permissions, and MCP — configuration reference

Settings use the `settings.json` schema. Native global settings live in SQLite; trusted project
overrides remain in `.pi/settings.json` (merged one level deep, project wins). File-backed SDK
and explicit Pi-file compatibility keep global settings at `~/.pi/agent/settings.json`.

Three surfaces expose the same configuration:

- **In the app**: `/plugins`, `/permissions`, and `/mcp` open configuration
  windows; `/model` picks models with context, cost, and capability columns.
  The `/plugins` window is the hub: type to filter, toggle plugins and
  external sub-agent CLIs (known CLIs found on PATH appear ready to enable),
  and install packages without leaving the session.
- **From the shell**: `orb plugins …` and `orb mcp …` work without a session.
- **By hand**: export native global settings with `orb storage config export settings.json file.json`,
  edit that file, then apply it with `orb storage config import settings.json file.json`. Project
  settings remain directly editable. Invalid plugin values fail closed at startup with the key named.

## Plugins

Bundled plugins are **off by default** (`orb plugins list` shows the available modules).
A plugin's value is either a boolean or an object holding its settings:

```json
{
  "plugins": {
    "tasks": true,
    "subagents": { "enabled": true, "external": { "claude": "claude -p --output-format text" } }
  }
}
```

`orb plugins enable <name>` / `disable <name>` toggle the gate and preserve any
object settings. `orb plugins list --all` prints the full resolved composition
(compiled extensions, plugins, MCP row, discovered JS extensions) with the
settings layer that decided each state — through the same code path the real
boot uses.

Bundled plugins are named `builtin:<name>` in errors and diagnostics, and
`-e builtin:<name>` loads one for a single run, even with `--no-extensions`.
The MCP plugin steps aside, with a startup warning, when another extension
registers one of its tool, command, or flag names (its own `/mcp`, say).

### subagents

```json
"subagents": {
  "enabled": true,
  "external": {
    "claude": "claude -p --output-format text",
    "codex": "codex exec --skip-git-repo-check -"
  }
}
```

Each `external` value is either the command string or
`{"command": "…", "enabled": false}` — the object form switches a CLI off
without losing its configuration (the `/plugins` window toggles between the
two). The subagent tool gains the enabled names as child roles next to the
built-in `scout` / `worker` / `reviewer`, in single or parallel mode (up to 32
children, 4 concurrent). An external CLI receives the task text on stdin, must
answer on stdout, runs in the session's working directory under the operator's
environment, and is bounded: 10-minute timeout, 1 MiB output caps, whole
process group killed on cancellation. The model can only ever pick a
configured name — never supply a command.

### jobs

Background commands for Orb's own models, as Claude Code runs them. `bash` gains
`run_in_background`: the call returns a job ID and its log file at once, and a message reports
the job's end (exit code, duration, last lines), starting a turn if the model is idle.
`monitor` does the same and also reports each line the command prints, at most once a second.
`stop_job` ends a job's whole process group (TERM, then KILL after two seconds); every job
ends with the session, and its log, in a temporary directory, with it. Jobs run
through the same bash as foreground commands, so the sandbox, shell, command prefix and
`permissions` rules for `bash` apply unchanged. At most 8 run at once. Claude models keep
Claude Code's own background tasks and `Monitor`.

### permissions

```json
"permissions": {
  "enabled": true,
  "preset": "workspace-write",
  "mode": "enforce",
  "rules": [ { "tool": "bash", "command": "git push*", "action": "ask" } ]
}
```

- `preset`: `workspace-write` (sandbox `workspace-write`, default mode `auto`) or
  `danger-full-access` (no sandbox + mode `log`). Explicit keys override the
  preset.
- `sandbox`: `read-only` | `workspace-write` | `danger-full-access` — native bash, edit and write
  filesystem containment (`os.Root` for file tools; Linux Landlock or macOS
  `sandbox-exec` for bash). Both restrictive modes keep the temp directory writable;
  bash also keeps `/dev` writable so
  `2>/dev/null` and `mktemp` keep working; `workspace-write` adds the session
  working directory. Enforcement is fail-closed: without Landlock or
  `sandbox-exec` the command refuses to run (exit 126) with the remedy named.
  Coverage is partial by nature (Landlock does not mediate chmod/chown-style
  metadata mutations).
- `mode`: `auto` (default), `enforce` (manual prompts), or `log` (audit only). Auto
  resolves asks without a model or prompt while preserving explicit denials, guards, cancellation
  and containment. Existing explicit modes remain unchanged. Guard denials and authorizer
  failures hold even in log mode; audit-only decisions never approve external tools.
- `rules`: last match wins; `tool` / `command` / `path` globs, `action` is
  `allow` | `deny` | `ask`. Paths support recursive `**`; an allow must cover all
  canonical targets. Bash rules match command text, not the effects of an invoked program.
  Compound/expanding syntax under scoped rules asks unless explicitly allowed as a whole tool
  or exact command. Auto mode denies these unresolved asks. Use host containment for filesystem limits.
- `askFallback`: `allow` | `deny` (default) in manual mode when no UI can prompt. Cancellation,
  prompt errors and invalid replies always deny. Approval dialogs show the actual arguments
  and working directory; session approvals cannot cross tools or working directories.

Use `/permissions` to save the mode, or `--auto` to enable auto mode for one run without
persisting settings. `--auto` cannot be combined with `--no-extensions`. The example above
selects manual mode so the `ask` rule prompts before a push.

Any unknown or malformed key refuses startup with the key named; SDK embedders
get a deny-all policy instead.

Configured filesystem containment remains active with `--no-extensions` or a disabled
permissions plugin. Select `danger-full-access` explicitly to remove it. In-process and external
subagents inherit containment; external CLIs retain their own internal tool policy. SDK hosts
use `plugins/permissions/native.ToolOptions` explicitly for local tools; browser/VFS hosts
supply their own operations. Reads and network access are not restricted.

### memtree

A tree of one-line summaries over each session's whole history, after
[OptChat](https://gist.github.com/VictorTaelin/91837951a5ce5b38f341ec1ba1df6449): every message
gets a line (a short message is its own line), adjacent lines merge in pairs up the tree, and a
view of it, recent messages one line each and older ones many per line, stays under a fixed budget
(128 KB, or half the model's context window in bytes). The agent opens any line back down to its
message with `zoom(id, n)`, and `date(id)` dates a message.

```json
{ "plugins": { "memtree": { "mode": "fresh", "model": "provider/model-id" } } }
```

- `mode: "fresh"` (default, OptChat's loop): each prompt starts a new context, the view and then
  the prompt, and earlier messages are a zoom away; the system prompt tells the agent so. A run that
  outgrows the context window is compacted onto a newer view.
- `mode: "compaction"`: sessions run as usual, and when Orb compacts, the summary is the view of
  everything before the kept messages: no model call, and nothing summarized twice.
- `model`: the compactor, as `provider/id`; the session's model when unset. It runs at medium
  effort about once or twice per message, each call reading the view as context, so pick a cheap,
  fast model: messages are summarized one at a time, and turns wait for them. Anthropic models
  reuse no cache across these calls.

A turn, or a compaction, waits until every earlier message is summarized, showing
`memtree: summarizing N messages`; Escape ends the wait. Summaries are kept in the session as
hidden `memtree` entries, so they follow it across forks, exports and hosts (the Worker and Celld
host turns the plugin on from `plugins.memtree` in its settings). Claude and Codex sessions run
their own loop and bypass the plugin.

### memory, tasks, websearch

Boolean gates. `memory` persists bounded remember/recall/replace/forget notes
in native SQLite (or under the agent dir for file-backed SDKs); `tasks` adds the todo tool and live task widget;
`websearch` adds web search and readable page fetching.

## MCP servers

Servers live in `mcp.json`: `~/.pi/agent/mcp.json`, plus `.pi/mcp.json` in a
trusted project (its entries replace global ones of the same name). The shape is
the `mcpServers` object other MCP clients use:

```json
{
  "mcpServers": {
    "files":  { "command": "mcp-files", "args": ["--root", "."], "env": { "TOKEN": "${FILES_TOKEN}" } },
    "remote": { "url": "https://example.com/mcp", "exposure": "direct", "description": "Docs search" }
  }
}
```

Optional per server: `enabled: false`, `cwd`, `timeout` (seconds per request,
default 60), `exposure` (`codemode`, the default, and `deferred` load through
`tool_search`; `direct`; `hidden`), `toolExposure` per tool or `*` pattern, and
`description`, which the `mcp_servers` system prompt section shows. Exactly one
of `command` / `url`; `args`, `env`, and `cwd` are stdio-only. See
`plugins/mcp/README.md`.

From the shell (no session):

```
orb mcp list [--json]          # connects each server once and reports it
orb mcp add files --env TOKEN=x -- mcp-files --root .
orb mcp add remote --url https://example.com/mcp --bearer-token-env-var REMOTE_TOKEN
orb mcp remove <name> [-l]     # -l / --local edits the project's .pi/mcp.json
orb mcp login <name>           # OAuth sign-in through the browser
orb mcp logout <name>
```

HTTP servers without an `Authorization` header sign in with OAuth (`oauth.clientId`,
`clientName`, `callbackPort`, `authServerMetadataUrl`, …); `"auth": {"provider": "anthropic"}`
in the global file sends that provider's `orb login` token instead.

Servers connect in the background when a session starts. `/mcp` opens the
live status window (state, transport, target, tools, errors) with in-place
reconnection; `/mcp login|logout|reconnect [server]` work everywhere.

## Questions

Enable **Questions** in `/plugins` to expose `ask_user_question` to Orb's agent. It accepts one to
four questions with stable IDs, optional described choices, multiple selection and free text.
Questions temporarily replace the composer, keeping conversation history scrollable and restoring
the previous draft afterward. Click choices, question tabs and Continue/Submit directly; dragging
does not submit an answer. The shared panel shows descriptions below numbered options. Use arrows or number keys to choose,
Enter or Space to toggle multiple choices, and select **Type your own answer** for custom text.
Tab and Left/Right move between questions and the final review; Enter submits the review. A single
choice submits immediately. Escape leaves custom editing first, then dismisses without answering.
Answers remain normal tool results in the session transcript; there is no separate question store.
Headless hosts need an attached controller to answer. Claude uses this same panel for its native
`AskUserQuestion`, independently of whether the Orb tool is enabled. Bridge views render the same
panel and send execution-bound replies; disconnecting never invents an answer.

## Claude Sessions

Enable **claude-sessions** in `/plugins` (or set `"plugins": {"claude-sessions": true}`) and pick a
Claude model in `/model`. Orb prepares the SDK automatically on first use. The executing host needs
Node ≥22.6, npm and the official Claude Code executable.

Claude is then a provider like the others. `/login` lists it as **Claude** with its accounts: the
Claude Code login you already have, and any account added with **+ Add account**, which runs the
official CLI's own sign-in (the first account opens the browser; a further one shows the link to
open where that account is signed in, then takes the code it shows). Each account is a Claude Code
configuration directory under `<agent-dir>/plugins/claude-sessions/accounts`; the CLI keeps its
credential and Orb never reads it. Everything but the sign-in (settings, skills, agents, hooks,
MCP servers) is shared with your own Claude configuration, and switching accounts, even in the
middle of a conversation, continues it. Accounts are named, switched, reconnected
and disconnected like any provider's, and each shows its plan limits (5-hour, 7-day, per model)
under **Usage and reset times** and in the account switcher, as Codex and OpenCode accounts do. Existing native API-key/cloud authentication is also
available.

Claude's models are listed in `/model` beside every other provider's, and one conversation moves
freely between them: Claude runs its turns through the SDK, Orb's own loop runs the others. Orb
holds the transcript. Claude's records are copied into the Orb conversation after each turn, and
whenever Claude must start again (another model answered, `/tree` moved, the account changed) Orb
brings the Claude Code session up to date: Claude's own records, and other models' turns as plain
messages, their tool calls and results as text. Other models read Claude's turns like any other.
An Orb conversation and its Claude Code session share one ID and one transcript file: `orb --resume
<claude-code-session-id>` (or `--session`) opens a Claude Code session as an Orb conversation that
Claude, or any model, continues, `claude --resume <id>` continues an Orb conversation in Claude
Code, and turns added there appear in Orb the next time it opens the conversation. Native terms, model entitlements and usage
limits apply; SDK cost metadata is not your subscription invoice.

For headless use, with the plugin enabled and the same automatic first-use setup: `orb --provider claude-sessions --model sonnet -p "your task"`.
The model picker uses the executing Claude CLI's `supportedModels()` catalog: native aliases,
resolved model names and supported effort levels. A Claude model opens at the thinking level last
chosen for it (kept as its
`modelThinkingLevels` entry, leaving `defaultThinkingLevel` to Orb's own providers). The existing model footer is retained, with a single compact Claude quota status. Discovery starts no model turn and writes no Claude transcript.

`/tree`, withdrawn prompts and branch summaries work as in any Orb session; Claude writes the
summary. Native approvals use Orb's choices, and **approve for this
session** lasts as long as the running Orb session. Messages sent while Claude works join the running
turn after its next tool result. Orb's system-prompt additions reach Claude; context files such as CLAUDE.md
are Claude's own and load natively, and Orb does not inject its AGENTS.md. In `-p`/JSON runs, approvals Claude would ask for run as Orb's own tools would,
unless one of your Claude ask rules forces the prompt. `--no-extensions`
disables this optional capability. There is no fallback to another account or model on errors.

Advanced settings use `plugins.claude-sessions`: `model`, `node`, `claude`, and `sdk`
(the absolute path to the official package's `sdk.mjs`). `ANTHROPIC_API_KEY` and
`ANTHROPIC_AUTH_TOKEN` are removed from the Claude process so a key exported for Orb's own providers
never bills a subscription session; set `inheritApiKey: true` to pass them through. The standard install goes into
`<agent-dir>/plugins/claude-sessions/sdk-0.3.280`, pinned to SDK 0.3.280. Setup validates the
installed version, stages replacements before publishing them, and preserves older installations
used by running sessions. A failed download can be retried. Custom SDK paths remain host-owned.
The SDK and native executable are not bundled in Orb's static binary. Installation happens when
starting a Claude session; opening settings installs nothing.

The same host can attach with `--bridge <profile> --instance <alias>`. Pairing and routing are
unchanged; the remote device needs neither Node nor Claude credentials. A pending permission or
question appears in the remote view. Questions use the shared panel; `/reply <choice or text>`
answers ordinary approvals, and `/cancel` cancels the execution. `/models` lists the executing host's models and effort levels; `/model <provider/id>
[effort]` changes the model while idle, under the existing session-management grant and revision
fence. Answers require the explicit `instance.input.reply` grant (included in newly created
full-access peer grants). Existing grants are not silently expanded. Closing a view leaves execution
and pending permission decisions on the host. Local TUI approvals use Orb's native dialogs.
Clarifying questions retain their descriptions and
previews; choose one answer, write your own, or toggle several choices and confirm them. Multiple
questions are presented in order. Dismissing a question sends a denial rather than inventing an
answer. Native plan approvals and readable question/task/tool summaries stay inside the plugin;
Orb never executes the presentation-only tool definitions. MCP form requests use the same shared
questions, validating each answer against the requested schema. MCP URL requests show the link on
the controlling client and require explicit confirmation; Orb never opens a browser on the execution
host. Cancelling a request returns cancellation to the SDK.

`/compact` in a Claude turn submits Claude's own compaction command.

When the **Permissions** plugin is enabled, Claude's native pre-tool hooks use its existing rules,
approval cache and audit log. Native tool names and paths are normalized only for policy evaluation;
Claude still executes its own tools and retains native restrictions. With Permissions disabled,
Claude's native permission behavior applies. Claude Sessions refuses restricted Orb filesystem
sandbox configurations because it cannot enforce them for Claude's native executable.

The footer shows subscription quota from native SDK rate-limit events, for example
`Claude 7d 40% left`, identifying the active window with the least quota remaining.
It appears after Claude reports a reading, normally after the first
reply, and is also shown in Bridge views. These are subscription limits, not session token counts.
Readings older than five minutes are marked stale; expired windows are omitted, and missing
percentages remain an explicit status rather than a fabricated number. Another Claude client can
consume quota between updates. Orb never reads native credentials. After each reply the plugin requests the SDK context summary
(without per-category token-count API calls) and displays used tokens and context percentage as `24k|12%`, matching
Orb’s compact footer after the working directory. The plugin supplies context through the generic
executor telemetry callback; Orb owns the layout. Context metadata survives session resume, but
new work, model changes and compaction invalidate the previous reading. Failed refreshes leave
context unknown and never fail a turn.

Claude owns native tools, skills, MCP, project settings and compaction. Orb's tool plugins are not
injected into that agent loop. Queued steer/follow-up messages enter at native turn boundaries.
A turn stays active until its non-ambient background tasks complete and the SDK stream drains;
cancellation interrupts that work. Retry, compaction and task notices use ordinary transcript events;
task notices cover subagents and background work only, since a foreground command already has its
tool row. Tool/task progress uses bounded updates. Subagent transcripts remain separate.
Orb stores the conversation and Claude's transcript records in its own journal, so any branch,
fork or copied session resumes on any host with the plugin. Interrupted operations are never
automatically replayed.
Headless runs without a local UI or an attached controller deny interactive permission requests;
native settings may already authorize individual actions.
