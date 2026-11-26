<script>
  import { onDestroy, onMount } from "svelte";
  import { orb } from "./lib/store.svelte.js";
  import { findLeaf } from "./lib/tiles.js";
  import PairScreen from "./components/PairScreen.svelte";
  import TopBar from "./components/TopBar.svelte";
  import Overview from "./components/Overview.svelte";
  import MitosisMap from "./components/MitosisMap.svelte";
  import TileTree from "./components/TileTree.svelte";
  import TerminalDrawer from "./components/TerminalDrawer.svelte";
  import Settings from "./components/Settings.svelte";
  import Interrupt from "./components/Interrupt.svelte";
  import ModelDeck from "./components/ModelDeck.svelte";
  import FolderExplorer from "./components/FolderExplorer.svelte";
  import CommandPalette from "./components/CommandPalette.svelte";
  import SpreadModal from "./components/SpreadModal.svelte";

  onMount(() => {
    orb.init();
  });

  let displayTree = $derived(
    orb.zoomedTileId ? (findLeaf(orb.tileTree, orb.zoomedTileId) || orb.tileTree) : orb.tileTree,
  );
  let termTab = $derived(orb.terminalTab ? orb.getTab(orb.terminalTab) : null);

  // An ask on any conversation currently visible stops the world (Evangelion Rupture overlay).
  let askingTab = $derived.by(() => {
    if (orb.screen !== "app") return null;
    for (const id of orb.visibleTabs) {
      const t = orb.getTab(id);
      if (t?.ask) return t;
    }
    return null;
  });

  function isEditable(target) {
    if (!target) return false;
    const tag = target.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || target.isContentEditable;
  }

  function hasTextSelection(target) {
    if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA")) {
      return target.selectionStart !== target.selectionEnd;
    }
    const sel = window.getSelection?.();
    return !!sel && !sel.isCollapsed;
  }

  function altMatches(e, letter, code) {
    return e.key.toLowerCase() === letter || e.code === code;
  }

  function isInTerminal(target) {
    return !!target?.closest?.(".term-drawer");
  }

  function focusActivePromptInput() {
    const el =
      document.querySelector(".tile-leaf.focused textarea.prompt-input") ||
      document.querySelector("textarea.prompt-input");
    if (el) {
      el.focus({ preventScroll: true });
      return true;
    }
    return false;
  }

  function closeTopOverlay() {
    if (orb.viewerImage) { orb.viewerImage = null; return true; }
    if (orb.pickerState) { orb.pickerState.resolve(null); return true; }
    if (orb.shortcutsOpen) { orb.shortcutsOpen = false; return true; }
    if (orb.spreadModalOpen) { orb.spreadModalOpen = false; return true; }
    if (orb.paletteOpen) { orb.paletteOpen = false; return true; }
    if (orb.modelDeckOpen) { orb.modelDeckOpen = false; return true; }
    if (orb.explorerPeer) { orb.explorerPeer = ""; return true; }
    if (orb.terminalTab) { orb.terminalTab = ""; return true; }
    if (askingTab) { orb.send({ do: "answer", tab: askingTab.id, value: null }); return true; }
    return false;
  }

  function onGlobalKeyDown(e) {
    const key = (e.key || "").toLowerCase();
    const code = e.code;
    const alt = e.altKey && !e.ctrlKey && !e.metaKey;
    const mod = (e.ctrlKey || e.metaKey) && !e.altKey;
    const ctrlOnly = e.ctrlKey && !e.metaKey && !e.altKey;
    const inApp = orb.screen === "app";
    const inAppOrSettings = inApp || orb.screen === "settings";

    // When focus is inside the embedded terminal, let xterm receive every key
    // (including Esc, Ctrl+C, Ctrl+R, Shift+Tab, arrows) except the toggle shortcut (Ctrl/Alt+J).
    if (isInTerminal(e.target)) {
      if ((ctrlOnly && key === "j") || (alt && altMatches(e, "j", "KeyJ"))) {
        e.preventDefault();
        orb.terminalTab = "";
      }
      return;
    }

    // Deterministic capture-phase Escape stack: ALWAYS closes the topmost overlay regardless of focus.
    if (e.key === "Escape") {
      if (closeTopOverlay()) {
        e.preventDefault();
        e.stopPropagation();
        return;
      }
      if (orb.screen === "settings") {
        e.preventDefault();
        orb.screen = "app";
        orb.bumpFocus();
        return;
      }
      if (orb.mitosisMap) {
        e.preventDefault();
        orb.toggleMitosisMap();
        return;
      }
      if (orb.overview) {
        e.preventDefault();
        orb.toggleOverview();
        return;
      }
      if (orb.inlinePaletteOpen) {
        // Let PromptBox dismiss its inline /command or @file palette first.
        return;
      }
      const active = orb.getTab(orb.activeTabId());
      if (active?.busy) {
        // TUI parity (app.interrupt): Esc stops the active turn.
        e.preventDefault();
        orb.send({ do: "abort", tab: active.id });
        return;
      }
      if (orb.shown) {
        e.preventDefault();
        orb.focus("");
      }
      return;
    }

    // Primary TUI palette & model toggles work even from Settings or another non-blocking modal
    // (layout-independent e.key match so AZERTY Ctrl+M / Ctrl+P work reliably).
    if (!askingTab && !orb.pickerState && !orb.viewerImage && inAppOrSettings) {
      if ((ctrlOnly && key === "p") || (mod && (key === "k" || key === "p"))) {
        e.preventDefault();
        if (orb.screen === "settings") orb.screen = "app";
        orb.modelDeckOpen = false;
        orb.shortcutsOpen = false;
        orb.spreadModalOpen = false;
        orb.explorerPeer = "";
        orb.paletteOpen = !orb.paletteOpen;
        if (!orb.paletteOpen) orb.bumpFocus();
        return;
      }
      if ((ctrlOnly && key === "m") || (alt && !e.shiftKey && altMatches(e, "m", "KeyM"))) {
        e.preventDefault();
        if (orb.screen === "settings") orb.screen = "app";
        orb.paletteOpen = false;
        orb.shortcutsOpen = false;
        orb.spreadModalOpen = false;
        orb.explorerPeer = "";
        if (orb.modelDeckOpen) {
          orb.modelDeckOpen = false;
          orb.bumpFocus();
        } else {
          orb.openModelDeck();
        }
        return;
      }
      if (mod && (key === "," || code === "Comma")) {
        e.preventDefault();
        closeTopOverlay();
        if (orb.screen === "settings") {
          orb.screen = "app";
          orb.bumpFocus();
        } else {
          orb.openSettings("general", orb.defaultPeer());
        }
        return;
      }
    }

    // Let active modal / interrupt handle its own navigation keys (↑/↓/Enter/etc.)
    if (askingTab || orb.pickerState || orb.viewerImage || orb.explorerPeer || orb.modelDeckOpen || orb.paletteOpen || orb.shortcutsOpen || orb.spreadModalOpen) {
      return;
    }

    if (!inApp) return;

    const tabs = orb.state.tabs || [];
    const active = orb.getTab(orb.activeTabId());

    // ? opens Keyboard Shortcuts when not typing in an input
    if (e.key === "?" && !e.ctrlKey && !e.metaKey && !e.altKey && !isEditable(e.target)) {
      e.preventDefault();
      orb.shortcutsOpen = true;
      return;
    }

    // Type-to-focus: starting to type any printable character anywhere in the app
    // focuses the active tile's prompt box and lets the character land in it naturally.
    if (
      !orb.overview &&
      !isEditable(e.target) &&
      !e.ctrlKey && !e.metaKey && !e.altKey &&
      e.key && e.key.length === 1 && e.key !== " "
    ) {
      focusActivePromptInput();
      return;
    }

    // Shift+Left / Shift+Right: switch tabs when not typing in an input or selecting text
    if (
      (e.key === "ArrowLeft" || e.key === "ArrowRight") &&
      e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey &&
      !isEditable(e.target) && !hasTextSelection(e.target)
    ) {
      e.preventDefault();
      orb.stepTab(e.key === "ArrowRight" ? 1 : -1);
      return;
    }

    // Shift+Tab: cycle reasoning / thinking level (TUI app.thinking.cycle)
    if (e.key === "Tab" && e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      orb.cycleReasoning();
      return;
    }

    // Ctrl+Tab / Ctrl+Shift+Tab: step conversations
    if (e.key === "Tab" && e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      orb.stepTab(e.shiftKey ? -1 : 1);
      return;
    }

    // Desktop-aligned Cmd/Ctrl split & tile shortcuts
    if (mod) {
      if (key === "d" || code === "KeyD") {
        e.preventDefault();
        orb.splitActiveTile(e.shiftKey ? "bottom" : "right");
        return;
      }
      if (e.shiftKey && e.key === "Enter" && orb.isSplit) {
        e.preventDefault();
        orb.toggleZoomTile();
        return;
      }
      if ((key === "[" || code === "BracketLeft") || (key === "]" || code === "BracketRight")) {
        const dir = (key === "]" || code === "BracketRight") ? 1 : -1;
        if (e.shiftKey) {
          e.preventDefault();
          orb.stepTab(dir);
          return;
        }
        if (orb.isSplit) {
          e.preventDefault();
          orb.cycleTile(dir);
          return;
        }
      }
      if (e.shiftKey && (key === "=" || code === "Equal") && orb.isSplit) {
        e.preventDefault();
        orb.equalizeTiles();
        return;
      }
    }

    // TUI-aligned Ctrl+<key> bindings (layout-independent via e.key)
    if (ctrlOnly) {
      if (key === "o") {
        e.preventDefault();
        orb.toggleToolsExpanded();
        return;
      }
      if (key === "n") {
        e.preventDefault();
        orb.focus("");
        return;
      }
      if (key === "r" && active) {
        e.preventDefault();
        orb.renamePrompt(active.title, name => orb.send({ do: "rename", tab: active.id, name }));
        return;
      }
      if (key === "x" && active && !hasTextSelection(e.target)) {
        e.preventDefault();
        orb.submitMessage(active.id, "/copy").catch(() => {});
        return;
      }
      if (key === "g") {
        e.preventDefault();
        orb.toggleOverview();
        return;
      }
      if (key === "j" && active?.remote) {
        e.preventDefault();
        orb.terminalTab = orb.terminalTab === active.id ? "" : active.id;
        return;
      }
    }

    // Alt+<key> aliases (safe fallback for browser-reserved Ctrl keys on Linux/Windows)
    if (alt) {
      if (altMatches(e, "h", "KeyH") || code === "Digit0" || altMatches(e, "n", "KeyN")) {
        e.preventDefault();
        orb.focus("");
        return;
      }
      if (altMatches(e, "g", "KeyG")) {
        e.preventDefault();
        orb.toggleOverview();
        return;
      }
      if (altMatches(e, "d", "KeyD")) {
        e.preventDefault();
        orb.splitActiveTile(e.shiftKey ? "bottom" : "right");
        return;
      }
      if (altMatches(e, "w", "KeyW")) {
        e.preventDefault();
        if (e.shiftKey && orb.isSplit) {
          orb.closeTile();
        } else if (active) {
          orb.closeTab(active);
        }
        return;
      }
      if (e.shiftKey && (key === "=" || code === "Equal") && orb.isSplit) {
        e.preventDefault();
        orb.equalizeTiles();
        return;
      }
      if (altMatches(e, "t", "KeyT") && e.shiftKey) {
        e.preventDefault();
        orb.reopenClosed();
        return;
      }
      if (altMatches(e, "m", "KeyM") && e.shiftKey) {
        e.preventDefault();
        orb.cycleReasoning();
        return;
      }
      if (altMatches(e, "l", "KeyL")) {
        e.preventDefault();
        orb.bumpFocus();
        return;
      }
      if (altMatches(e, "r", "KeyR") && active) {
        e.preventDefault();
        orb.renamePrompt(active.title, name => orb.send({ do: "rename", tab: active.id, name }));
        return;
      }
      if (altMatches(e, "j", "KeyJ") && active?.remote) {
        e.preventDefault();
        orb.terminalTab = orb.terminalTab === active.id ? "" : active.id;
        return;
      }
      if ((key === "." || code === "Period") && active) {
        e.preventDefault();
        orb.send({ do: "abort", tab: active.id });
        return;
      }
      if (altMatches(e, "c", "KeyC") && e.shiftKey && active) {
        e.preventDefault();
        orb.submitMessage(active.id, "/copy").catch(() => {});
        return;
      }
      if (key === "]" || code === "BracketRight") {
        e.preventDefault();
        orb.stepTab(1);
        return;
      }
      if (key === "[" || code === "BracketLeft") {
        e.preventDefault();
        orb.stepTab(-1);
        return;
      }
      if (key === "/" || key === "?" || code === "Slash") {
        e.preventDefault();
        orb.shortcutsOpen = true;
        return;
      }
      const digit = /^Digit([1-9])$/.exec(code)?.[1] || (/^[1-9]$/.test(key) ? key : "");
      if (digit) {
        e.preventDefault();
        const target = digit === "9" ? tabs.at(-1) : tabs[Number(digit) - 1];
        if (target) orb.focus(target.id);
        return;
      }
    }
  }

  window.addEventListener("keydown", onGlobalKeyDown, true);
  onDestroy(() => window.removeEventListener("keydown", onGlobalKeyDown, true));
</script>

{#if orb.screen === "boot"}
  <div class="boot-mark" aria-label="Loading Orb">
    <span class="mark"></span>
  </div>
{:else if orb.screen === "invite" || orb.screen === "approve" || orb.screen === "elsewhere"}
  <PairScreen />
{:else}
  <div class="app-shell">
    <TopBar />

    <div class="workspace">
      {#if orb.mitosisMap}
        <MitosisMap />
      {:else if orb.overview}
        <Overview />
      {:else}
        <div class="panes">
          <TileTree node={displayTree} />
        </div>
      {/if}

      {#if termTab}
        <TerminalDrawer tab={termTab} onClose={() => (orb.terminalTab = "")} />
      {/if}
    </div>

    {#if orb.error}
      <div class="global-err alarm" role="alert">
        <span>{orb.error}</span>
        <button type="button" class="btn plain" onclick={() => orb.showError("")}>×</button>
      </div>
    {/if}
  </div>
{/if}

<!-- Modals & Overlays -->
{#if orb.screen === "settings"}
  <Settings />
{/if}
{#if askingTab}
  <Interrupt tab={askingTab} ask={askingTab.ask} />
{/if}
<ModelDeck />
<FolderExplorer />
<CommandPalette />
<SpreadModal />

{#if orb.toastState}
  <div class="toast" role="status">
    <span class="toast-text">{orb.toastState.text}</span>
    {#if orb.toastState.undo}
      <button
        type="button"
        class="toast-undo"
        onclick={() => {
          const fn = orb.toastState.undo;
          orb.dismissToast();
          fn();
        }}
      >undo</button>
    {/if}
    <button
      type="button"
      class="toast-x"
      aria-label="Dismiss"
      onclick={() => orb.dismissToast()}
    >×</button>
  </div>
{/if}

<style>
  .boot-mark {
    flex: 1;
    display: grid;
    place-items: center;
    animation: backdrop-in var(--t-med) var(--ease);
  }
  .boot-mark .mark {
    width: 30px;
    height: 30px;
    background: var(--fg);
    mask: url(ordalie.svg) center / contain no-repeat;
    animation: led-breathe 1.6s ease-in-out infinite;
  }
  .app-shell {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    animation: backdrop-in var(--t-fast) var(--ease);
  }
  .workspace {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .panes {
    flex: 1;
    min-height: 0;
    display: flex;
    animation: view-in var(--t-med) var(--ease);
  }
  .global-err {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 8px var(--margin);
    font-size: var(--small);
    background: var(--rupture-soft);
    border-top: 1px solid color-mix(in srgb, var(--rupture) 35%, transparent);
    animation: drop-in var(--t-fast) var(--ease);
  }
  .toast {
    position: fixed;
    right: 16px;
    bottom: 16px;
    z-index: 30;
    display: flex;
    align-items: center;
    gap: 8px;
    max-width: min(360px, calc(100vw - 32px));
    padding: 5px 8px 5px 11px;
    font-size: 12px;
    font-weight: 600;
    color: var(--bg);
    background: var(--fg);
    border: 1px solid var(--fg);
    border-radius: var(--radius-sm);
    animation: toast-in 110ms var(--ease);
  }
  @keyframes toast-in {
    from { opacity: 0; transform: translateY(4px); }
    to { opacity: 1; transform: translateY(0); }
  }
  .toast-text {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .toast-undo {
    flex-shrink: 0;
    padding: 1px 7px;
    border: 1px solid color-mix(in srgb, var(--bg) 45%, transparent);
    border-radius: var(--radius-sm);
    background: none;
    color: var(--bg);
    font-size: 11px;
    font-weight: 700;
    cursor: pointer;
  }
  .toast-undo:hover { background: color-mix(in srgb, var(--bg) 16%, transparent); }
  .toast-x {
    flex-shrink: 0;
    padding: 0 3px;
    border: 0;
    background: none;
    color: color-mix(in srgb, var(--bg) 65%, transparent);
    font-size: 14px;
    line-height: 1;
    cursor: pointer;
  }
  .toast-x:hover { color: var(--bg); }
  @media (max-width: 640px) {
    .toast {
      right: auto;
      left: 50%;
      bottom: 14px;
      transform: translateX(-50%);
    }
    @keyframes toast-in {
      from { opacity: 0; transform: translate(-50%, 4px); }
      to { opacity: 1; transform: translate(-50%, 0); }
    }
  }
</style>
