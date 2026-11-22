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
    max-width: calc(760px + 2 * var(--margin));
    margin: 0 auto;
    padding: 14px var(--margin) 0;
    animation: view-in var(--t-med) var(--ease);
  }

  /* High-contrast MAGI × TE hardware module cards */
  .magi {
    display: flex;
    gap: 8px;
    overflow-x: auto;
    scrollbar-width: none;
    flex-shrink: 0;
  }
  .magi-unit {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 5px;
    flex: 1 0 148px;
    max-width: 230px;
    padding: 9px 11px;
    text-align: left;
    border: 1px solid var(--rule);
    border-radius: var(--pane);
    background: var(--raised);
    cursor: pointer;
    transition:
      border-color var(--t-fast) var(--ease),
      background-color var(--t-fast) var(--ease),
      color var(--t-fast) var(--ease),
      transform 80ms var(--ease);
  }
  .magi-unit.on {
    border-color: var(--fg);
    background: var(--fg);
    color: var(--bg);
  }
  .magi-unit:active { transform: translateY(1px); }
  .magi-top {
    display: flex;
    align-items: center;
    gap: 7px;
    min-width: 0;
  }
  .magi-name {
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: inherit;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .magi-bot {
    display: flex;
    align-items: center;
    font-size: 11.5px;
    color: var(--meta);
  }
  .magi-unit.on .magi-bot { color: color-mix(in srgb, var(--bg) 75%, transparent); }
  .magi-bot b {
    margin-left: auto;
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.06em;
    color: var(--mute);
  }
  .magi-unit.on .magi-bot b { color: var(--rupture); }
  .magi-unit.down .magi-bot { color: var(--rupture); }
  @media (hover: hover) {
    .magi-unit:hover:not(.on) {
      border-color: var(--fg);
    }
  }

  .summary-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 2px 4px;
    flex-shrink: 0;
  }
  .summary {
    font-size: 11.5px;
    font-weight: 500;
    color: var(--meta);
  }
  .summary.asks { color: var(--rupture); font-weight: 700; }
  .browse-btn { font-size: 12px; color: var(--fg); }

  .launch-line {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 7px 10px;
    margin-top: 6px;
    border: 1px solid var(--fg);
    border-radius: var(--soft);
    background: var(--raised);
    text-align: left;
    font-size: var(--small);
    color: var(--fg);
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
    padding: 6px var(--margin) 18px;
    mask-image: linear-gradient(transparent, #000 8px, #000 calc(100% - 14px), transparent);
  }
  .home-section {
    margin-bottom: 22px;
  }
  .hud-action {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 2px 6px;
    border: 1px solid transparent;
    border-radius: var(--soft);
    background: none;
    font-size: 11px;
    font-weight: 600;
    color: var(--mute);
    cursor: pointer;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .hud-action:hover { border-color: var(--rule); background: var(--raised); color: var(--fg); }
  }

  .open-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
    gap: 8px;
    margin-top: 10px;
  }
  .open-card {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 10px 11px;
    border: 1px solid var(--rule);
    border-radius: var(--pane);
    background: var(--raised);
    cursor: pointer;
    transition:
      border-color var(--t-fast) var(--ease),
      transform 80ms var(--ease);
  }
  .open-card.busy { border-color: var(--fg); }
  .open-card.asks { border-color: var(--rupture); }
  .open-card.asleep { opacity: 0.62; }
  .open-card:active { transform: translateY(1px); }
  @media (hover: hover) {
    .open-card:hover { border-color: var(--fg); }
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
    font-weight: 700;
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
    font-size: 11px;
    color: var(--meta);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .oc-tail {
    font-size: 11.5px;
    color: var(--mute);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }

  .thread-search {
    margin-left: auto;
    width: 190px;
    padding: 3px 9px;
    font-size: 12px;
    text-transform: none;
    letter-spacing: 0;
    border: 1px solid var(--rule);
    border-radius: var(--soft);
    background: var(--raised);
    transition: border-color var(--t-fast) var(--ease);
  }
  .thread-search:focus-visible { outline: 0; border-color: var(--fg); }

  .entries {
    margin-top: 8px;
    border-top: 1px solid var(--rule);
  }
  .entry-row {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 9px 8px;
    border: 0;
    border-bottom: 1px solid var(--rule-soft);
    border-radius: 0;
    background: transparent;
    text-align: left;
    font-size: var(--body);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), padding-left var(--t-fast) var(--ease);
  }
  .entry-title {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .entry-row.open .entry-title { font-weight: 700; }
  .entry-meta {
    flex-shrink: 0;
    font-size: 11.5px;
    color: var(--meta);
  }
  .entry-meta.asks { color: var(--rupture); font-weight: 700; }
  @media (hover: hover) {
    .entry-row:hover {
      background: var(--hover);
      padding-left: 12px;
    }
  }
  .reading {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 20px 0;
  }
  .empty-msg { padding: 20px 0; }

  /* Crisp TE high-contrast session deck bar */
  .title-card {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
    padding: 10px 2px 4px;
    border-top: 1px solid var(--fg);
  }
  .card-idx {
    padding: 1px 5px;
    border-radius: 2px;
    background: var(--fg);
    color: var(--bg);
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.06em;
  }
  .card-title {
    font-size: 11.5px;
    font-weight: 700;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--fg);
  }
  .card-pip {
    width: 6px;
    height: 6px;
    border-radius: 1px;
    background: var(--rupture);
  }
  .kbd-hint {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 2px 6px;
    border: 0;
    border-radius: var(--soft);
    background: none;
    font-size: 11px;
    color: var(--mute);
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
