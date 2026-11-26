<script>
  import { orb } from "../lib/store.svelte.js";
  import { ago, hasVisibleText, stripAnsi, tidy, tightestWindow, usageLabel, word } from "../lib/utils.js";
  import VirtualTranscript from "./VirtualTranscript.svelte";
  import PromptBox from "./PromptBox.svelte";

  let { tab, tileId = "" } = $props();

  let transcriptRef = $state(null);
  let expandedSlots = $state({});
  let dismissedActing = $state(false);

  $effect(() => {
    if (!tab.acting) dismissedActing = false;
  });

  let rows = $derived(orb.rowsByTab[tab.id] || []);
  let statusWord = $derived(word(tab));
  let tightWin = $derived(tightestWindow(tab.usage));
  let visibleSlots = $derived((tab.slots || []).filter(s => hasVisibleText(s.lines)));
  let isFocusedPane = $derived(orb.isSplit && orb.activeTileId === tileId);
  let isZoomed = $derived(orb.zoomedTileId === tileId);

  async function openTileMenu(e) {
    e.preventDefault();
    const opts = [
      { label: "split right", detail: "⌘/Alt+D" },
      { label: "split down", detail: "⌘/Alt+Shift+D" },
      ...(orb.isSplit
        ? [
            { label: isZoomed ? "unzoom tile" : "zoom tile", detail: "⌘⇧↩" },
            { label: "even out tiles" },
            { label: "close tile", detail: "Alt+Shift+W" },
          ]
        : []),
    ];
    const what = await orb.pick(tab.title || "tile", opts);
    if (what === "split right") orb.splitActiveTile("right", "", tileId);
    if (what === "split down") orb.splitActiveTile("bottom", "", tileId);
    if (what === "zoom tile" || what === "unzoom tile") orb.toggleZoomTile(tileId);
    if (what === "even out tiles") orb.equalizeTiles();
    if (what === "close tile") orb.closeTile(tileId);
  }

  function showPlan() {
    const u = tab.usage;
    if (!u?.windows?.length) return;
    orb.pick(
      [tab.model?.split("/")[0], u.plan, `read ${ago(u.at)}`].filter(Boolean).join(" · "),
      u.windows.map(w => ({
        label: `${w.name} · ${Math.round(w.left)}% left`,
        detail: w.resets
          ? `resets ${new Date(w.resets).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" })}`
          : "",
      })),
    );
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="chat-pane"
  class:split-focus={isFocusedPane}
>
  <VirtualTranscript
    bind:this={transcriptRef}
    tabId={tab.id}
    {rows}
    earlier={!!tab.earlier}
    loaded={!!tab.loaded}
    asleep={!!tab.asleep}
    busy={!!tab.busy}
    streaming={!!tab.streaming}
    status={tab.status || ""}
    onEarlier={() => orb.send({ do: "earlier", tab: tab.id })}
  />

  <div class="strip-dock">
    <div class="strip" class:busy={tab.busy} oncontextmenu={openTileMenu}>
      <span class="strip-where" title={tidy(tab.cwd || tab.where)}>
        {#if orb.isSplit}
          <i class="led" class:live={tab.busy} class:asks={!!tab.ask} style="--hue: var(--hue{orb.getMachine(tab.peer)?.hue || 0})"></i>
          <b class="strip-title">{tab.title || "new session"}</b>
        {:else if tab.busy || tab.ask}
          <i class="led" class:live={tab.busy} class:asks={!!tab.ask} style="--hue: var(--hue{orb.getMachine(tab.peer)?.hue || 0})"></i>
        {/if}
        <span class="strip-loc">{[tab.remote && tab.where, tab.cwd && tidy(tab.cwd)].filter(Boolean).join(" · ")}</span>
        {#if statusWord}
          <span class="strip-status" class:live={tab.busy} class:asks={!!tab.ask}>{statusWord}</span>
        {/if}
      </span>

      {#if tightWin}
        <button
          type="button"
          class="strip-pill"
          class:alarm={tightWin.left < 15}
          onclick={showPlan}
          title="Activity limit · click for all windows"
        >
          <span class="jp">活動限界</span>
          <span>{usageLabel(tab.usage)}</span>
        </button>
      {/if}

      {#if tab.cost > 0}
        <span class="strip-cost">${tab.cost.toFixed(2)}</span>
      {/if}

      {#if tab.remote}
        <button
          type="button"
          class="strip-term"
          class:on={orb.terminalTab === tab.id}
          onclick={() => (orb.terminalTab = orb.terminalTab === tab.id ? "" : tab.id)}
          title="Remote terminal (Ctrl/Alt+J)"
        >&gt;_</button>
      {/if}

      <div class="strip-tile-tools">
        <button
          type="button"
          class="strip-tile-btn"
          onclick={() => orb.splitActiveTile("right", "", tileId)}
          title="Split right (⌘/Alt+D)"
          aria-label="Split right"
        ><span class="ico-split-h"><i></i><i></i></span></button>
        <button
          type="button"
          class="strip-tile-btn"
          onclick={() => orb.splitActiveTile("bottom", "", tileId)}
          title="Split down (⌘/Alt+Shift+D)"
          aria-label="Split down"
        ><span class="ico-split-v"><i></i><i></i></span></button>
        {#if orb.isSplit}
          <button
            type="button"
            class="strip-tile-btn"
            class:on={isZoomed}
            onclick={() => orb.toggleZoomTile(tileId)}
            title={isZoomed ? "Restore split (⌘⇧↩)" : "Zoom tile (⌘⇧↩)"}
            aria-label={isZoomed ? "Restore split" : "Zoom tile"}
          >{isZoomed ? "⊟" : "⊞"}</button>
          <button
            type="button"
            class="strip-close"
            onclick={() => orb.closeTile(tileId)}
            title="Close tile (Alt+Shift+W)"
            aria-label="Close tile"
          >×</button>
        {/if}
      </div>
    </div>

    {#if tab.acting && !dismissedActing}
      <div class="acting">
        <b class="stretch">PATTERN BLUE</b>
        <p>パターン青 · another Orb is driving this tab</p>
        <div class="acting-actions">
          <button type="button" class="btn" onclick={() => orb.send({ do: "abort", tab: tab.id })}>stop</button>
          <button
            type="button"
            class="acting-dismiss"
            onclick={() => (dismissedActing = true)}
            title="Hide banner (peer keeps running)"
            aria-label="Dismiss banner"
          >×</button>
        </div>
      </div>
    {/if}
  </div>

  <div class="composer-dock">
    {#if visibleSlots.length > 0}
      <div class="slots">
        {#each visibleSlots as s (s.key)}
          {@const open = !!expandedSlots[s.key]}
          <button
            type="button"
            class="slot-line"
            class:alarm={s.alarm}
            class:folded={!open}
            onclick={() => (expandedSlots = { ...expandedSlots, [s.key]: !open })}
          >
            {stripAnsi(s.lines.join("\n"))}
          </button>
        {/each}
      </div>
    {/if}

    <PromptBox {tab} onSent={() => transcriptRef?.scrollToBottom()} />
  </div>
</div>

<style>
  .chat-pane {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    position: relative;
    overflow: hidden;
  }
  .strip-dock {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    z-index: 3;
    pointer-events: none;
    background: linear-gradient(to bottom, var(--bg) 68%, transparent);
    padding-bottom: 10px;
  }
  .strip {
    pointer-events: auto;
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    max-width: calc(780px + 2 * var(--margin));
    margin: 0 auto;
    padding: 7px var(--margin) 4px;
    font-size: 11.5px;
    color: var(--meta);
  }
  .composer-dock {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 3;
    pointer-events: none;
    background: linear-gradient(to bottom, transparent, var(--bg) 34%);
    padding-top: 20px;
  }
  .composer-dock > :global(*) {
    pointer-events: auto;
  }
  .strip.busy .strip-where { color: var(--fg); }
  .strip-where {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 8px;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .strip-loc {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .strip-status {
    flex-shrink: 0;
    color: var(--meta);
  }
  .strip-status.live {
    color: var(--fg);
    font-weight: 600;
  }
  .strip-status.asks {
    color: var(--rupture);
    font-weight: 700;
  }
  .strip-title {
    font-size: 12px;
    color: var(--fg);
    font-weight: 700;
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    flex-shrink: 0;
  }
  .strip-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 2px 6px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    font-size: 11px;
    font-weight: 500;
    color: var(--meta);
    cursor: pointer;
    flex-shrink: 0;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .strip-pill .jp {
    font-size: 10px;
    letter-spacing: 0.04em;
    color: var(--meta);
  }
  .strip-pill.alarm {
    color: var(--rupture);
  }
  .strip-cost {
    font-size: 11.5px;
    font-weight: 500;
    color: var(--meta);
    flex-shrink: 0;
  }
  .strip-term {
    padding: 2px 6px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    font-size: 11.5px;
    font-weight: 600;
    color: var(--meta);
    cursor: pointer;
    flex-shrink: 0;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .strip-term.on {
    color: var(--fg);
    background: var(--recessed);
  }
  .strip-close {
    padding: 0 5px;
    border: 0;
    background: none;
    color: var(--meta);
    font-size: 14px;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .strip-pill:hover, .strip-term:hover:not(.on) {
      background: var(--hover);
      color: var(--fg);
    }
    .strip-pill:hover .jp { color: var(--fg); }
    .strip-close:hover { background: var(--hover); color: var(--fg); }
  }
  .strip-tile-tools {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    flex-shrink: 0;
    opacity: 0.45;
    transition: opacity var(--t-fast) var(--ease);
  }
  .strip:hover .strip-tile-tools,
  .chat-pane.split-focus .strip-tile-tools {
    opacity: 1;
  }
  .strip-tile-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 20px;
    padding: 0;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--meta);
    font-size: 12px;
    cursor: pointer;
    transition: color var(--t-fast) var(--ease), background-color var(--t-fast) var(--ease);
  }
  .strip-tile-btn:hover,
  .strip-tile-btn.on {
    color: var(--fg);
    background: var(--hover);
  }
  .ico-split-h {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1.5px;
    width: 11px;
    height: 9px;
  }
  .ico-split-v {
    display: grid;
    grid-template-rows: 1fr 1fr;
    gap: 1.5px;
    width: 11px;
    height: 9px;
  }
  .ico-split-h i,
  .ico-split-v i {
    border: 1px solid currentColor;
    border-radius: 1px;
  }
  .acting {
    pointer-events: auto;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 16px;
    flex-shrink: 0;
    padding: 10px var(--margin);
    color: var(--paper-ink);
    background: var(--blue);
    border-top: 4px solid;
    border-image: repeating-linear-gradient(45deg, #FFFFFF66 0 6px, transparent 6px 12px) 4;
    animation: drop-in var(--t-med) var(--ease);
  }
  .acting b { font-size: 14px; line-height: 20px; }
  .acting p {
    flex: 1;
    font-size: 12px;
    opacity: 0.85;
  }
  .acting-actions {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    flex-shrink: 0;
  }
  .acting .btn {
    color: var(--blue);
    background: var(--paper-ink);
    border-color: transparent;
  }
  .acting-dismiss {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    padding: 0;
    border: 1px solid #FFFFFF44;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--paper-ink);
    font-size: 16px;
    line-height: 1;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease);
  }
  .acting-dismiss:hover {
    background: #FFFFFF22;
    border-color: #FFFFFF88;
  }
  .slots {
    width: 100%;
    max-width: 752px;
    margin: 0 auto;
    padding: 4px var(--margin) 0;
    flex-shrink: 0;
  }
  .slot-line {
    display: block;
    width: 100%;
    padding: 5px 10px;
    margin: 2px 0;
    border: 1px solid var(--rule);
    border-radius: 0;
    background: var(--recessed);
    text-align: left;
    font-size: 11.5px;
    color: var(--mute);
    white-space: pre-wrap;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease);
  }
  .slot-line:hover { background: var(--hover); }
  .slot-line.folded {
    display: -webkit-box;
    -webkit-line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  @media (max-width: 600px) {
    .strip-pill .jp { display: none; }
  }
</style>
