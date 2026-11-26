<script>
  import TileTree from "./TileTree.svelte";
  import ChatPane from "./ChatPane.svelte";
  import Home from "./Home.svelte";
  import { orb } from "../lib/store.svelte.js";

  let { node } = $props();

  let splitEl = $state(null);
  let dragging = $state(false);
  let dropZone = $state(""); // "", "left", "right", "top", "bottom", "center"

  function startResize(e) {
    if (!splitEl || !node?.dir) return;
    e.preventDefault();
    dragging = true;
    const target = e.currentTarget;
    target.setPointerCapture?.(e.pointerId);
    const rect = splitEl.getBoundingClientRect();
    const isH = node.dir === "h";

    const onMove = ev => {
      const pos = isH ? ev.clientX - rect.left : ev.clientY - rect.top;
      const total = isH ? rect.width : rect.height;
      if (total > 0) orb.setSplitRatio(node.id, pos / total);
    };
    const onUp = () => {
      dragging = false;
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onUp);
  }

  function computeDropZone(e) {
    const rect = e.currentTarget.getBoundingClientRect();
    if (!rect.width || !rect.height) return "center";
    const rx = (e.clientX - rect.left) / rect.width;
    const ry = (e.clientY - rect.top) / rect.height;
    const edge = 0.24;
    if (rx < edge) return "left";
    if (rx > 1 - edge) return "right";
    if (ry < edge) return "top";
    if (ry > 1 - edge) return "bottom";
    return "center";
  }

  function hasOrbTabDrag(e) {
    return Array.from(e.dataTransfer?.types || []).includes("text/orb-tab");
  }

  function onDragOver(e) {
    if (!hasOrbTabDrag(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    dropZone = computeDropZone(e);
  }

  function onDragLeave(e) {
    if (e.currentTarget.contains(e.relatedTarget)) return;
    dropZone = "";
  }

  function onDrop(e) {
    if (!hasOrbTabDrag(e)) return;
    e.preventDefault();
    const tabId = e.dataTransfer.getData("text/orb-tab");
    const zone = dropZone || computeDropZone(e);
    dropZone = "";
    if (tabId) orb.dropTabOnTile(tabId, node.id, zone);
  }
</script>

{#if node?.dir}
  <div
    class="split-box"
    class:vertical={node.dir === "v"}
    class:dragging
    bind:this={splitEl}
  >
    <div class="split-child" style="flex: {node.ratio ?? 0.5} 1 0%">
      <TileTree node={node.a} />
    </div>
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <div
      class="split-gutter"
      class:vertical={node.dir === "v"}
      class:dragging
      role="separator"
      aria-orientation={node.dir === "h" ? "vertical" : "horizontal"}
      title="Drag to resize · double-click to equalize"
      onpointerdown={startResize}
      ondblclick={() => orb.equalizeTiles()}
    ></div>
    <div class="split-child" style="flex: {1 - (node.ratio ?? 0.5)} 1 0%">
      <TileTree node={node.b} />
    </div>
  </div>
{:else if node}
  {@const tab = node.tab ? orb.getTab(node.tab) : null}
  {@const isFocused = orb.isSplit && orb.activeTileId === node.id}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="tile-leaf"
    class:split-leaf={orb.isSplit}
    class:focused={isFocused}
    onmousedown={() => { if (orb.activeTileId !== node.id) orb.focusTile(node.id); }}
    ondragover={onDragOver}
    ondragleave={onDragLeave}
    ondrop={onDrop}
  >
    {#if tab}
      <ChatPane {tab} tileId={node.id} />
    {:else}
      {#if orb.isSplit}
        <div class="home-tile-strip">
          <span class="home-tile-title">new session</span>
          <span class="spacer"></span>
          <button type="button" class="tile-btn" onclick={() => orb.splitActiveTile("right", "", node.id)} title="Split right (⌘/Alt+D)">▯|▯</button>
          <button type="button" class="tile-btn" onclick={() => orb.splitActiveTile("bottom", "", node.id)} title="Split down (⌘/Alt+⇧D)">▭/▭</button>
          <button type="button" class="tile-btn" onclick={() => orb.closeTile(node.id)} title="Close tile (⌘/Alt+⇧W)">×</button>
        </div>
      {/if}
      <Home />
    {/if}

    {#if dropZone}
      <div class="drop-overlay zone-{dropZone}" aria-hidden="true">
        <span>{dropZone === "center" ? "open here" : `split ${dropZone}`}</span>
      </div>
    {/if}
  </div>
{/if}

<style>
  .split-box {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: row;
  }
  .split-box.vertical {
    flex-direction: column;
  }
  .split-box.dragging {
    user-select: none;
  }
  .split-child {
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .split-gutter {
    position: relative;
    flex-shrink: 0;
    width: 1px;
    background: var(--rule);
    cursor: col-resize;
    z-index: 2;
    transition: background-color var(--t-fast) var(--ease);
  }
  .split-gutter::before {
    content: "";
    position: absolute;
    inset: 0 -3px;
  }
  .split-gutter.vertical {
    width: auto;
    height: 1px;
    cursor: row-resize;
  }
  .split-gutter.vertical::before {
    inset: -3px 0;
  }
  .split-gutter:hover,
  .split-gutter.dragging {
    background: var(--fg);
  }
  .tile-leaf {
    position: relative;
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .tile-leaf.split-leaf::before {
    content: "";
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    height: 2px;
    background: transparent;
    pointer-events: none;
    z-index: 4;
    transition: background-color var(--t-fast) var(--ease);
  }
  .tile-leaf.split-leaf.focused::before {
    background: var(--fg);
  }
  .home-tile-strip {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 30px;
    flex-shrink: 0;
    padding: 0 10px;
    border-bottom: 1px solid var(--rule);
    background: var(--bg);
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--meta);
    user-select: none;
  }
  .home-tile-title {
    font-weight: 500;
    color: var(--mute);
  }
  .spacer { flex: 1; }
  .tile-btn {
    border: 0;
    background: none;
    color: var(--meta);
    cursor: pointer;
    padding: 1px 5px;
    font-family: var(--font-mono);
    font-size: 11px;
    border-radius: var(--radius-sm);
    transition: color var(--t-fast) var(--ease), background-color var(--t-fast) var(--ease);
  }
  .tile-btn:hover {
    color: var(--fg);
    background: var(--hover);
  }
  .drop-overlay {
    position: absolute;
    z-index: 12;
    pointer-events: none;
    display: grid;
    place-items: center;
    background: color-mix(in srgb, var(--fg) 8%, transparent);
    border: 1px solid var(--fg);
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 600;
    color: var(--fg);
  }
  .drop-overlay.zone-center { inset: 8px; }
  .drop-overlay.zone-left { top: 6px; bottom: 6px; left: 6px; width: 48%; }
  .drop-overlay.zone-right { top: 6px; bottom: 6px; right: 6px; width: 48%; }
  .drop-overlay.zone-top { left: 6px; right: 6px; top: 6px; height: 48%; }
  .drop-overlay.zone-bottom { left: 6px; right: 6px; bottom: 6px; height: 48%; }
</style>
