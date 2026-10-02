# Bundled MCP extension

An MCP client built on the official Go SDK and registered through the same native ExtensionAPI as
every other extension, following pi 1.0's built-in MCP integration. MCP does not enter the agent
loop, provider layer, or built-in tool registry directly. `goExtensions.mcp: false` or
`--no-extensions` turns it off.

## mcp.json

Servers are configured in `mcp.json` in the agent directory (`~/.pi/agent/mcp.json`) and, once the
project is trusted, `.pi/mcp.json` in the project, whose entries replace global ones of the same
name. The `mcpServers` shape is the one other MCP clients use, so their configurations copy over.
`orb mcp add|remove|list` edits and checks the files without starting a session.

```json
{
  "mcpServers": {
    "filesystem": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."] },
    "docs": {
      "url": "https://example.com/mcp",
      "headers": { "Authorization": "Bearer ${DOCS_TOKEN}" },
      "exposure": "direct",
      "description": "Product documentation search"
    },
    "off": { "command": "another-server", "enabled": false }
  }
}
```

`command` selects stdio and takes `args`, `env` and `cwd` (relative to the session's directory;
`~` expands); `url` selects streamable HTTP and takes `headers`. Header and env values may be
`${NAME}` or `!command`. `timeout` is the per-request timeout in seconds (default 60), reset by
progress notifications. Server names use letters, digits, `_` and `-`; names that differ only in
`-` and `_` would share a tool namespace and are rejected. Invalid entries are reported once
(and by `/mcp`) while the others load.

`exposure` says how the model reaches a server's tools, and `toolExposure` overrides it per tool,
by exact name or `*` pattern:

- `codemode` (default): Orb has no codemode, so these behave as `deferred`.
- `deferred`: declared once `tool_search` loads them, then called directly.
- `direct`: declared like built-in tools.
- `hidden`: registered but unreachable.

## Lifecycle and tools

Servers connect in the background when a session starts. The first prompt waits up to 10 seconds
for servers with direct tools; `tool_search` waits for every server still connecting. Each prompt
carries an `mcp_servers` system prompt section listing the servers whose tools are not declared,
with their descriptions (or instructions) cut to fit 4096 characters. A server that fails is
reported once; `/mcp` shows every server and `/mcp reconnect [server]` reconnects. A call that
finds the connection dead marks the server failed and the next call reconnects.

Tools are named `mcp__<server>__<tool>` with everything but `[A-Za-z0-9_]` turned into `_`; names
over 64 characters or shared by several tools get an 8-hex hash suffix. Each tool carries its
server's namespace, its annotations' hints, and a `CallToolResult` output schema: the result's
`structuredContent` is the whole MCP result without `_meta`, and `isError` results are error
results. Tools a server stops offering are re-registered hidden.

## tool_search

`tool_search` (the `tool-search` row) is registered for every session and activated when a server
has deferred tools. It ranks the deferred tools that are not active with BM25 over their names,
descriptions, schema text and namespace, and activates the matches, so the transcript records them
and resume restores them; tools of a restored loadout that register late (servers still
connecting) turn on when they register. Providers with native tool search receive later
additions as deferred declarations.
