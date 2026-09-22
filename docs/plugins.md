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

### permissions

```json
"permissions": {
  "enabled": true,
  "preset": "workspace-write",
  "rules": [ { "tool": "bash", "command": "git push*", "action": "ask" } ]
}
```

- `preset`: `workspace-write` (sandbox `workspace-write` + mode `enforce`) or
  `danger-full-access` (no sandbox + mode `log`). Explicit keys override the
  preset.
- `sandbox`: `read-only` | `workspace-write` | `danger-full-access` — bash
  filesystem containment (Linux Landlock, macOS `sandbox-exec`). Both
  restrictive modes keep `/dev` and the temp directory writable so
  `2>/dev/null` and `mktemp` keep working; `workspace-write` adds the session
  working directory. Enforcement is fail-closed: without Landlock or
  `sandbox-exec` the command refuses to run (exit 126) with the remedy named.
  Coverage is partial by nature (Landlock does not mediate chmod/chown-style
  metadata mutations).
- `mode`: `enforce` or `log` (audit only). Guard denials contributed through
  the SDK hold even in log mode.
- `rules`: last match wins; `tool` / `command` / `path` globs, `action` is
  `allow` | `deny` | `ask`. Bash is matched by its command text only.
- `askFallback`: `allow` | `deny` when no UI can prompt.

Any unknown or malformed key refuses startup with the key named; SDK embedders
get a deny-all policy instead.

Note `--no-extensions` disables the permissions plugin — and with it the
sandbox.

### memory, tasks, websearch

Boolean gates. `memory` persists bounded remember/recall/replace/forget notes
in native SQLite (or under the agent dir for file-backed SDKs); `tasks` adds the todo tool and live task widget;
`websearch` adds web search and readable page fetching.

## MCP servers

Top-level `mcpServers` object, Claude Desktop / Cline dialect. Two transports:

```json
{
  "mcpServers": {
    "files":  { "command": "mcp-files", "args": ["--root", "."], "env": { "TOKEN": "…" } },
    "remote": { "url": "https://example.com/mcp", "headers": { "Authorization": "Bearer …" } }
  }
}
```

Optional per server: `enabled: false` (or the `disabled: true` alias),
`cwd`, `timeoutMs` (default 10000), `maxRetries` (HTTP only). Exactly one of
`command` / `url`; `args`, `env`, and `cwd` are stdio-only.

From the shell (no session, no server is spawned):

```
orb mcp list
orb mcp get <name>
orb mcp add files --env TOKEN=x -- mcp-files --root .
orb mcp add remote --url https://example.com/mcp --header "Authorization=Bearer x"
orb mcp remove <name>
orb mcp enable <name> | disable <name>
```

`orb mcp add` validates through the exact parser the session uses, so anything
it accepts is something the session will start. Writes go to the global
settings; project entries are edited in `.pi/settings.json` by hand.

In a session, `/mcp` opens the live status window (state, transport, target,
registered tools, errors) with in-place reconnection; `/mcp reconnect [server]`
still works everywhere.

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

Open `/claude` (also available from the command palette) and choose **New Claude session**.
Orb prepares the SDK automatically on first use, then opens the conversation. The executing host needs Node ≥22.6, npm and the official Claude Code executable. Complete
sign-in in a terminal with `claude auth login`; Orb does not implement a Claude.ai login screen or
read tokens. Existing native API-key/cloud authentication is also available. Native terms, model
entitlements and usage limits apply; SDK cost metadata is not your subscription invoice.

For headless use (with the same automatic first-use setup): `orb --provider claude-sessions --model sonnet -p "your task"`.
The model picker uses the executing Claude CLI's `supportedModels()` catalog: native aliases,
resolved model names and supported effort levels. New sessions use Claude's native default unless
you select another model. `/model` changes the current session; `/claude` → Model chooses the default
for new sessions. The bottom bar labels Claude sessions and reports the actual native model after
each turn. Discovery starts no model turn and writes no Claude transcript.

`--session` and the ordinary Sessions picker resume the selected Orb conversation using its explicit
native Claude session ID. `/claude` selects the model for explicitly created Claude sessions.
**Switch to Orb** opens a separate regular conversation and keeps the Claude session saved; it also
works before an Orb provider is configured. Ordinary launches never implicitly choose Claude. `--no-extensions`
disables this optional capability. There is no fallback to another account or model on errors.

Advanced settings use `plugins.claude-sessions`: `model`, `node`, `claude`, and `sdk`
(the absolute path to the official package's `sdk.mjs`). The standard install goes into
`<agent-dir>/plugins/claude-sessions`, pinned to SDK 0.3.278. The SDK and native executable are not
bundled in Orb's static binary. Installation happens when starting a Claude session; opening settings installs nothing.

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
Orb never executes the presentation-only tool definitions.

When the **Permissions** plugin is enabled, Claude's native pre-tool hooks use its existing rules,
approval cache and audit log. Native tool names and paths are normalized only for policy evaluation;
Claude still executes its own tools and retains native restrictions. With Permissions disabled,
Claude's native permission behavior applies. Orb's Bash filesystem sandbox does not sandbox Claude's
native executable.

Claude owns native tools, skills, MCP, project settings and compaction. Orb's tool plugins are not
injected into that agent loop. Queued steer/follow-up messages enter at native turn boundaries.
Orb stores its transcript projection and private checkpoint metadata in SQLite; the native Claude
transcript remains on the execution host and is required for resume. Pi export does not make native
Claude context portable. Interrupted operations are never automatically replayed.
Forks resume from a confirmed native checkpoint. Rewinding an existing Orb branch does not rewind
Claude's native history: continuing that branch fails explicitly, and requires a new fork.
Headless runs without a local UI or an attached controller deny interactive permission requests;
native settings may already authorize individual actions.
