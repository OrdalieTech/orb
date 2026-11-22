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
    padding: 16px var(--margin) 28px;
  }
  .ov-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    max-width: 1120px;
    margin: 0 auto 14px;
  }
  .ov-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
    gap: 10px;
    max-width: 1120px;
    margin: 0 auto;
  }
  .ov-card {
    display: flex;
    flex-direction: column;
    height: 224px;
    padding: 10px 12px;
    border: 1px solid var(--rule);
    border-radius: var(--pane);
    background: var(--raised);
    cursor: pointer;
    transition: border-color 140ms, transform 120ms var(--ease);
  }
  .ov-card.busy { border-color: var(--fg); }
  .ov-card.asks { border-color: var(--rupture); box-shadow: 0 0 0 1px color-mix(in srgb, var(--rupture) 35%, transparent); }
  .ov-card.asleep { opacity: .66; }
  @media (hover: hover) {
    .ov-card:hover { background: var(--hover); }
  }
  .ov-top {
    display: flex;
    align-items: center;
    gap: 7px;
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
    border-radius: 3px;
    font-size: 10px;
    font-weight: 700;
    background: var(--fg);
    color: var(--bg);
  }
  .ov-x {
    padding: 0 4px;
    border: 0;
    background: none;
    color: var(--meta);
    font-size: 15px;
    cursor: pointer;
  }
  .ov-sub {
    margin-top: 2px;
    font-size: 11px;
    color: var(--meta);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .ov-tail {
    flex: 1;
    min-height: 0;
    margin-top: 8px;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    mask-image: linear-gradient(transparent, #000 30%);
  }
  .ov-tail pre {
    margin: 0;
    font: inherit;
    font-size: 11.5px;
    line-height: 1.4;
    color: var(--mute);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .ov-foot {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 8px;
    padding-top: 6px;
    border-top: 1px solid var(--rule-soft);
    font-size: 10.5px;
    color: var(--meta);
  }
  .ov-bar {
    width: 48px;
    height: 4px;
    background: var(--recessed);
    border: 1px solid var(--rule-soft);
    border-radius: 2px;
    overflow: hidden;
  }
  .ov-bar i {
    display: block;
    height: 100%;
    background: var(--fg);
  }
  .ov-bar i.alarm { background: var(--rupture); }
  .ov-new {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 8px;
    min-height: 92px;
    border: 1px dashed var(--rule);
    border-radius: var(--pane);
    background: none;
    color: var(--mute);
    font-size: var(--small);
    font-weight: 600;
    cursor: pointer;
  }
  @media (hover: hover) {
    .ov-new:hover { background: var(--hover); border-color: var(--fg); color: var(--fg); }
  }
</style>
