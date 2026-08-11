# Changelog

Orb's own release history (independent 0.x semver; upstream parity target recorded per release).
The embedded upstream changelog under `codingagent/modes/assets/` is a product asset driving
`/changelog` and is not this file.

## [Unreleased]

### Added

- The model selector (`/model`) and auth-provider selector (`/login`, `/logout`) are now fully mouse-aware: click selects a row, double-click confirms, and the wheel moves the selection — the same paths the keyboard drives. Hovering a row moves the selection highlight in every selector whose list fits its window (model, auth, session, tree, startup, and extension dialogs); hover reports (any-motion tracking, `1003`) are enabled only while such a selector holds focus and are reverted the moment focus returns to the editor, so normal typing never pays for a motion-event flood and Shift+drag native text selection keeps working everywhere.

### Changed

- `--help` and `--list-models` now answer from a metadata snapshot cache written on every successful extension-host load (sha256-fingerprinted over the SDK, entry files, package trees, lock file, and trust state) instead of spawning a Node child per invocation: `--help` ~284ms → ~12ms and `--list-models` ~290ms → ~27ms on a machine with installed extensions, with byte-identical output. Any fingerprint mismatch, corruption, or native-provider registration falls back to the full spawn, and real sessions always spawn, so staleness self-heals on the next run. An extension whose load-time registrations depend on env or network may show stale flags/models in these two commands until that next run.
- The binary is ~3.2MB smaller and startup leaner: the syntax-highlighting lexer XML corpus, CJK segmentation dictionary, bundled upstream changelog, and generated model catalog now embed gzip-compressed and decode lazily on first use (each keeps its uncompressed source in-repo behind a byte-compare staleness test), websearch's charset decoding drops the CJK legacy-encoding tables (unknown charsets fall through to the existing UTF-8 scrub floor), and ~40 package-level regexp tables defer compilation behind `sync.OnceValue`. `orb --version` 9.2ms → 7.6ms, peak RSS 21.2MB → 19.3MB, init-phase allocations -35%.
- Hot paths allocate far less, identical bytes throughout: streamed Anthropic tool-call turns -67% wall time and -53% memory (streaming JSON is trusted as already normalized, with a fixed-point property test as the permanent gate), a prompt on a 1000-turn session -46% time and -59% memory (the session path is built and deep-cloned once instead of five times, and branch scans no longer clone every entry), session creation -41% memory, TUI steady frames 1,368B/3 allocs → 728B/2, and a cold 1M-line render -50% time.

### Fixed

- `orb --help` no longer connects to configured MCP servers before printing (a slow server could stall help for its full connect timeout); it now takes the same metadata-only path as `--list-models`, and new guard tests pin that `--version` spawns nothing at all.

## [0.4.13] - 2026-08-10

### Changed

- Faster startup and session resume, identical behavior: the syntax-highlighting lexer registry now builds on first highlight instead of at process start (`orb --version` wall time roughly halves, peak RSS drops ~5MB), the built-in model catalog is decoded once per process and merged without redundant deep clones (offline registry construction ~17ms → ~1.5ms, 29k → 4k allocations), and the session/harness JSONL loaders skip redundant validation scans, per-line UTF-8 transforms, and oversized read buffers (1000-turn v3 session load ~37ms → ~28ms, allocations -26%).

- Extensions importing the pi SDK (`@earendil-works/pi-*` or the legacy `@mariozechner/pi-*` names) are now served entirely by Orb's embedded `orb-extension-sdk`: `pi-dynamic-workflows` runs unchanged — child agent sessions, model catalogs, shared-store/web tools, structured output, worktree isolation, background runs, pause/resume, and persisted transcripts included — with zero Pi-SDK code on the machine. The previous on-demand `npm install` of the real SDK is gone, and `ORB_PI_SDK_ROOT` with it.
- An extension that imports a pi SDK surface Orb does not implement now gets a precise diagnostic — `OrbUnsupportedCapability: <package>#<export> is not implemented by orb-extension-sdk <version>; supported exports: …` — at the point of use, instead of a version-dependent resolution or missing-export failure; any import that would reach a real installed pi SDK is refused at load with the full import chain named.

### Fixed

- `orb install npm:<package>` now materializes the package's required peer dependencies natively from the registry (respecting `peerDependenciesMeta.optional`), so extensions that peer-depend on ordinary packages — e.g. `pi-dynamic-workflows` on `typebox` — load instead of failing with "Cannot find package". Upstream pi serves such peers in-process, which Orb's out-of-process extension host cannot; the `@earendil-works/pi-*` / `@mariozechner/pi-*` SDK peers are deliberately never fetched (the embedded orb-extension-sdk serves that surface), and the dependency `npm install` for managed npm packages now passes upstream's peer-resolution opt-out flags (`--legacy-peer-deps` / bun `--omit=peer` / pnpm config) so no package manager materializes a real pi SDK either.
- The bash tool's system-prompt guideline matches upstream v0.84.1's wording ("You can inspect PI_* environment variables…").

### Changed

- Orb's default system prompt now presents it as a general-purpose problem-solving harness for work and software development, while retaining its coding capabilities and pi-compatible prompt assembly.
- The upstream compatibility target is now pi v0.84.1. Harness session repos write the JSONL v4 format (product session files remain v3, matching upstream), JSON/RPC `message_update` events carry only deltas, Gemini-3 tool-call ids and structured Bedrock failure diagnostics ride the provider wire, OpenAI Responses ending incomplete without a provider reason surface as errors, and `/scoped-models` opens its selector from cached models immediately.

### Added

- Orb automatically discovers Agent Skills installed for Claude Code, Codex, OpenCode, Gemini CLI, Cursor, and GitHub Copilot; project skills remain trust-gated and duplicate external copies collapse deterministically.
- Interactive `@` autocomplete now presents loaded skills as clearly badged, themed entries alongside files; accepting one inserts the canonical `/skill:name` command, ready to submit.
- Built-in Baseten and Qwen Token Plan Individual providers, the `scrollbarThumb` theme color, and iTerm image size metadata, matching upstream v0.84.1.
- Mermaid code blocks in assistant and user messages now render as Unicode terminal diagrams (flowchart, sequence, state, class, and ER), with a "Mermaid diagrams" `/settings` toggle (`markdown.mermaid`: off/final/streaming, default streaming).

## [0.4.11] - 2026-08-03

### Fixed

- Provider turns that declare tool use without emitting a call now receive up to three bounded internal retries without persisting the recovery scaffold, and duplicate tool-call IDs are made deterministic before execution so results remain unambiguous.

## [0.4.10] - 2026-07-30

### Changed

- The project’s public identity is now **Orb**: repository and module path `github.com/OrdalieTech/orb`, `orb` executable and release artifacts, `ORB_*` environment variables, and `orb.*` private namespaces. Upstream compatibility names such as `.pi`, `PI_*`, the JavaScript `pi` API, and session/RPC wire formats remain unchanged.
- The upstream compatibility target is now pi v0.83.0. Stored OAuth credentials refresh with less than five minutes remaining, and tool-schema conformance follows TypeBox 1.3.7, including nullable arrays.
- Sending a message snaps the transcript back to the live tail, so a view scrolled up for reading shows the message and its reply. Scrolling away still holds position against streaming frames.
- When enabled, the memory plugin now has one Hermes-inspired behavior for local and SDK users: a frozen, character-bounded `USER PROFILE`/`MEMORY`, model-led consolidation through `replace`, and `remember`/`recall`/`forget` over the existing `memory.Store`. The former injection and shutdown-distillation options were removed.
- Memory plugin instances no longer share one process-wide store lock: each `MemoryWithStore` instance serializes only its own operations, so multi-tenant embedders' stores can serve concurrent queries across instances. Cross-instance and cross-process coherence remains the `memory.Store`'s responsibility, as durable adapters already require.
- Plain `agent.Agent` SDK users can attach the same bounded memory through `memory/agent`; concurrent same-tenant sessions can make compound mutations atomic through `memory.TransactionalStore`.

### Added

- `orb auth print-api-key` and `orb auth print-bearer-token` export configured credentials without mixing diagnostics into stdout; bearer export refreshes tokens to a configurable minimum validity.
- Streaming messages expose the upstream `pending` and raw provider stop reasons, supported provider requests accept an injected HTTP client, OpenRouter login accepts a pasted redirect URL or code, and extensions receive live `ctx.scopedModels`.
- Interactive startup lists file-backed `SYSTEM.md` and `APPEND_SYSTEM.md` inputs alongside project context files, and `ResourceLoader` exposes their source paths to embedders.

### Fixed

- `/model <query>` opens a searchable model picker with all/scoped tabs and selects the best fuzzy match; session replacement and shutdown cancel the picker before tearing down its runtime.
- Provider requests now use Qwen Token Plan thinking controls, Z.AI `max_tokens`, configured Bedrock profiles, GitHub Copilot Claude Opus 5 metadata, and valid OpenAI function arguments when malformed deltas also carry an empty custom payload.
- Active session replacement and tree navigation settle aborted turns, concurrent bash commands all remain cancellable, and RPC bash commands pass through extension `user_bash` handlers.
- Nested linked worktrees no longer load the same context file twice, failed Git package installs clean their partial checkout, tool-output toggles report their state, and image fallbacks shorten, link, and clamp long paths.

## [0.4.9] - 2026-07-28

### Fixed

- Anthropic streams are no longer hard-killed at `timeoutMs` (5 minutes by default): the timeout bounds only the header phase, matching the pinned `@anthropic-ai/sdk` (10-minute default when unset), so long streaming turns complete. Bedrock no longer applies `timeoutMs` as a whole-stream deadline at all (upstream applies none) and can no longer misreport an internal timeout as a user abort, and OpenRouter image generation no longer races the response body read.
- Aborting a stream mid-flight records upstream's `Request was aborted` in every adapter (openai-responses, openai-completions, Azure, Google, Mistral) instead of raw Go error text such as `context canceled`; aborted provider-retry requests and aborted OpenRouter image body reads return upstream's `Request aborted`, and HTTP-error paths no longer leak the header-timeout wrapper's context.
- JS extension callbacks (tools, commands, shortcuts, event handlers, providers, renderers, dialogs, state actions) are no longer capped at 30 seconds; only host startup RPCs stay bounded, matching upstream's untimed awaits. Extension tools and provider streams now receive a live positional `AbortSignal` that fires when the agent-side context is cancelled, extension-registered provider streams reach the agent incrementally instead of buffering the whole response, extensions get the full Node `console` surface, and the Node host exits when its transport closes so it cannot be orphaned by a hard Orb crash. A cancel arriving in the same stdin chunk as its request still aborts it, abandoning a provider stream mid-iteration terminates the host-side generator instead of buffering unboundedly, abort reasons arrive as Error values, undecodable streamed events fail the stream like the buffered path, and the console bridge adds profile/profileEnd/timeStamp/createTask and a constructible `console.Console`.
- `extensions.Exec` sends SIGTERM and only SIGKILLs after a 5-second grace on abort or timeout, letting children trap TERM and clean up, and a successful command that leaves a background grandchild holding the stdio pipes keeps its own exit code after the bounded wait instead of reporting 1.
- The `project_trust` extension event fires during startup project-trust resolution — for sessions and package commands alike — consulted ahead of the trust store and the interactive prompt as upstream does.
- A panic on any TUI-spawned goroutine (render timer, input reader, loader ticker, autocomplete, colour-scheme timers, stdin flush) restores the terminal — cooked mode, main screen, cursor, bracketed paste off, kitty keyboard protocol popped — before printing the panic and exiting 1, instead of leaving the terminal raw with a hidden cursor. A split escape sequence whose second chunk arrives exactly at the stdin flush deadline keeps its full completion window (the same stale-timer guard now covers the capability-negotiation fragment buffer), astral-plane characters echoed after their kitty CSI-u printable report are no longer suppressed, and the crash restore path can no longer deadlock on a mutex the panicking goroutine holds.
- Terminal cell widths match upstream for keycap emoji, East-Asian-Wide text-presentation symbols, and halfwidth voiced sound marks, fixing wrap and truncation drift on lines containing them.
- A 0-byte `auth.json` (for example after a crash between create and first write) self-heals on the next credential write instead of failing every login with `EOF`.
- Session-file writes no longer leave permanent `.jsonl.lock` files: session locking uses the proper-lockfile-compatible directory lock, which self-cleans on release, is stolen when stale, and interoperates with a concurrently running upstream pi.
- Session files with an explicit empty header id keep it on load, `trust.json` keys sort in JS UTF-16 code-unit order, `models-store.json` and the extension provider store are byte-identical to upstream `JSON.stringify(value, null, 2)` instead of HTML-escaping `<`, `>` and `&`, `harness.CompactionResult` wire JSON routes through jsonwire, extension-host OAuth credentials serialize in upstream member order, and `jsonwire.Marshal` emits `0` for negative zero.
- Harness `PrepareCompaction`/`PrepareTreeCompaction` use upstream harness's own cut-point algorithm, while `FindCutPoint`/`PrepareLegacyCompaction` keep the coding-agent algorithm and treat empty-summary `branch_summary` entries as invisible metadata in cut-point selection — while branch summarization still projects them unconditionally — matching upstream on both sides.
- RPC frames with a missing or non-string `type` answer upstream's untyped `Unknown command` response with the id and type echoed in upstream's `JSON.stringify` canonical form, the untyped path honors a pending extension shutdown like every command, and `--mode rpc` with `@file` arguments fails up front with upstream's error instead of silently dropping them.
- Startup `Error:`/`Warning:` diagnostics are coloured red and yellow when stderr is a TTY (`NO_COLOR` and `TERM=dumb` respected), and unstamped dev builds report `orb dev` instead of a stale version number.
- Interrupting a run restores queued messages to the editor instead of discarding them, and the dequeue binding keeps the current draft after them and reports what it restored, matching upstream.
- chat: the Telegram webhook caps request bodies at 1 MB, and image attachments larger than 20 MB fall back to a textual attachment note instead of being base64-inlined into the prompt.

### Added

- Interrupting a turn that has not shown anything yet now takes its prompt back: the branch rewinds to before it and the text returns to the editor, so you can edit and resend instead of leaving a stranded message (the abandoned attempt stays reachable in the session tree). Once any output has appeared, interrupting aborts as before.
- Upstream's `--verbose` flag forces verbose startup instead of being rejected as an unknown option.
- `--export <session> <output>.md` writes the session as portable Markdown; every other output path keeps the HTML export.
- Exported provider constructors such as `providers.GitHubCopilot()` return complete registry metadata (APIs, base URL, env-key lists) instead of a partially populated struct.

## [0.4.8] - 2026-07-27

### Fixed

- JavaScript extension components can use SDK helpers such as `BorderedLoader` without an uninitialized-theme failure, and `ctx.modelRegistry` now resolves request-time credentials through the owning Go context so account-usage extensions no longer report `auth unavailable` for an authenticated provider.
- Extension credential reads no longer rebuild and transmit the full state snapshot; interactive rendering moves Git/provider metadata off the render thread, reuses the editor’s rendered scroll state for border decoration, and caches stable task-widget lines.

## [0.4.7] - 2026-07-27

### Added

- Anthropic simple streams accept an optional upstream client, preserving Orb's tool and reasoning mapping for hosts that use AnthropicVertex or another client-owned transport.
- Embedders can collect `CompleteSimple` directly and use portable `auto`, `none`, or `required` tool choice through `SimpleStreamOptions`; forcing a named tool stays compositionally small by advertising only that tool with `required`.
- Interactive sessions place transient working/retry/compaction status in the built-in editor’s top border when it fits, with a right-aligned, truncated session-name badge; dialogs, scrolled drafts, and extension-provided editors retain the standard status lane and all existing UI/keybinding contracts.
- The optional tasks plugin now keeps its persistent widget and collapsed tool results to a one-row current/progress/queue summary, lets the widget expand or collapse on click with dimmed, inset details, and exposes the full branch-aware list through `/tasks` and Ctrl+O expansion; retry statuses count down, and queued messages use one-row truncation with their count and configured dequeue-key hint.
- 0.82.1 port, first waves: `ANTHROPIC_AUTH_TOKEN` resolves to bearer headers ahead of the other anthropic credentials; `orb login openrouter` (PKCE) and `orb login kimi-coding` (device flow) mint credentials; bash commands see `PI_SESSION_ID`, `PI_SESSION_FILE`, `PI_PROVIDER`, `PI_MODEL` and `PI_REASONING_LEVEL`, and RPC clients receive streamed `bash_execution_update` events; `/models` lists configured-but-missing ids as `[unavailable]` and picks up `models.json` edits on open; custom renderers receive the live `outputPad`; the external editor works out of its own temp directory with the resolved `$VISUAL`/`$EDITOR`/nano chain; DNS failures retry; scroll borders survive narrow terminals; harness paths expand `~` and `file://` and children are reaped on cleanup.

- Embedders can parse and marshal individual harness session-tree entries without constructing a JSONL file.
- Embedders can prepare compaction directly from canonical harness session-tree entries.
- A loose extension that imports the pi SDK without declaring it — the shape upstream permits because pi bundles its SDK — now installs the pinned `@earendil-works/pi-coding-agent` into orb's own npm root automatically on the launch that first needs it, from the npm registry, never from an installed pi. One line announces the install; `ORB_PI_SDK_ROOT`, `PI_OFFLINE` and a missing npm all skip it, leaving the existing guidance message. Extensions that declare their dependencies are untouched.
- `ORB_NODE` names the Node executable to use, for installs no search reaches; `ORB_NODE=none` disables JavaScript extensions.
- TypeScript published inside `node_modules` on Node 22.6-22.12 now reports the file, the running version and the fix instead of failing opaquely.

### Removed

- The staged-entry mechanism and its `packages/` mirror. Supplying transpiled source from the load hook covers the entry as well as its dependencies, so resolution is now exactly what the package manager laid out.

### Fixed

- Provider-side constrained sampling now survives the complete tool path, including Go agent tools and native or JavaScript extensions, instead of being dropped before the provider request.
- `SessionRuntime.ExecuteBash` retains its exact three-argument Go signature while the new `ExecuteBashWithID` carries an RPC correlation id; OpenRouter’s callback server now drains its response before shutdown instead of intermittently resetting the browser connection.
- Upstream fixture extraction validates and normalizes random summary session UUIDs, and the export/shutdown manifests now identify the 0.82.1 sources, restoring deterministic Linux CI.
- SDK auto-provisioning now also covers package-installed extensions: the ecosystem declares the SDK as a peerDependency (pi's bundling satisfied it implicitly), npm does not materialize absent peers, and conflicting peer ranges across installed packages are tolerated with --legacy-peer-deps. Resolvability up the entry's tree is now the only criterion.
- orb no longer resolves the extension SDK from an installed upstream pi. It searched `PATH` for the `pi` executable and pointed extensions at the npm package that owns it, so a machine without pi got a different result from one with it. The SDK is now taken from orb's own managed npm root, project scope first when the project is trusted, then the user scope. Reading pi's configuration files stays supported; borrowing its code does not. `ORB_PI_SDK_ROOT` remains as an explicit override for a checkout or vendored copy.
- JavaScript extensions now run on Node 22.6, where every TypeScript extension previously failed: the module loader returned a string source for a format that release requires a buffer for.
- The extension host no longer dies on Node 26, which removed `--experimental-transform-types`; an unknown flag aborts Node outright rather than warning. The flag set and the TypeScript transpiler options are now taken from what the Node build in hand accepts.
- pnpm-installed extensions resolve their dependencies: `--preserve-symlinks` made a package reached through the `.pnpm` store resolve from the link site instead of the store. The flag existed only for the staged-entry mechanism and is gone with it.
- An extension published as ESM without `"type": "module"` — the shape npm packs by default — no longer fails on `export`.
- Node is found under nvm, fnm, volta, asdf, mise, nodenv, n, Homebrew and system packages even when a spawned process inherits no `node` on `PATH`, and a broken or chatty shim no longer hides a working install behind it. The chosen runtime is added to the extension host's `PATH`.
- TypeScript published inside `node_modules` runs: Node refuses to type-strip any file under that path, which is where every installed extension and its dependencies live.

- JS extensions importing the pi SDK now resolve the surface upstream gives them. Node's type stripping kept type-only imports from package specifiers, so an extension importing a type such as `ApiKeyCredential` failed to load; and `@earendil-works/pi-ai`'s root entry no longer carries the global API, so imports of `complete`, `stream` or `getModel` resolved to a narrower module than upstream's jiti loader provides. The extension host now elides type-only imports from package specifiers and resolves the legacy surface from the same installed copy, leaving pinned installs untouched.
- Bun hosts resolve the SDK specifiers extensions import under their historical names (`@mariozechner/pi-tui`, `@earendil-works/pi-agent-core`, `@earendil-works/pi-ai/compat`), which previously only Node aliased. A package the extension installs itself still wins.
- Bun's implicit auto-install is disabled, so an unresolved import can no longer fetch a package from npm mid-session; dependencies come only from the explicit install step.
- Skill, prompt-template and slash-command frontmatter keeps the trailing newline that YAML clip chomping gives a closing `>` or `|` block scalar, matching upstream. Frontmatter consisting of an empty `---`/`---` block no longer panics.

### Changed

- orb now tracks upstream pi **0.82.1** (`b4f29368`); every item of the 0.81.1 → 0.82.1 delta is ported and the conformance goldens, embedded changelog, model catalog and version identity moved together. Second wave: `Tool.constrainedSampling` with OpenAI custom tool calls and strict/grammar flags across six providers; abortable provider retries owning the SDKs' backoff with an interruptible sleep; models-store ETag revalidation; compaction and branch summaries isolated with `cacheRetention: "none"` and fresh session ids; the Codex `previous_response_not_found` retry; OpenRouter cache breakpoints on tool results; and the catalog's reasoning-level derivation, at full ID-set parity with the published 0.82.1 package.
- Fixture extraction now scrubs the terminal-identity environment (`GHOSTTY_RESOURCES_DIR` alone flipped the theme to truecolor), fixing most of the documented macOS extraction irreproducibility.
- Conformance extraction now runs against upstream 0.82.x: it synthesizes the generated
  `providers/data/.manifest.json` that `providers/all.ts` began importing, writes synthesized
  provider catalogs in the flat or grouped-by-API shape the checked-out revision expects,
  records the Anthropic provider's resolved credential verbatim so a headers-only resolution is
  captured, derives subscription-provider APIs from the provider factory, and supplies the
  session scope the `/models` command now reads. `UPSTREAM.lock` and every committed manifest now
  pin 0.82.1, with Linux CI regenerating the complete fixture tree before release.

## [0.4.6] - 2026-07-25

### Added

- Native mouse support across interactive mode. Click a row in the session tree to select it, click the `⊞`/`⊟` marker to fold or unfold a branch, and double-click to open it. Clicking also works in `/resume`, `/settings`, `/model`, permission prompts and extension selectors — single click highlights, double click confirms — and clicking an autocomplete suggestion accepts it. Click anywhere in the input editor to place the cursor, and the wheel scrolls inside selectors as well as the transcript. Text selection is unchanged: hold shift to drag-select over any clickable surface, and the scrollbar, wheel-detach and `ctrl+end` reattach behave as before. Terminals without SGR mouse reporting stay keyboard-only.

### Fixed

- Web search and page fetching returned nothing for any page larger than roughly 50 KB while still reporting success; readable extraction now keeps block structure so truncation returns the head of the page instead of an empty result.
- `fetch_content` now rejects loopback, private, link-local and unresolvable destinations, and re-validates every redirect against the same rules.
- Permission `path` rules now also match paths named inside a bash command, so a rule that denies a file is no longer bypassed by reading it through the shell.
- Streaming responses no longer accumulate quadratically: a 64 KB tool call is about 28x faster and allocates about 28x less. Emitted bytes are unchanged.
- Lone UTF-16 surrogates split across streaming chunks are preserved instead of being replaced with U+FFFD, so emoji in tool arguments survive chunk boundaries.
- Editing a session label in a terminal narrower than the label no longer panics the renderer.
- The session tree no longer rebuilds in quadratic time; large sessions stay responsive per keystroke.
- `orb -p` no longer exits 0 with empty output when a prompt fails before producing a reply, such as on context overflow.
- Extension host shutdown can no longer block indefinitely when a grandchild process inherited the host's stderr.
- `orb --help` no longer waits for configured MCP servers to connect.
- `@file` completion now uses orb's managed `fd`, so it works without a system `fd` on `PATH`.
- Subagent progress widgets are cleared when a run ends, and a failing parallel child is reported as an error rather than a successful result.
- `recall` falls back to word overlap, so a query no longer has to be a literal substring of a stored memory.
- Web search honours an explicit `provider` in `web-search.json`, decodes non-UTF-8 pages, rejects binary responses, and no longer echoes provider error bodies that can contain an API key.
- Compaction cut-point selection now counts earlier compaction entries, so a second and later compaction retains the same history upstream retains instead of compacting too little and risking overflow.
- Unified patches now number the second and later hunks correctly; multi-edit patches previously reported the wrong new-file start line.
- Serializing a session containing an unprojectable custom message no longer panics.
- A legacy session that is too small to compact now reports "Nothing to compact" instead of failing.
- Tool execution updates are delivered in order and no longer overlap; concurrent delivery could make streaming output visibly regress. Updates still reach the sink without the tool waiting on it.
- Extensions handling `tool_call` and `tool_result` now receive the prepared arguments the tool actually executes, so documented argument rewriting works; previously the edit silently changed only the recorded call.
- `Dispose()` now cancels every `SubscribeChan` it handed out, so a long-running embedder no longer leaks a goroutine per subscription.
- A disposed session refuses further work instead of quietly calling the model and persisting the turn.
- Concurrent settings writes no longer lose updates, and a failed write no longer leaves the in-memory value ahead of what was persisted.
- The chat gateway's `/stop` is bounded by its own worker pool instead of spawning a goroutine per message, and a permanently failing handler now gives up instead of retrying forever.
- The chat gateway reuses a conversation's session manager between messages instead of re-reading and re-parsing the whole session file on every inbound message.
- Disposing a session or reloading plugins while subagents were running could terminate the host process; child work now fails that child instead.
- Parallel subagent runs are capped at 32 children per call, so one tool call can no longer fan out as wide as the model asks.
- A JavaScript extension host that dies with a fatal error no longer leaves the terminal staggered; its output is line-normalised while stderr is a terminal.
- `orb install` now declares the package it installed in the install root's `package.json`. Packages are still fetched natively, so npm remains optional, but an undeclared entry looked extraneous to npm and the next `pi install` deleted it while `settings.json` still listed it.
- `--list-models` now reports settings errors instead of silently listing against defaults when `settings.json` fails to parse.
- Compacting a disposed session is refused instead of summarizing through the model and rewriting history.
- Refreshing the model catalog left a zero-byte `models-store.json.lock` file behind, which permanently broke `pi update --models` for a coexisting upstream pi: upstream locks with `proper-lockfile`, which creates the lock as a directory and treats any existing path as held. Orb now uses the same directory protocol, removes the lock on release, and reclaims a leftover file from an older Orb release. Delete a stale `~/.pi/agent/models-store.json.lock` once to recover.
- Token costs recorded in sessions could differ from upstream by one unit in the last place in release builds. The compiler fused a multiply and an add on arm64, which the race-instrumented test builds did not, so the shipped binary wrote values the test suite never saw. `make check` now also verifies the byte-compared surfaces in the shipped build shape.

### Changed

- Bundled plugin tools now describe every schema property and declare their required arguments.
- Task list state moved into tool-result details, matching upstream's `todo` extension, so it survives resume and stays correct across branches.
- `make fixtures-check` runs the reciprocal TypeScript-reads-Go gates before the fixture diff; a fixture difference previously aborted the target and skipped them silently.

## [0.4.5] - 2026-07-25

### Fixed

- Terminal shutdown now stops the session picker's reader and restores keyboard mode on the same screen where it was enabled, preventing frozen input and CSI-u leakage into the shell on macOS.
- JavaScript extension reloads no longer block the host reader, change the parent terminal mode through inherited stderr, or crash on late stale-context UI events.
- Stale autocomplete results can no longer rewrite a complete slash command such as `/plugins` when Enter is pressed.
- Double-Escape now opens the session tree at the current leaf with pi-compatible search, filters, paging, branch folding, copy, and labels.
- Node caches compiled extension modules between runs, reducing repeat startup time with JavaScript extensions enabled.

## [0.4.4] - 2026-07-24

### Fixed

- Extension hosts shut down with Orb instead of becoming orphaned and crashing later with `EPIPE`.
- Large sessions remain available to extensions without duplicating the transcript across bridge snapshots, and stale asynchronous extension actions are isolated instead of panicking Orb.

## [0.4.3] - 2026-07-23

### Fixed

- Extensions retain access to hoisted transitive Node dependencies after staging.
- Typeless TypeScript extensions start quietly, and the custom-model example uses the current upstream shape.

## [0.4.2] - 2026-07-23

### Fixed

- Extensions can reuse SDK packages from an installed `pi`; incompatible extensions remain isolated warnings.

## [0.4.1] - 2026-07-23

### Fixed

- Loading frames reuse incremental session totals; Ctrl-C exits from an empty editor, and quit drains Ghostty/Kitty key releases before returning to the shell.
- The session tree keeps linear conversations flat and adds indentation and connectors only where branches exist.

## [0.4.0] - 2026-07-23

### Changed

- The memory SDK moved from `codingagent/memory` to root-level `memory` before external adoption, changing its import path to `github.com/OrdalieTech/orb/memory`.

### Fixed

- Memory distillation now derives provider authentication through the model registry and cannot block session shutdown beyond 30 seconds.
- OpenRouter Anthropic caching now anchors the latest tool result and enables cache controls for the `~anthropic/*-latest` aliases.

## [0.3.4] - 2026-07-23

### Added

- An Orb-original `codingagent/memory.Store` SDK seam ships with an append-only, locked JSONL file store and optional semantic-search interface.
- A fifth bundled-but-dormant memory plugin adds `remember`/`recall`, bounded startup index injection, opt-in session distillation, and custom-store injection.

### Changed

- Mouse selection keeps scrollbar drags captured and double-clicks copy the visible sentence.

## [0.3.3] - 2026-07-23

### Added

- Left-dragging in interactive mode highlights visible text, holds the viewport stable during streaming, and copies the selection on release.

## [0.3.2] - 2026-07-23

### Changed

- Interactive mode collapses the idle/working spacer and adds a one-column clickable scroll thumb.
- `orb update` now reports installed package versions dynamically and `--extensions` names every package that changed.

## [0.3.1] - 2026-07-22

### Changed

- `orb update` now reports whether the running release is current before showing reinstall instructions.

### Fixed

- Permission `path` rules canonicalize both the rule pattern and the candidate path, so rules on
  symlinked locations (e.g. macOS `/tmp`) match reliably.
- Extension-host dialog cancellations arriving before the handler registers are preserved instead
  of dropped, removing an intermittent hang in custom-component flows.

## [0.3.0] - 2026-07-22

### Added

- A bundled-but-dormant tasks plugin adds the `todo` tool and live session checklist, enabled through settings, `orb plugins`, or `/plugins`.
- A bundled-but-dormant websearch plugin adds Exa, Brave, and Tavily search plus lightweight HTML/text fetching.
- A bundled-but-dormant subagents plugin adds injectable in-process scout, worker, and reviewer child sessions with bounded parallel execution.
- A bundled-but-dormant permissions plugin adds last-match-wins allow, deny, and ask rules with permissive audit-only defaults and inherited subagent policy.
- `orb chat <platform>` runs every built-in chat adapter through one durable CLI gateway system.
- An out-of-process extension host runs the full JavaScript/TypeScript extension API through a
  local Node.js or Bun process, including providers, UI callbacks, state synchronization, package
  dependency materialization, and the PATH-to-orb compatibility shim.

### Changed

- JavaScript and TypeScript extensions now require local Node.js ≥22.6 or Bun. Without either
  runtime, orb reports one clear diagnostic while skills, prompt templates, MCP servers, and
  built-in tools continue to work.
- Interactive mode now keeps the status, extension widgets, input, and footer fixed at the bottom
  while the transcript scrolls independently. Mouse-wheel or `Ctrl+PageUp` scrolling pauses live
  follow, and scrolling down or pressing `Ctrl+End` returns to the latest loading or streamed output.
- Huge transcripts now cache stable message layout and render only the visible window plus a changed
  tail, keeping loading and streaming frame cost independent of conversation length after warm-up.

### Fixed

- Subagent children now inherit request authentication from the parent model registry and surface
  provider stream errors instead of reporting an empty final response.

### Removed

- The embedded Sobek JavaScript engine, esbuild transpiler, Node compatibility shims, vendored
  TypeBox runtime, and their bridge-only conformance fixtures.

## [0.2.1] - 2026-07-22

### Fixed

- `terminal.clearOnShrink` now erases vacated visible rows with the differential renderer instead
  of clearing and replaying the terminal, so streamed responses no longer destroy scrollback when
  their Markdown or loading layout becomes shorter.

## [0.1.3] - 2026-07-22

### Fixed

- `orb update --extensions` now reconciles installed Git packages pinned to abbreviated commit
  IDs from the existing clone instead of passing the abbreviation as an invalid remote fetch ref.
- Live TUI redraws disable xterm-compatible scroll-on-output mode while Orb is running, so
  supporting terminals keep a user's scrollback position during loading and streamed responses.

## [0.1.2] - 2026-07-22

### Added

- A reproducible public-extension compatibility harness locks the 44 most-downloaded valid Pi
  packages, compares stable load and registration behavior against Pi 0.81.1, audits each primary
  workflow, and measures seven offline command handlers plus Piolium's knowledge-base workflow.

### Changed

- Synchronized the complete in-scope upstream target to pi 0.81.1: compaction and branch-summary
  retries with lifecycle events, the restored default stream fallback, deferred interactive model
  refresh, Kimi K3 compatibility metadata, and regenerated Gemini catalogs and conformance fixtures.
- Releases now include a checksummed deterministic source archive that CI rebuilds before publish;
  the Homebrew publisher uses GoReleaser's current cask configuration.

### Fixed

- `--no-extensions` now disables discovery while preserving explicit `-e` extensions, and the
  upstream `--theme`/`--no-themes` resource-selection flags are available.
- JS extensions can import `buffer`/`node:buffer`, append transcript streams with
  `fs.createWriteStream`, resolve the pi-ai root as the upstream compat superset, and use
  `import.meta.dirname`/`filename` to locate bundled resources from the package directory.
- Popular extensions can use common `fs` realpath/copy/remove/access APIs, their promise
  counterparts, and synchronous argument-safe child processes through `execFileSync`.
- OpenAI and Azure Responses requests now match the pinned SDK's ten-minute header timeout and
  `X-Stainless-Timeout` wire format, while Codex error fallbacks stringify parsed events and drop
  non-string event types like upstream.
- Bedrock payload hooks preserve a deleted `inferenceConfig`, Vertex ADC reports unknown metadata
  detection modes verbatim, and Anthropic's subscription warning survives OAuth refresh failures.
- Model generation now requires an explicit NVIDIA NIM listing instead of an invented fallback,
  while concurrent and cancelled remote-catalog refreshes preserve upstream cache semantics.
- The extension runtime now supports Piolium child sessions and its filesystem, streaming decode,
  and cancellation workflows, alongside the Node/SDK surfaces used by more ecosystem packages.
- Bundled dependencies now keep module-local `import.meta` paths, Node-compatible UID scoping, and
  `Buffer.byteLength`; this restores exact `pi-subagents /subagents-doctor` discovery of all eight
  shipped agents and Piolium's bounded file-reading workflow.

## [0.1.1] - 2026-07-21

### Fixed

- JS extensions and bundled dependencies can import the Node process built-in as `process` or
  `node:process`; both resolve to the existing process global.

## [0.1.0] - 2026-07-21

### Added

- Current upstream SDK surface: image-model registry and OpenRouter catalog, typed RPC client,
  public retry/overflow and skill-block helpers, custom-theme HTML export, and notify-only update
  checks with orb and upstream version identity.
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
  modules fail with explicit "not supported by the orb extension runtime" diagnostics.
- jsbridge pi-* module surface: `@earendil-works/pi-ai` exports `EventStream`,
  `AssistantMessageEventStream`, `createAssistantMessageEventStream` (upstream
  `utils/event-stream.ts` port) and `calculateCost`; `pi-coding-agent` exports `getAgentDir`,
  `getMarkdownTheme`, `VERSION`, `parseFrontmatter`/`stripFrontmatter`; `pi-tui` exports the
  full `Key` builder and `isKeyRelease`. Unknown imports from the pi-* shims now fail at first
  touch with a clear "not exported" error instead of resolving `undefined` and breaking later.
- Extensions from installed pi packages load in every session (`orb install` now delivers its
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
- Renamed the repository, Go module, release artifacts, and CLI to `orb`, so it installs beside
  upstream `pi`; `orb update` now prints exact installer and Go routes.
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
- SECURITY: `orb --help` and unknown-flag invocations no longer load untrusted project settings.
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
the M5 trim checklist (retired).

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
  and Node-style `execSync` errors; superseded by `docs/sync/ecosystem-extension-matrix.md`.
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
