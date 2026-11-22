<script>
  import { orb } from "../lib/store.svelte.js";
  import { ago, named, tailPreview, tidy, word } from "../lib/utils.js";
  import PromptBox from "./PromptBox.svelte";

  let threadFilter = $state("");

  let aim = $derived.by(() => {
    void orb.aimVersion;
    void orb.home;
    void orb.state;
    return orb.getAim();
  });

  let showOwn = $derived(orb.launchers().length === 0 || !!aim.machine?.self);
  let machines = $derived(
    [...(orb.home.machines || [])]
      .filter(m => showOwn || !m.self)
      .sort((x, y) => Number(!!y.self) - Number(!!x.self)),
  );
  let openTabs = $derived.by(() => {
    const rank = t => (t.ask ? 0 : t.busy ? 1 : t.unread > 0 ? 2 : !t.asleep ? 3 : 4);
    return [...(orb.state.tabs || [])].sort((a, b) => rank(a) - rank(b));
  });
  let selectedMachine = $derived(orb.deviceFilter ? orb.getMachine(orb.deviceFilter) : null);
  let reading = $derived(
    (orb.home.machines || []).filter(m => m.reading && (!orb.deviceFilter || m.id === orb.deviceFilter)),
  );
  let entries = $derived.by(() => {
    const q = threadFilter.trim().toLowerCase();
    return (orb.home.entries || []).filter(e => {
      if (orb.deviceFilter && e.machine !== orb.deviceFilter) return false;
      if (!showOwn && e.machine === orb.state.self && !e.open) return false;
      if (!q) return true;
      const m = orb.getMachine(e.machine);
      return `${e.title || ""} ${e.cwd || ""} ${m?.name || ""}`.toLowerCase().includes(q);
    });
  });

  let launching = $derived(orb.state.launching || "");
  let starting = $derived(launching.startsWith("starting"));
</script>

<div class="home">
  <!-- 01 · MAGI machine unit strip -->
  <div class="magi scroll">
    {#each machines as m (m.id)}
      {@const up = m.self ? orb.state.up : m.connected}
      {@const ok = up && !m.outdated}
      <button
        type="button"
        class="magi-unit"
        class:on={orb.deviceFilter === m.id}
        class:down={!ok}
        onclick={() => (orb.deviceFilter = orb.deviceFilter === m.id ? "" : m.id)}
        oncontextmenu={e => { e.preventDefault(); orb.openDeviceMenu(m); }}
        title="Click to filter · Right-click for options"
      >
        <span class="magi-top">
          <i class="led" style="--hue: var(--hue{m.hue || 0})"></i>
          <span class="magi-name">{m.self ? "this tab" : m.name}</span>
        </span>
        <span class="magi-bot">
          <span>{!up ? "offline" : m.outdated ? "update Orb" : `${m.running?.length || 0} running`}</span>
          <b>{ok ? "承認" : "否決"}</b>
        </span>
      </button>
    {/each}
  </div>

  <div class="summary-bar">
    <p class="summary" class:asks={(orb.state.summary || "").startsWith("needs")}>{orb.state.summary || ""}</p>
    {#if selectedMachine && !selectedMachine.self && selectedMachine.launch && selectedMachine.connected}
      <button
        type="button"
        class="btn plain browse-btn"
        onclick={() => (orb.explorerPeer = selectedMachine.id)}
      >
        folders on {selectedMachine.name} ›
      </button>
    {/if}
  </div>

  {#if launching && launching !== orb.dismissedLaunch}
    <button
      type="button"
      class="launch-line"
      class:alarm={!starting}
      onclick={() => { if (!starting) orb.dismissedLaunch = launching; }}
    >
      <i class="led round" class:live={starting} style="--hue: {starting ? 'var(--mute)' : 'var(--rupture)'}"></i>
      <span>{launching}</span>
      {#if !starting}<span class="x">×</span>{/if}
    </button>
  {/if}

  <div class="home-scroll scroll">
    <!-- Live open sessions cards (Desktop openCards style) -->
    {#if openTabs.length > 0 && !threadFilter}
      <section class="home-section">
        <div class="hud-label">
          <span class="idx">01</span>
          <span>open · {openTabs.length}</span>
          <button type="button" class="hud-action" onclick={() => orb.toggleOverview()}>
            overview <kbd>Alt+G</kbd>
          </button>
        </div>
        <div class="open-grid">
          {#each openTabs.slice(0, 6) as t (t.id)}
            {@const m = orb.getMachine(t.peer)}
            {@const tail = tailPreview(orb.rowsByTab[t.id], 1)}
            <div
              class="open-card"
              class:asks={!!t.ask}
              class:busy={t.busy}
              class:asleep={t.asleep}
              role="button"
              tabindex="0"
              onclick={() => orb.focus(t.id)}
              onkeydown={e => { if (e.key === "Enter") orb.focus(t.id); }}
              oncontextmenu={e => { e.preventDefault(); orb.openTabMenu(t); }}
            >
              <div class="oc-head">
                <i class="led" class:live={t.busy} class:asks={!!t.ask} style="--hue: var(--hue{m?.hue || 0})"></i>
                <span class="oc-title">{named(t.title)}</span>
                <button
                  type="button"
                  class="oc-x"
                  onclick={e => { e.stopPropagation(); orb.closeTab(t); }}
                  aria-label="Close"
                >×</button>
              </div>
              <div class="oc-meta" class:alarm={!!t.ask}>
                {[t.remote && t.where, t.cwd && tidy(t.cwd), t.ask ? "asks" : word(t) || (t.unread ? `${t.unread} new` : "")].filter(Boolean).join(" · ")}
              </div>
              {#if tail}
                <div class="oc-tail">{tail}</div>
              {/if}
            </div>
          {/each}
        </div>
      </section>
    {/if}

    <!-- Unified threads list -->
    <section class="home-section">
      <div class="hud-label">
        <span class="idx">{openTabs.length > 0 && !threadFilter ? "02" : "01"}</span>
        <span>threads · {entries.length}</span>
        {#if (orb.home.entries || []).length > 10}
          <input
            class="thread-search"
            type="search"
            bind:value={threadFilter}
            placeholder="filter threads…"
            aria-label="Filter threads"
          />
        {/if}
      </div>

      <div class="entries">
        {#each entries.slice(0, 200) as e (e.key)}
          {@const m = orb.getMachine(e.machine)}
          {@const followed = orb.getTab(e.tab)}
          {@const age = followed ? word(followed) : ""}
          <button
            type="button"
            class="entry-row"
            class:open={e.open}
            onclick={() => (followed ? orb.focus(followed.id) : orb.openEntry(e.key))}
            oncontextmenu={ev => { ev.preventDefault(); orb.openThreadMenu(e); }}
          >
            <i class="led" class:live={e.live} class:asks={e.asks} style="--hue: var(--hue{m?.hue || 0})"></i>
            <span class="entry-title">{named(e.title)}</span>
            <small class="entry-meta" class:asks={e.asks}>
              {[
                !orb.deviceFilter && m && (m.self ? "this tab" : m.name),
                e.asks ? "asks" : e.live ? "working" : age || (e.unstored ? "open" : ago(e.modified)),
              ]
                .filter(Boolean)
                .join(" · ")}
            </small>
          </button>
        {/each}

        {#if (orb.home.machines || []).length === 0}
          <p class="meta reading"><i class="led live"></i> reading conversations…</p>
        {:else if reading.length > 0}
          <p class="meta reading"><i class="led live"></i> reading {reading.map(m => m.name).join(", ")}…</p>
        {:else if entries.length === 0}
          <p class="meta empty-msg">{threadFilter ? `no thread matches "${threadFilter}"` : "no conversations here yet"}</p>
        {/if}
      </div>
    </section>
  </div>

  <div class="title-card">
    <span class="card-idx">00</span>
    <span class="card-title">New session</span>
    <i class="card-pip" aria-hidden="true"></i>
    <button type="button" class="kbd-hint" onclick={() => (orb.shortcutsOpen = true)} title="Keyboard shortcuts">
      <kbd>?</kbd> shortcuts
    </button>
  </div>

  <PromptBox tab={null} />
</div>

<style>
  .home {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    width: 100%;
    max-width: calc(740px + 2 * var(--margin));
    margin: 0 auto;
    padding: 16px var(--margin) 0;
    animation: view-in var(--t-med) var(--ease);
  }

  /* Field instrument machine strip */
  .magi {
    display: flex;
    gap: 8px;
    overflow-x: auto;
    scrollbar-width: none;
    flex-shrink: 0;
    padding-bottom: 2px;
  }
  .magi-unit {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 5px;
    flex: 1 0 144px;
    max-width: 230px;
    padding: 10px 12px;
    text-align: left;
    border: 1px solid var(--rule);
    border-radius: 10px;
    background: var(--raised);
    box-shadow: 0 1px 2px var(--shade);
    cursor: pointer;
    overflow: hidden;
    transition:
      transform var(--t-fast) var(--ease),
      border-color var(--t-fast) var(--ease),
      background-color var(--t-fast) var(--ease),
      box-shadow var(--t-med) var(--ease);
  }
  .magi-unit::after {
    content: "";
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 2px;
    background: var(--rupture);
    transform: scaleX(0);
    transform-origin: left center;
    transition: transform var(--t-med) var(--ease);
  }
  .magi-unit.on {
    border-color: var(--rule-strong);
    box-shadow: 0 3px 10px var(--shade);
  }
  .magi-unit.on::after {
    transform: scaleX(1);
  }
  .magi-unit:active { transform: scale(0.985); }
  .magi-top {
    display: flex;
    align-items: center;
    gap: 7px;
    min-width: 0;
  }
  .magi-name {
    font-size: 12.5px;
    font-weight: 600;
    letter-spacing: -0.01em;
    color: var(--fg);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .magi-bot {
    display: flex;
    align-items: center;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--meta);
  }
  .magi-bot b {
    margin-left: auto;
    font-size: 10px;
    font-weight: 500;
    letter-spacing: 0.04em;
    color: var(--meta);
  }
  .magi-unit.down .magi-bot { color: var(--rupture); }
  @media (hover: hover) {
    .magi-unit:hover {
      border-color: var(--rule-strong);
      transform: translateY(-1px);
      box-shadow: 0 4px 12px var(--shade);
    }
  }

  .summary-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 10px 2px 4px;
    flex-shrink: 0;
  }
  .summary {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--meta);
  }
  .summary.asks { color: var(--rupture); font-weight: 600; }
  .browse-btn { font-size: 12px; color: var(--fg); }

  .launch-line {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 12px;
    margin-top: 6px;
    border: 1px solid var(--rule);
    border-radius: var(--soft);
    background: var(--raised);
    text-align: left;
    font-size: var(--small);
    color: var(--mute);
    cursor: pointer;
    flex-shrink: 0;
    animation: drop-in var(--t-fast) var(--ease);
  }
  .launch-line .x { margin-left: auto; font-size: 15px; }

  .home-scroll {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    margin: 8px calc(-1 * var(--margin)) 0;
    padding: 6px var(--margin) 20px;
    mask-image: linear-gradient(transparent, #000 10px, #000 calc(100% - 16px), transparent);
  }
  .home-section {
    margin-bottom: 24px;
  }
  .hud-action {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 2px 7px;
    border: 0;
    border-radius: var(--soft);
    background: none;
    font-family: var(--font-sans);
    font-size: 11.5px;
    font-weight: 500;
    color: var(--mute);
    cursor: pointer;
    text-transform: none;
    letter-spacing: 0;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .hud-action:hover { background: var(--hover); color: var(--fg); }
  }

  .open-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(205px, 1fr));
    gap: 8px;
    margin-top: 10px;
  }
  .open-card {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 11px 12px;
    border: 1px solid var(--rule);
    border-radius: 10px;
    background: var(--raised);
    box-shadow: 0 1px 2px var(--shade);
    cursor: pointer;
    transition:
      transform var(--t-fast) var(--ease),
      border-color var(--t-fast) var(--ease),
      box-shadow var(--t-med) var(--ease);
  }
  .open-card.busy { border-color: var(--rule-strong); }
  .open-card.asks {
    border-color: color-mix(in srgb, var(--rupture) 55%, transparent);
    box-shadow: 0 0 0 2px var(--rupture-soft);
  }
  .open-card.asleep { opacity: 0.68; }
  .open-card:active { transform: scale(0.985); }
  @media (hover: hover) {
    .open-card:hover {
      border-color: var(--rule-strong);
      transform: translateY(-1px);
      box-shadow: 0 4px 12px var(--shade);
    }
    .open-card:hover .oc-x { opacity: 1; }
  }
  .oc-head {
    display: flex;
    align-items: center;
    gap: 7px;
  }
  .oc-title {
    flex: 1;
    min-width: 0;
    font-size: var(--small);
    font-weight: 550;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .oc-x {
    padding: 0 4px;
    border: 0;
    background: none;
    color: var(--meta);
    font-size: 14px;
    cursor: pointer;
    opacity: 0;
    transition: opacity var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .oc-x:hover { color: var(--fg); }
  .oc-meta {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--meta);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .oc-tail {
    font-size: 12px;
    color: var(--mute);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }

  .thread-search {
    margin-left: auto;
    width: 185px;
    padding: 4px 10px;
    font-family: var(--font-sans);
    font-size: 12px;
    text-transform: none;
    letter-spacing: 0;
    border: 1px solid var(--rule);
    border-radius: var(--soft);
    background: var(--raised);
    transition: border-color var(--t-fast) var(--ease), box-shadow var(--t-fast) var(--ease);
  }
  .thread-search:focus-visible {
    outline: 0;
    border-color: var(--fg);
    box-shadow: 0 0 0 3px var(--hover);
  }

  .entries {
    margin-top: 8px;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .entry-row {
    display: flex;
    align-items: center;
    gap: 11px;
    width: calc(100% + 16px);
    margin: 0 -8px;
    padding: 9px 10px;
    border: 0;
    border-radius: 8px;
    background: transparent;
    text-align: left;
    font-size: var(--body);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), transform 100ms var(--ease);
  }
  .entry-row:active { transform: scale(0.995); }
  .entry-title {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .entry-row.open .entry-title { font-weight: 550; }
  .entry-meta {
    flex-shrink: 0;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--meta);
  }
  .entry-meta.asks { color: var(--rupture); font-weight: 600; }
  @media (hover: hover) {
    .entry-row:hover { background: var(--hover); }
  }
  .reading {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 20px 0;
  }
  .empty-msg { padding: 20px 0; }

  /* Clean TE session deck header above prompt */
  .title-card {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
    padding: 12px 4px 4px;
    border-top: 1px solid var(--rule);
  }
  .card-idx {
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 600;
    color: var(--rupture);
    letter-spacing: 0.04em;
  }
  .card-title {
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 600;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--fg);
  }
  .card-pip {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: var(--rupture);
  }
  .kbd-hint {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 3px 7px;
    border: 0;
    border-radius: var(--soft);
    background: none;
    font-size: 11.5px;
    color: var(--meta);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .kbd-hint:hover { background: var(--hover); color: var(--fg); }
  }
  @media (max-width: 600px) {
    .kbd-hint { display: none; }
  }
</style>
