<script>
  import { untrack } from "svelte";
  import { orb } from "../lib/store.svelte.js";

  let { action, tabId, expandedMap, onToggle } = $props();

  let open = $derived(!!expandedMap.get(action.k));
  let detail = $state(null);
  let loading = $state(false);
  let copied = $state(false);
  let lastFetchedResult = "";

  function copyDetail() {
    if (!detail) return;
    const text =
      action.verb === "thought"
        ? detail.text || ""
        : detail.diff
          ? detail.diff.trimEnd()
          : [detail.args?.trim(), detail.output].filter(Boolean).join("\n");
    if (!text) return;
    navigator.clipboard?.writeText(text).then(() => {
      copied = true;
      setTimeout(() => (copied = false), 1200);
    });
  }

  $effect(() => {
    const isOpen = open;
    const live = action.live;
    const res = action.result || "";
    const key = action.k;
    const tid = tabId;
    if (!isOpen) return;
    untrack(() => {
      if (!detail || live || res !== lastFetchedResult) {
        lastFetchedResult = res;
        loading = !detail;
        orb
          .ask("detail", { tab: tid, key })
          .then(d => {
            detail = d || {};
            loading = false;
          })
          .catch(() => {
            loading = false;
          });
      }
    });
  });

  function toggle() {
    onToggle(action.k);
  }
</script>

<div class="act-wrap">
  <button class="act-line" class:open type="button" data-no-copy onclick={toggle} aria-expanded={open}>
    <span class="act-glyph">
      {#if action.live}
        <i class="led round live" style="--hue: var(--rupture)"></i>
      {:else if action.failed}
        <span class="fail-mark">×</span>
      {:else}
        <span class="chev">{open ? "⌄" : "›"}</span>
      {/if}
    </span>
    <span class="act-verb" class:live={action.live} class:open>{action.verb}</span>
    <span class="act-target" class:open title={action.target}>{action.target}</span>
    {#if action.result}
      <span class="act-res" class:failed={action.failed}>{action.result}</span>
    {/if}
  </button>

  {#if open}
    <div class="act-pane" class:failed={action.failed}>
      {#if loading && !detail}
        <span class="meta">loading…</span>
      {:else}
        <button class="pane-copy" type="button" data-no-copy onclick={copyDetail}>{copied ? "copied" : "copy"}</button>
        {#if action.verb === "thought"}
          <pre class="pane-pre muted scroll">{detail?.text || ""}</pre>
        {:else if detail?.diff}
          <pre class="pane-pre diff scroll">{#each detail.diff.trimEnd().split("\n") as line}<span class="diff-line" class:add={line[0] === "+"} class:del={line[0] === "-"}>{line}</span>{/each}</pre>
        {:else}
          {#if detail?.args?.trim()}
            {#if !detail.args.startsWith("$ ") && action.verb !== "write"}
              <span class="pane-lbl" data-no-copy>input</span>
            {/if}
            <pre class="pane-pre muted scroll">{detail.args}</pre>
            <div class="pane-rule"></div>
          {/if}
          <pre class="pane-pre muted scroll" class:alarm={action.failed}>{detail?.output ?? "…"}</pre>
        {/if}
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
    color: var(--meta);
    user-select: none;
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
  .chev { font-size: 13px; line-height: 1; opacity: 0.75; }
  .fail-mark { font-size: 12px; line-height: 1; color: var(--rupture); }
  .act-verb {
    flex-shrink: 0;
    min-width: 64px;
    font-weight: 500;
    color: var(--mute);
  }
  .act-verb.live, .act-verb.open { color: var(--fg); }
  .act-target {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
    color: var(--meta);
  }
  .act-target.open { color: var(--mute); }
  .act-res {
    flex-shrink: 1;
    max-width: 42%;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
    font-size: 11.5px;
    color: var(--meta);
  }
  .act-res.failed { color: var(--rupture); font-weight: 500; }
  .act-pane {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 4px 0 6px 18px;
    padding: 9px 12px;
    border: 0;
    border-radius: var(--radius-sm);
    background: var(--recessed);
    animation: drop-in 90ms var(--ease);
  }
  .act-pane.failed {
    background: color-mix(in srgb, var(--rupture) 6%, var(--recessed));
  }
  .pane-copy {
    position: absolute;
    top: 6px;
    right: 8px;
    z-index: 2;
    cursor: pointer;
    padding: 1px 6px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--meta);
    background: var(--recessed);
    border: 0;
    border-radius: var(--radius-sm);
    opacity: 0;
    user-select: none;
    transition: opacity var(--t-fast) var(--ease), color var(--t-fast) var(--ease), background-color var(--t-fast) var(--ease);
  }
  .act-pane:hover .pane-copy,
  .pane-copy:focus-visible { opacity: 1; }
  .pane-copy:hover { color: var(--fg); background: var(--hover); }
  .pane-lbl {
    font-size: 11px;
    font-weight: 600;
    color: var(--meta);
  }
  .pane-rule {
    height: 1px;
    background: color-mix(in srgb, var(--rule) 65%, transparent);
  }
  .pane-pre {
    margin: 0;
    max-height: 260px;
    overflow: auto;
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.5;
    white-space: pre-wrap;
  }
  .pane-pre.muted { color: var(--mute); }
  .pane-pre.alarm { color: var(--rupture); }
  .pane-pre.diff { display: flex; flex-direction: column; gap: 1px; }
  .diff-line { display: block; padding: 0 6px; border-radius: 2px; color: var(--meta); }
  .diff-line.add { color: var(--hue5); background: color-mix(in srgb, var(--hue5) 10%, transparent); }
  .diff-line.del { color: var(--rupture); background: color-mix(in srgb, var(--rupture) 10%, transparent); }
</style>
