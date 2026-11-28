<script>
  import { orb } from "../lib/store.svelte.js";
  import { named, word } from "../lib/utils.js";

  let tabs = $derived(orb.state.tabs || []);
  let activeTab = $derived(orb.getTab(orb.activeTabId()));
  let contextShare = $derived(activeTab?.context || 0);
  let litSegments = $derived(Math.round(contextShare * 10));
  let contextOpen = $state(false);
  let contextWrap = $state(null);

  $effect(() => {
    if (!contextOpen) return;
    const onDown = e => {
      if (contextWrap && !contextWrap.contains(e.target)) contextOpen = false;
    };
    const onKey = e => {
      if (e.key === "Escape") {
        contextOpen = false;
        e.stopPropagation();
      }
    };
    window.addEventListener("pointerdown", onDown, true);
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("pointerdown", onDown, true);
      window.removeEventListener("keydown", onKey, true);
    };
  });

  function compactNow() {
    if (!activeTab) return;
    contextOpen = false;
    orb.submitMessage(activeTab.id, "/compact").catch(() => {});
  }

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
    class:on={!orb.shown && !orb.overview && !orb.mitosisMap}
    onclick={() => orb.focus("")}
    title="Home (Alt+H)"
    aria-label="Home"
  >
    <b class="stretch">ORB</b>
  </button>

  <div class="tab-strip scroll">
    {#each tabs as c, i (c.id)}
      {@const isActive = !orb.overview && c.id === orb.activeTabId()}
      {@const isVisible = !orb.overview && orb.visibleTabs.includes(c.id)}
      {@const m = orb.getMachine(c.peer)}
      <div
        class="tab-chip"
        class:on={isActive || isVisible}
        class:secondary={isVisible && !isActive}
        role="tab"
        tabindex="0"
        aria-selected={isActive}
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
          <small class="tab-unread">({c.unread > 99 ? "99+" : c.unread})</small>
        {/if}
        <button
          type="button"
          class="tab-x"
          aria-label="Close {named(c.title)}"
          onclick={e => { e.stopPropagation(); orb.closeTab(c); }}
        >×</button>
        {#if c.busy}
          <span class="tab-rail activity-rail active" class:asks={!!c.ask} aria-hidden="true"></span>
        {/if}
      </div>
    {/each}
  </div>

  <button type="button" class="bar-btn plus" onclick={() => orb.focus("")} title="New session (Ctrl+N)" aria-label="New session">+</button>

  <div class="bar-right">
    {#if contextShare > 0 && !orb.overview}
      <div class="ctx-wrap" bind:this={contextWrap}>
        <button
          type="button"
          class="sync-bay ctx-btn"
          class:alarm={contextShare > 0.8}
          class:on={contextOpen}
          onclick={() => (contextOpen = !contextOpen)}
          aria-expanded={contextOpen}
          title="Context window usage · click for details"
        >
          <span class="sync-bars">
            {#each Array(10) as _, idx}
              <i class:lit={idx < litSegments}></i>
            {/each}
          </span>
          <span>{Math.round(contextShare * 100)}%</span>
        </button>

        {#if contextOpen && activeTab}
          {@const pct = Math.round(contextShare * 100)}
          <div class="ctx-pop" role="dialog" aria-label="Context window usage">
            <div class="ctx-head">
              <span class="ctx-title">context window</span>
              <span class="ctx-pct" class:alarm={contextShare > 0.8}>{pct}% used</span>
            </div>
            <div class="ctx-meter">
              <div class="ctx-fill" class:alarm={contextShare > 0.8} style="width: {Math.min(100, pct)}%"></div>
            </div>
            <p class="ctx-desc">
              Share of the model’s context window filled by this conversation. Compact summarizes earlier turns to free room.
            </p>
            <div class="ctx-meta">
              {#if activeTab.model}
                <span>model</span>
                <strong>{activeTab.model.split("/").pop()}{activeTab.thinking && activeTab.thinking !== "off" ? ` · ${activeTab.thinking}` : ""}</strong>
              {/if}
              {#if activeTab.cost > 0}
                <span>session cost</span>
                <strong>${activeTab.cost.toFixed(2)}</strong>
              {/if}
            </div>
            <button type="button" class="ctx-compact" onclick={compactNow} disabled={!!activeTab.busy}>
              {activeTab.busy ? "turn running…" : "compact context (/compact)"}
            </button>
          </div>
        {/if}
      </div>
    {/if}

    <button
      type="button"
      class="bar-btn cmd-k"
      onclick={() => (orb.paletteOpen = true)}
      title="Command palette (Ctrl+P / ⌘K)"
    >
      <kbd>^P</kbd>
    </button>

    <button
      type="button"
      class="bar-btn"
      class:on={orb.mitosisMap}
      onclick={() => orb.toggleMitosisMap()}
      title="Mitosis topology map"
      aria-label="Mitosis topology map"
    >
      <svg class="mt-ico" viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.35" aria-hidden="true">
        <path d="M8 8 C6.5 6 5.5 4.8 3.6 3.8 M8 8 C10 6.5 11.2 5.2 12.6 4.2 M8 8 C6.2 9.8 5.4 11 3.8 12.2 M8 8 C9.8 9.6 10.8 11 12.4 12" />
        <circle cx="8" cy="8" r="2.1" fill={orb.mitosisMap ? "currentColor" : "var(--bg)"} />
        <circle cx="3.2" cy="3.6" r="1.4" fill="currentColor" />
        <circle cx="12.8" cy="4" r="1.4" fill="currentColor" />
        <circle cx="3.5" cy="12.4" r="1.4" fill="currentColor" />
        <circle cx="12.6" cy="12.2" r="1.4" fill="currentColor" />
      </svg>
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

    <button
      type="button"
      class="bar-btn split-btn"
      class:on={orb.isSplit}
      onclick={() => orb.splitActiveTile("right")}
      title="Split right (⌘/Alt+D · ⇧D down)"
      aria-label="Split view"
    >
      <span class="split-ico"><i></i><i></i></span>
    </button>

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
    gap: 4px;
    height: 38px;
    flex-shrink: 0;
    padding: 0 8px;
    background: var(--bg);
    border-bottom: 1px solid var(--rule-soft);
    z-index: 5;
  }
  .orb-mark {
    display: inline-flex;
    align-items: center;
    height: 26px;
    padding: 0 10px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    cursor: pointer;
    flex-shrink: 0;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .orb-mark b {
    font-size: 11px;
    line-height: 14px;
    letter-spacing: 0.08em;
    color: var(--mute);
  }
  .orb-mark.on {
    background: var(--recessed);
  }
  .orb-mark.on b { color: var(--fg); }

  .tab-strip {
    display: flex;
    align-items: center;
    gap: 2px;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .tab-chip {
    position: relative;
    display: flex;
    align-items: center;
    gap: 7px;
    flex-shrink: 1;
    height: 26px;
    min-width: 72px;
    max-width: 210px;
    padding: 0 5px 0 10px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--meta);
    font-size: 12px;
    font-weight: 500;
    cursor: pointer;
    user-select: none;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .tab-chip.on {
    color: var(--fg);
    font-weight: 600;
    background: var(--recessed);
  }
  .tab-chip.secondary {
    background: transparent;
    color: var(--mute);
  }
  .tab-chip.secondary::after {
    content: "";
    position: absolute;
    left: 10px;
    right: 10px;
    bottom: 2px;
    height: 1.5px;
    background: var(--meta);
    opacity: 0.45;
  }
  .tab-label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .tab-asks {
    font-size: 11px;
    font-weight: 600;
    color: var(--rupture);
  }
  .tab-unread {
    font-size: 11px;
    font-weight: 600;
    color: var(--meta);
  }
  .tab-chip.on .tab-unread {
    color: var(--mute);
  }
  .tab-x {
    display: grid;
    place-items: center;
    width: 18px;
    height: 18px;
    flex-shrink: 0;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: inherit;
    font-size: 13px;
    cursor: pointer;
    opacity: 0;
    transition: opacity var(--t-fast) var(--ease), background-color var(--t-fast) var(--ease);
  }
  .tab-chip.on .tab-x { opacity: 0.65; }
  .tab-chip .tab-rail {
    position: absolute;
    left: 8px;
    right: 8px;
    bottom: 0;
    width: auto;
  }

  .bar-btn {
    display: grid;
    place-items: center;
    height: 26px;
    min-width: 28px;
    padding: 0 7px;
    flex-shrink: 0;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--meta);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .bar-btn.plus {
    font-size: 15px;
    font-weight: 500;
    color: var(--meta);
  }
  .bar-btn.on {
    background: var(--recessed);
    color: var(--fg);
  }
  .bar-right {
    display: flex;
    align-items: center;
    gap: 2px;
    margin-left: auto;
    flex-shrink: 0;
  }
  .bar-right .sync-bay {
    height: 26px;
    padding: 0 8px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease);
  }
  .bar-right .sync-bay:hover,
  .bar-right .sync-bay.on {
    background: var(--recessed);
  }
  .ctx-wrap {
    position: relative;
    display: flex;
    align-items: center;
  }
  .ctx-pop {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    z-index: 25;
    width: 280px;
    padding: 12px 14px;
    display: flex;
    flex-direction: column;
    gap: 10px;
    background: var(--raised);
    border: 1px solid var(--rule);
    border-radius: var(--radius-md);
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg);
    animation: drop-in 100ms var(--ease);
  }
  .ctx-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 8px;
  }
  .ctx-title {
    font-weight: 600;
    color: var(--fg);
  }
  .ctx-pct {
    font-size: 11.5px;
    color: var(--mute);
  }
  .ctx-pct.alarm {
    color: var(--rupture);
    font-weight: 600;
  }
  .ctx-meter {
    height: 4px;
    border-radius: 2px;
    background: var(--recessed);
    overflow: hidden;
  }
  .ctx-fill {
    height: 100%;
    background: var(--fg);
    transition: width var(--t-med) var(--ease);
  }
  .ctx-fill.alarm {
    background: var(--rupture);
  }
  .ctx-desc {
    margin: 0;
    font-size: 11.5px;
    line-height: 1.5;
    color: var(--mute);
  }
  .ctx-meta {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 4px 12px;
    font-size: 11px;
    color: var(--meta);
  }
  .ctx-meta strong {
    font-weight: 500;
    color: var(--mute);
    text-align: right;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ctx-compact {
    width: 100%;
    padding: 6px 10px;
    border: 0;
    border-radius: var(--radius-sm);
    background: var(--recessed);
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 500;
    cursor: pointer;
    text-align: center;
    transition: background-color var(--t-fast) var(--ease);
  }
  .ctx-compact:hover:enabled { background: var(--hover); }
  .ctx-compact:disabled { color: var(--meta); cursor: default; }

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
