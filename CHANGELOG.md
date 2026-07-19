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

### Changed

- Conformance extraction is environment-independent (COLORTERM pinned, deterministic fixture cwd).
