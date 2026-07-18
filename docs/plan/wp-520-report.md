# WP-520 — JavaScript ExtensionAPI non-UI bindings

WP-520 exposes the complete non-UI extension surface to each isolated Sobek VM. Event hooks retain
upstream ordering and mutable/lazy payload behavior; tools, commands, flags, shortcuts, messages,
session metadata, model and thinking controls, active tools, the cross-VM event bus, process
execution, abort signals, session-manager reads, and the non-UI command context all marshal through
the owning VM goroutine. UI render callbacks remain recorded with their plain-text fallback for
WP-541, and Node built-ins remain WP-530 scope.

Provider registration is integrated with the shared concrete model registry rather than a bridge
side catalog. Native and configuration-style providers participate in models.json layering,
configured and stored authentication, availability filtering, refresh, model lookup, request
headers, and stream dispatch. Refresh callbacks run without holding the registry operation lock,
so an extension can synchronously replace or unregister a provider without deadlocking; revision
checks prevent an older refresh snapshot from overwriting that mutation.
Explicit reloads remain serialized across the unlocked callback window, while provider-scoped
versions preserve a network refresh result when an overlapping automatic cache refresh publishes.

The bridge embeds the real npm `typebox@1.1.38` distribution once and resolves every extension's
typebox import to that shared module. F11 copies the pinned upstream todo, summarize, commands,
send-user-message, dynamic-tools, structured-output, tool-override, and event-bus examples
byte-for-byte, then the Go bridge executes their documented behavior without source edits. Focused
tests also cover every event shape, callback lifetime, asynchronous completion, provider auth and
streaming, cross-VM events, cancellation, execution timeout, and stale VM behavior.

## Verification

- F11-jsbridge regenerated from pinned upstream commit
  `3da591ab74ab9ab407e72ed882600b2c851fae21` with zero diff
- `go test -race -run F11 ./conformance/runner`
- focused race suites for config, extensions, jsbridge, codingagent, and cmd/pi
- `make lint` and `go mod verify`
- `CGO_ENABLED=0 go build ./...` for Linux and Darwin on amd64 and arm64
- `git diff --check`

The full race suite has only the three pre-existing F12 Unicode word-navigation failures, reproduced
unchanged from this WP's clean base. No dependency was added outside ARCHITECTURE section 8, no
golden was hand-edited, and the product remains pure Go with CGO disabled.
