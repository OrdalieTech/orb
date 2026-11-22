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

  function onGlobalKeyDown(e) {
    // Let active modal / interrupt handle its own keys first
    if (askingTab || orb.pickerState || orb.viewerImage || orb.explorerPeer || orb.modelDeckOpen || orb.paletteOpen || orb.shortcutsOpen) {
      return;
    }

    const code = e.code;
    const alt = e.altKey && !e.ctrlKey && !e.metaKey;
    const mod = (e.ctrlKey || e.metaKey) && !e.altKey;
    const ctrlOnly = e.ctrlKey && !e.metaKey && !e.altKey;

    // ? opens Keyboard Shortcuts when not typing in an input
    if (e.key === "?" && !isEditable(e.target) && orb.screen === "app") {
      e.preventDefault();
      orb.shortcutsOpen = true;
      return;
    }

    // Global modifiers (work even from inside textarea!)
    if (mod && (code === "KeyK" || code === "KeyP") && orb.screen === "app") {
      e.preventDefault();
      orb.paletteOpen = true;
      return;
    }
    if (mod && code === "Comma" && (orb.screen === "app" || orb.screen === "settings")) {
      e.preventDefault();
      if (orb.screen === "settings") orb.screen = "app";
      else orb.openSettings("general", orb.defaultPeer());
      return;
    }
    if (ctrlOnly && code === "KeyM" && orb.screen === "app") {
      e.preventDefault();
      orb.openModelDeck();
      return;
    }

    if (orb.screen === "app" && alt) {
      const tabs = orb.state.tabs || [];
      const active = orb.getTab(orb.activeTabId());

      if (code === "KeyH" || code === "Digit0" || code === "KeyN") {
        e.preventDefault();
        orb.focus("");
        return;
      }
      if (code === "KeyG") {
        e.preventDefault();
        orb.toggleOverview();
        return;
      }
      if (code === "KeyD") {
        e.preventDefault();
        orb.toggleSplit();
        return;
      }
      if (code === "KeyW" && active) {
        e.preventDefault();
        orb.closeTab(active);
        return;
      }
      if (code === "KeyT" && e.shiftKey) {
        e.preventDefault();
        orb.reopenClosed();
        return;
      }
      if (code === "KeyM" && e.shiftKey) {
        e.preventDefault();
        orb.cycleReasoning();
        return;
      }
      if (code === "KeyM") {
        e.preventDefault();
        orb.openModelDeck();
        return;
      }
      if (code === "KeyL") {
        e.preventDefault();
        orb.bumpFocus();
        return;
      }
      if (code === "KeyR" && active) {
        e.preventDefault();
        orb.renamePrompt(active.title, name => orb.send({ do: "rename", tab: active.id, name }));
        return;
      }
      if (code === "KeyJ" && active?.remote) {
        e.preventDefault();
        orb.terminalTab = orb.terminalTab === active.id ? "" : active.id;
        return;
      }
      if (code === "Period" && active) {
        e.preventDefault();
        orb.send({ do: "abort", tab: active.id });
        return;
      }
      if (code === "KeyC" && e.shiftKey && active) {
        e.preventDefault();
        orb.submitMessage(active.id, "/copy");
        return;
      }
      if (code === "BracketRight") {
        e.preventDefault();
        orb.stepTab(1);
        return;
      }
      if (code === "BracketLeft") {
        e.preventDefault();
        orb.stepTab(-1);
        return;
      }
      if (code === "Slash") {
        e.preventDefault();
        orb.shortcutsOpen = true;
        return;
      }
      const digit = /^Digit([1-9])$/.exec(code)?.[1];
      if (digit) {
        e.preventDefault();
        const target = digit === "9" ? tabs.at(-1) : tabs[Number(digit) - 1];
        if (target) orb.focus(target.id);
        return;
      }
    }

    if (e.key === "Escape") {
      if (orb.screen === "settings") {
        orb.screen = "app";
        orb.bumpFocus();
      } else if (orb.terminalTab) {
        orb.terminalTab = "";
      } else if (orb.overview) {
        orb.toggleOverview();
      } else if (orb.shown) {
        orb.focus("");
      }
    }
  }

  window.addEventListener("keydown", onGlobalKeyDown);
  onDestroy(() => window.removeEventListener("keydown", onGlobalKeyDown));
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
  }
  .boot-mark .mark {
    width: 28px;
    height: 28px;
    background: var(--rule);
    mask: url(ordalie.svg) center / contain no-repeat;
  }
  .app-shell {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
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
    padding: 6px var(--margin);
    font-size: var(--small);
    background: var(--raised);
    border-top: 1px solid var(--rupture);
  }
  .toast {
    position: fixed;
    left: 50%;
    bottom: 96px;
    z-index: 30;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 7px 10px 7px 14px;
    font-size: var(--small);
    font-weight: 500;
    color: var(--bg);
    background: var(--fg);
    border-radius: var(--soft);
    transform: translateX(-50%);
    box-shadow: 0 10px 28px var(--shade);
    animation: appear 140ms var(--ease);
  }
  .toast-undo {
    padding: 2px 8px;
    border: 1px solid var(--bg);
    border-radius: 3px;
    background: none;
    color: var(--bg);
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
  }
</style>
