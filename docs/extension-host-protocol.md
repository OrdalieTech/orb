# pigo extension-host protocol

The extension host is a local Node.js or Bun process owned by pigo. It interprets JavaScript and
TypeScript extension modules, while pigo remains responsible for the agent loop, tools, sessions,
authentication, UI, and the Go extension registry. The host has no direct relationship with the
upstream `pi` executable.

## Transport and framing

The protocol is UTF-8 JSON Lines over the child process's standard streams:

- pigo writes frames to the host's stdin.
- the host writes frames to stdout.
- each frame is one JSON object followed by a single LF byte; embedded newlines are JSON escapes.
- stderr is unstructured diagnostics and is never parsed as protocol data.
- either peer may have multiple requests in flight, and responses may arrive in any order.
- a frame may be at most 4 MiB, excluding its terminating LF. A larger or malformed frame is a
  protocol error and terminates that host generation.

Every frame uses this envelope:

```json
{
  "protocol": "pigo-extension-host",
  "version": 1,
  "kind": "request",
  "id": "pigo-4",
  "method": "execute_tool",
  "params": {}
}
```

`kind` is `request`, `response`, or `event`. Requests require a non-empty `id`, `method`, and
object-shaped `params`. Responses repeat the request id and contain exactly one of `result` or
`error`. Events have a `method` and `params`, but no id and no response. Request ids are opaque;
pigo currently prefixes its ids with `pigo-` and the host uses `host-` so logs remain readable.

Errors have a stable machine-readable code and a human-readable message:

```json
{
  "protocol": "pigo-extension-host",
  "version": 1,
  "kind": "response",
  "id": "pigo-4",
  "error": {
    "code": "extension_error",
    "message": "tool failed",
    "data": { "stack": "..." }
  }
}
```

Unknown object fields must be ignored. An unknown request method receives `method_not_found`; an
unknown event is ignored. This lets later phases add UI, provider, and session-state messages
without changing version 1. A peer must reject a different major `version` during the handshake.

## Handshake and loading

The first stdout frame must be a host-to-pigo `handshake` request. Nothing else may be sent until
pigo answers it.

```json
{"protocol":"pigo-extension-host","version":1,"kind":"request","id":"host-1","method":"handshake","params":{"runtime":{"name":"node","version":"24.15.0"},"capabilities":["tool_updates"]}}
```

The response supplies stable extension ids, entry paths, and agent information. Paths are absolute
filesystem paths. `path` remains the source identity used in diagnostics and registry metadata;
Node entries under `node_modules` may also carry an internal `runtimePath` symlink outside that
directory because Node refuses native TypeScript stripping there. `agent` is deliberately additive
so later capabilities can extend the state snapshot without changing the envelope.

```json
{"protocol":"pigo-extension-host","version":1,"kind":"response","id":"host-1","result":{"extensionEntries":[{"id":"ext-1","path":"/work/ext.mjs"}],"agent":{"name":"pigo","version":"dev","cwd":"/work","agentDir":"/home/me/.pi/agent"},"capabilities":["tool_updates"]}}
```

After the handshake, pigo sends one `load_extension` request per entry, in entry-list order. The
host uses a real dynamic `import()` and awaits the module's default factory. Registrations made by
that factory are host-to-pigo requests described below. A successful load response is:

```json
{"protocol":"pigo-extension-host","version":1,"kind":"response","id":"pigo-1","result":{"extensionId":"ext-1","path":"/work/ext.mjs","loaded":true}}
```

A missing default factory, import failure, or factory failure returns an `extension_load_error`
response for that entry. Pigo continues with later entries, so one bad extension cannot prevent
the others from loading.

Local `.ts` files and package entrypoints are imported without rewriting the source tree. Bun runs
TypeScript directly. Node 22.6 or newer uses native type stripping; the host's loader resolves
extensionless TypeScript imports, `.js` specifiers backed by `.ts` files, package `#imports`, and
TypeScript dependency exports. Package entries are staged through symlinks outside `node_modules`
because Node refuses to strip TypeScript below that directory. An extension's declared SDK version
wins, while unresolved peer SDK imports bind to the coding-agent SDK family installed with the
package; the legacy `@sinclair/typebox` name binds to the current `typebox` SDK for upstream schema
compatibility.

Runtime discovery prefers Node when it is at least 22.6 and otherwise tries Bun. If neither is
available, pigo emits exactly `JS extensions require Node.js ≥22.6 or Bun; skills, prompt templates,
MCP servers and built-in tools work without it` and continues loading non-JavaScript resources.

## Host-to-pigo registrations

Registrations are requests because pigo must validate them before the extension is reported as
loaded. The synchronous `pi.registerTool()`, `pi.registerCommand()`, and `pi.on()` stubs collect
registrations while the factory runs; after the awaited factory completes, the host flushes those
requests in call order. Every registration includes the `extensionId` assigned during the
handshake.

Registrations made after the factory returns are sent immediately in the same call order. Tool,
command, shortcut, event, provider, renderer, event-bus, and flag callbacks await that registration
tail before their enclosing callback response completes, so the native registry observes dynamic
changes before processing the next agent action.

`register_tool` carries the serializable part of the upstream tool definition:

```json
{
  "extensionId": "ext-1",
  "definition": {
    "name": "hello",
    "label": "Hello",
    "description": "Greet someone",
    "promptSnippet": "Greet a person",
    "promptGuidelines": ["Use hello for greetings."],
    "parameters": {"type":"object","properties":{"name":{"type":"string"}},"required":["name"]},
    "renderShell": "default",
    "executionMode": "parallel"
  }
}
```

The host retains the JavaScript `execute` callback. Pigo installs a normal Go
`extensions.ToolDefinition` whose execution closure sends `execute_tool` back to the host.
`prepareArguments` remains a separately inventoried compatibility surface.

`register_command` carries `extensionId`, `name`, and an options object containing `description`.
The host retains the handler; argument completion is outside Phase 1. Pigo installs a normal
`extensions.Command` whose handler sends `execute_command`.

`subscribe_event` carries `extensionId`, a host-generated `subscriptionId`, and the upstream event
name. Each call to `pi.on()` creates a distinct subscription, preserving handler registration
order. Pigo installs an ordinary registry handler which sends `emit_event` to that subscription.

`register_renderer` carries `extensionId`, `kind` (`message` or `entry`), and `customType`. Pigo
registers a native renderer closure. Invoking it sends `create_registered_renderer_component` with
the value, expanded state, and theme snapshot; the returned generation-scoped handle is used by
`render_registered_renderer_component` and `dispose_registered_renderer_component`. Both the
renderer and its component's `render(width)` remain synchronous from the extension's perspective.

Each accepted registration response is `{ "accepted": true }`. Invalid definitions return
`invalid_registration`, which fails only the extension currently loading.

## Pigo-to-host execution

`execute_tool` is a request with `extensionId`, `toolName`, `toolCallId`, arbitrary JSON `params`,
and an additive `context` object. The host calls the retained tool callback with an AbortSignal,
an update function, and a Phase-1 context containing `cwd`, `mode`, and `hasUI`.

Each `onUpdate` call emits a fire-and-forget `tool_update` event whose `requestId` is the enclosing
`execute_tool` request id and whose `partial` value is an upstream-shaped tool result. The final
tool result is the request response. Updates received after the final response are ignored.

`execute_command` is a request with `extensionId`, `commandName`, `arguments`, and the same
additive context object. Phase 1 command context exposes `cwd`, `mode`, and `hasUI`; UI and session
actions are Phase 2. A successful command returns `{ "completed": true }`.

`emit_event` is a request with `extensionId`, `subscriptionId`, `event`, `payload`, and `context`.
The host awaits the subscribed handler and returns `{ "value": <handler result> }`; an omitted or
undefined handler result is represented by an omitted `value`. This request form preserves the
existing Go registry's ordered middleware and return-value semantics. Later non-blocking observer
events may use the same method as an event frame when no result is required.

## Diagnostics, shutdown, and process generations

`log` is a host-to-pigo event with `level` (`debug`, `info`, `warn`, or `error`), `message`, and an
optional `extensionId`. Raw runtime diagnostics still go to stderr.

For clean shutdown pigo sends a `shutdown` request. The host stops accepting new work, answers
`{ "stopped": true }`, closes stdin processing, and exits. Pigo closes or kills the child if it
does not exit within the shutdown timeout.

An unexpected EOF or child exit fails every in-flight request for that process generation. Pigo
may start a fresh generation after backoff, repeat the handshake, and reload the complete entry
list. Registrations are generation-scoped inside the host; the stable Go wrappers address an
extension and registration by id/name, so callbacks automatically target the fresh process.
Explicit reload uses the same restart path with zero backoff and never reuses module state.

## Phase 2 additions

Version 1 reserves additive request methods for `ui_request`, `register_provider`, and
`state_snapshot`/state actions. They use the existing bidirectional request correlation, so dialogs
can be initiated by the host and answered asynchronously while tool requests remain in flight.
New capabilities are advertised in the handshake; absence of a capability means the caller must
not use its methods. No Phase 1 field changes shape when these surfaces are added.

## Phase 2b: extension UI

Phase 2b advertises the `ui` capability. Every pigo-to-host callback context adds an opaque
`uiContextId` and a `ui` snapshot while retaining the Phase-1 `cwd`, `mode`, and `hasUI` fields.
The id binds calls back to the exact native `extensions.Context`; it is valid only until the
enclosing tool, command, or event callback settles. The snapshot makes the synchronous UI reads
local to the host: editor text, tool-expansion state, available theme metadata, and resolved theme
styling functions. Mutations update the host snapshot before being forwarded.

The host uses `ui_request` in both frame directions. Dialogs and custom takeover are request frames
because the JavaScript caller awaits a result:

```json
{"kind":"request","id":"host-9","method":"ui_request","params":{"extensionId":"ext-1","contextId":"ui-4","method":"select","title":"Choose","options":["one","two"],"timeout":5000}}
```

`select`, `input`, and `editor` respond with `{ "value": "..." }` or `{ "cancelled": true }`;
`confirm` responds with `{ "confirmed": true }`. The `timeout` value is milliseconds and is passed
through the native `DialogOptions`. A host generation ending while a dialog is pending cancels its
native context with `UIDialogCancellationError{Reason: "host_restarted"}`; a response still
possible on the old transport uses error code `ui_cancelled`.

Fire-and-forget UI mutations are `event` frames with method `ui_request`. Their `params.method` and
payloads are:

- `notify`: `message`, optional `notifyType`.
- `setStatus`: `statusKey`, optional `statusText`; omission clears it.
- `setWorkingMessage`: optional `text`; omission restores the default.
- `setWorkingVisible`: `visible`.
- `setWorkingIndicator`: optional `workingIndicator` with `frames` and `intervalMs`.
- `setHiddenThinkingLabel`: optional `text`.
- `setWidget`: `widgetKey`, optional `widgetLines` or `factoryHandle`, and optional
  `widgetPlacement`; omitting both content fields clears the widget.
- `setFooter`, `setHeader`, and `setEditorComponent`: optional `factoryHandle`; omission clears the
  replacement.
- `setTitle`, `pasteToEditor`, `setEditorText`, `setTheme`, and `setToolsExpanded` carry `title`,
  `text`, `text`, `themeName`, and `expanded`, respectively.
- `onTerminalInput` and `unsubscribeTerminalInput` carry `handlerHandle`.
- `cancelDialog` carries the correlated host `requestId` when a JavaScript `AbortSignal` fires.
- `addAutocompleteProvider` carries `factoryHandle`.
- `custom_done`, `component_request_render`, and `overlay_action` carry the associated
  `componentHandle` plus their result or action fields.

The host implements `getEditorText`, `getEditorComponent`, `theme`, `getAllThemes`, `getTheme`,
`setTheme`, and `getToolsExpanded` synchronously from its context-local snapshot and retained
factory identity. `pasteToEditor` remains a distinct native call, so TUI paste handling is
preserved; RPC mode may still apply its existing `set_editor_text` degradation. All mutations and
dialogs terminate at the normal `extensions.UI` interface, so TUI, RPC, JSON, and print modes keep
their native behavior rather than acquiring a second mode-specific implementation.

### Component handles and pushed rendering

Component factories are retained in the host under generation-scoped `factoryHandle` values.
Custom takeover adds a `componentHandle` and `customOptions`; pigo invokes the retained factory by
sending a correlated `ui_component_event` request with `event: "mount"`, the live terminal size,
theme snapshot, keybindings snapshot, and footer data when applicable. The same request method
carries keyboard, focus, and editor operations (`input`, `focus`, `set_text`, terminal input, and
editor autocomplete attachment). The mount response reports whether the component handles input,
tracks `focused`, or requests key-release events so the proxy implements the matching native TUI
interfaces. Render and dispose notifications use event frames, because pigo never waits
synchronously for JavaScript rendering.

Rendering is push-based. Calling the Go component's `Render(width)` returns the latest cached line
array and emits a debounced pigo-to-host `ui_component_event` with `event: "render"`. The host calls
the extension's synchronous `render(width)` and pushes the new lines back as:

```json
{"kind":"event","method":"ui_component_render","params":{"componentHandle":"ext-1-ui-component-3","lines":["counter: 2"],"width":80}}
```

Pigo replaces the cache and invalidates the native UI host. Input handlers schedule the same
pushed render after they run. `dispose` is generation-scoped and idempotent. Overlay creation sends
`event: "overlay_handle"`; the JavaScript proxy forwards hide/show/focus/unfocus as `overlay_action`
mutations while retaining synchronous hidden/focused state locally.

Autocomplete factories use correlated `ui_autocomplete` requests. Mounting returns trigger
characters; later `getSuggestions`, `applyCompletion`, and `shouldTriggerFileCompletion` calls
carry the upstream line/cursor/item shapes and return the corresponding upstream result shapes.
There is no partial-response or component-stream frame in Phase 2b: component updates are complete
line-array replacements, and dialogs still have exactly one response.

## Phase 2a — provider registration and auth callbacks

Peers advertise this surface with the `providers` capability. Provider messages are additive to
version 1: a peer without the capability continues to use every Phase 1 message unchanged.

The host sends `register_provider` after an extension calls either `pi.registerProvider(provider)`
or `pi.registerProvider(id, config)`. The `provider.kind` discriminator is `native` for the first
overload and `config` for the second. A native registration carries its static identity, endpoint,
headers, current model list, auth display metadata, and opaque generation-scoped callback handles:

```json
{
  "extensionId": "ext-1",
  "provider": {
    "kind": "native",
    "id": "example",
    "name": "Example",
    "baseUrl": "https://example.invalid/v1",
    "headers": {"X-Client":"pigo"},
    "models": [{"id":"example-1","name":"Example 1","api":"openai-responses","provider":"example","baseUrl":"https://example.invalid/v1","reasoning":false,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":8192,"maxTokens":2048}],
    "auth": {
      "apiKey": {"name":"Example API key","resolve":"ext-1-provider-1","login":"ext-1-provider-2","check":"ext-1-provider-3"},
      "oauth": {"name":"Example OAuth","login":"ext-1-provider-4","refresh":"ext-1-provider-5","toAuth":"ext-1-provider-6"}
    },
    "stream":"ext-1-provider-7",
    "streamSimple":"ext-1-provider-8"
  }
}
```

For `kind:"config"`, `provider.config` contains only the fields defined by the extension:
`name`, `baseUrl`, `apiKey`, `api`, `headers`, `authHeader`, `models`, and an optional retained
`streamSimple` handle. Its `defined` object records presence separately from zero values so Go can
apply the existing `ProviderConfig` merge semantics. Pigo validates either form and responds with
`{"accepted":true}` before the extension load completes.

Pigo invokes a retained callable with `provider_invoke`. `handle` is opaque and valid only for the
current process generation; `extensionId`, `providerId`, and `method` prevent a handle from being
used for another registration. `method` is one of `apiKey.resolve`, `apiKey.login`, `apiKey.check`,
`oauth.login`, `oauth.refresh`, `oauth.toAuth`, `stream`, or `streamSimple`.

```json
{
  "extensionId":"ext-1",
  "providerId":"example",
  "handle":"ext-1-provider-1",
  "method":"apiKey.resolve",
  "invocationId":"provider-invoke-12",
  "args":{"credential":{"type":"api_key","key":"secret"}}
}
```

The response is `{"present":false}` for JavaScript `undefined`, or
`{"present":true,"value":...}` for any other result. Stream callbacks are consumed as async
iterators by the host and return `value.events` in upstream event order; each element has the
normal `AssistantMessageEvent` shape. The enclosing request uses the manager request deadline, so
provider calls cannot outlive the configured host timeout.

Auth callbacks can call back into Go while `provider_invoke` is pending. The host emits a
`provider_interaction` event with the invocation id, a call id, and one of these operations:

- `env` with `{name}` and `fileExists` with `{path}` implement `AuthContext`.
- `prompt` carries an upstream `AuthPrompt` and waits for entered text or a selected option id.
- `notify` carries an upstream `AuthEvent`, including `auth_url`, and has no response.

Pigo answers the request-like event through the same event channel with
`provider_interaction_result`: `{callId,present,value}` on success, or `{callId,error}` on failure.
This keeps paste/manual-code prompts bidirectional without blocking the JSONL reader, while auth
URL and progress notifications remain fire-and-forget.

Extension exceptions use the normal protocol error envelope. The host includes the JavaScript
message and stack in `error.data.stack`; pigo reports both as an extension diagnostic and returns a
non-retryable `ProviderInvokeError`. If the process exits or restarts while an invocation is in
flight, pigo returns a retryable `ProviderInvokeError`. A replacement generation reloads all
extensions and re-sends `register_provider`; the Go wrapper resolves the fresh handle by stable
extension/provider/method identity immediately before every later invocation, so re-registration
is idempotent and callers do not retain stale generation handles.

## Phase 2c — synchronous state mirrors and runtime actions

Peers advertise this additive version 1 surface with `state_v1`. The handshake result includes a
`stateSnapshot` containing the stable agent defaults available before any extension factory runs:
registered flag values, session name, active and available tools, commands, thinking level, agent
context, readonly session data, and readonly model-registry data. Each loaded extension owns a
local copy. Synchronous JavaScript getters read only that copy; they never issue a request.

Pigo replaces an extension's copy before every tool, command, or event callback, and after a
successful runtime mutation, with this event:

```json
{"kind":"event","method":"state_delta","params":{"extensionId":"ext-1","stateSnapshot":{"flags":{"trace":true},"sessionName":"review","activeTools":["read"],"allTools":[],"commands":[],"thinkingLevel":"low","context":{"cwd":"/work","mode":"print","hasUI":false,"model":null,"idle":true,"projectTrusted":true,"hasPendingMessages":false,"contextUsage":null,"systemPrompt":""},"session":null,"modelRegistry":null}}}
```

The delta is a complete replacement, not a patch. A host keeps registered flag defaults when an
early runtime snapshot cannot yet resolve them. Successful `setSessionName`, `setActiveTools`,
`setThinkingLevel`, and label mutations also update the local mirror optimistically, so the void
upstream facade remains synchronous while the authoritative delta is in flight.

`register_flag` carries `{extensionId,definition}` and is processed with tool, command, and event
registrations during extension loading. Runtime operations use a correlated `state_action`
request with `{extensionId,action,args}`. Actions are `send_message`, `send_user_message`,
`append_entry`, `set_session_name`, `set_label`, `exec`, `set_active_tools`, `set_model`,
`set_thinking_level`, `abort`, `shutdown`, and `compact`. The `exec` response is the upstream
`{stdout,stderr,code,killed}` object; `set_model` returns its boolean selection result. State
actions are serialized per extension, which preserves JavaScript call order even though void
facades do not await their acknowledgements. An exec with an `AbortSignal` carries a generation-
scoped operation id; `state_exec_cancel` cancels the corresponding Go process context and the exec
response reports `killed:true`.

The event bus uses `event_bus_subscribe`, `event_bus_unsubscribe`, and `event_bus_emit` host
requests. Pigo delivers cross-extension values with a correlated `event_bus_dispatch` request.
The source extension invokes its own listeners immediately and is suppressed from the Go-bus
echo, so each listener runs once. Subscription ids and retained callbacks are generation-scoped.

Event callback responses now include the handler's post-callback `payload` alongside optional
`value`. Pigo copies mutable `tool_call.input` and `before_provider_headers.headers` objects back
before decoding the return value, then decodes every modifying or veto result into its typed Go
form. A `user_bash` result containing JavaScript operations replaces the function with an opaque
`hostOperationId`; later `execute_bash_operation` requests invoke it, `tool_update` events stream
base64 data for that request id, and the terminal response carries `exitCode`.

Before starting the child host, pigo atomically materializes `<agentDir>/host/bin/pi`, prepends its
directory to `PATH`, and exports `PI_SUBAGENT_PI_BINARY`, `PI_CODING_AGENT_DIR`, and
`PI_CODING_AGENT=true`. The same environment is used by `pi.exec`. Pigo then locates each entry's
nearest owning `package.json`; declared production dependencies are left alone when resolvable from
local or hoisted `node_modules`, otherwise npm runs with `--omit=dev --no-audit --no-fund`, or Bun
runs with `--production`. Package staging exposes those declared dependencies and prioritizes the
SDK family nested under the pinned coding-agent package; legacy `@mariozechner/*` and unscoped
package names resolve to the same real SDK modules. An install or staging failure is reported for
that entry and later extensions continue. Node starts with native type stripping, native transformation for
non-erasable TypeScript syntax when supported (Node ≥22.7), and preserved symlink resolution; Bun
runs TypeScript directly. Node 22.6 supports the required erasable TypeScript baseline.

If neither Node.js ≥22.6 nor Bun is available, pigo does not start a host and emits `JS extensions
require Node.js ≥22.6 or Bun; skills, prompt templates, MCP servers and built-in tools work without
it`. This is an extension-load diagnostic, not a process-start failure.

Callback contexts carry a generation-scoped `signal` descriptor with `{id,aborted,reason}`. The
host materializes it as a real JavaScript `AbortSignal`; pigo sends `state_signal_abort` while the
callback is running and `state_signal_release` when it returns. For `session_before_compact` and
`session_before_tree`, the event payload's `signal` is the same live signal exposed on `ctx`.
