<script>
  import { untrack } from "svelte";
  import { orb } from "../lib/store.svelte.js";
  import { fuzzyFilterModels } from "../lib/modelSearch.js";
  import { REASONING } from "../lib/utils.js";

  let query = $state("");
  let providerFilter = $state("");
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

  let allAccounts = $derived(orb.accountsByPeer[peerId] || []);
  let mineProvider = $derived(source.model?.split("/")[0] || "");

  function tightestAcctWindow(acct) {
    if (!acct?.windows?.length) return null;
    return [...acct.windows].sort((x, y) => x.left - y.left)[0];
  }

  function formatReset(resets) {
    if (!resets) return "";
    const d = new Date(resets);
    if (Number.isNaN(d.getTime())) return "";
    const sameDay = d.toDateString() === new Date().toDateString();
    return sameDay
      ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
      : d.toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" });
  }

  let availableProviders = $derived.by(() => {
    const set = new Set((source.models || []).map(id => id.split("/")[0]).filter(Boolean));
    return [...set]
      .sort((x, y) => {
        const xm = Number(x === mineProvider), ym = Number(y === mineProvider);
        if (xm !== ym) return ym - xm;
        const xs = Number(!!providerInfo(x)?.methods?.some(a => a.account));
        const ys = Number(!!providerInfo(y)?.methods?.some(a => a.account));
        if (xs !== ys) return ys - xs;
        return x.localeCompare(y);
      })
      .map(id => {
        const accts = allAccounts.filter(a => a.provider === id);
        const active = accts.find(a => a.active) || accts[0] || null;
        const tight = tightestAcctWindow(active);
        return {
          id,
          label: providerInfo(id)?.name || id,
          badge: tight ? `${Math.round(tight.left)}%` : active?.name || "",
          alarm: !!tight && tight.left < 15,
        };
      });
  });

  let scopedModels = $derived(
    providerFilter
      ? (source.models || []).filter(id => id.split("/")[0] === providerFilter)
      : source.models || [],
  );
  let isSearching = $derived(query.trim().length > 0);
  let filteredModels = $derived(
    fuzzyFilterModels(scopedModels, query, id => providerInfo(id)?.name || ""),
  );

  let usedModels = $derived.by(() => {
    if (isSearching) return [];
    const set = new Set([
      def?.default,
      ...(orb.state.tabs || []).filter(c => c.peer === peerId).map(c => c.model),
    ]);
    return [...set].filter(id => id && filteredModels.includes(id));
  });

  let grouped = $derived.by(() => {
    if (isSearching) return [];
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
  let flatList = $derived(
    isSearching
      ? filteredModels.map(id => ({ id, full: false }))
      : [
          ...usedModels.map(id => ({ id, full: true })),
          ...grouped.flatMap(g => g.models.map(id => ({ id, full: false }))),
        ],
  );

  let focusedProvider = $derived(providerFilter || mineProvider);
  let providerAccounts = $derived(
    allAccounts.filter(a => a.provider === focusedProvider),
  );

  $effect(() => {
    if (orb.modelDeckOpen) {
      query = "";
      providerFilter = "";
      deckAt = -1;
      const pid = peerId;
      if (pid) untrack(() => orb.refreshPeerMeta(pid));
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
          {/if}
          {#if peerId}
            <button
              type="button"
              class="btn plain prov-head-btn"
              onclick={() => { close(); orb.openSettings("providers", peerId); }}
            >
              providers ›
            </button>
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

      {#if availableProviders.length > 1}
        <div class="prov-pills" role="group" aria-label="Filter by provider">
          <button
            type="button"
            class="prov-pill"
            class:on={!providerFilter}
            onclick={() => { providerFilter = ""; deckAt = -1; }}
          >
            all
          </button>
          {#each availableProviders as p (p.id)}
            <button
              type="button"
              class="prov-pill"
              class:on={providerFilter === p.id}
              onclick={() => { providerFilter = providerFilter === p.id ? "" : p.id; deckAt = -1; }}
            >
              <span>{p.label}</span>
              {#if p.badge}
                <small class:alarm={p.alarm}>{p.badge}</small>
              {/if}
            </button>
          {/each}
        </div>
      {/if}

      <div class="deck-list scroll">
        {#if isSearching}
          {#if filteredModels.length > 0}
            <p class="sec-lbl">matches · {filteredModels.length}</p>
            {#each filteredModels as id, idx (id)}
              {@const prov = id.split("/")[0]}
              {@const pLabel = providerInfo(prov)?.name || prov}
              <button
                type="button"
                class="mdl-row"
                class:on={id === source.model}
                class:hi={idx === deckAt}
                onclick={() => chooseModel(id)}
              >
                <span class="mdl-main">
                  <span class="mdl-short">{id.split("/").slice(1).join("/") || id}</span>
                  <small class="mdl-prov">{pLabel}</small>
                </span>
                {#if id === def?.default}<small>default</small>{/if}
                {#if id === source.model}<i class="led round" style="--hue: var(--fg)"></i>{/if}
              </button>
            {/each}
          {/if}
        {:else}
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
        {/if}

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
          <p class="sec-lbl">account · {providerInfo(focusedProvider)?.name || focusedProvider}{whereLabel ? ` · ${whereLabel}` : ""}</p>
          {#each providerAccounts as a (a.id)}
            {@const tight = tightestAcctWindow(a)}
            {@const resetStr = tight ? formatReset(tight.resets) : ""}
            <button
              type="button"
              class="mdl-row acct-row"
              class:on={a.active}
              onclick={() => orb.switchAccount(peerId, a.provider, a.id)}
            >
              <i class="led round" style="--hue: {a.active ? 'var(--fg)' : 'var(--rule)'}"></i>
              <span class="acct-main">
                <span class="acct-name">{a.name}</span>
                {#if a.plan}<small class="acct-plan">{a.plan}</small>{/if}
              </span>
              {#if tight}
                <small class="acct-quota" class:alarm={tight.left < 15}>
                  {Math.round(tight.left)}%{resetStr ? ` · ${resetStr}` : ""}
                </small>
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
    animation: backdrop-in 100ms var(--ease);
  }
  .deck {
    width: min(480px, 100%);
    max-height: min(620px, 84dvh);
    display: flex;
    flex-direction: column;
    padding: 14px;
    border: 1px solid var(--rule);
    border-radius: var(--radius-md);
    background: var(--raised);
    animation: sheet-in 120ms var(--ease);
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
  .head-right {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
  }
  .prov-head-btn {
    font-size: 12px;
    color: var(--mute);
  }
  .prov-head-btn:hover { color: var(--fg); }
  .prov-pills {
    display: flex;
    flex-wrap: wrap;
    gap: 5px;
    margin-bottom: 8px;
  }
  .prov-pill {
    display: inline-flex;
    align-items: baseline;
    gap: 5px;
    padding: 3px 9px;
    border: 1px solid var(--rule-soft);
    border-radius: var(--radius-sm);
    background: var(--bg);
    color: var(--mute);
    font-size: 11.5px;
    font-weight: 600;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .prov-pill small {
    font-size: 10.5px;
    font-weight: 500;
    color: var(--meta);
  }
  .prov-pill small.alarm { color: var(--rupture); font-weight: 700; }
  .prov-pill:hover:not(.on) { border-color: var(--rule-focus); color: var(--fg); }
  .prov-pill.on {
    background: var(--fg);
    border-color: var(--fg);
    color: var(--bg);
  }
  .prov-pill.on small { color: var(--bg); opacity: 0.8; }
  .prov-pill.on small.alarm { color: var(--rupture); opacity: 1; }
  .acct-main {
    display: flex;
    align-items: baseline;
    gap: 7px;
  }
  .acct-name { flex: 0 1 auto !important; }
  .acct-plan {
    flex-shrink: 0;
    font-size: 10.5px;
    color: var(--meta);
  }
  .acct-quota {
    flex-shrink: 0;
    font-size: 11px;
    color: var(--meta);
  }
  .acct-quota.alarm {
    color: var(--rupture);
    font-weight: 700;
  }
  .places {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 10px;
  }
  .place-chip {
    padding: 4px 10px;
    font-size: 12px;
    font-weight: 600;
    border: 1px solid var(--rule);
    border-radius: var(--radius-sm);
    background: var(--raised);
    color: var(--mute);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .place-chip:hover:not(.on) { border-color: var(--rule-focus); color: var(--fg); }
  .place-chip.on {
    color: var(--bg);
    background: var(--fg);
    border-color: var(--fg);
  }
  .deck-filter {
    width: 100%;
    padding: 8px 10px;
    margin-bottom: 8px;
    border: 1px solid var(--rule);
    border-radius: var(--radius-sm);
    background: var(--bg);
    font-size: var(--small);
    transition: border-color var(--t-fast) var(--ease);
  }
  .deck-filter:focus-visible { outline: 0; border-color: var(--rule-focus); }

  .deck-list {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding-right: 2px;
  }
  .sec-lbl {
    margin: 12px 6px 4px;
    font-size: 12px;
    font-weight: 700;
    color: var(--meta);
  }
  .mdl-row {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 7px 9px;
    border: 0;
    border-bottom: 1px solid var(--rule-soft);
    border-radius: 0;
    background: none;
    text-align: left;
    font-size: 12.5px;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .mdl-row span {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .mdl-main {
    display: flex;
    align-items: baseline;
    gap: 8px;
  }
  .mdl-short {
    flex: 0 1 auto !important;
  }
  .mdl-prov {
    flex-shrink: 0;
    font-size: 11px;
    color: var(--meta);
  }
  .mdl-row small {
    font-size: 11px;
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
    border: 1px solid var(--rule);
    border-radius: var(--radius-sm);
    overflow: hidden;
  }
  .lvl-seg button {
    flex: 1;
    padding: 7px 0;
    border: 0;
    border-right: 1px solid var(--rule);
    background: var(--raised);
    font-size: 11.5px;
    font-weight: 700;
    color: var(--mute);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .lvl-seg button:last-child { border-right: 0; }
  .lvl-seg button:hover:not(.on) { color: var(--fg); background: var(--hover); }
  .lvl-seg button.on {
    color: var(--bg);
    background: var(--fg);
  }
</style>
