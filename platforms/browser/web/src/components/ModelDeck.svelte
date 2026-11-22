<script>
  import { orb } from "../lib/store.svelte.js";
  import { REASONING } from "../lib/utils.js";

  let query = $state("");
  let deckAt = $state(-1);
  let filterEl = $state(null);

  let activeTab = $derived(orb.modelDeckTab || orb.getTab(orb.activeTabId()));
  let aim = $derived.by(() => {
    void orb.aimVersion;
    void orb.home;
    void orb.state;
    return orb.getAim();
  });
  let targetMachine = $derived(activeTab ? orb.getMachine(activeTab.peer) : aim.machine);
  let peerId = $derived(targetMachine?.id || "");
  let whereLabel = $derived(targetMachine ? (targetMachine.self ? "this tab" : targetMachine.name) : "");
  let def = $derived(orb.defaultsByPeer[peerId] || null);
  let source = $derived(
    activeTab ||
      (def ? { model: def.default, models: def.models || [], levels: def.levels || [], thinking: def.thinking } : { models: [] }),
  );
  let providers = $derived(orb.providersByPeer[peerId] || []);
  let places = $derived([...orb.launchers(), ...(orb.getMachine(orb.state.self) ? [orb.getMachine(orb.state.self)] : [])]);

  function providerInfo(id) {
    return providers.find(p => p.id === id);
  }

  let filteredModels = $derived.by(() => {
    const q = query.trim().toLowerCase();
    return (source.models || []).filter(id => {
      if (!q) return true;
      const pName = providerInfo(id.split("/")[0])?.name || "";
      return `${id} ${pName}`.toLowerCase().includes(q);
    });
  });

  let mineProvider = $derived(source.model?.split("/")[0] || "");
  let usedModels = $derived.by(() => {
    const set = new Set([
      def?.default,
      ...(orb.state.tabs || []).filter(c => c.peer === peerId).map(c => c.model),
    ]);
    return [...set].filter(id => id && filteredModels.includes(id));
  });

  let grouped = $derived.by(() => {
    const map = new Map();
    for (const id of filteredModels) {
      const p = id.split("/")[0];
      if (!map.has(p)) map.set(p, []);
      map.get(p).push(id);
    }
    const keys = [...map.keys()].sort((x, y) => {
      const xm = Number(x === mineProvider), ym = Number(y === mineProvider);
      if (xm !== ym) return ym - xm;
      const xs = Number(!!providerInfo(x)?.methods?.some(a => a.account));
      const ys = Number(!!providerInfo(y)?.methods?.some(a => a.account));
      if (xs !== ys) return ys - xs;
      return x.localeCompare(y);
    });
    return keys.map(p => ({ provider: p, info: providerInfo(p), models: map.get(p) }));
  });

  // Flat list of selectable model entries for keyboard ↑/↓/Enter
  let flatList = $derived([
    ...usedModels.map(id => ({ id, full: true })),
    ...grouped.flatMap(g => g.models.map(id => ({ id, full: false }))),
  ]);

  let providerAccounts = $derived(
    (orb.accountsByPeer[peerId] || []).filter(a => a.provider === mineProvider),
  );

  $effect(() => {
    if (orb.modelDeckOpen) {
      query = "";
      deckAt = -1;
      requestAnimationFrame(() => filterEl?.focus());
    }
  });

  function close() {
    orb.modelDeckOpen = false;
    orb.bumpFocus();
  }

  function chooseModel(id) {
    if (activeTab) {
      orb.send({ do: "model", tab: activeTab.id, name: id });
    } else if (peerId) {
      orb.setDefaultModel(peerId, id);
    }
    close();
  }

  function chooseLevel(level) {
    if (activeTab) {
      orb.send({ do: "thinking", tab: activeTab.id, name: level });
    } else if (peerId && source.model) {
      orb.setDefaultModel(peerId, source.model, level);
    }
  }

  function onKeyDown(e) {
    if (e.key === "Escape") {
      e.preventDefault();
      close();
      return;
    }
    if (!flatList.length) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (deckAt < 0) deckAt = e.key === "ArrowDown" ? 0 : flatList.length - 1;
      else deckAt = (deckAt + (e.key === "ArrowDown" ? 1 : -1) + flatList.length) % flatList.length;
    } else if (e.key === "Enter" && deckAt >= 0 && flatList[deckAt]) {
      e.preventDefault();
      chooseModel(flatList[deckAt].id);
    }
  }
</script>

{#if orb.modelDeckOpen}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="backdrop" role="dialog" aria-modal="true" aria-label="Model and reasoning" tabindex="-1" onclick={e => { if (e.target === e.currentTarget) close(); }} onkeydown={onKeyDown}>
    <div class="deck">
      <div class="deck-head">
        <div class="head-left">
          <span class="hud-label"><span class="idx">MDL</span><span>{activeTab ? "model" : `default · ${whereLabel}`}</span></span>
          <strong>{source.model?.split("/").slice(1).join("/") || "none"}</strong>
        </div>
        <div class="head-right">
          {#if activeTab && def?.default && activeTab.model && activeTab.model !== def.default}
            <button type="button" class="btn subtle" onclick={() => orb.setDefaultModel(peerId, activeTab.model, activeTab.thinking)}>
              make default
            </button>
          {:else}
            <span class="meta">{[source.model?.split("/")[0], activeTab && whereLabel].filter(Boolean).join(" · ")}</span>
          {/if}
        </div>
      </div>

      {#if !activeTab && places.length > 1}
        <div class="places">
          {#each places as d (d.id)}
            <button
              type="button"
              class="place-chip"
              class:on={d.id === peerId}
              onclick={() => { orb.aimAt(d.id); orb.refreshPeerMeta(d.id); }}
            >
              {d.self ? "this tab" : d.name}
            </button>
          {/each}
        </div>
      {/if}

      <input
        bind:this={filterEl}
        class="deck-filter"
        bind:value={query}
        oninput={() => (deckAt = query.trim() ? 0 : -1)}
        placeholder="Search models…"
        autocomplete="off"
        spellcheck="false"
      />

      <div class="deck-list scroll">
        {#if usedModels.length > 0}
          <p class="sec-lbl">in use</p>
          {#each usedModels as id, idx (id)}
            <button
              type="button"
              class="mdl-row"
              class:on={id === source.model}
              class:hi={idx === deckAt}
              onclick={() => chooseModel(id)}
            >
              <span>{id}</span>
              {#if id === def?.default}<small>default</small>{/if}
              {#if id === source.model}<i class="led round" style="--hue: var(--fg)"></i>{/if}
            </button>
          {/each}
        {/if}

        {#each grouped as g (g.provider)}
          <p class="sec-lbl">{[g.info?.name || g.provider, g.info?.holds, `${g.models.length}`].filter(Boolean).join(" · ")}</p>
          {#each g.models as id (id)}
            {@const flatIdx = flatList.findIndex((f, i) => i >= usedModels.length && f.id === id)}
            <button
              type="button"
              class="mdl-row"
              class:on={id === source.model}
              class:hi={flatIdx === deckAt}
              onclick={() => chooseModel(id)}
            >
              <span>{id.split("/").slice(1).join("/")}</span>
              {#if id === def?.default}<small>default</small>{/if}
              {#if id === source.model}<i class="led round" style="--hue: var(--fg)"></i>{/if}
            </button>
          {/each}
        {/each}

        {#if filteredModels.length === 0}
          <p class="meta empty-note">{query ? "no model matches" : targetMachine && !def && !activeTab ? "reading models…" : "no models available"}</p>
        {/if}

        {#if peerId && peerId !== orb.state.self}
          <button
            type="button"
            class="btn plain add-prov"
            onclick={() => { close(); orb.openSettings("providers", peerId); }}
          >
            + connect a provider
          </button>
        {/if}
      </div>

      {#if providerAccounts.length > 0}
        <div class="acct-sec">
          <p class="sec-lbl">account · {whereLabel}</p>
          {#each providerAccounts as a (a.id)}
            {@const tight = [...(a.windows || [])].sort((x, y) => x.left - y.left)[0]}
            <button
              type="button"
              class="mdl-row"
              class:on={a.active}
              onclick={() => orb.switchAccount(peerId, a.provider, a.id)}
            >
              <i class="led round" style="--hue: {a.active ? 'var(--fg)' : 'var(--rule)'}"></i>
              <span>{a.name}</span>
              {#if tight}
                <small class:alarm={tight.left < 15}>{tight.name} {Math.round(tight.left)}% left</small>
              {/if}
            </button>
          {/each}
        </div>
      {/if}

      {#if (source.levels || []).length > 0}
        <div class="levels-sec">
          <div class="lvl-head">
            <span class="sec-lbl">reasoning</span>
            <small>{REASONING[source.thinking] || source.thinking || ""}</small>
          </div>
          <div class="lvl-seg">
            {#each source.levels as l (l)}
              <button
                type="button"
                class:on={l === source.thinking}
                onclick={() => chooseLevel(l)}
              >
                {l.replace("minimal", "min").replace("medium", "med")}
              </button>
            {/each}
          </div>
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 20;
    display: grid;
    place-items: center;
    padding: var(--margin);
    background: var(--backdrop);
    backdrop-filter: blur(8px);
    -webkit-backdrop-filter: blur(8px);
    animation: backdrop-in 140ms var(--ease);
  }
  .deck {
    width: min(470px, 100%);
    max-height: min(620px, 84dvh);
    display: flex;
    flex-direction: column;
    padding: 14px;
    border: 1px solid var(--rule);
    border-radius: 14px;
    background: var(--raised);
    box-shadow: 0 24px 64px var(--shade-lg);
    animation: sheet-in 190ms var(--ease);
  }
  .deck-head {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 12px;
    padding: 2px 4px 12px;
  }
  .head-left strong {
    display: block;
    margin-top: 3px;
    font-size: 18px;
    font-weight: 600;
    letter-spacing: -0.025em;
  }
  .places {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 10px;
  }
  .place-chip {
    padding: 4px 11px;
    font-size: 12px;
    font-weight: 500;
    border: 1px solid var(--rule);
    border-radius: var(--pill);
    background: var(--raised);
    color: var(--mute);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .place-chip:hover:not(.on) { border-color: var(--rule-strong); color: var(--fg); }
  .place-chip.on {
    color: var(--bg);
    background: var(--fg);
    border-color: var(--fg);
  }
  .deck-filter {
    width: 100%;
    padding: 9px 12px;
    margin-bottom: 8px;
    border: 1px solid var(--rule);
    border-radius: 9px;
    background: var(--recessed);
    font-size: var(--small);
    transition: border-color var(--t-fast) var(--ease), background-color var(--t-fast) var(--ease);
  }
  .deck-filter:focus-visible { outline: 0; border-color: var(--fg); background: var(--raised); }

  .deck-list {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding-right: 2px;
  }
  .sec-lbl {
    margin: 12px 6px 4px;
    font-family: var(--font-mono);
    font-size: var(--label);
    font-weight: 500;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--meta);
  }
  .mdl-row {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 8px 10px;
    border: 0;
    border-radius: 8px;
    background: none;
    text-align: left;
    font-family: var(--font-mono);
    font-size: 12.5px;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease);
  }
  .mdl-row span {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .mdl-row small {
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--meta);
    flex-shrink: 0;
  }
  .mdl-row.on { font-weight: 600; background: var(--recessed); }
  .mdl-row.hi { background: var(--hover); }
  @media (hover: hover) { .mdl-row:hover { background: var(--hover); } }

  .empty-note { padding: 12px 8px; }
  .add-prov { margin-top: 6px; }

  .acct-sec {
    margin-top: 10px;
    padding-top: 6px;
    border-top: 1px solid var(--rule-soft);
  }
  .levels-sec {
    margin-top: 12px;
    padding-top: 10px;
    border-top: 1px solid var(--rule);
  }
  .lvl-head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    padding: 0 4px 8px;
  }
  .lvl-head .sec-lbl { margin: 0; }
  .lvl-head small { font-size: 12px; color: var(--mute); }
  .lvl-seg {
    display: flex;
    padding: 3px;
    background: var(--recessed);
    border: 1px solid var(--rule-soft);
    border-radius: 9px;
    gap: 2px;
  }
  .lvl-seg button {
    flex: 1;
    padding: 7px 0;
    border: 0;
    border-radius: 6px;
    background: transparent;
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 550;
    color: var(--mute);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease), box-shadow var(--t-fast) var(--ease);
  }
  .lvl-seg button:hover:not(.on) { color: var(--fg); }
  .lvl-seg button.on {
    color: var(--fg);
    background: var(--raised);
    box-shadow: 0 1px 3px var(--shade);
  }
</style>
