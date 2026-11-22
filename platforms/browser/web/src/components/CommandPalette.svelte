<script>
  import { orb } from "../lib/store.svelte.js";
  import { named, SHORTCUT_GROUPS } from "../lib/utils.js";

  // 1. Universal Cmd/Ctrl+K Command Palette
  let palQuery = $state("");
  let palAt = $state(0);
  let palInput = $state(null);

  let paletteItems = $derived.by(() => {
    if (!orb.paletteOpen) return [];
    const q = palQuery.trim().toLowerCase();
    const items = [
      { group: "navigate", label: "Home", detail: "all devices & threads", kbd: "Alt+H", run: () => orb.focus("") },
      { group: "navigate", label: "Overview grid", detail: "live cards of open tabs", kbd: "Alt+G", run: () => orb.toggleOverview() },
      { group: "navigate", label: "Toggle split view", detail: "two conversations side by side", kbd: "Alt+D", run: () => orb.toggleSplit() },
      { group: "navigate", label: "Model & reasoning deck", detail: "switch model or effort", kbd: "Alt+M", run: () => orb.openModelDeck() },
      { group: "navigate", label: "Settings · General", detail: "theme, defaults, shortcuts", kbd: "⌘,", run: () => orb.openSettings("general", orb.defaultPeer()) },
      { group: "navigate", label: "Settings · Providers", detail: "accounts, quotas, sign-in", run: () => orb.openSettings("providers", orb.defaultPeer()) },
      { group: "navigate", label: "Settings · Plugins", detail: "toggle capabilities", run: () => orb.openSettings("plugins", orb.defaultPeer()) },
      { group: "navigate", label: "Settings · Mitosis", detail: "devices, sync group, updates", run: () => orb.openSettings("mitosis", orb.defaultPeer()) },
      { group: "navigate", label: "Keyboard shortcuts", detail: "full reference", kbd: "?", run: () => (orb.shortcutsOpen = true) },
      { group: "navigate", label: `Theme: ${orb.theme}`, detail: "cycle auto → light → dark", run: () => orb.applyTheme({ auto: "light", light: "dark", dark: "auto" }[orb.theme] || "auto") },
      ...(orb.state.tabs || []).map(c => ({
        group: "open",
        label: named(c.title),
        detail: c.where,
        hue: orb.getMachine(c.peer)?.hue || 0,
        run: () => orb.focus(c.id),
      })),
      ...(orb.launchers() || []).map(m => ({
        group: "devices",
        label: `Browse folders on ${m.name}`,
        detail: "remote folder explorer",
        hue: m.hue,
        run: () => (orb.explorerPeer = m.id),
      })),
      ...(orb.home.entries || [])
        .filter(e => !e.open && (e.machine !== orb.state.self || orb.launchers().length === 0))
        .slice(0, 120)
        .map(e => ({
          group: "threads",
          label: named(e.title),
          detail: orb.getMachine(e.machine)?.name || "",
          hue: orb.getMachine(e.machine)?.hue || 0,
          run: () => orb.openEntry(e.key),
        })),
    ];
    if (!q) return items.slice(0, 40);
    return items.filter(it => `${it.label} ${it.detail || ""} ${it.group}`.toLowerCase().includes(q)).slice(0, 40);
  });

  $effect(() => {
    if (orb.paletteOpen) {
      palQuery = "";
      palAt = 0;
      requestAnimationFrame(() => palInput?.focus());
    }
  });

  function onPalKey(e) {
    if (e.key === "Escape") {
      e.preventDefault();
      orb.paletteOpen = false;
      orb.bumpFocus();
    } else if (e.key === "ArrowDown" && paletteItems.length) {
      e.preventDefault();
      palAt = (palAt + 1) % paletteItems.length;
    } else if (e.key === "ArrowUp" && paletteItems.length) {
      e.preventDefault();
      palAt = (palAt - 1 + paletteItems.length) % paletteItems.length;
    } else if (e.key === "Enter" && paletteItems[palAt]) {
      e.preventDefault();
      const fn = paletteItems[palAt].run;
      orb.paletteOpen = false;
      fn();
    }
  }

  // 2. Generic Picker / Field modal (used for menus, confirmations, rename)
  let pickFilter = $state("");
  let pickAt = $state(0);
  let pickInput = $state(null);

  let pickShown = $derived.by(() => {
    const pk = orb.pickerState;
    if (!pk || pk.field) return [];
    const q = pickFilter.trim().toLowerCase();
    return (pk.options || []).filter(o => `${o.label} ${o.detail || ""}`.toLowerCase().includes(q));
  });

  $effect(() => {
    const pk = orb.pickerState;
    if (pk) {
      pickFilter = pk.field ? pk.value || "" : "";
      pickAt = Math.max(0, (pk.options || []).findIndex(o => o.on));
      requestAnimationFrame(() => pickInput?.focus());
    }
  });

  function onPickKey(e) {
    const pk = orb.pickerState;
    if (!pk) return;
    if (e.key === "Escape") {
      e.preventDefault();
      pk.resolve(null);
    } else if (!pk.field && (e.key === "ArrowDown" || e.key === "ArrowUp") && pickShown.length) {
      e.preventDefault();
      pickAt = (pickAt + (e.key === "ArrowDown" ? 1 : -1) + pickShown.length) % pickShown.length;
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (pk.field) pk.resolve(pickFilter);
      else if (pickShown[pickAt]) pk.resolve(pickShown[pickAt].value ?? pickShown[pickAt].label);
    }
  }

  // 3. Zoomable Image Viewer
  let imgZoom = $state(1);
  $effect(() => {
    if (orb.viewerImage) imgZoom = 1;
  });
</script>

<!-- Command Palette (Cmd/Ctrl+K) -->
{#if orb.paletteOpen}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="backdrop" role="dialog" aria-modal="true" aria-label="Command palette" tabindex="-1" onclick={e => { if (e.target === e.currentTarget) orb.paletteOpen = false; }} onkeydown={onPalKey}>
    <div class="sheet wide">
      <div class="sheet-head">
        <span class="hud-label"><span class="idx">GO</span><span>command palette</span></span>
        <kbd>Esc</kbd>
      </div>
      <input
        bind:this={palInput}
        class="sheet-in"
        bind:value={palQuery}
        oninput={() => (palAt = 0)}
        placeholder="Jump to conversation, thread, device, or action…"
        autocomplete="off"
        spellcheck="false"
      />
      <div class="sheet-list scroll">
        {#each paletteItems as it, i}
          <button
            type="button"
            class="sheet-row"
            class:on={i === palAt}
            onclick={() => { orb.paletteOpen = false; it.run(); }}
          >
            {#if it.hue !== undefined}
              <i class="led" style="--hue: var(--hue{it.hue})"></i>
            {/if}
            <span class="sr-lbl">{it.label}</span>
            {#if it.detail}<small class="sr-det">{it.detail}</small>{/if}
            {#if it.kbd}<kbd>{it.kbd}</kbd>{/if}
          </button>
        {/each}
        {#if paletteItems.length === 0}
          <p class="meta pad">nothing matches</p>
        {/if}
      </div>
    </div>
  </div>
{/if}

<!-- Generic Picker / Rename Sheet -->
{#if orb.pickerState}
  {@const pk = orb.pickerState}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="backdrop" role="dialog" aria-modal="true" aria-label={pk.title} tabindex="-1" onclick={e => { if (e.target === e.currentTarget) pk.resolve(null); }} onkeydown={onPickKey}>
    <div class="sheet">
      <div class="sheet-head">
        <span class="hud-label"><span class="idx">·</span><span>{pk.title}</span></span>
        <kbd>Esc</kbd>
      </div>
      {#if pk.field || (pk.options || []).length >= 8}
        <input
          bind:this={pickInput}
          class="sheet-in"
          bind:value={pickFilter}
          oninput={() => { if (!pk.field) pickAt = 0; }}
          placeholder={pk.hint || (pk.field ? "" : "type to filter")}
          autocomplete="off"
          spellcheck="false"
        />
      {/if}
      {#if pk.field}
        <div class="field-actions">
          <button type="button" class="btn subtle" onclick={() => pk.resolve(null)}>cancel</button>
          <button type="button" class="btn inverted" onclick={() => pk.resolve(pickFilter)}>save</button>
        </div>
      {:else}
        <div class="sheet-list scroll">
          {#each pickShown as o, i}
            <button
              type="button"
              class="sheet-row"
              class:on={i === pickAt}
              onclick={() => pk.resolve(o.value ?? o.label)}
            >
              <span class="sr-lbl">{o.label}</span>
              {#if o.detail}<small class="sr-det">{o.detail}</small>{/if}
            </button>
          {/each}
        </div>
      {/if}
    </div>
  </div>
{/if}

<!-- Keyboard Shortcuts Reference (?) -->
{#if orb.shortcutsOpen}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="backdrop" role="dialog" aria-modal="true" aria-label="Keyboard shortcuts" tabindex="-1" onclick={e => { if (e.target === e.currentTarget) orb.shortcutsOpen = false; }} onkeydown={e => { if (e.key === "Escape" || e.key === "?") orb.shortcutsOpen = false; }}>
    <div class="sheet wide">
      <div class="sheet-head">
        <span class="hud-label"><span class="idx">KBD</span><span>keyboard shortcuts</span></span>
        <button type="button" class="btn plain" onclick={() => (orb.shortcutsOpen = false)}><kbd>Esc</kbd></button>
      </div>
      <div class="kbd-grid scroll">
        {#each SHORTCUT_GROUPS as g}
          <div class="kbd-group">
            <div class="hud-label"><span class="idx">{g.index}</span><span>{g.title}</span></div>
            {#each g.items as [key, desc]}
              <div class="kbd-row">
                <kbd>{key}</kbd>
                <span>{desc}</span>
              </div>
            {/each}
          </div>
        {/each}
      </div>
    </div>
  </div>
{/if}

<!-- Zoomable Image Viewer -->
{#if orb.viewerImage}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="img-backdrop"
    role="dialog"
    aria-modal="true"
    aria-label="Image viewer"
    tabindex="-1"
    onclick={e => { if (e.target === e.currentTarget) orb.viewerImage = null; }}
    onkeydown={e => {
      if (e.key === "Escape") orb.viewerImage = null;
      if (e.key === "+" || e.key === "=") imgZoom = Math.min(4, imgZoom + 0.5);
      if (e.key === "-") imgZoom = Math.max(0.5, imgZoom - 0.5);
      if (e.key === "0") imgZoom = 1;
    }}
    onwheel={e => { e.preventDefault(); imgZoom = Math.max(0.5, Math.min(4, imgZoom + (e.deltaY < 0 ? 0.2 : -0.2))); }}
  >
    <div class="img-bar">
      <button type="button" class="btn" onclick={() => (imgZoom = Math.max(0.5, imgZoom - 0.5))}>−</button>
      <button type="button" class="btn" onclick={() => (imgZoom = 1)}>{Math.round(imgZoom * 100)}%</button>
      <button type="button" class="btn" onclick={() => (imgZoom = Math.min(4, imgZoom + 0.5))}>+</button>
      <button type="button" class="btn inverted" onclick={() => (orb.viewerImage = null)}>close</button>
    </div>
    <img
      src={orb.viewerImage.src}
      alt=""
      style="transform: scale({imgZoom});"
      ondblclick={() => (imgZoom = imgZoom > 1 ? 1 : 2)}
    />
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 25;
    display: grid;
    place-items: center;
    padding: var(--margin);
    background: var(--backdrop);
    backdrop-filter: blur(8px);
    -webkit-backdrop-filter: blur(8px);
    animation: backdrop-in 140ms var(--ease);
  }
  .sheet {
    width: min(460px, 100%);
    max-height: min(560px, 82dvh);
    display: flex;
    flex-direction: column;
    padding: 14px;
    border: 1px solid var(--rule);
    border-radius: 14px;
    background: var(--raised);
    box-shadow: 0 24px 64px var(--shade-lg);
    animation: sheet-in 190ms var(--ease);
  }
  .sheet.wide { width: min(580px, 100%); }
  .sheet-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 2px 4px 12px;
  }
  .sheet-in {
    width: 100%;
    margin-bottom: 10px;
    padding: 9px 12px;
    border: 1px solid var(--rule);
    border-radius: 9px;
    background: var(--recessed);
    font-size: var(--small);
    transition: border-color var(--t-fast) var(--ease), background-color var(--t-fast) var(--ease);
  }
  .sheet-in:focus-visible { outline: 0; border-color: var(--fg); background: var(--raised); }
  .sheet-list {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
  }
  .sheet-row {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 9px 10px;
    border: 0;
    border-radius: 8px;
    background: none;
    text-align: left;
    font-size: var(--small);
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease);
  }
  .sheet-row.on { background: var(--hover); }
  @media (hover: hover) { .sheet-row:hover { background: var(--hover); } }
  .sr-lbl {
    flex-shrink: 0;
    max-width: 65%;
    font-weight: 500;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .sr-det {
    flex: 1;
    min-width: 0;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--meta);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .field-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 6px;
  }
  .pad { padding: 10px 8px; }

  .kbd-grid {
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 18px;
    padding: 4px;
  }
  .kbd-group {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .kbd-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 6px 0;
    border-bottom: 1px solid var(--rule-soft);
    font-size: var(--small);
    color: var(--mute);
  }
  .kbd-row:last-child { border-bottom: 0; }

  .img-backdrop {
    position: fixed;
    inset: 0;
    z-index: 50;
    display: grid;
    place-items: center;
    background: rgba(10, 11, 12, 0.88);
    backdrop-filter: blur(10px);
    overflow: auto;
    animation: backdrop-in 150ms var(--ease);
  }
  .img-bar {
    position: fixed;
    top: 16px;
    right: 16px;
    z-index: 2;
    display: flex;
    gap: 6px;
  }
  .img-backdrop img {
    max-width: 92vw;
    max-height: 88dvh;
    border-radius: 8px;
    transition: transform 160ms var(--ease);
    cursor: zoom-in;
  }
</style>
