# Bundled MCP extension

This package is the D18 addition to upstream pi: an MCP client built on the official Go SDK and
registered through the same native ExtensionAPI used by every other extension. MCP does not enter
the agent loop, provider layer, or built-in tool registry directly. With no enabled server in
settings, the CLI does not construct the extension and performs no MCP work.

## Settings

Add a top-level `mcpServers` object to global or trusted-project `settings.json`. Each key is the
server's local name and each value selects exactly one transport:

```json
{
  "mcpServers": {
    "local": {
      "command": "my-mcp-server",
      "args": ["--stdio"],
      "env": { "API_TOKEN": "..." },
      "cwd": ".",
      "timeoutMs": 10000
    },
    "remote": {
      "url": "https://example.test/mcp",
      "headers": { "Authorization": "Bearer ..." },
      "maxRetries": 2,
      "timeoutMs": 10000
    },
    "temporarily-off": {
      "command": "another-server",
      "enabled": false
    }
  }
}
```

`command` selects stdio and accepts `args`, `env`, and `cwd`; `url` selects streamable HTTP and
accepts `headers` and the SDK's `maxRetries` value. `cwd` is resolved from pi-go's working
directory. The process inherits the current environment before applying `env` overrides.
`timeoutMs` defaults to 10 seconds and bounds connect, initialization, and initial tool discovery.
Unknown fields are ignored so settings remain forward-compatible. Project entries are invisible
until the existing project-trust flow accepts that project. Setting `goExtensions.mcp` to `false`
or passing `--no-extensions` disables the bundled extension as a whole.

## Lifecycle and tool mapping

All enabled servers connect independently during extension loading, so one unavailable server is
reported by `/mcp` without preventing the session from starting. `/mcp reconnect [server]`
recreates one or every connection. Session shutdown closes the SDK sessions and stdio children.
Server tool-list notifications add new tools and deactivate removed ones without retrying calls
whose side effects may already have run.

Remote names are exposed as stable, provider-safe names of the form
`mcp__<server>__<tool>_<hash>`. The hash prevents collisions after sanitizing or truncating long
names. JSON input schemas pass through unchanged. Text and image results map to pi-go's native tool
result blocks, structured content and MCP metadata remain in `Details`, and MCP progress
notifications become normal tool execution updates. MCP tool errors are returned through the
agent's ordinary error path so providers receive an error tool result.

## Test strategy

There is no upstream pi implementation to extract a conformance golden from. The package tests
instead run the official SDK example-server pattern end to end, including progress and mixed
text/image output, and separately exercise real stdio and streamable-HTTP transports. Dynamic tool
changes, reconnect/error isolation, disabled configuration, deterministic naming, and the
unconfigured zero-work path are ordinary Go tests.
