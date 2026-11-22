<script>
  import { onDestroy, onMount } from "svelte";
  import { orb } from "./lib/store.svelte.js";
  import PairScreen from "./components/PairScreen.svelte";
  import TopBar from "./components/TopBar.svelte";
  import Home from "./components/Home.svelte";
  import Overview from "./components/Overview.svelte";
  import ChatPane from "./components/ChatPane.svelte";
  import TerminalDrawer from "./components/TerminalDrawer.svelte";
  import Settings from "./components/Settings.svelte";
  import Interrupt from "./components/Interrupt.svelte";
  import ModelDeck from "./components/ModelDeck.svelte";
  import FolderExplorer from "./components/FolderExplorer.svelte";
  import CommandPalette from "./components/CommandPalette.svelte";

  onMount(() => {
    orb.init();
  });

  let primaryTab = $derived(orb.getTab(orb.shown));
  let secondaryTab = $derived(orb.splitTab ? orb.getTab(orb.splitTab) : null);
  let termTab = $derived(orb.terminalTab ? orb.getTab(orb.terminalTab) : null);

  // An ask on a conversation currently on screen stops the world (Evangelion Rupture overlay).
  let askingTab = $derived.by(() => {
    if (orb.screen !== "app") return null;
    if (primaryTab?.ask) return primaryTab;
    if (secondaryTab?.ask) return secondaryTab;
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

  function closeTopOverlay() {
    if (orb.viewerImage) { orb.viewerImage = null; return true; }
    if (orb.pickerState) { orb.pickerState.resolve(null); return true; }
    if (orb.shortcutsOpen) { orb.shortcutsOpen = false; orb.bumpFocus(); return true; }
    if (orb.paletteOpen) { orb.paletteOpen = false; orb.bumpFocus(); return true; }
    if (orb.modelDeckOpen) { orb.modelDeckOpen = false; orb.bumpFocus(); return true; }
    if (orb.explorerPeer) { orb.explorerPeer = ""; orb.bumpFocus(); return true; }
    if (orb.terminalTab) { orb.terminalTab = ""; orb.bumpFocus(); return true; }
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
    if (askingTab || orb.pickerState || orb.viewerImage || orb.explorerPeer || orb.modelDeckOpen || orb.paletteOpen || orb.shortcutsOpen) {
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
        orb.toggleSplit();
        return;
      }
      if (altMatches(e, "w", "KeyW") && active) {
        e.preventDefault();
        orb.closeTab(active);
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
{:else if orb.screen === "settings"}
  <Settings />
{:else}
  <div class="app-shell">
    <TopBar />

    <div class="workspace">
      {#if orb.overview}
        <Overview />
      {:else if primaryTab}
        <div class="panes" class:split={!!secondaryTab}>
          <ChatPane tab={primaryTab} pane="primary" />
          {#if secondaryTab}
            <div class="split-divider"></div>
            <ChatPane tab={secondaryTab} pane="secondary" />
          {/if}
        </div>
      {:else}
        <Home />
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
{#if askingTab}
  <Interrupt tab={askingTab} ask={askingTab.ask} />
{/if}
<ModelDeck />
<FolderExplorer />
<CommandPalette />

{#if orb.toastState}
  <div class="toast" role="status">
    <span>{orb.toastState.text}</span>
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
  .split-divider {
    width: 1px;
    flex-shrink: 0;
    background: var(--rule);
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
    left: 50%;
    bottom: 92px;
    z-index: 30;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 7px 12px;
    font-size: var(--small);
    font-weight: 700;
    color: var(--bg);
    background: var(--fg);
    border: 1px solid var(--fg);
    border-radius: var(--radius-sm);
    transform: translateX(-50%);
    animation: toast-in var(--t-fast) var(--ease);
  }
  @keyframes toast-in {
    from { opacity: 0; transform: translate(-50%, 6px); }
    to { opacity: 1; transform: translate(-50%, 0); }
  }
  .toast-undo {
    padding: 1px 8px;
    border: 1px solid color-mix(in srgb, var(--bg) 55%, transparent);
    border-radius: var(--radius-sm);
    background: none;
    color: var(--bg);
    font-size: 11.5px;
    font-weight: 700;
    cursor: pointer;
  }
  .toast-undo:hover { background: color-mix(in srgb, var(--bg) 16%, transparent); }
</style>
