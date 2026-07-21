# Phase 5 — JS extension bridge

Runs upstream's TS extensions inside pigo: sobek + embedded esbuild (D17), API-complete-subset
fidelity. The Go ExtensionAPI (WP-350/351) is the target surface; the bridge is a *client* of it —
no special powers. Threading rule (ARCHITECTURE §5): one goroutine per VM, message-passing only.

## WP-510 — Bridge runtime foundation

**Upstream refs:** `packages/coding-agent/src/core/extensions/loader.ts` (discovery, jiti loading,
factory cache, aliases), `docs/extensions.md` §loading; extension examples `hello.ts`, `pirate.ts`.

**Scope:** discovery parity (global/project dirs, settings arrays, `-e` paths incl. `npm:`/`git:`
via WP-360 storage, trust gating); esbuild pipeline: bundle entry TS → single CJS/ESM artifact,
target es2017, async-generator lowering, `pi`/`@earendil-works/pi-*` marked external and resolved to
bridge modules, extension-local `node_modules` bundled (pure-JS), source maps for error mapping;
sobek VM lifecycle (create, eval bundle, invoke default-export factory, await async factory),
per-extension VM isolation, error isolation to upstream semantics, `/reload` = rebuild + fresh VMs;
build cache keyed by content hash.

**Acceptance:** hello.ts and pirate.ts (unmodified upstream files) load, register, and function in
a faux print-mode session; a TS syntax error surfaces as the upstream-style load error with mapped
line numbers; reload picks up file edits.

## WP-520 — JS ExtensionAPI bindings wave 1 (non-UI)

**Upstream refs:** `src/core/extensions/types.ts` (ExtensionAPI shape), `docs/extensions.md` API
sections; typebox usage across examples.

**Scope:** the `pi` object in-VM: `on(event, handler)` for ALL hooks (payload marshaling Go↔JS with
lazy wrappers for big objects like message lists), `registerTool` (typebox schema objects →
JSON Schema extraction in-engine; execute callbacks with onUpdate streaming; prepareArguments;
promptSnippet/Guidelines; renderCall/renderResult deferred to WP-541 — plain-text fallback now),
`registerCommand` (+argument completions), `registerFlag`, `registerShortcut` (recorded; active with
TUI), `registerProvider`/`unregisterProvider`, `sendMessage`/`sendUserMessage`/`appendEntry`,
session name/label, model + thinking accessors, active-tools management, `pi.events` bus
(cross-VM), `pi.exec`; `ctx` non-UI surface (cwd, mode, hasUI, signal→AbortSignal bridge,
sessionManager reads, modelRegistry, isIdle/abort, shutdown, compact, contextUsage, systemPrompt,
trust). typebox: bundle the real library once, shared across extension bundles.

**Acceptance:** upstream examples todo, summarize, commands, send-user-message, dynamic-tools,
structured-output, tool-override, event-bus run unmodified with upstream-documented behavior
(assertions scripted in json mode — seeds of F11).

## WP-530 — Node shims + fetch + exec

**Upstream refs:** builtin usage across examples (fs 10×, path 12×, child_process 5×, os/url/util);
`pi.exec` semantics; goja_nodejs modules.

**Scope:** module shims resolved at bundle time (esbuild plugin mapping `node:fs` etc. to bridge
modules): fs (sync + promises subsets actually used: read/write/exists/stat/mkdir/readdir/watch→
polling stub documented), path (full — pure JS port is fine), os (subset), process (env/cwd/platform),
url, util (promisify, inspect-lite), console (goja_nodejs if sobek-compatible, else own),
Buffer (goja_nodejs), timers (VM event loop); `fetch` on net/http (Request/Response/Headers subset,
streaming bodies); child_process subset (`exec`/`spawn` minimal) routed through the Go exec bridge
with the same env/cwd rules as `pi.exec`.

**Acceptance:** upstream examples file-trigger, protected-paths, git-checkpoint, dirty-repo-guard,
claude-rules (the node-builtin users) run unmodified or with ledgered gaps; shim coverage table
committed as `docs/sync/node-shims.md`.

## WP-541 — ctx.ui bridge

**Upstream refs:** `docs/extensions.md` UI sections; ctx.ui in `types.ts`; examples notify,
timed-confirm, question, status-line, widget-placement, custom-footer/header, model-status.

**Scope:** JS bindings over the WP-450 ctx.ui implementation: dialogs (select/confirm/input/editor,
timeout + signal), notify, status/widget/footer/header/title setters, working indicator/message,
editor text get/set/paste, addAutocompleteProvider, theme access; per-mode degradation identical to
upstream (RPC-bridged, print/json no-ops); custom tool renderers (renderCall/renderResult returning
pi-tui component descriptors or strings).

**Acceptance:** the listed examples run unmodified in TUI mode with upstream-matching behavior;
same extensions in json/rpc mode degrade exactly as upstream docs specify (F7 scenarios extended).

## WP-542 — Custom components, editors, overlays — gate G3

**Upstream refs:** `ctx.ui.custom()`, `setEditorComponent`, overlay mode + anchoring; examples
modal-editor (vim), rainbow-editor, border-status-editor, overlay-test, snake, doom-overlay.

**Scope:** JS objects implementing `render(width): string[]` + `handleInput` wrapped as Go
Components (calls marshaled onto the VM goroutine synchronously with frame budget guard); full
editor replacement; **gate G3**: overlay/anchoring surfaces are experimental upstream — bridge them
if stable at the pin, else document as a ledger gap with the examples that need them.

**Acceptance:** modal-editor (vim) and one game example run playably; frame budget holds (no VM
call > 8 ms on the corpus, measured); G3 decision recorded.

## WP-550 — Example-extension matrix (F11) + fix wave

**Scope:** harness executing all ~69 single-file + 9 directory upstream examples headlessly where
possible (json mode + scripted faux sessions + synthetic inputs), asserting each one's documented
observable effect; matrix published at `docs/sync/extension-matrix.md` (works / works-with-gap /
unsupported+reason); one fix wave on the highest-value failures; matrix wired into `make fixtures`
so sync runs regenerate it.

**Acceptance:** matrix committed and honest; ≥ 80% of single-file examples "works"; every
"unsupported" has a reason tied to a ledger line or a follow-up WP proposal.
