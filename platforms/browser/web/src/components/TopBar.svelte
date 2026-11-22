<script>
  import { orb } from "../lib/store.svelte.js";
  import { named, word } from "../lib/utils.js";

  let tabs = $derived(orb.state.tabs || []);
  let activeTab = $derived(orb.getTab(orb.activeTabId()));
  let contextShare = $derived(activeTab?.context || 0);
  let litSegments = $derived(Math.round(contextShare * 10));

  function onDragStart(e, id) {
    e.dataTransfer.setData("text/orb-tab", id);
  }
  function onDrop(e, targetIdx) {
    e.preventDefault();
    const id = e.dataTransfer.getData("text/orb-tab");
    if (id && tabs[targetIdx]?.id !== id) {
      orb.send({ do: "move", tab: id, to: targetIdx });
    }
  }
</script>

<nav class="topbar" aria-label="Conversations">
  <button
    type="button"
    class="orb-mark"
    class:on={!orb.shown && !orb.overview}
    onclick={() => orb.focus("")}
    title="Home (Alt+H)"
    aria-label="Home"
  >
    <i class="orb-dot" aria-hidden="true"></i>
    <b>ORB</b>
  </button>

  <div class="tab-strip scroll">
    {#each tabs as c, i (c.id)}
      {@const isShown = !orb.overview && (c.id === orb.shown || c.id === orb.splitTab)}
      {@const m = orb.getMachine(c.peer)}
      <div
        class="tab-chip"
        class:on={isShown}
        class:secondary={c.id === orb.splitTab}
        role="tab"
        tabindex="0"
        aria-selected={isShown}
        title={[c.where, word(c)].filter(Boolean).join(" · ")}
        draggable="true"
        onclick={() => orb.focus(c.id)}
        onkeydown={e => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); orb.focus(c.id); } }}
        onauxclick={e => { if (e.button === 1) orb.closeTab(c); }}
        oncontextmenu={e => { e.preventDefault(); orb.openTabMenu(c); }}
        ondblclick={e => { e.preventDefault(); orb.openTabMenu(c); }}
        ondragstart={e => onDragStart(e, c.id)}
        ondragover={e => e.preventDefault()}
        ondrop={e => onDrop(e, i)}
      >
        <i class="led" class:live={c.busy} class:asks={!!c.ask} style="--hue: var(--hue{m?.hue || 0})"></i>
        <span class="tab-label">{named(c.title)}</span>
        {#if c.ask}
          <small class="tab-asks">asks</small>
        {:else if c.unread > 0}
          <small class="tab-unread">{c.unread > 99 ? "99+" : c.unread}</small>
        {/if}
        <button
          type="button"
          class="tab-x"
          aria-label="Close {named(c.title)}"
          onclick={e => { e.stopPropagation(); orb.closeTab(c); }}
        >×</button>
      </div>
    {/each}
  </div>

  <button type="button" class="bar-btn plus" onclick={() => orb.focus("")} title="New conversation (Alt+N)" aria-label="New conversation">+</button>

  <div class="bar-right">
    {#if contextShare > 0 && !orb.overview}
      <span class="sync-bay" class:alarm={contextShare > 0.8} title="Context window usage">
        <span class="sync-bars">
          {#each Array(10) as _, idx}
            <i class:lit={idx < litSegments}></i>
          {/each}
        </span>
        <span>{Math.round(contextShare * 100)}%</span>
      </span>
    {/if}

    <button
      type="button"
      class="bar-btn cmd-k"
      onclick={() => (orb.paletteOpen = true)}
      title="Command palette (Ctrl/⌘+K)"
    >
      <kbd>⌘K</kbd>
    </button>

    {#if tabs.length > 0}
      <button
        type="button"
        class="bar-btn"
        class:on={orb.overview}
        onclick={() => orb.toggleOverview()}
        title="Overview grid (Alt+G)"
        aria-label="Overview grid"
      >
        <span class="grid-ico" class:filled={orb.overview}><i></i><i></i><i></i><i></i></span>
      </button>
    {/if}

    {#if tabs.length > 1}
      <button
        type="button"
        class="bar-btn split-btn"
        class:on={!!orb.splitTab}
        onclick={() => orb.toggleSplit()}
        title="Split view (Alt+D)"
        aria-label="Split view"
      >
        <span class="split-ico"><i></i><i></i></span>
      </button>
    {/if}

    <button
      type="button"
      class="bar-btn menu-btn"
      onclick={() => orb.openSettings("general", orb.defaultPeer())}
      title="Settings (Ctrl/⌘+,)"
      aria-label="Settings"
    >
      <span class="menu-lines"></span>
    </button>
  </div>
</nav>

<style>
  .topbar {
    display: flex;
    align-items: center;
    gap: 5px;
    height: 48px;
    flex-shrink: 0;
    padding: 0 14px;
    background: var(--bg-glass);
    backdrop-filter: blur(14px);
    -webkit-backdrop-filter: blur(14px);
    border-bottom: 1px solid var(--rule);
    z-index: 5;
  }
  .orb-mark {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    height: 30px;
    padding: 0 10px;
    margin-right: 4px;
    border: 1px solid transparent;
    border-radius: var(--soft);
    background: transparent;
    cursor: pointer;
    flex-shrink: 0;
    transition:
      background-color var(--t-fast) var(--ease),
      border-color var(--t-fast) var(--ease),
      box-shadow var(--t-fast) var(--ease),
      transform 100ms var(--ease);
  }
  .orb-mark:active { transform: scale(0.96); }
  .orb-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--rupture);
    transition: transform var(--t-med) var(--spring);
  }
  .orb-mark.on .orb-dot {
    transform: scale(1.12);
  }
  .orb-mark b {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 0.14em;
    color: var(--mute);
    transition: color var(--t-fast) var(--ease);
  }
  .orb-mark.on {
    background: var(--raised);
    border-color: var(--rule);
    box-shadow: 0 1px 2px var(--shade);
  }
  .orb-mark.on b { color: var(--fg); }

  .tab-strip {
    display: flex;
    align-items: center;
    gap: 4px;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .tab-chip {
    display: flex;
    align-items: center;
    gap: 7px;
    flex-shrink: 1;
    min-width: 76px;
    max-width: 215px;
    height: 30px;
    padding: 0 5px 0 10px;
    border: 1px solid transparent;
    border-radius: var(--soft);
    background: transparent;
    color: var(--mute);
    font-size: var(--small);
    font-weight: 450;
    cursor: pointer;
    user-select: none;
    transition:
      background-color var(--t-fast) var(--ease),
      border-color var(--t-fast) var(--ease),
      color var(--t-fast) var(--ease),
      box-shadow var(--t-fast) var(--ease);
  }
  .tab-chip.on {
    color: var(--fg);
    font-weight: 550;
    background: var(--raised);
    border-color: var(--rule);
    box-shadow: 0 1px 2px var(--shade);
  }
  .tab-chip.secondary {
    border-style: dashed;
    border-color: var(--rule-strong);
  }
  .tab-label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .tab-asks {
    font-family: var(--font-mono);
    font-size: 9.5px;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--rupture);
  }
  .tab-unread {
    padding: 0 6px;
    border-radius: var(--pill);
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 600;
    color: var(--bg);
    background: var(--fg);
  }
  .tab-x {
    display: grid;
    place-items: center;
    width: 19px;
    height: 19px;
    flex-shrink: 0;
    border: 0;
    border-radius: 5px;
    background: none;
    color: var(--meta);
    font-size: 14px;
    cursor: pointer;
    opacity: 0;
    transition: opacity var(--t-fast) var(--ease), background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .tab-chip.on .tab-x { opacity: 0.65; }

  .bar-btn {
    display: grid;
    place-items: center;
    min-width: 30px;
    height: 30px;
    padding: 0 7px;
    flex-shrink: 0;
    border: 1px solid transparent;
    border-radius: var(--soft);
    background: transparent;
    color: var(--mute);
    cursor: pointer;
    transition:
      background-color var(--t-fast) var(--ease),
      border-color var(--t-fast) var(--ease),
      color var(--t-fast) var(--ease),
      transform 100ms var(--ease);
  }
  .bar-btn:active { transform: scale(0.95); }
  .bar-btn.plus { font-size: 17px; font-weight: 400; padding: 0; }
  .bar-btn.on {
    background: var(--raised);
    border-color: var(--rule);
    color: var(--fg);
    box-shadow: 0 1px 2px var(--shade);
  }
  .bar-right {
    display: flex;
    align-items: center;
    gap: 5px;
    margin-left: auto;
    flex-shrink: 0;
  }

  .grid-ico {
    display: grid;
    grid-template-columns: 5px 5px;
    gap: 2.5px;
  }
  .grid-ico i {
    width: 5px;
    height: 5px;
    border: 1.25px solid currentColor;
    border-radius: 1.5px;
    transition: background-color var(--t-fast) var(--ease);
  }
  .grid-ico.filled i { background: currentColor; }

  .split-ico {
    display: flex;
    gap: 2.5px;
  }
  .split-ico i {
    width: 6px;
    height: 11px;
    border: 1.25px solid currentColor;
    border-radius: 1.5px;
  }

  .menu-lines {
    width: 15px;
    height: 10px;
    background:
      linear-gradient(currentColor 0 0) top / 100% 1.4px no-repeat,
      linear-gradient(currentColor 0 0) center / 100% 1.4px no-repeat,
      linear-gradient(currentColor 0 0) bottom / 100% 1.4px no-repeat;
    color: var(--fg);
  }

  @media (hover: hover) {
    .orb-mark:hover:not(.on),
    .tab-chip:hover:not(.on),
    .tab-x:hover,
    .bar-btn:hover:not(.on) {
      background: var(--hover);
      color: var(--fg);
    }
    .tab-chip:hover .tab-x { opacity: 1; }
  }
  @media (max-width: 720px) {
    .cmd-k, .split-btn, .sync-bars { display: none; }
  }
</style>
