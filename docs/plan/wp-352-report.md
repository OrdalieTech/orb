# WP-352 bundled MCP extension report

Status: **complete**. Settings-declared stdio and streamable-HTTP servers now surface their tools
through the native extension pipeline, while an unconfigured binary constructs no MCP extension
and performs no MCP connection work.

## Design and behavior

`codingagent/mcp` is the D18 boundary: the official Go SDK owns MCP protocol and transport
behavior, while the package translates server tools into ordinary `extensions.ToolDefinition`
values. The core agent and provider layers have no MCP-specific path. `cmd/pi` appends the hidden,
default-enabled compiled extension only when the effective trusted settings contain at least one
enabled server; `goExtensions.mcp=false` and `--no-extensions` keep it dormant.

Each server initializes independently under its configured timeout. Successful sessions paginate
tool discovery, register deterministic provider-safe names, pass JSON input schemas through, map
text and image results into native content blocks, retain structured output and metadata in
details, and forward MCP progress as tool execution updates. Tool-list notifications refresh the
registry and active-tool set; removed tools reject stale calls. Transport failures mark the
session unavailable without retrying the current call, so a potentially side-effecting operation
is never executed twice. `/mcp` reports per-server transport, state, error, and tool count, and
`/mcp reconnect [server]` recreates one or every session. Session shutdown closes all SDK sessions
and stdio children.

The in-package README is the required design document and settings schema. Stdio configuration
accepts `command`, `args`, `env`, and `cwd`; HTTP accepts `url`, `headers`, and `maxRetries`;
`enabled` and `timeoutMs` apply to both. Project settings reach the parser only after the existing
WP-360 trust gate.

## Acceptance evidence

- The official SDK example-server pattern runs end to end over in-memory transports: its tool is
  discovered through the extension registry, executed through the wrapped agent tool, emits a
  progress update, and returns mixed text/image plus structured content.
- Separate round trips use a real `CommandTransport` child process and the SDK streamable-HTTP
  handler, including HTTP header injection.
- Tool-list changed notifications add and remove dynamic tools; reconnection preserves active
  tools; one timed-out server does not block a healthy server.
- Disabled server entries, the compiled-extension override, `--no-extensions`, invalid settings,
  and the absent-config zero-work path are covered in package and CLI tests.
- There is no upstream fixture to extract because D18 is an explicit addition. No golden was
  created or edited by hand; the tests exercise the official SDK implementation directly.

The only new direct dependency is `github.com/modelcontextprotocol/go-sdk v1.6.1`, explicitly
approved by ARCHITECTURE §8. Its module requirements raise `golang.org/x/sys` to `v0.41.0` and add
the SDK's pure-Go transitive modules; `go mod tidy -diff` and `go mod verify` are clean.

Final verification:

```text
make fixtures-check
make build
make lint
go test -race ./...
go mod verify
go mod tidy -diff
git diff --check
CGO_ENABLED=0 GOOS={linux,darwin} GOARCH={amd64,arm64} go build ./...
```

The accumulated ordinary and race suites retain only the four unchanged WP-420 CJK dictionary
segmentation assertions documented in `wp-420-report.md`; WP-352 introduces no additional red.
