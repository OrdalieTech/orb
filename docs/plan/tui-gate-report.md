# Sprint 2 — TUI core parity gate report

This Sprint-0 consolidation preserves the completed core F12 work from the
historical WP-460 scratch checkout on plain `main`, against pinned upstream
`3da591ab74ab9ab407e72ed882600b2c851fae21`. It closes the overlay,
terminal-color, primitive-composite, stress, performance, raw-write-log, and
broad ICU/CJK fixture surfaces. It does not close Sprint 2 or M3 because the
application-level TS pi versus pi-go frame replay and manual terminal checks
remain separate acceptance gates.

## Core overlay parity

`tui/overlay.go` ports the pinned `packages/tui/src/tui.ts` overlay types and
behavior: all nine anchors, per-edge and uniform margins, absolute and
percentage sizes and positions, offsets, height clamping, responsive
visibility, permanent removal and temporary hiding, focus-order stacking,
non-capturing overlays, explicit focus/unfocus targets, and the upstream
blocked-focus restoration state machine. Composition runs against the bottom
terminal viewport, reserves terminal height even when base content is short or
all overlays are hidden, and disables clear-on-shrink while any overlay entry
exists, matching the upstream renderer's historical-area rule.

Go's scheduled renderer is concurrent where upstream JavaScript is
single-threaded. `Container` child storage is private and accessed through
locked snapshots, lifecycle state no longer shares the render callback lock,
and a forced render invalidates and serializes any scheduled callback. That
keeps child replacement, invalidation-triggered render requests, and visual
order deterministic under `-race` without exposing a mutation path around the
lock.

The line compositor uses grapheme cell widths while preserving ANSI/OSC state.
Focused tests port the pinned short-content, CJK boundary, tab-width, style
leakage, declared-width truncation, stacking, responsive visibility,
non-capturing focus, and cursor-marker regressions. They also cover the
upstream quirk that `hideOverlay` removes the most recently created overlay
while visual precedence follows the independently updated focus order.

`conformance/extract/f12-core.ts` independently executes the pinned TypeScript
renderer and generates 45 complete synchronized-output frames, all 44 named
`overlay-non-capturing.test.ts` state-machine traces, and one cursor-lifecycle
trace. Generation parses the pinned focus test names and fails unless the trace
list is an exact ordered match; it likewise maps all 24 named
`overlay-options.test.ts` cases to generated frames. The data-driven Go replay
uses the same small operation language for creation, focus, unfocus, explicit
targets including nil, hide/unhide/removal, responsive flags, input handlers,
deferred work, base replacement, observations, and frontmost render probes.
There is no per-case Go implementation switch.

The exact frames additionally cover all nine anchors, object and uniform
margins, offsets and edge clamps, absolute and percentage row/column
precedence, absolute and percentage maximum heights, requested render widths,
short and long base content, multiple-overlay stacking, both CJK boundary
placements, complex ANSI/OSC base and overlay content, no-overlay and overlay
style resets, and the physical-row tab regression. Focused ordinary tests port
the remaining CJK segment and tab-helper assertions byte-for-byte.

## Terminal-color parity

`tui/terminal_colors.go` ports strict OSC 11 framing and background parsing for
six- and twelve-digit hex plus `rgb:`/`rgba:` channel forms with upstream
scaling. It also ports `CSI ? 997 ; 1/2 n` scheme parsing, OSC 11 and scheme
query bytes and timeouts, color-scheme listeners, and `CSI ? 2031 h/l`
notification enable/disable sequences.

Color replies are consumed before ordinary input listeners, cell-size parsing,
debug handling, or focused-component dispatch. Concurrent OSC 11 queries keep
their FIFO reply slots after timeout, so a late reply is consumed by the timed
out query rather than resolving the next query. Scheme notification dispatch
iterates the live registration order like upstream's `Set`, so a listener
removed by an earlier callback is skipped and a listener added by that callback
receives the same report. The generated
`terminal-colors.json` golden records 10 OSC parser inputs, five scheme-parser
inputs, all five named upstream OSC 11 query cases, the separate late-reply
FIFO trace, scheme query/timeout/concurrent-listener behavior including live
listener mutation, and notification idempotence plus stop cleanup. The Go
conformance test replays query bytes, results, listener order, input
consumption, focus dispatch, and late-report behavior against the public port.
Notification state transitions, lifecycle checks, and terminal writes share a
separate serializer so concurrent enable/disable or stop calls cannot emit a
stale final terminal mode.

## Primitive composite and performance evidence

The retained `full-screen.json` is honestly a deterministic primitive
composite. Its extractor executes upstream `Container`, `Text`, `Markdown`,
`Editor`, `Spacer`, `TruncatedText`, and `TUI` code to render the same
chat-shaped editor/status tree at widths 100, 72, 48, and 32. It does not
execute `packages/tui/test/chat-simple.ts`, so that file has been removed from
F12 provenance. The Go runner compares every ANSI byte and trailing space and
checks every visible line against the terminal width.

The retained frame-budget gate builds and renders fresh trees in 11 batches of
six corpus passes and requires the p90 batch average to stay below 16 ms/frame.
The first pass recorded 229.089–283.475 microseconds/frame p90 in ordinary
runs and 1.274977 ms/frame under `-race` on the development Ryzen 5 PRO 3600.
Those numbers describe the primitive composite, not the still-missing
application replay.

Approximate steady full-frame benchmark on the same Ryzen 5 PRO 3600 (120×40;
TypeScript median versus Go benchmark mean):

| Lines | pi TypeScript | pigo | Speedup |
| ---: | ---: | ---: | ---: |
| 100,000 | 32.147 ms | 0.091 ms | ~350× |
| 1,000,000 | 345.166 ms | 0.075 ms | ~4,600× |

Reproduce with `go test ./tui -run '^$' -bench BenchmarkViewportHugeHistory -benchmem` and
`node --import ./.upstream/node_modules/tsx/dist/loader.mjs conformance/benchmarks/tui_huge_history.ts`.
The scroll thumb costs one allocation and about 160 bytes per full frame.
Worst-case cold hydration with one unique component per line is 35 ms/23 MiB at 100k and
441 ms/249 MiB at 1M (`-bench BenchmarkWindowedContainerColdUniqueHistory -benchtime=1x`).

## Stress and terminal-write evidence

The retained deterministic stress suite covers resize storms, ordered editor
paste floods, chunk-split stdin paste floods, 100,000-character logical lines,
and 4,096-line differential tail updates. The four fuzz targets retain ASCII,
CJK/emoji, and giant-line seeds. `PI_TUI_WRITE_LOG` remains implemented in
`ProcessTerminal`: every raw write is appended to the configured file, or to
an upstream-shaped timestamped file when the setting names a directory, and
logging failures remain non-fatal.

The validated pure-Go ICU 78.2 implementation remains in
`internal/cjksegment`. Its F12 word-navigation corpus is now the union of the
scratch checkout's broad every-UTF-16-offset corpus and main's focused
dictionary-rule regressions: 266 generated cases. Regeneration exposed and
closed the upstream quirk where moving backward from a UTF-16 cursor inside a
supplementary code point first lands at that code point's leading surrogate.
Dictionary provenance and the exact ICU license live beside the embedded data.

## Historical scratch-pass verification

The source scratch checkout recorded these focused checks before consolidation:

- `go test -race ./tui ./conformance/runner -count=1` — `tui` passed in
  17.486 s and `conformance/runner` passed in 5.165 s;
- `CGO_ENABLED=0 go test ./tui ./conformance/runner -count=1`;
- `CGO_ENABLED=0 go test ./conformance/runner -run '^TestF12' -count=1`;
- `make lint` — `go vet` and golangci-lint reported zero issues;
- a second direct pinned F12 generation into a fresh temporary directory
  produced an empty recursive diff against `conformance/fixtures/F12`.

It also recorded these full gates on that independent pre-integration tree:

- `make fixtures` followed by `make fixtures-check`, including the F6 and auth
  TypeScript cross-read checks;
- `make test` (`go test -race ./...`), including the overlay/color replay and
  resize, paste, stdin-chunking, and giant-output stress tests;
- the 44-case focus replay passed 20 consecutive race-detector runs after its
  terminal-stream frontmost probe was made order-aware;
- all four fuzz targets under `-race`, including direct concurrent resize plus
  paste and chunked-stdin paste, with ASCII, CJK/emoji, and giant-line seeds;
- `make build lint`, `go mod verify`, `go mod tidy -diff`, formatting, and
  diff-hygiene checks;
- `CGO_ENABLED=0 go build ./...` for linux and darwin on amd64 and arm64;
- the frame-budget gate at 200.545 microseconds/frame p90 over 264 timed frames,
  plus a 100-iteration benchmark at 174,572 ns/op on the development host.

F12 was regenerated directly through `generateF12` at the pinned checkout;
goldens were not hand-edited. Those historical numbers are retained as
provenance, not claimed as current mainline evidence.

## Plain-main integration evidence

- Direct pinned `generateF12` regeneration into a fresh temporary directory
  produced an empty recursive diff against `conformance/fixtures/F12`.
- The generated overlay, terminal-color, and full-screen goldens are
  byte-identical to the scratch source; the word-navigation golden is the
  deliberate 266-case superset described above.
- `CGO_ENABLED=0 go test ./tui ./conformance/runner ./codingagent/modes -count=1`
  is green after the compatibility adapter and surrogate-boundary fix.
- The focused overlay/color/full-screen/CJK/stress selection passed five
  consecutive pure-Go runs; `tui` completed in 58.140 s and the conformance
  runner in 0.529 s.
- `CGO_ENABLED=0 go test ./... -count=1` is green with loopback-enabled test
  execution; the sandbox-only attempt failed solely because it denied
  `httptest` listener creation.
- `CGO_ENABLED=0 go build` and `CGO_ENABLED=0 go vet` are green for `tui`,
  `conformance/runner`, `codingagent/modes`, and `internal/cjksegment`.

## Remaining gate and ledger status

The WP-460 divergence-ledger delta is zero. The existing Darwin native
modifier-addon gap remains unchanged, with Kitty/CSI-u as the supported path.
The integrated interactive layer is present, but its full application-level
replay, command/ctx.ui acceptance, and manual terminal verification remain
scheduled work, not accepted divergences.

The remaining gate is Sprint 2's application-level side-by-side replay,
followed by resize and paste race/fuzz verification under the project-wide
gate and manual kitty, iTerm2, clipboard, and image checks. Until those pass
and every replay deviation is fixed or ledgered, M3 remains open.
