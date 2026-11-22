<script>
  import { orb } from "../lib/store.svelte.js";
  import { crumbs, tidy } from "../lib/utils.js";

  let peerId = $derived(orb.explorerPeer);
  let machine = $derived(orb.getMachine(peerId));

  let dir = $state({ path: "", parent: "", dirs: [] });
  let filter = $state("");
  let loading = $state(false);
  let err = $state("");
  let activeIdx = $state(0);
  let inputEl = $state(null);

  let loadedPeer = "";
  $effect(() => {
    if (peerId && peerId !== loadedPeer) {
      loadedPeer = peerId;
      readDir("");
    }
    if (!peerId) loadedPeer = "";
  });

  async function readDir(path) {
    loading = true;
    err = "";
    filter = "";
    activeIdx = 0;
    try {
      const r = await orb.ask("dirs", { machine: peerId, cwd: path });
      dir = r || { path: path || "~", parent: "", dirs: [] };
      requestAnimationFrame(() => inputEl?.focus());
    } catch (e) {
      err = e.message || "could not list folders";
    } finally {
      loading = false;
    }
  }

  let filteredDirs = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    const list = dir.dirs || [];
    if (!q) return list;
    return list.filter(d => d.name.toLowerCase().includes(q));
  });

  let trail = $derived.by(() => {
    const c = crumbs(dir.path);
    if (c.length <= 4) return c;
    return [c[0], { label: "…", path: c[c.length - 3].path }, ...c.slice(-2)];
  });

  function joinPath(base, name) {
    return base.endsWith("/") ? `${base}${name}` : `${base}/${name}`;
  }

  function selectFolder(path) {
    if (!path) return;
    orb.aimAt(peerId, path);
    orb.explorerPeer = "";
    orb.focus("");
  }

  function close() {
    orb.explorerPeer = "";
  }

  function onKeyDown(e) {
    if (e.key === "Escape") {
      e.preventDefault();
      close();
      return;
    }
    if (e.key === "ArrowDown" && filteredDirs.length) {
      e.preventDefault();
      activeIdx = (activeIdx + 1) % filteredDirs.length;
    } else if (e.key === "ArrowUp" && filteredDirs.length) {
      e.preventDefault();
      activeIdx = (activeIdx - 1 + filteredDirs.length) % filteredDirs.length;
    } else if (e.key === "Backspace" && !filter && dir.parent) {
      e.preventDefault();
      readDir(dir.parent);
    } else if (e.key === "Enter") {
      e.preventDefault();
      const t = filter.trim();
      if (t.startsWith("/") || t.startsWith("~")) {
        readDir(t);
      } else if (filteredDirs[activeIdx]) {
        readDir(joinPath(dir.path, filteredDirs[activeIdx].name));
      } else if (dir.path) {
        selectFolder(dir.path);
      }
    }
  }
</script>

{#if peerId && machine}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="backdrop" role="dialog" aria-modal="true" aria-label="Browse folders" tabindex="-1" onclick={e => { if (e.target === e.currentTarget) close(); }} onkeydown={onKeyDown}>
    <div class="panel">
      <div class="exp-head">
        <div class="hud-label">
          <span class="idx">DIR</span>
          <span>{machine.name}</span>
        </div>
        <button type="button" class="btn plain" onclick={close}><kbd>Esc</kbd></button>
      </div>

      <div class="crumbs scroll">
        {#each trail as cr, i}
          {#if i > 0 && trail[i - 1].label !== "/"}<span class="sep">/</span>{/if}
          <button
            type="button"
            class="crumb"
            class:last={i === trail.length - 1}
            onclick={() => readDir(cr.path)}
          >{cr.label}</button>
        {/each}
      </div>

      <input
        bind:this={inputEl}
        class="exp-input"
        bind:value={filter}
        placeholder="filter · or type a path, ↩"
        autocomplete="off"
        spellcheck="false"
      />

      <div class="exp-list scroll">
        {#if err}
          <p class="meta alarm pad">{err}</p>
        {/if}

        {#if dir.parent && !filter}
          <button type="button" class="dir-row" onclick={() => readDir(dir.parent)}>
            <span>..</span>
            <small>parent</small>
          </button>
        {/if}

        {#each filteredDirs as item, i (item.name)}
          {@const full = joinPath(dir.path, item.name)}
          <button
            type="button"
            class="dir-row"
            class:hi={i === activeIdx}
            onclick={() => readDir(full)}
            ondblclick={() => selectFolder(full)}
          >
            <span class="dir-name">{item.name}/</span>
            {#if item.git}<span class="git-pill">git</span>{/if}
          </button>
        {/each}

        {#if loading}
          <p class="meta pad">reading…</p>
        {:else if !err && filteredDirs.length === 0}
          <p class="meta pad">{filter ? "no folder matches · ↩ opens path" : "no subfolders"}</p>
        {/if}

        {#if !filter && tidy(dir.path) === "~" && (machine.folders || []).length > 0}
          <div class="hud-label rec-hdr"><span class="idx">·</span><span>recent folders</span></div>
          {#each machine.folders.slice(0, 5) as f (f.cwd)}
            <button type="button" class="dir-row" onclick={() => selectFolder(f.cwd)}>
              <span class="dir-name">{tidy(f.cwd)}</span>
              <small>{f.threads} thread{f.threads === 1 ? "" : "s"}</small>
            </button>
          {/each}
        {/if}
      </div>

      <div class="exp-foot">
        <span class="meta">click opens · double-click selects</span>
        <button type="button" class="btn inverted" disabled={!dir.path} onclick={() => selectFolder(dir.path)}>
          use this folder
        </button>
      </div>
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
  .panel {
    width: min(480px, 100%);
    max-height: min(580px, 82dvh);
    display: flex;
    flex-direction: column;
    border: 1px solid var(--rule);
    border-radius: var(--radius-md);
    background: var(--raised);
    overflow: hidden;
    animation: sheet-in 120ms var(--ease);
  }
  .exp-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 14px 4px;
  }
  .crumbs {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 2px 14px 10px;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .sep { color: var(--meta); font-size: 12px; opacity: 0.6; }
  .crumb {
    padding: 2px 6px;
    border: 0;
    border-radius: 0;
    background: none;
    font-size: 12px;
    color: var(--mute);
    cursor: pointer;
    white-space: nowrap;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .crumb.last { color: var(--fg); font-weight: 700; }
  @media (hover: hover) { .crumb:hover { background: var(--fg); color: var(--bg); } }

  .exp-input {
    margin: 0 14px 10px;
    padding: 8px 10px;
    border: 1px solid var(--rule);
    border-radius: var(--radius-sm);
    background: var(--bg);
    font-size: var(--small);
    transition: border-color var(--t-fast) var(--ease);
  }
  .exp-input:focus-visible { outline: 0; border-color: var(--rule-focus); }

  .exp-list {
    flex: 1;
    min-height: 120px;
    overflow-y: auto;
    padding: 0 10px 10px;
  }
  .dir-row {
    display: flex;
    align-items: center;
    gap: 8px;
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
  .dir-row.hi { background: var(--fg); color: var(--bg); }
  @media (hover: hover) { .dir-row:hover:not(.hi) { background: var(--hover); } }
  .dir-name { flex: 1; min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  .dir-row small { color: var(--meta); font-size: 11px; flex-shrink: 0; }
  .git-pill {
    padding: 0 5px;
    border: 1px solid var(--rule);
    border-radius: 0;
    font-size: 11px;
    font-weight: 700;
    color: var(--amber);
    background: var(--bg);
  }
  .rec-hdr { padding: 14px 10px 5px; }
  .pad { padding: 10px; }

  .exp-foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 10px 14px;
    border-top: 1px solid var(--rule);
    background: var(--bg);
  }
</style>
