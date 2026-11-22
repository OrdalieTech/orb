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
    <b class="stretch">ORB</b>
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
    align-items: stretch;
    gap: 0;
    height: 38px;
    flex-shrink: 0;
    padding: 0;
    background: var(--bg);
    border-bottom: 1px solid var(--fg);
    z-index: 5;
  }
  .orb-mark {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 0 14px;
    border: 0;
    border-right: 1px solid var(--rule);
    background: transparent;
    cursor: pointer;
    flex-shrink: 0;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .orb-dot {
    width: 7px;
    height: 7px;
    background: var(--rupture);
  }
  .orb-mark b {
    font-size: 11px;
    line-height: 14px;
    letter-spacing: 0.1em;
    color: var(--fg);
  }
  .orb-mark.on {
    background: var(--fg);
    border-right-color: var(--fg);
  }
  .orb-mark.on b { color: var(--bg); }

  .tab-strip {
    display: flex;
    align-items: stretch;
    gap: 0;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .tab-chip {
    position: relative;
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 1;
    min-width: 84px;
    max-width: 220px;
    padding: 0 6px 0 12px;
    border: 0;
    border-right: 1px solid var(--rule);
    background: transparent;
    color: var(--mute);
    font-size: 12px;
    font-weight: 500;
    cursor: pointer;
    user-select: none;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .tab-chip.on {
    color: var(--bg);
    font-weight: 700;
    background: var(--fg);
    border-right-color: var(--fg);
  }
  .tab-chip.on::before {
    content: "";
    position: absolute;
    left: 0;
    right: 0;
    top: 0;
    height: 2px;
    background: var(--rupture);
  }
  .tab-chip.secondary {
    background: var(--recessed);
    color: var(--fg);
  }
  .tab-chip.secondary::before {
    content: "";
    position: absolute;
    left: 0;
    right: 0;
    top: 0;
    height: 2px;
    background: var(--amber);
  }
  .tab-label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .tab-asks {
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--rupture);
  }
  .tab-unread {
    padding: 0 5px;
    font-size: 10.5px;
    font-weight: 700;
    color: #FFFFFF;
    background: var(--rupture);
  }
  .tab-x {
    display: grid;
    place-items: center;
    width: 20px;
    height: 20px;
    flex-shrink: 0;
    border: 0;
    background: none;
    color: inherit;
    font-size: 14px;
    cursor: pointer;
    opacity: 0;
    transition: opacity var(--t-fast) var(--ease), background-color var(--t-fast) var(--ease);
  }
  .tab-chip.on .tab-x { opacity: 0.78; }

  .bar-btn {
    display: grid;
    place-items: center;
    min-width: 36px;
    padding: 0 9px;
    flex-shrink: 0;
    border: 0;
    border-left: 1px solid var(--rule);
    background: transparent;
    color: var(--mute);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .bar-btn.plus {
    border-left: 0;
    border-right: 1px solid var(--rule);
    font-size: 16px;
    font-weight: 700;
    color: var(--fg);
  }
  .bar-btn.on {
    background: var(--fg);
    color: var(--bg);
  }
  .bar-right {
    display: flex;
    align-items: stretch;
    gap: 0;
    margin-left: auto;
    flex-shrink: 0;
  }
  .bar-right .sync-bay {
    height: auto;
    padding: 0 10px;
  }

  .grid-ico {
    display: grid;
    grid-template-columns: 5px 5px;
    gap: 2px;
  }
  .grid-ico i {
    width: 5px;
    height: 5px;
    border: 1.4px solid currentColor;
  }
  .grid-ico.filled i { background: currentColor; }

  .split-ico {
    display: flex;
    gap: 2px;
  }
  .split-ico i {
    width: 6px;
    height: 11px;
    border: 1.4px solid currentColor;
  }

  .menu-lines {
    width: 14px;
    height: 10px;
    background:
      linear-gradient(currentColor 0 0) top / 100% 1.6px no-repeat,
      linear-gradient(currentColor 0 0) center / 100% 1.6px no-repeat,
      linear-gradient(currentColor 0 0) bottom / 100% 1.6px no-repeat;
    color: var(--fg);
  }

  @media (hover: hover) {
    .orb-mark:hover:not(.on),
    .tab-chip:hover:not(.on),
    .bar-btn:hover:not(.on) {
      background: var(--hover);
      color: var(--fg);
    }
    .tab-chip:hover .tab-x { opacity: 1; }
    .tab-x:hover { opacity: 1; background: rgba(128, 128, 128, 0.25); }
  }
  @media (max-width: 720px) {
    .cmd-k, .split-btn, .sync-bars { display: none; }
  }
</style>
