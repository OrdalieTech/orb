# Upstream single-file extension example matrix (F11-jsbridge)

Status of every upstream example in `packages/coding-agent/examples/extensions/*.ts`
(pinned commit in `UPSTREAM.lock`) against the pi-go JS extension bridge.

**runs-unmodified** means the example file is copied verbatim into
`conformance/fixtures/F11-jsbridge/` by `conformance/extract/f11-jsbridge.ts`, loads through the
bridge, registers its surface, and has a behavior test in `codingagent/extensions/jsbridge/`
(`bindings_test.go`, `ui_bindings_test.go`, `ui_custom_test.go`, `examples_wave3_test.go`).
**unsupported** rows state the missing surface.

## Count

| Status | Count |
| --- | --- |
| runs-unmodified | 61 / 69 (88%) |
| unsupported | 8 / 69 |

M4 criterion: ≥80% (56+) running unmodified — met.

## Matrix

| Example | Status | Needed |
| --- | --- | --- |
| auto-commit-on-exit | runs-unmodified | `session_shutdown`, `pi.exec` git, `sessionManager.getEntries`, `ctx.ui.notify` |
| bash-spawn-hook | unsupported | `createBashTool(cwd, { spawnHook })` — built-in tools are native Go and not exported as JS tool factories |
| bookmark | runs-unmodified | `pi.setLabel`, `sessionManager.getEntries/getLabel` |
| border-status-editor | runs-unmodified | `CustomEditor` base class over the built-in editor |
| built-in-tool-renderer | unsupported | `createBashTool/createEditTool/createReadTool/createWriteTool` factories to wrap with custom renderers — not exported to JS |
| claude-rules | runs-unmodified | `node:fs` shim, `before_agent_start` prompt append |
| commands | runs-unmodified | `pi.getCommands`, command argument completions |
| confirm-destructive | runs-unmodified | `ctx.ui.confirm/select`, `session_before_switch/fork` cancel results |
| custom-compaction | runs-unmodified | `session_before_compact` preparation payload, `convertToLlm` + `serializeConversation` module exports, `modelRegistry.find/getApiKeyAndHeaders`, compat `complete` |
| custom-footer | runs-unmodified | `ctx.ui.setFooter` factory with footer data provider |
| custom-header | runs-unmodified | `ctx.ui.setHeader` factory |
| dirty-repo-guard | runs-unmodified | `pi.exec` git status on `session_start`, `ctx.ui.notify` |
| dynamic-tools | runs-unmodified | `registerTool` at runtime (session events + commands) |
| entry-renderer | runs-unmodified | `pi.registerEntryRenderer` bridged to real components, `pi.appendEntry`, pi-tui `Box`/`Text` classes |
| event-bus | runs-unmodified | `pi.events` on/emit across extensions |
| file-trigger | runs-unmodified | `fs.watch` shim, `pi.sendUserMessage` |
| git-checkpoint | runs-unmodified | `pi.exec` git, tool/turn events, `ctx.ui.select` |
| git-merge-and-resolve | runs-unmodified | `node:readline` `createInterface` + async iteration shim, `fs.createReadStream`, `pi.exec` git, follow-up `sendUserMessage` |
| github-issue-autocomplete | runs-unmodified | `ctx.ui.addAutocompleteProvider`, `fetch` shim |
| handoff | runs-unmodified | `convertToLlm`, `serializeConversation`, `BorderedLoader` component, `ctx.ui.custom/editor`, `ctx.newSession` |
| hello | runs-unmodified | `registerTool` with typebox schema |
| hidden-thinking-label | runs-unmodified | `ctx.ui.setHiddenThinkingLabel` |
| inline-bash | runs-unmodified | `input` event transform, `pi.exec` with timeout |
| input-transform | runs-unmodified | `input` event continue/transform/handled + source filter |
| input-transform-streaming | runs-unmodified | `input` event `streamingBehavior` (steer fast-path) |
| interactive-shell | runs-unmodified | `ctx.ui.custom` with terminal input |
| kimi-deferred-tools | runs-unmodified | `registerTool` promptSnippet, `get/setActiveTools` |
| mac-system-theme | runs-unmodified | `process.platform`, `child_process` shim, `ctx.ui.setTheme` |
| message-renderer | runs-unmodified | `pi.registerMessageRenderer` bridged to real components, `pi.sendMessage`, pi-tui `Box`/`Text` classes |
| minimal-mode | unsupported | all seven `create*Tool` factories (bash/edit/find/grep/ls/read/write) — not exported to JS |
| modal-editor | runs-unmodified | `CustomEditor` base class, modal keymap over built-in editor |
| model-status | runs-unmodified | `ctx.ui.setStatus`, `ctx.model` |
| notify | runs-unmodified | `agent_end`, `process.stdout.write` OSC escape passthrough |
| overlay-qa-tests | unsupported | pi-tui `Input` component class and the overlay QA harness surface — not bridged |
| overlay-test | runs-unmodified | `ctx.ui.custom` with `overlay: true`, `CURSOR_MARKER`, `matchesKey`, `visibleWidth` |
| permission-gate | runs-unmodified | `tool_call` gating, `ctx.ui.confirm` |
| pirate | runs-unmodified | `registerCommand`, `before_agent_start` systemPrompt override |
| preset | unsupported | pi-tui `SelectList`/`Key` interactive classes and `getAgentDir` — interactive select surface not bridged |
| project-trust | runs-unmodified | `project_trust` event, trust dialogs |
| prompt-customizer | runs-unmodified | `before_agent_start` `systemPromptOptions` (selectedTools, skills, appendSystemPrompt) |
| protected-paths | runs-unmodified | `tool_call` block results |
| provider-payload | runs-unmodified | `before_provider_request`/`after_provider_response`, `CONFIG_DIR_NAME` export, `node:fs` appendFileSync |
| qna | runs-unmodified | `BorderedLoader`, `ctx.ui.custom/setEditorText`, `sessionManager.getBranch`, `modelRegistry` auth |
| question | unsupported | pi-tui `Editor` component class (full editor as embeddable JS component) and `Key` — not bridged |
| questionnaire | unsupported | same as question: pi-tui `Editor`/`Key` classes |
| rainbow-editor | runs-unmodified | `CustomEditor` render decoration |
| reload-runtime | runs-unmodified | `ctx.reload`, tool-queued `sendUserMessage` follow-up |
| rpc-demo | runs-unmodified | `ctx.ui` dialog suite (select/confirm/input/editor) |
| send-user-message | runs-unmodified | `pi.sendUserMessage` |
| session-name | runs-unmodified | `pi.set/getSessionName` |
| shutdown-command | runs-unmodified | `ctx.shutdown` from commands and tools, tool `onUpdate` |
| snake | runs-unmodified | `ctx.ui.custom` game loop (timers + input) |
| space-invaders | runs-unmodified | `ctx.ui.custom` game loop (timers + input) |
| ssh | unsupported | `create*Tool` factories with custom `BashOperations`/`ReadOperations`/`EditOperations`/`WriteOperations` injection — not exported to JS |
| status-line | runs-unmodified | `ctx.ui.setStatus` across turn events |
| structured-output | runs-unmodified | `registerTool` with `terminate` result |
| summarize | runs-unmodified | compat `complete`, `modelRegistry` auth, session entries |
| system-prompt-header | runs-unmodified | `ctx.getSystemPrompt`, `ctx.ui.setStatus` |
| tic-tac-toe | runs-unmodified | pi-tui `Text` class, `matchesKey/truncateToWidth/visibleWidth`, `StringEnum`, bridged message renderers, `ctx.ui.custom`, `executionMode: "sequential"` tools |
| timed-confirm | runs-unmodified | `ctx.ui.confirm` with timers |
| titlebar-spinner | runs-unmodified | `ctx.ui.setTitle` with timers |
| todo | runs-unmodified | `registerTool` CRUD with render hooks carried |
| tool-override | runs-unmodified | `registerTool` override-by-name |
| tools | runs-unmodified | `ctx.ui.custom` tool picker |
| trigger-compact | runs-unmodified | `ctx.getContextUsage`, `ctx.compact` with onComplete/onError |
| truncated-tool | runs-unmodified | `DEFAULT_MAX_LINES/DEFAULT_MAX_BYTES/formatSize/truncateHead/withFileMutationQueue` exports, pi-tui `Text`, `child_process.execSync` with `err.status`, `fs/promises` mkdtemp |
| widget-placement | runs-unmodified | `ctx.ui.setWidget` placements |
| working-indicator | runs-unmodified | `ctx.ui.setWorkingIndicator` |
| working-message-test | runs-unmodified | `ctx.ui.setWorkingMessage/Visible` |

## Unsupported summary

All eight unsupported examples need one of two surfaces the bridge does not expose:

1. **Built-in tool factories** (`createBashTool`, `createEditTool`, `createReadTool`,
   `createWriteTool`, `createFindTool`, `createGrepTool`, `createLsTool`) as JS values with
   overridable operations/spawn hooks: bash-spawn-hook, built-in-tool-renderer, minimal-mode, ssh.
   pi-go's built-in tools are native Go; wrapping them as JS-callable factories with faithful
   operations injection is a dedicated work package.
2. **Interactive pi-tui component classes** (`Editor`, `SelectList`, `Input`, `Key`): preset,
   question, questionnaire, overlay-qa-tests. These are full keyboard-driven components; the
   bridge currently exports the render-only classes (`Text`, `Box`, `Container`, `Spacer`,
   `Loader`, `CancellableLoader`) plus `DynamicBorder`/`BorderedLoader`.
