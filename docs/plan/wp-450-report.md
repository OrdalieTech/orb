# WP-450: TUI app assembly

WP-450 ports the interactive application layer from upstream pi `0.80.10` at
`3da591ab74ab9ab407e72ed882600b2c851fae21`. The Go app now assembles the
session transcript, streaming assistant and tool views, editor, dialogs,
autocomplete, status zones, overlays, extension UI, settings, and command
lifecycle over the Phase 4 TUI primitives.

## Delivered behavior

The chat path renders user, assistant, thinking, custom, compaction, branch,
bash, and tool messages, including custom tool renderers and edit diffs. It
honors image blocking and display settings, thinking visibility, cache-miss
notices, output and editor padding, hardware cursor behavior, terminal progress,
clear-on-shrink, queue modes, autocomplete limits, quiet startup, and the
double-Escape action. Text and image clipboard paths use the pure-Go command
fallbacks fixed by D7.

Every command in the WP scope is wired: `/login`, `/logout`, `/model`,
`/scoped-models`, `/settings`, `/resume`, `/new`, `/name`, `/session`, `/tree`,
`/trust`, `/fork`, `/clone`, `/compact`, `/copy`, `/export`, `/import`, `/reload`,
`/hotkeys`, `/changelog`, `/quit`, and the D18 `/share` local export. The
interactive host rebuilds complete runtimes for new, switch, fork, import,
trust, and reload operations. Replacement is teardown-first like upstream
AgentSessionRuntime: the current runtime emits `session_shutdown`, the host
synchronously invalidates its UI, and the runtime is disposed before replacement
creation. Creation, setup, or rebind failures propagate without rollback; a
successful replacement emits `session_start` only after the TUI rebind, with
upstream reasons and target session files.

The settings dialog persists the upstream UI surface rather than maintaining a
display-only copy. It covers auto-compaction, images, skills, cursor and padding,
terminal behavior, steering and follow-up modes, transport, thinking, cache
notices, quiet startup, project trust, double-Escape, tree filtering, and theme.
The config selector reads and writes global and project resource paths. OAuth
selection exposes Anthropic, OpenAI Codex, GitHub Copilot, and xAI, while logout
removes the persisted credential. Login and logout refresh the composed model
registry in place without running session shutdown/start lifecycle or discarding
extension providers. A fresh install starts on the upstream `unknown` model
sentinel, so `/login` is reachable before any credential exists; a successful
first login selects that provider's exact pinned default when it is available.

Startup `--resume` now uses the same full TUI session selector as upstream
instead of a numbered terminal prompt. It loads current-folder and all-project
scopes with progress, preserves threaded/recent/fuzzy ordering, supports fuzzy,
quoted exact, and `re:` searches, filters named sessions, toggles paths, confirms
and removes sessions, honors configured keybindings, and returns select/cancel
through the existing CLI dependency seam. The subsequent missing-CWD selector
remains a separate startup step after the session file is chosen.

The extension `ctx.ui` implementation includes select, confirm, input, editor,
custom overlays, status, widgets, custom header and footer, title, notifications,
working indicators, editor access, terminal input hooks, themes, tool expansion,
and stacked autocomplete providers. The retained update-check integration is a
narrow, non-blocking startup callback with mode-scoped cancellation; updater
policy and transport remain Sprint 4/G4 work.

## Conformance evidence

`conformance/extract/wp450-replay.ts` drives pinned upstream components through
the real `@xterm/headless` virtual terminal and records ten settled transcript
states at widths 52 and 88. The Go replay matches all 20 normalized frames
exactly. The same fixture family covers the upstream status-line,
widget-placement, custom-header, and custom-footer examples, and
`conformance/fixtures/WP450/manifest.json` has an empty divergence ledger.

`conformance/extract/wp450-session-selector.ts` drives the pinned selector and
search module directly. `WP450-session-selector` records loading, scope, sort,
search, named/path toggle, deletion, selection, and cancellation frames with an
empty divergence ledger; the Go component replays those frames through its
public render and input surface.

Session-host tests cover successful teardown-first replacement, post-teardown
creation failure, no-rollback rebind failure, missing-cwd retry, fork/clone/tree
semantics, import, reload, trust persistence, auth option identity/status,
fresh-install login, in-place credential refresh, extension command actions, and
disposal. Lifecycle tests also race notifications against resize and require
the update callback to stop before interactive mode returns.

## Verification

- `make test` passed the complete repository under the race detector, including the interactive
  host, auth lifecycle, startup/session selectors, F12, MCP, and JS bridge packages.
- `make fixtures-check` regenerated every registered fixture byte-identically; the session-selector,
  WP-450 replay, and expanded F12 artifacts remained clean.
- `make upstream-rpc-tests` passed all six pinned files and 27 tests without exclusions.
- `make build` and `make lint` passed; `go mod verify` and `go mod tidy -diff` were clean.
- `CGO_ENABLED=0 go build ./...` passed for Linux and Darwin on amd64 and arm64.

Live OAuth authorization could not be exercised without provider credentials.
Real kitty/iTerm2 image emission and native Darwin/X11/Wayland clipboard smoke
also require those terminal environments and remain explicit Sprint 2/M3 acceptance
work; WP-450's `/copy` binding and deterministic command-path tests are complete.
