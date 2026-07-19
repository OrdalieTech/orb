# WP-350 Go-native ExtensionAPI report

Status: **historical integration snapshot**. The gaps described here were open when WP-350 first
landed; current Sprint 1 status and superseding evidence live in `docs/compare/sprint-1.md`.

## Behavior

Registrations retain extension and source order. The first tool and flag registration wins,
duplicate commands receive upstream's numeric invocation suffixes, and later extension shortcuts
override earlier non-reserved bindings with diagnostics. Runtime actions reject use during factory
loading, provider registrations queue until core binding, and captured APIs and contexts become
unusable after invalidation.

Generic handlers isolate failures and continue in order. Context, provider request, input,
before-agent, message-end, and tool-result handlers form the same middleware chains as upstream;
session-before events stop on cancellation, project trust stops at the first decided answer, and a
failing `tool_call` handler blocks execution with its error as the reason. Print and JSON contexts
always receive the no-op UI, while RPC accepts a supplied dialog bridge and the exported no-op base
lets headless adapters implement only the methods their protocol supports. TUI implementations
remain Sprint 2 work.

The native permission-gate example is a direct port of the pinned upstream regex and confirmation
flow. Its faux-agent test submits a dangerous bash call, proves the underlying tool executes zero
times, and checks that the agent receives the blocked error result.

## Conformance evidence

`conformance/extract/f11-extension-runner.ts` imports the real pinned upstream runner and generates
deterministic cases for ordered error isolation, structured context cloning, tool-result patches,
mutable and fail-safe tool calls, system-prompt chaining, input transforms, provider payloads and
headers, resource discovery, project trust, and session cancellation. The Go consumer reconstructs
each scenario through the native public API and compares canonical JSON. This `F11-native` family
is the core-runner precursor to the unmodified JavaScript example matrix in Sprint 3.

At integration time, Sprint 1 still had to close the known native-runner gaps: event emission must match upstream's
void/error-isolation contract, project-trust handlers need the limited context and startup order,
provider registration must use the canonical provider/auth types without dropping queued entries,
and input identity, panic-origin stacks, registration conflicts, and nil wiring need upstream-derived
cases before this historical spec can be called complete.

The final candidate passes:

```text
make fixtures-check
make build
make test
make lint
go mod verify
go mod tidy -diff
git diff --check
CGO_ENABLED=0 GOOS={linux,darwin} GOARCH={amd64,arm64} go build ./...
```

## Status at integration time

| Criterion | Status | Evidence |
|---|---|---|
| Go-native ExtensionAPI, registry, and context seams | Integrated; parity pending | compile-time API plus registration, runtime, event-bus, UI, command-context, and stale-context unit coverage |
| Complete upstream runner dispatch semantics | Pending Sprint 1 | retained `F11-native` extraction and Go replay pass, but the missing cases above remain |
| Per-mode headless UI degradation | Passed | print/JSON no-op enforcement and RPC supplied-bridge test |
| Native permission-gate blocks a faux-session tool call | Passed | faux agent records zero bash executions and an error tool result with the upstream reason |

No dependency was added and no golden was edited by hand.
