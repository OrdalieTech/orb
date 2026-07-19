# Changelog

pi-go's own release history (independent 0.x semver; upstream parity target recorded per release).
The embedded upstream changelog under `codingagent/modes/assets/` is a product asset driving
`/changelog` and is not this file.

## [Unreleased]

### Added

- Full TUI parity with upstream pi 0.80.10: components, application frames, all interactive
  commands, `ctx.ui` lifecycle, themes, terminal images, clipboard command paths (M3).
- Headless parity: print/JSON/RPC modes, upstream RPC suite compatibility, eight provider API
  shapes, Anthropic/ChatGPT-Codex/Copilot/xAI OAuth flows, MCP client, packages and project trust,
  JS extension bridge runtime with non-UI API and node shims (M1–M2 plus consolidated expansion).

- JS extension bridge `ctx.ui`: dialogs (select/confirm/input/editor), notifications, status,
  widgets, footer/header factories, hidden-thinking label, working indicator and message, title,
  theme access and switching, tools-expanded state, autocomplete providers, and AbortController —
  seventeen more upstream single-file examples run unmodified.
- JS extension bridge custom UI (gate G3): `ctx.ui.custom` with overlay options and
  `OverlayHandle`, focusable components, `setEditorComponent`/`getEditorComponent`, and the
  `CustomEditor` base class backed by the real built-in editor — modal-editor and six more
  custom-UI examples wired.
- JS extension bridge example matrix (M4): 61 of the 69 upstream single-file extension examples
  (88%) run unmodified — pi-tui `Text`/`Box`/`Container`/`Spacer`/`Loader`/`CancellableLoader`
  component classes, `BorderedLoader`/`DynamicBorder`, `convertToLlm`/`serializeConversation`,
  truncation utilities, `CONFIG_DIR_NAME`, a `node:readline` shim, live message/entry renderers,
  and Node-style `execSync` errors; full status in `docs/sync/extension-matrix.md`.
- JS extensions load in the product: settings-configured and project extension paths plus the new
  `--extension`/`-e` flag route through the bridge loader into the shared registry; `/reload`
  rebuilds changed bundles and replaces per-path VMs.
- OpenRouter image-generation client (`openrouter-images` API shape): non-streaming Chat
  Completions request with image/text modalities, data-URL result decoding, and the `ai/api`
  `GenerateImages` dispatch entry point.
- SDK parity helpers mirroring upstream exports: `tools.NewCodingTools`/`NewReadOnlyTools`
  bundles and public `ai.CalculateCost`, `ai.SupportedThinkingLevels`, `ai.ClampThinkingLevel`,
  `ai.ModelsAreEqual`, `ai.HasAPI` (private duplicates removed).
- `settings.httpProxy` is honored: exported as HTTP(S)_PROXY for pi-managed clients unless the
  environment already sets them (upstream http-dispatcher semantics).
- Release machinery: goreleaser config for linux/darwin × amd64/arm64 with ldflags-injected
  version, a tag-triggered release workflow that re-runs the full gate and extracts notes from
  this changelog, a checksum-verifying curl install script, and CI running `make check` on every
  push. Update checks remain notify-only (gate G4 resolved).
- README newcomer path: install, first session, SDK embedding, and running upstream extensions.

### Changed

- Conformance extraction is environment-independent (COLORTERM pinned, deterministic fixture cwd).
