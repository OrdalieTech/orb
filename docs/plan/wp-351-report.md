# WP-351 extension wire-through report

Status: **integrated, Sprint 1 parity still red**. The retained `F11-wire` cases pass and the native
runner reaches the agent, provider, tool, input, command, user-bash, resource, compaction/tree, and
CLI paths, but those cases do not yet close the complete upstream extension seam.

## Behavior

`SessionRuntime` binds a non-empty extension registry before subscribing its ordinary listeners,
so extension lifecycle handlers run before persistence and external event delivery. Context,
provider payload/header/response, tool-call/result, message replacement, and settled hooks retain
the runner's ordered middleware and error-isolation behavior. Compaction and tree hooks can cancel,
replace summaries, override navigation inputs, and receive the persisted after-event with upstream
provenance. Input processing follows upstream's command-first order, including extension-originated
messages while streaming; idle input events omit `streamingBehavior`, while transformed steer and
follow-up messages enter the normal agent queues. Header transforms reach every integrated adapter;
response hooks run for the exact adapters that expose them upstream.

The tool registry starts with all built-ins, overlays the first registered extension tool by name,
then applies allowlists and deny-lists. `--no-builtin-tools` starts with no active built-ins while
keeping them discoverable through `getAllTools`, so extension tools still activate and explicit
`--tools` remains authoritative. Refreshes preserve active tools and activate newly registered tools
in registry order. Wrapped extension tools retain upstream's deferred-loading quirk:
`addedToolNames` changes only for an additive active-tool change, deduplicates only when a new name
was added, and otherwise preserves duplicates.

Compiled extensions load in declaration order and are controlled by the merged `goExtensions`
settings object; `--no-extensions` disables the catalog without allocating a registry. The three
ported demos are catalogued but disabled by default. Registered flags participate in help and CLI
validation, registered commands execute before input interception, and their bound API retains the
shared event bus and `Exec` helper. State calls preserve model-selection event suppression and clamp
thinking levels without duplicate session entries. `resources_discover` paths retain handler and
extension order with source metadata and are exposed by the session. Discovered skills and prompts
extend the WP-340 resolver after package/global/project resources, rebuild skill disclosure, and
appear after extension commands in both the extension API and RPC `get_commands` response.

## Conformance evidence

`conformance/extract/f11-extension-wiring.ts` imports the pinned upstream permission-gate, pirate,
status-line, and tool wrapper implementations. It generates the exact headless permission result,
pirate prompt replacement, status values, and additive/no-change/removal `addedToolNames` cases.
The Go runner reconstructs those cases through the native APIs and compares canonical JSON. A
separate print/JSON integration test runs all three demos through a faux session, proves the
dangerous bash tool executes zero times, checks the pirate prompt, and verifies clean headless
output in both modes.

`make fixtures-check` regenerated every family from pinned upstream commit
`3da591ab74ab9ab407e72ed882600b2c851fae21` and byte-compared cleanly. `git diff --exit-code --
conformance/fixtures/F3 conformance/fixtures/F3-session` is clean, and both F3 trace families pass
through the full race suite. No dependency was added and no golden was hand-edited. The current
core-only consolidation does not include the historical TUI ancestry or its CJK observations;
Sprint 2 owns that independently.

Sprint 1 still has to expand the red wire surface around session replacement, provider/auth type
unification, runner lifecycle ordering, resource metadata/precedence, and the native-runner gaps
listed in the WP-350 report. The retained matrix is evidence for the implemented seams, not M2.

## Acceptance status

| Criterion | Status | Evidence |
|---|---|---|
| Lifecycle, context, provider, tool, input, user-bash, compaction/tree, resource, command, flag, event-bus, and Exec wire-through | Integrated; parity pending | focused unit/integration suite and pinned F11-wire replay |
| Built-in override and dynamic active/deferred tools | Passed | runtime override integration plus additive/no-change/removal upstream cases |
| Compiled Go discovery order and settings enable/disable | Passed | catalog/settings tests, early CLI flag validation, and `--no-extensions` factory-avoidance test |
| Permission-gate, pirate, and status-line in print/JSON headless modes | Passed | faux-session mode matrix with zero dangerous-tool executions |
| F3 byte preservation and allocation-light unused path | Passed | unchanged F3 fixture directories, F3 tests, and nil extension-state assertion |
| Full `make fixtures-check` | Passed | all generated families byte-match and the F6 TypeScript replay is green |
| Full `go test ./...` and `go test -race ./...` | Passed on plain `main` | full race suite is green before the consolidation commit |
| Linux/Darwin amd64/arm64 pure-Go builds | Passed | `CGO_ENABLED=0 go build ./...` for all four targets |
| `go vet` and golangci-lint | Passed | golangci-lint v2.7.2 reports zero issues |

## Commands run

```text
pwd
git rev-parse --show-toplevel
GOCACHE=$PWD/.tools/cache/go-build go test ./... -count=1
GOCACHE=$PWD/.tools/cache/go-build go test -race ./... -count=1
make fixtures-check
make lint
for target_os in linux darwin; do for target_arch in amd64 arm64; do CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOCACHE=$PWD/.tools/cache/go-build go build ./...; done; done
go mod verify
GOCACHE=$PWD/.tools/cache/go-build go mod tidy -diff
git diff --check
git diff --exit-code -- conformance/fixtures/F3 conformance/fixtures/F3-session
```
