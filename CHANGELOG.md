# Changelog

pigo's own release history (independent 0.x semver; upstream parity target recorded per release).
The embedded upstream changelog under `codingagent/modes/assets/` is a product asset driving
`/changelog` and is not this file.

## [Unreleased]

### Fixed

- `--no-extensions` now disables discovery while preserving explicit `-e` extensions, and the
  upstream `--theme`/`--no-themes` resource-selection flags are available.
- JS extensions can import `buffer`/`node:buffer`, append transcript streams with
  `fs.createWriteStream`, resolve the pi-ai root as the upstream compat superset, and use
  `import.meta.dirname`/`filename` to locate bundled resources from the package directory.

## [0.1.1] - 2026-07-21

### Fixed

- JS extensions and bundled dependencies can import the Node process built-in as `process` or
  `node:process`; both resolve to the existing process global.

## [0.1.0] - 2026-07-21

### Added

- Current upstream SDK surface: image-model registry and OpenRouter catalog, typed RPC client,
  public retry/overflow and skill-block helpers, custom-theme HTML export, and notify-only update
  checks with pigo and upstream version identity.
- Release hardening: immutable CI action SHAs, fixture regeneration at tag time, strict changelog
  notes, clean-macOS checksum support, and a 754 KB amd64 linker-alignment reduction.
- Upstream pi 0.80.10 sync to `3a40794e`: tool-result and summary usage accounting, Qwen Token
  Plan and refreshed provider catalogs, deferred model refresh with upstream's offline quirk,
  public text and UUIDv7 helpers, RPC thinking levels, editor paste history, cursor cleanup, and
  regenerated conformance fixtures.
- Chat gateway wave 2: stdlib-only Slack, Teams, Discord, Messenger, and Google Chat adapters,
  plus shared RFC 6455 and Meta Graph webhook helpers.
- jsbridge Node compatibility for real ecosystem extensions: `node:crypto` (randomUUID,
  randomBytes, createHash/createHmac with hex/base64/base64url digests), `node:http`/`node:https`
  (minimal server + client over Go net/http), `node:module` `createRequire`, and the
  `atob`/`btoa`/`TextDecoder`/`structuredClone` globals; fs shim errors are Node-shaped
  (`code`/`errno`/`syscall`/`path`, so `err.code === "ENOENT"` idioms work); `import.meta.url`
  is defined per bundle as the entry's `file://` URL; `.node` native addons and WebAssembly
  modules fail with explicit "not supported by the pigo extension runtime" diagnostics.
- jsbridge pi-* module surface: `@earendil-works/pi-ai` exports `EventStream`,
  `AssistantMessageEventStream`, `createAssistantMessageEventStream` (upstream
  `utils/event-stream.ts` port) and `calculateCost`; `pi-coding-agent` exports `getAgentDir`,
  `getMarkdownTheme`, `VERSION`, `parseFrontmatter`/`stripFrontmatter`; `pi-tui` exports the
  full `Key` builder and `isKeyRelease`. Unknown imports from the pi-* shims now fail at first
  touch with a clear "not exported" error instead of resolving `undefined` and breaking later.
- Extensions from installed pi packages load in every session (`pigo install` now delivers its
  main payload), and `-e npm:<pkg>` / `-e git:<repo>` performs upstream's temporary-install
  resolution instead of treating the spec as a literal path. npm/git package dependencies are
  installed through the settings `npmCommand` (default `npm install --omit=dev`), skipped when
  deps are absent or bundled, with a warning instead of a failure when npm is missing. The npm
  registry honors `npm_config_registry`, project and user `.npmrc` `registry=` lines, and
  nerf-darted `_authToken` bearer auth.
- Interactive extension shortcuts: `pi.registerShortcut` handlers now dispatch on keypress
  (matched before built-in keybindings, reserved bindings still win with a stored diagnostic),
  mirroring upstream interactive-mode dispatch and insertion order.
- RPC extension UI: the extension UI bridge is bound on every session rebind, so
  `extension_ui_request` events (notify, dialogs, status, widgets) stream to RPC clients and
  `ctx.hasUI` is true, matching upstream rpc-mode. MCP: `"disabled": true` on a server entry is
  honored as a disable switch (config portability from other MCP clients); one invalid
  `mcpServers` entry no longer disables the rest (per-entry warnings); explicit `maxRetries: 0`
  disables streamable-HTTP reconnect retries; startup connects run concurrently per server.

### Changed

- Synchronized the behavioral target to upstream pi 0.81.0 (`9c480b6a`): required stream injection,
  retained-tail session APIs, split public/coding compaction contracts, refreshed model and image
  catalogs, strict catalog validation, product assets, actions, and regenerated conformance goldens.
- Model generation now intersects NVIDIA NIM and consumes the live OpenRouter and Vercel catalogs;
  runtime catalog freshness follows upstream's `checkedAt`/`lastModified` rules.
- Interactive login now auto-opens OAuth URLs, uses the searchable fuzzy selector, reports exact
  completion/default-model outcomes, and warns once for Anthropic subscription extra usage.
- Renamed the repository, Go module, release artifacts, and CLI from `pi-go`/`pi` to `pigo`, so it
  installs beside upstream `pi`; `pigo update` now prints exact installer and Go routes.
- Releases, CI, and `go install` now pin Go 1.26.5. On identical source, the in-memory 1,000-turn
  Processor core and F12 renderer are each 2.8% faster; no-prompt startup is 1.7% slower, minimal
  session creation is 4.8% slower, and the stripped Linux binary is 0.9% larger than Go 1.25.0.

### Fixed

- Closed 52 provider, catalog, and login parity gaps, including Codex consumer cancellation and
  zstd transport, OpenAI/Azure timeout and pricing behavior, lossless unknown pi-message events,
  Bedrock payload hooks, Mistral streamed arguments, Cloudflare auth, and OAuth credential wire data.
- Turn refresh now carries prompt, tools, model, and thinking changes into the next provider call;
  custom and branch-summary entries count toward compaction; model/thinking mutations share
  persistence and extension events; provider-header hooks run before affinity headers.
- CI now pins the signed Node 24 `actions/checkout` v7.0.1 commit instead of the deprecated
  Node 20 action runtime.
- Hosted macOS verification now handles APFS realpath, case, and Unicode normalization without
  weakening Linux coverage; interactive session replacement is race-free and custom extension
  messages request their render deterministically.
- Session entry IDs no longer copy the complete ID index before every append, removing quadratic
  allocation growth from long sessions while preserving collision handling.
- Interactive history renders skill invocations as the upstream collapsible skill block plus an
  optional separate user message instead of exposing the raw `<skill>` envelope.
- Long-session compaction checks now walk directly from the active leaf to the latest compaction,
  avoiding a full cloned branch on every turn; the retained 20,000-entry benchmark is allocation-free.
- Resource discovery now deduplicates canonical paths in linear time and reuses package metadata,
  cutting minimal agent-session creation from about 49 ms to 32 ms on a 25-skill install.
- Chat gateway hot paths allocate less and wake only the worker needed, with wire, authentication,
  Unicode, recovery, and per-conversation ordering behavior unchanged.
- `make test` and the fixture race checks explicitly enable CGo for Go's development-only race
  runtime, so an inherited `CGO_ENABLED=0` no longer prevents the gate from starting; every product
  and release build remains static with CGo disabled.
- RPC state responses can no longer overtake the prompt acknowledgement that initiated a session
  replacement, while extension UI replies remain live during that replacement.
- Chat wave-2 transport hardening: WebSocket message limits cannot overflow,
  Slack file tokens stay on Slack hosts, Google Chat JWKS refreshes and
  per-space writes are throttled, Discord reconnect/heartbeat state is
  bounded per connection, and Teams conversation state is bounded.
- SECURITY: `pigo --help` and unknown-flag invocations no longer load untrusted project settings.
  Previously those paths constructed settings without the project-trust gate, so an untrusted
  project's `mcpServers` could execute arbitrary commands and make network requests from the
  most innocuous invocations.
- RPC mode dispatches extension commands (`/mcp`, ...) before model/API-key preflight, matching
  upstream agent-session ordering — MCP diagnostics work on keyless installs.
- Extension factories ran twice per startup (duplicated side effects); the resource loader now
  adopts the pre-loaded registry once and only `Fresh()`es on real reloads.
- MCP tools survive session registry rebinds: re-running the MCP extension factory re-registers
  discovered tools on the new API instead of silently dropping all of them; `Start()` failures
  surface as warnings; child exit statuses no longer report as `session_shutdown` extension
  errors; a tool call failing with EOF deactivates that server's tools immediately.
- Interactive `/reload` leaked ~16 MB per reload (previous jsbridge loader VMs were never
  closed); RSS now plateaus.
- `registerEntryRenderer` receives the full custom session entry (`entry.data` works) instead
  of the bare data payload; `ctx.compact()` `onComplete`/`onError` fire even when the
  dispatching event's context is gone.
- Skills parity edges: nested ignore-file basename patterns scope to the ignore file's own
  directory and root-anchored `/patterns` match at any depth (upstream npm-ignore semantics,
  bug-for-bug); non-string frontmatter `name`/`description` reject the skill with upstream's
  type-error warning shape; collision diagnostics trail all warnings; headless (`-p`/RPC) runs
  no longer print per-skill validation warnings (interactive keeps them, with paths).
- `--list-models` creates the full runtime so extension-registered providers appear (but skips
  MCP servers, which contribute tools not models, so model enumeration no longer spawns and
  connects them); `--help` documents `--extension/-e` and the package subcommands; package git
  operations are quiet (`-q`, no detached-HEAD advice).
- RPC extensions see a live `ctx.ui` on `session_start`: the session defers its start until the
  RPC extension UI is bound, so startup `notify`/`setTitle`/`setWidget`/`setStatus` calls reach
  the client instead of firing against the headless noop UI.
- Ported upstream's `docs/providers.md` and `docs/models.md`, which the "No API key found"
  guidance and the system prompt reference; the guidance falls back to the hosted copies when no
  docs directory ships next to the binary.

- Streaming TUI flicker: long/streaming bash tool output is no longer rendered uncapped, which
  had pushed the block above the viewport and forced a full-screen clear (ESC[2J) on every
  streaming update (measured ~192 full clears over 260 tool-delta frames). Collapsed tool output
  now shows a bounded preview of the last visual lines with an "(N earlier lines, … to expand)"
  hint, mirroring upstream's bash renderer; `!` bash-mode output caps while still running, not
  only when complete. Ported upstream's `truncateToVisualLines`; guarded by a renderer-level test
  asserting zero full-screen clears during in-viewport streaming, plus a WP450 byte-parity golden.
  The concurrent tool-component render race (torn frames during rebuild) was fixed separately.

Full-parity port of upstream pi v0.80.10 (`3a40794e`). Release candidate: every locally
provable M1–M5 criterion is green; the owner-gated verification remainder is listed in
`docs/trim/M5.md`.

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
- `/session` shows upstream's full cost panel: cached/uncached prompt split, per-model cost
  breakdown (`provider/responseModel`, sorted by cost), and "Cache Re-billed" totals from the
  ported cache-stats arithmetic (upstream unit cases included).
- `/settings` gains upstream's "HTTP idle timeout" entry (30 sec/1 min/2 min/5 min/disabled),
  persisted to `httpIdleTimeoutMs` and applied to the next request.
- `/export` HTML pre-renders custom extension tool calls/results through their TUI renderers
  with upstream's ANSI-to-HTML conversion, and embeds the active tool list.
- opencode models send `x-opencode-session`/`x-opencode-client` session-affinity headers on
  every request; the per-request stream session id now also reaches providers from the CLI
  runtime path (prompt-cache keys and affinity headers for Anthropic/OpenAI/Mistral/Codex).
- Tool headers `~`-shorten home paths and emit OSC 8 `file://` hyperlinks in terminals that
  support them (upstream render-utils).
- Six upstream numbered regression tests ported: message_end cost override (3982), explicit
  provider retry guidance (6019), pending tool renders surviving chat rebuilds (4167),
  session_start render/notify ordering (5943), queued extension slash follow-ups staying raw
  text (2023), and the extension factory cache (bundle cached, factories re-run).
- Typed per-tool event accessors in `codingagent/extensions` (`BashToolCall`/`BashToolResult`
  through `LsToolCall`/`LsToolResult`) — the Go analog of upstream's `isBashToolResult`-family
  type guards over the tool_call/tool_result union.
- `ai.ParseStreamingJSON` exports the streaming tool-call argument parser publicly, matching
  pi-ai's `parseStreamingJson` index export (delegates to the internal partial-JSON port).
- Extension UI kit exports from `codingagent/modes`: `ExtensionSelectorComponent`,
  `ExtensionInputComponent`, `ExtensionEditorComponent` (with constructors) and the
  `KeyText`/`KeyHint`/`RawKeyHint` hint helpers from upstream's "UI components for extensions"
  index block.

### Fixed

- Legacy app-scoped keybinding names (`interrupt`, `expandTools`, `tree`, ...) now migrate to
  their namespaced ids when `keybindings.json` loads, completing upstream's
  `KEYBINDING_NAME_MIGRATIONS` table; previously only the `tui.*` names migrated.

- Footer shows `detached` on a detached HEAD (was the literal `HEAD`), matching upstream's
  footer-data-provider.
- Live extension custom messages (`display: true`) render in the interactive transcript as they
  arrive; previously only the rebuild-from-entries path showed them.
- Selector lists use upstream's select-list palette (accent selection, muted descriptions);
  the previous unknown `selectedText` color crashed once a real theme was active.

### Changed

- Conformance extraction is environment-independent (COLORTERM pinned, deterministic fixture cwd).
