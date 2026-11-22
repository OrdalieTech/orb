<script>
  import { orb } from "../lib/store.svelte.js";
  import { stripAnsi, tokenizeText } from "../lib/utils.js";
  import MarkdownBlocks from "./MarkdownBlocks.svelte";
  import ActionItem from "./ActionItem.svelte";

  let { row, tabId, expandedMap, onToggle } = $props();

  let noteExpanded = $state(false);
  let thumbs = $state({});

  $effect(() => {
    const imgs = row.images;
    if (!imgs?.length) return;
    for (const ref of imgs) {
      if (thumbs[ref]) continue;
      orb
        .ask("image", { tab: tabId, ref, px: 384 })
        .then(r => {
          if (r?.data) thumbs = { ...thumbs, [ref]: `data:image/png;base64,${r.data}` };
        })
        .catch(() => {});
    }
  });

  function openViewer(ref) {
    orb
      .ask("image", { tab: tabId, ref, px: 0 })
      .then(r => {
        if (r?.data) orb.viewerImage = { src: `data:image/png;base64,${r.data}`, tabId, ref };
      })
      .catch(() => {});
  }
</script>

{#if row.kind === "you"}
  <div class="row-you">
    {#if row.via}
      <small class="via">from a peer</small>
    {/if}
    {#if row.images?.length}
      <div class="pictures scroll">
        {#each row.images as ref (ref)}
          {#if thumbs[ref]}
            <button type="button" class="pic-btn" onclick={() => openViewer(ref)}>
              <img src={thumbs[ref]} alt="" loading="lazy" />
            </button>
          {:else}
            <div class="pic-ph"></div>
          {/if}
        {/each}
      </div>
    {/if}
    {#if row.text?.trim()}
      <p class="you-bubble">
        {#each tokenizeText(row.text) as seg}
          {#if seg.kind === "skill"}
            <span class="tok-skill">{seg.text}</span>
          {:else if seg.kind === "file"}
            <span class="tok-file">{seg.text}</span>
          {:else if seg.kind === "ref"}
            <span class="tok-ref">{seg.text}</span>
          {:else}
            {seg.text}
          {/if}
        {/each}
      </p>
    {/if}
  </div>
{:else if row.kind === "md"}
  <div class="row-said">
    {#if row.block}
      <MarkdownBlocks blocks={[row.block]} />
    {/if}
  </div>
{:else if row.kind === "note"}
  {@const clean = stripAnsi(row.text)}
  <button
    type="button"
    class="row-note"
    class:alarm={row.alarm}
    class:folded={!noteExpanded}
    onclick={() => (noteExpanded = !noteExpanded)}
  >
    {clean}
  </button>
{:else if row.kind === "run"}
  {@const actions = row.actions || []}
  <div class="row-said">
    {#if actions.length === 1}
      <ActionItem action={actions[0]} {tabId} {expandedMap} {onToggle} />
    {:else if actions.length > 1}
      {@const groupOpen = !!expandedMap.get(row.k)}
      <div class="run-group">
        <button class="run-head" type="button" onclick={() => onToggle(row.k)} aria-expanded={groupOpen}>
          <span class="run-glyph">
            {#if row.live}
              <i class="led round live" style="--hue: var(--rupture)"></i>
            {:else}
              <span>{groupOpen ? "⌄" : "›"}</span>
            {/if}
          </span>
          <b class:live={row.live}>{row.live ? "working" : "worked"}</b>
          <span class="run-what">{[row.what, !groupOpen && row.now].filter(Boolean).join(" · ")}</span>
          {#if row.failed > 0}
            <em class="failed">{row.failed} failed</em>
          {/if}
        </button>
        {#if groupOpen}
          <div class="run-body">
            {#each actions as a (a.k)}
              <ActionItem action={a} {tabId} {expandedMap} {onToggle} />
            {/each}
          </div>
        {/if}
      </div>
    {/if}
    {#if row.images?.length}
      <div class="pictures scroll">
        {#each row.images as ref (ref)}
          {#if thumbs[ref]}
            <button type="button" class="pic-btn" onclick={() => openViewer(ref)}>
              <img src={thumbs[ref]} alt="" loading="lazy" />
            </button>
          {:else}
            <div class="pic-ph"></div>
          {/if}
        {/each}
      </div>
    {/if}
  </div>
{/if}

<style>
  .row-you {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 6px;
    padding: 22px 0 14px 48px;
  }
  .via {
    font-family: var(--font-mono);
    font-size: var(--label);
    font-weight: 600;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--hue1);
  }
  .you-bubble {
    margin: 0;
    padding: 10px 15px;
    white-space: pre-wrap;
    border-radius: 14px 14px 4px 14px;
    background: var(--recessed);
    border: 1px solid var(--rule-soft);
    max-width: 100%;
    line-height: 1.55;
  }
  .tok-skill {
    padding: 1px 6px;
    border-radius: 5px;
    font-family: var(--font-mono);
    font-size: 0.9em;
    font-weight: 600;
    color: var(--bg);
    background: var(--fg);
  }
  .tok-file {
    font-family: var(--font-mono);
    font-size: 0.92em;
    text-decoration: underline;
    text-decoration-color: var(--rule-strong);
    text-underline-offset: 3px;
    font-weight: 500;
  }
  .tok-ref {
    font-family: var(--font-mono);
    font-size: 0.92em;
    color: var(--meta);
    font-weight: 500;
  }
  .row-said {
    padding: 4px 0;
  }
  .row-note {
    display: block;
    width: 100%;
    padding: 5px 0;
    border: 0;
    background: none;
    text-align: left;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--meta);
    white-space: pre-wrap;
    transition: color var(--t-fast) var(--ease);
  }
  .row-note:hover { color: var(--mute); }
  .row-note.folded {
    display: -webkit-box;
    -webkit-line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .run-head {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 4px 6px;
    margin: 0 -6px;
    border: 0;
    border-radius: var(--soft);
    background: none;
    text-align: left;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--mute);
    transition: background-color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .run-head:hover { background: var(--hover); }
  }
  .run-glyph {
    width: 14px;
    display: grid;
    place-items: center;
    flex-shrink: 0;
    color: var(--meta);
  }
  .run-head b {
    flex-shrink: 0;
    min-width: 64px;
    font-weight: 600;
    color: var(--fg);
  }
  .run-head b.live { color: var(--rupture); }
  .run-what {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .run-head em.failed {
    flex-shrink: 0;
    font-style: normal;
    font-size: 11.5px;
    font-weight: 600;
    color: var(--rupture);
  }
  .run-body {
    margin-left: 16px;
    padding-left: 10px;
    border-left: 1px solid var(--rule);
    animation: drop-in 140ms var(--ease);
  }
  .pictures {
    display: flex;
    gap: 8px;
    overflow-x: auto;
    padding: 6px 0;
  }
  .pic-btn {
    padding: 0;
    border: 1px solid var(--rule);
    border-radius: 9px;
    overflow: hidden;
    background: var(--raised);
    cursor: zoom-in;
    flex-shrink: 0;
    transition: transform var(--t-fast) var(--ease), box-shadow var(--t-fast) var(--ease);
  }
  .pic-btn:hover {
    transform: translateY(-1px);
    box-shadow: 0 4px 12px var(--shade);
  }
  .pic-btn img {
    display: block;
    height: 112px;
    width: auto;
  }
  .pic-ph {
    width: 96px;
    height: 112px;
    border-radius: 9px;
    background: var(--recessed);
    border: 1px solid var(--rule-soft);
    flex-shrink: 0;
  }
</style>
