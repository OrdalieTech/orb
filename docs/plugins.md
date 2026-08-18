# Plugins, permissions, and MCP — configuration reference

Everything here lives in `settings.json` — global at `~/.pi/agent/settings.json`,
per-project at `.pi/settings.json` (merged one level deep, project wins, and
project settings only apply once the project is trusted).

Three surfaces expose the same configuration:

- **In the app**: `/plugins`, `/permissions`, and `/mcp` open configuration
  windows; `/model` picks models with context, cost, and capability columns.
- **From the shell**: `orb plugins …` and `orb mcp …` work without a session.
- **By hand**: edit `settings.json` directly; invalid values fail closed at
  startup with the exact key named.

## Plugins

Bundled plugins are **off by default** (`orb plugins list` shows the five).
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

The subagent tool gains the configured names as child roles next to the
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
under the agent dir; `tasks` adds the todo tool and live task widget;
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
