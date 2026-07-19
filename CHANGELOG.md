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

### Changed

- Conformance extraction is environment-independent (COLORTERM pinned, deterministic fixture cwd).
