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
    <b class="stretch">NEW SESSION</b>
    <i class="card-pip"></i>
    <button type="button" class="kbd-hint" onclick={() => (orb.shortcutsOpen = true)} title="Keyboard shortcuts">
      <kbd>?</kbd> keys
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
    max-width: calc(720px + 2 * var(--margin));
    margin: 0 auto;
    padding: 10px var(--margin) 0;
  }

  /* MAGI hardware machine cards */
  .magi {
    display: flex;
    gap: 6px;
    overflow-x: auto;
    scrollbar-width: none;
    flex-shrink: 0;
  }
  .magi-unit {
    display: flex;
    flex-direction: column;
    gap: 3px;
    flex: 1 0 132px;
    max-width: 220px;
    padding: 7px 9px;
    text-align: left;
    border: 1px solid var(--rule);
    border-radius: var(--soft);
    background: var(--recessed);
    cursor: pointer;
    transition: border-color 110ms, background-color 110ms;
  }
  .magi-unit.on {
    border-color: var(--fg);
    background: var(--raised);
    box-shadow: inset 0 2px 0 var(--fg);
  }
  .magi-top {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .magi-name {
    font-size: var(--label);
    font-weight: 600;
    letter-spacing: .07em;
    text-transform: uppercase;
    color: var(--mute);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .magi-unit.on .magi-name { color: var(--fg); }
  .magi-bot {
    display: flex;
    align-items: center;
    font-size: 11.5px;
    color: var(--mute);
  }
  .magi-bot b {
    margin-left: auto;
    font-size: var(--label);
    letter-spacing: .04em;
  }
  .magi-unit.down .magi-bot { color: var(--rupture); }
  @media (hover: hover) {
    .magi-unit:hover:not(.on) { background: var(--hover); }
  }

  .summary-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 6px 2px 2px;
    flex-shrink: 0;
  }
  .summary { font-size: var(--small); color: var(--meta); }
  .summary.asks { color: var(--rupture); font-weight: 600; }
  .browse-btn { font-size: 12px; color: var(--fg); }

  .launch-line {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 8px;
    margin-top: 4px;
    border: 1px solid var(--rule-soft);
    border-radius: var(--soft);
    background: var(--raised);
    text-align: left;
    font-size: var(--small);
    color: var(--mute);
    cursor: pointer;
    flex-shrink: 0;
  }
  .launch-line .x { margin-left: auto; font-size: 15px; }

  .home-scroll {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    margin: 6px calc(-1 * var(--margin)) 0;
    padding: 4px var(--margin) 16px;
    mask-image: linear-gradient(transparent, #000 8px, #000 calc(100% - 14px), transparent);
  }
  .home-section {
    margin-bottom: 18px;
  }
  .hud-action {
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
    text-transform: none;
    letter-spacing: 0;
  }
  @media (hover: hover) {
    .hud-action:hover { background: var(--hover); color: var(--fg); }
  }

  .open-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(195px, 1fr));
    gap: 7px;
    margin-top: 8px;
  }
  .open-card {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 8px 10px;
    border: 1px solid var(--rule);
    border-radius: var(--pane);
    background: var(--raised);
    cursor: pointer;
    transition: border-color 110ms, background-color 110ms;
  }
  .open-card.busy { border-color: var(--fg); }
  .open-card.asks { border-color: var(--rupture); }
  .open-card.asleep { opacity: .72; }
  @media (hover: hover) {
    .open-card:hover { background: var(--hover); }
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
    font-weight: 600;
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
    opacity: .35;
  }
  .oc-meta {
    font-size: 11.5px;
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
    width: 170px;
    padding: 3px 8px;
    font-size: 12px;
    text-transform: none;
    letter-spacing: 0;
    border: 1px solid var(--rule);
    border-radius: var(--soft);
    background: var(--raised);
  }
  .thread-search:focus-visible { outline: 0; border-color: var(--fg); }

  .entries {
    margin-top: 6px;
  }
  .entry-row {
    display: flex;
    align-items: center;
    gap: 10px;
    width: calc(100% + 14px);
    margin: 0 -7px;
    padding: 9px 8px;
    border: 0;
    border-radius: var(--soft);
    background: none;
    text-align: left;
    font-size: var(--body);
    cursor: pointer;
  }
  .entry-title {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .entry-row.open .entry-title { font-weight: 600; }
  .entry-meta {
    flex-shrink: 0;
    font-size: var(--small);
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
    padding: 18px 0;
  }
  .empty-msg { padding: 18px 0; }

  /* NERV × TE title card above prompt */
  .title-card {
    display: flex;
    align-items: flex-end;
    gap: 8px;
    flex-shrink: 0;
    height: 58px;
    padding: 8px 4px 2px;
    border-top: 1px solid var(--rule);
  }
  .title-card b {
    font-size: 20px;
    line-height: 24px;
    transform: scaleY(1.9);
    transform-origin: 0 100%;
    animation: wipe 260ms 40ms var(--ease) backwards;
  }
  .card-pip {
    width: 7px;
    height: 7px;
    margin-bottom: 3px;
    border-radius: 1px;
    background: var(--rupture);
  }
  .kbd-hint {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 2px 6px;
    border: 0;
    border-radius: var(--soft);
    background: none;
    font-size: 11px;
    color: var(--meta);
    cursor: pointer;
  }
  @media (hover: hover) {
    .kbd-hint:hover { background: var(--hover); color: var(--fg); }
  }
  @media (max-width: 600px) {
    .kbd-hint { display: none; }
  }
</style>
