<script>
  import { orb } from "../lib/store.svelte.js";
  import { named, tailPreview, word } from "../lib/utils.js";

  let tabs = $derived(orb.state.tabs || []);
</script>

<div class="overview scroll">
  <div class="ov-head">
    <div class="hud-label">
      <span class="idx">OV</span>
      <span>live overview · {tabs.length} open</span>
    </div>
    <button type="button" class="btn plain" onclick={() => orb.toggleOverview()}>
      done <kbd>Esc</kbd>
    </button>
  </div>

  <div class="ov-grid">
    {#each tabs as t (t.id)}
      {@const m = orb.getMachine(t.peer)}
      {@const tail = tailPreview(orb.rowsByTab[t.id], 9)}
      {@const st = t.ask ? `asks · ${t.ask.title || "approval"}` : word(t) || t.model?.split("/").slice(1).join("/") || ""}
      <div
        class="ov-card"
        class:asks={!!t.ask}
        class:busy={t.busy}
        class:asleep={t.asleep}
        role="button"
        tabindex="0"
        onclick={() => orb.focus(t.id)}
        onkeydown={e => { if (e.key === "Enter") orb.focus(t.id); }}
        oncontextmenu={e => { e.preventDefault(); orb.openTabMenu(t); }}
      >
        <div class="ov-top">
          <i class="led" class:live={t.busy} class:asks={!!t.ask} style="--hue: var(--hue{m?.hue || 0})"></i>
          <span class="ov-name">{named(t.title)}</span>
          {#if t.unread > 0}
            <span class="ov-unread">{t.unread}</span>
          {/if}
          <button
            type="button"
            class="ov-x"
            aria-label="Close"
            onclick={e => { e.stopPropagation(); orb.closeTab(t); }}
          >×</button>
        </div>

        <div class="ov-sub" class:alarm={!!t.ask}>
          {[t.remote ? t.where : "this tab", st].filter(Boolean).join(" · ")}
        </div>

        <div class="ov-tail">
          <pre>{tail || (t.asleep ? "asleep · a message wakes it" : t.loaded ? "ready" : "loading…")}</pre>
        </div>

        {#if t.context > 0}
          <div class="ov-foot">
            <span class="ov-bar">
              <i class:alarm={t.context > 0.8} style="width:{Math.min(100, Math.round(t.context * 100))}%"></i>
            </span>
            <small>{Math.round(t.context * 100)}%</small>
          </div>
        {/if}
      </div>
    {/each}

    <button type="button" class="ov-new" onclick={() => orb.focus("")}>
      <span>+ new session</span>
      <kbd>Alt+N</kbd>
    </button>
  </div>
</div>

<style>
  .overview {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 20px var(--margin) 32px;
    animation: view-in var(--t-med) var(--ease);
  }
  .ov-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    max-width: 1120px;
    margin: 0 auto 16px;
  }
  .ov-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(255px, 1fr));
    gap: 12px;
    max-width: 1120px;
    margin: 0 auto;
  }
  .ov-card {
    display: flex;
    flex-direction: column;
    height: 226px;
    padding: 12px 13px;
    border: 1px solid var(--rule);
    border-radius: var(--pane);
    background: var(--raised);
    cursor: pointer;
    transition:
      border-color var(--t-fast) var(--ease),
      transform 80ms var(--ease);
  }
  .ov-card.busy { border-color: var(--fg); }
  .ov-card.asks { border-color: var(--rupture); }
  .ov-card.asleep { opacity: 0.58; }
  .ov-card:active { transform: translateY(1px); }
  @media (hover: hover) {
    .ov-card:hover { border-color: var(--fg); }
  }
  .ov-top {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .ov-name {
    flex: 1;
    min-width: 0;
    font-size: var(--small);
    font-weight: 600;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .ov-unread {
    padding: 0 5px;
    border-radius: 2px;
    font-size: 10px;
    font-weight: 700;
    background: var(--rupture);
    color: #FFFFFF;
  }
  .ov-x {
    padding: 0 4px;
    border: 0;
    background: none;
    color: var(--meta);
    font-size: 15px;
    cursor: pointer;
    transition: color var(--t-fast) var(--ease);
  }
  .ov-x:hover { color: var(--fg); }
  .ov-sub {
    margin-top: 3px;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--meta);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .ov-tail {
    flex: 1;
    min-height: 0;
    margin-top: 10px;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    mask-image: linear-gradient(transparent, #000 32%);
  }
  .ov-tail pre {
    margin: 0;
    font: inherit;
    font-size: 11.5px;
    line-height: 1.45;
    color: var(--mute);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .ov-foot {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 10px;
    padding-top: 8px;
    border-top: 1px solid var(--rule);
    font-size: 10.5px;
    font-weight: 600;
    color: var(--meta);
  }
  .ov-bar {
    width: 54px;
    height: 5px;
    background: var(--recessed);
    border: 1px solid var(--rule);
    border-radius: 0;
    overflow: hidden;
  }
  .ov-bar i {
    display: block;
    height: 100%;
    background: var(--fg);
    border-radius: 0;
    transition: width 200ms var(--ease);
  }
  .ov-bar i.alarm { background: var(--rupture); }
  .ov-new {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    min-height: 100px;
    border: 1px dashed var(--rule-strong);
    border-radius: var(--pane);
    background: transparent;
    color: var(--mute);
    font-size: var(--small);
    font-weight: 500;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .ov-new:hover { background: var(--hover); border-color: var(--fg); color: var(--fg); }
  }
</style>
