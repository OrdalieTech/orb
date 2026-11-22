<script>
  import { orb } from "../lib/store.svelte.js";

  let { action, tabId, expandedMap, onToggle } = $props();

  let open = $derived(!!expandedMap.get(action.k));
  let detail = $state(null);
  let loading = $state(false);
  let lastFetchedResult = "";

  $effect(() => {
    if (open && (!detail || action.live || action.result !== lastFetchedResult)) {
      lastFetchedResult = action.result || "";
      loading = !detail;
      orb
        .ask("detail", { tab: tabId, key: action.k })
        .then(d => {
          detail = d || {};
          loading = false;
        })
        .catch(() => {
          loading = false;
        });
    }
  });

  function toggle() {
    onToggle(action.k);
  }
</script>

<div class="act-wrap">
  <button class="act-line" type="button" onclick={toggle} aria-expanded={open}>
    <span class="act-glyph">
      {#if action.live}
        <i class="led round live" style="--hue: var(--rupture)"></i>
      {:else}
        <span class="chev">{open ? "⌄" : "›"}</span>
      {/if}
    </span>
    <span class="act-verb" class:live={action.live}>{action.verb}</span>
    <span class="act-target" title={action.target}>{action.target}</span>
    {#if action.result}
      <span class="act-res" class:failed={action.failed}>{action.result}</span>
    {/if}
  </button>

  {#if open}
    <div class="act-pane">
      {#if loading && !detail}
        <span class="meta">loading…</span>
      {:else if action.verb === "thought"}
        <pre class="pane-pre muted scroll">{detail?.text || ""}</pre>
      {:else if detail?.diff}
        <pre class="pane-pre scroll">{#each detail.diff.trimEnd().split("\n") as line}<span class:add={line[0] === "+"} class:del={line[0] === "-"}>{line}{"\n"}</span>{/each}</pre>
      {:else}
        {#if detail?.args?.trim()}
          <span class="pane-lbl">input</span>
          <pre class="pane-pre muted scroll">{detail.args}</pre>
        {/if}
        <span class="pane-lbl">output</span>
        <pre class="pane-pre scroll" class:alarm={action.failed}>{detail?.output ?? "…"}</pre>
      {/if}
    </div>
  {/if}
</div>

<style>
  .act-wrap {
    display: flex;
    flex-direction: column;
  }
  .act-line {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 3px 4px;
    margin: 0 -4px;
    border: 0;
    border-radius: 0;
    background: none;
    text-align: left;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--mute);
    transition: background-color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .act-line:hover { background: var(--hover); }
  }
  .act-glyph {
    width: 14px;
    display: grid;
    place-items: center;
    flex-shrink: 0;
    color: var(--meta);
  }
  .chev { font-size: 13px; line-height: 1; }
  .act-verb {
    flex-shrink: 0;
    min-width: 64px;
    font-weight: 600;
    color: var(--fg);
  }
  .act-verb.live { color: var(--rupture); }
  .act-target {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .act-res {
    flex-shrink: 1;
    max-width: 42%;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
    font-size: 11.5px;
    color: var(--meta);
  }
  .act-res.failed { color: var(--rupture); font-weight: 600; }
  .act-pane {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 4px 0 6px 18px;
    padding: 9px 11px;
    border: 1px solid var(--fg);
    border-radius: 0;
    background: var(--raised);
    animation: drop-in 90ms var(--ease);
  }
  .pane-lbl {
    font-size: 11.5px;
    font-weight: 700;
    color: var(--meta);
  }
  .pane-pre {
    margin: 0;
    max-height: 45vh;
    overflow: auto;
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.5;
    white-space: pre-wrap;
  }
  .pane-pre.muted { color: var(--mute); }
  .add { color: var(--hue5); }
  .del { color: var(--rupture); }
</style>
