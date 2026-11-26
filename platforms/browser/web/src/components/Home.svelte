<script>
  import { orb } from "../lib/store.svelte.js";
  import { ago, base, named, tailPreview, tidy, word } from "../lib/utils.js";
  import PromptBox from "./PromptBox.svelte";

  let threadFilter = $state("");
  let folderFilter = $state("");
  let showHiddenPeers = $state(false);

  let aim = $derived.by(() => {
    void orb.aimVersion;
    void orb.home;
    void orb.state;
    return orb.getAim();
  });

  let showOwn = $derived(orb.launchers().length === 0 || !!aim.machine?.self);
  let allMachines = $derived(
    [...(orb.home.machines || [])]
      .filter(m => showOwn || !m.self)
      .sort((x, y) => Number(!!y.self) - Number(!!x.self)),
  );
  let hiddenCount = $derived(allMachines.filter(m => orb.isPeerHidden(m.id)).length);
  let machines = $derived(
    showHiddenPeers ? allMachines : allMachines.filter(m => !orb.isPeerHidden(m.id)),
  );
  let openTabs = $derived.by(() => {
    const rank = t => (t.ask ? 0 : t.busy ? 1 : t.unread > 0 ? 2 : !t.asleep ? 3 : 4);
    return [...(orb.state.tabs || [])].sort((a, b) => rank(a) - rank(b));
  });
  let selectedMachine = $derived(orb.deviceFilter ? orb.getMachine(orb.deviceFilter) : null);
  let scopeMachine = $derived(selectedMachine || aim.machine);
  let scopeFolders = $derived((scopeMachine?.folders || []).filter(f => f.cwd).slice(0, 8));
  let reading = $derived(
    (orb.home.machines || []).filter(m => m.reading && (!orb.deviceFilter || m.id === orb.deviceFilter)),
  );
  let entries = $derived.by(() => {
    const q = threadFilter.trim().toLowerCase();
    return (orb.home.entries || []).filter(e => {
      if (orb.deviceFilter && e.machine !== orb.deviceFilter) return false;
      if (folderFilter && e.cwd !== folderFilter) return false;
      if (!showOwn && e.machine === orb.state.self && !e.open) return false;
      if (!q) return true;
      const m = orb.getMachine(e.machine);
      return `${e.title || ""} ${e.cwd || ""} ${m?.name || ""}`.toLowerCase().includes(q);
    });
  });

  function selectPeer(m) {
    if (orb.deviceFilter === m.id) {
      orb.deviceFilter = "";
      folderFilter = "";
      return;
    }
    const sameAim = aim.machine?.id === m.id;
    const nextCwd = sameAim ? aim.cwd || "" : "";
    orb.deviceFilter = m.id;
    if (!sameAim) folderFilter = "";
    orb.aimAt(m.id, nextCwd);
  }

  function selectFolder(cwd) {
    if (!scopeMachine) return;
    const next = folderFilter === cwd ? "" : cwd;
    folderFilter = next;
    orb.aimAt(scopeMachine.id, next);
  }

  let launching = $derived(orb.state.launching || "");
  let starting = $derived(launching.startsWith("starting"));
</script>

<div class="home">
  <!-- 01 · MAGI machine unit grid -->
  <div class="magi">
    {#each machines as m (m.id)}
      {@const up = m.self ? orb.state.up : m.connected}
      {@const ok = up && !m.outdated}
      {@const isHidden = orb.isPeerHidden(m.id)}
      {@const folderCount = m.folders?.length || 0}
      <button
        type="button"
        class="magi-unit"
        class:on={orb.deviceFilter === m.id}
        class:down={!ok}
        class:hidden-peer={isHidden}
        onclick={() => selectPeer(m)}
        oncontextmenu={e => { e.preventDefault(); orb.openDeviceMenu(m); }}
        title="Click to filter · Right-click for options"
      >
        <span class="magi-top">
          <i class="led" style="--hue: var(--hue{m.hue || 0})"></i>
          <span class="magi-name">{m.self ? "this tab" : m.name}</span>
          <b class="magi-verdict">{ok ? "承認" : "否決"}</b>
        </span>
        <span class="magi-bot">
          <span>
            {!up ? "offline" : m.outdated ? "update Orb" : `${m.running?.length || 0} running`}{folderCount > 0 ? ` · ${folderCount} ${folderCount === 1 ? "folder" : "folders"}` : ""}
          </span>
        </span>
      </button>
    {/each}
    <button
      type="button"
      class="magi-unit magi-pair"
      onclick={() => orb.pairDevicePrompt()}
      title="Pair another device with a mitosis code"
    >
      <span class="magi-pair-title">+ pair device</span>
      <span class="magi-pair-sub">paste code · mitosis</span>
    </button>
  </div>

  <div class="summary-bar">
    <p class="summary" class:asks={(orb.state.summary || "").startsWith("needs")}>{orb.state.summary || ""}</p>
    <div class="summary-actions">
      {#if hiddenCount > 0}
        <button
          type="button"
          class="hidden-toggle"
          onclick={() => (showHiddenPeers = !showHiddenPeers)}
        >
          {hiddenCount} hidden · {showHiddenPeers ? "hide" : "show"}
        </button>
      {/if}
      {#if selectedMachine && !selectedMachine.self && selectedMachine.launch && selectedMachine.connected && scopeFolders.length === 0}
        <button
          type="button"
          class="btn plain browse-btn"
          onclick={() => (orb.explorerPeer = selectedMachine.id)}
        >
          folders on {selectedMachine.name} ›
        </button>
      {/if}
    </div>
  </div>

  {#if scopeMachine && scopeFolders.length > 0}
    <div class="folder-strip scroll" role="group" aria-label="Folder scope">
      <button
        type="button"
        class="folder-pill"
        class:on={!folderFilter}
        onclick={() => { folderFilter = ""; orb.aimAt(scopeMachine.id, ""); }}
      >
        all
      </button>
      {#each scopeFolders as f (f.cwd)}
        <button
          type="button"
          class="folder-pill"
          class:on={folderFilter === f.cwd}
          onclick={() => selectFolder(f.cwd)}
          title={tidy(f.cwd)}
        >
          <span>{base(f.cwd) || tidy(f.cwd)}</span>
          {#if f.threads}<small>{f.threads}</small>{/if}
        </button>
      {/each}
      {#if !scopeMachine.self && scopeMachine.launch && scopeMachine.connected}
        <button
          type="button"
          class="folder-pill browse"
          onclick={() => (orb.explorerPeer = scopeMachine.id)}
        >
          browse ›
        </button>
      {/if}
    </div>
  {/if}

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
            overview <kbd>^G</kbd>
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
    max-width: calc(780px + 2 * var(--margin));
    margin: 0 auto;
    padding: 14px var(--margin) 0;
    animation: view-in var(--t-med) var(--ease);
  }

  /* NERV MAGI multi-line wrapping peer grid — no horizontal scroll */
  .magi {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(188px, 1fr));
    gap: 8px;
    flex-shrink: 0;
  }
  .magi-unit {
    position: relative;
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    gap: 8px;
    min-height: 70px;
    padding: 11px 13px;
    text-align: left;
    border: 1px solid var(--rule-soft);
    border-radius: 8px;
    background: var(--raised);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .magi-unit.hidden-peer {
    opacity: 0.58;
    border-style: dashed;
  }
  .magi-unit.on {
    background: var(--recessed);
    border-color: var(--fg);
    color: var(--fg);
  }
  .magi-top {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .magi-name {
    font-size: 13px;
    font-weight: 700;
    color: inherit;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .magi-verdict {
    margin-left: auto;
    flex-shrink: 0;
    font-size: 10.5px;
    font-weight: 700;
    letter-spacing: 0.06em;
    color: var(--amber);
  }
  .magi-bot {
    display: flex;
    align-items: center;
    font-size: 11.5px;
    color: var(--meta);
    min-width: 0;
  }
  .magi-bot span {
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .magi-unit.on .magi-bot { color: var(--mute); }
  .magi-unit.down .magi-bot,
  .magi-unit.down .magi-verdict { color: var(--rupture); }
  .magi-pair {
    justify-content: center;
    align-items: center;
    gap: 3px;
    text-align: center;
    border-style: dashed;
    background: transparent;
    color: var(--mute);
  }
  .magi-pair-title {
    font-size: 12.5px;
    font-weight: 600;
    color: var(--mute);
  }
  .magi-pair-sub {
    font-size: 11px;
    color: var(--meta);
  }
  @media (hover: hover) {
    .magi-unit:hover:not(.on) {
      background: var(--hover);
      border-color: var(--rule);
    }
    .magi-pair:hover .magi-pair-title {
      color: var(--fg);
    }
  }

  .summary-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 7px 0 4px;
    flex-shrink: 0;
  }
  .summary-actions {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    flex-shrink: 0;
  }
  .hidden-toggle {
    padding: 2px 6px;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    font-size: 11.5px;
    font-weight: 500;
    color: var(--meta);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .hidden-toggle:hover {
    background: var(--hover);
    color: var(--fg);
  }
  .summary {
    font-size: 12px;
    font-weight: 500;
    color: var(--meta);
  }
  .summary.asks { color: var(--rupture); font-weight: 700; }
  .browse-btn { font-size: 12px; color: var(--fg); }

  .folder-strip {
    display: flex;
    align-items: center;
    gap: 4px;
    overflow-x: auto;
    scrollbar-width: none;
    padding: 2px 0 6px;
    flex-shrink: 0;
  }
  .folder-pill {
    display: inline-flex;
    align-items: baseline;
    gap: 5px;
    flex-shrink: 0;
    padding: 3px 9px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--meta);
    font-size: 12px;
    font-weight: 500;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .folder-pill small {
    font-size: 10.5px;
    color: var(--meta);
    opacity: 0.8;
  }
  .folder-pill.on {
    background: var(--recessed);
    color: var(--fg);
    font-weight: 600;
  }
  .folder-pill.on small { color: var(--mute); }
  .folder-pill.browse { color: var(--mute); }
  @media (hover: hover) {
    .folder-pill:hover:not(.on) {
      background: var(--hover);
      color: var(--fg);
    }
  }

  .launch-line {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 10px;
    margin-top: 4px;
    border: 1px solid var(--rule);
    border-radius: var(--radius-sm);
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
    margin: 6px calc(-1 * var(--margin)) 0;
    padding: 6px var(--margin) 18px;
    mask-image: linear-gradient(transparent, #000 8px, #000 calc(100% - 14px), transparent);
  }
  .home-section {
    margin-bottom: 20px;
  }
  .hud-action {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 1px 6px;
    border: 0;
    background: none;
    font-size: 12px;
    font-weight: 500;
    color: var(--mute);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .hud-action:hover { background: var(--fg); color: var(--bg); }
  }

  /* Flush joined console cells for open sessions — zero Cardocalypse */
  .open-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
    gap: 0;
    margin-top: 0;
    border-left: 1px solid var(--rule);
    border-right: 1px solid var(--rule);
  }
  .open-card {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 9px 11px;
    border: 0;
    border-right: 1px solid var(--rule-soft);
    border-bottom: 1px solid var(--rule);
    background: var(--raised);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease);
  }
  .open-card.busy {
    background: var(--recessed);
  }
  .open-card.asks {
    background: var(--rupture);
    color: #FFFFFF;
  }
  .open-card.asks .oc-meta,
  .open-card.asks .oc-tail,
  .open-card.asks .oc-x { color: #FFFFFF; }
  .open-card.asleep { opacity: 0.58; }
  @media (hover: hover) {
    .open-card:hover:not(.asks) { background: var(--hover); }
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
    transition: opacity var(--t-fast) var(--ease);
  }
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
    padding: 2px 8px;
    font-size: 12px;
    text-transform: none;
    letter-spacing: 0;
    border: 1px solid var(--rule);
    border-radius: var(--radius-sm);
    background: var(--bg);
    transition: border-color var(--t-fast) var(--ease);
  }
  .thread-search:focus-visible { outline: 0; border-color: var(--rule-focus); }

  .entries {
    margin-top: 0;
  }
  .entry-row {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 8px 6px;
    border: 0;
    border-bottom: 1px solid var(--rule-soft);
    background: transparent;
    text-align: left;
    font-size: var(--body);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
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
    font-size: 12px;
    color: var(--meta);
  }
  .entry-meta.asks { color: var(--rupture); font-weight: 700; }
  @media (hover: hover) {
    .entry-row:hover {
      background: var(--hover);
      color: var(--fg);
    }
  }
  .reading {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 18px 0;
  }
  .empty-msg { padding: 18px 0; }

  /* Authentic NERV Evangelion stretched title card */
  .title-card {
    display: flex;
    align-items: flex-end;
    gap: 9px;
    flex-shrink: 0;
    height: 54px;
    padding: 8px 2px 3px;
    border-top: 1px solid var(--rule);
  }
  .title-card b {
    font-size: 20px;
    line-height: 24px;
    letter-spacing: 0.04em;
    transform: scaleY(1.85);
    transform-origin: 0 100%;
    animation: wipe 220ms 30ms var(--ease) backwards;
  }
  .kbd-hint {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 2px 6px;
    border: 0;
    background: none;
    font-size: 11px;
    color: var(--mute);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .kbd-hint:hover { background: var(--fg); color: var(--bg); }
  }
  @media (max-width: 600px) {
    .kbd-hint { display: none; }
  }
</style>
