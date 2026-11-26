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
        <button class="run-head" type="button" data-no-copy onclick={() => onToggle(row.k)} aria-expanded={groupOpen}>
          <span class="run-glyph">
            {#if row.live}
              <i class="led round live" style="--hue: var(--rupture)"></i>
            {:else}
              <span>{groupOpen ? "⌄" : "›"}</span>
            {/if}
          </span>
          <b class:live={row.live} class:open={groupOpen}>{row.live ? "working" : "worked"}</b>
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
{:else if row.kind === "peer" && row.exchange}
  {@const ex = row.exchange}
  {@const isPeer = ex.scope === "peer"}
  {@const peerHue = isPeer && ex.peer ? orb.getMachine(ex.peer)?.hue : null}
  {@const canJump = isPeer && !!(ex.session || ex.peer)}
  {@const subOpen = !!expandedMap.get(row.k)}
  {@const replyPreview = ex.reply ? ex.reply.split("\n").map(l => l.trim()).find(Boolean) || "" : ""}
  <div
    class="row-peer"
    class:is-peer={isPeer}
    class:is-failed={ex.failed}
  >
    <div class="peer-head">
      {#if isPeer}
        <i class="led round" class:live={ex.live} style={peerHue !== null && peerHue !== undefined ? `--hue: var(--hue${peerHue})` : ""}></i>
      {/if}
      <span class="peer-arrow" aria-hidden="true">{ex.dir === "in" ? "←" : "→"}</span>
      {#if canJump}
        <button
          type="button"
          class="peer-who jump"
          title="Open conversation"
          onclick={() => orb.openExchange(ex)}
        >{ex.who}</button>
      {:else}
        <span class="peer-who">{ex.who}</span>
      {/if}
      {#if ex.status}
        <span class="peer-status" class:live={ex.live} class:failed={ex.failed}>
          {#if ex.live && !isPeer}<i class="led round live"></i>{/if}
          {ex.status}
        </span>
      {/if}
    </div>
    {#if ex.text}
      <p class="peer-body">{ex.text}</p>
    {/if}
    {#if ex.scope === "subagent" && !ex.live && (ex.reply || ex.steps)}
      <button
        type="button"
        class="peer-reply-toggle"
        aria-expanded={subOpen}
        onclick={() => onToggle(row.k)}
      >
        <span class="peer-reply-arrow">←</span>
        <span class="peer-reply-preview">{subOpen ? "hide reply" : replyPreview || "view details"}</span>
      </button>
      {#if subOpen}
        <div class="peer-reply-tray">
          {#if ex.steps}
            <pre class="peer-steps">{ex.steps}</pre>
          {/if}
          {#if ex.reply}
            <p class="peer-reply-full">{ex.reply}</p>
          {/if}
        </div>
      {/if}
    {/if}
  </div>
{/if}

<style>
  .row-you {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 4px;
    padding: 18px 0 12px 0;
  }
  .via {
    font-size: 11.5px;
    font-weight: 700;
    color: var(--mute);
  }
  .you-bubble {
    margin: 0;
    width: 100%;
    padding: 9px 13px;
    white-space: pre-wrap;
    border-radius: var(--radius-md);
    background: var(--recessed);
    line-height: 1.5;
  }
  .you-bubble::before {
    content: "> ";
    font-weight: 700;
    color: var(--fg);
  }
  .tok-skill {
    padding: 0 5px;
    border-radius: 0;
    font-weight: 700;
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
  .row-peer {
    margin: 6px 0;
    padding: 7px 10px;
    background: var(--recessed);
    border: 1px solid var(--rule-soft);
    border-radius: var(--radius-sm);
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .row-peer.is-failed {
    border-color: color-mix(in srgb, var(--rupture) 40%, var(--rule-soft));
  }
  .peer-head {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    line-height: 1.3;
  }
  .peer-arrow {
    color: var(--meta);
    font-weight: 700;
    flex-shrink: 0;
  }
  .peer-who {
    font-weight: 700;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .peer-who.jump {
    padding: 0;
    border: 0;
    background: none;
    font: inherit;
    font-weight: 700;
    cursor: pointer;
    text-align: left;
  }
  .peer-who.jump:hover {
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .peer-status {
    margin-left: auto;
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 11px;
    color: var(--meta);
  }
  .peer-status.live {
    color: var(--fg);
    font-weight: 600;
  }
  .peer-status.failed {
    color: var(--rupture);
    font-weight: 600;
  }
  .peer-body {
    margin: 0;
    font-size: var(--body);
    line-height: 1.45;
    white-space: pre-wrap;
    color: var(--fg);
  }
  .peer-reply-toggle {
    display: flex;
    align-items: baseline;
    gap: 6px;
    padding: 2px 0 0;
    border: 0;
    background: none;
    font-size: 12px;
    color: var(--meta);
    cursor: pointer;
    text-align: left;
    min-width: 0;
  }
  .peer-reply-toggle:hover {
    color: var(--fg);
  }
  .peer-reply-arrow {
    flex-shrink: 0;
    opacity: 0.7;
  }
  .peer-reply-preview {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .peer-reply-tray {
    margin-top: 4px;
    padding: 8px 10px;
    background: var(--recessed);
    max-height: 260px;
    overflow: auto;
    font-size: 12px;
    line-height: 1.45;
  }
  .peer-steps {
    margin: 0 0 6px;
    padding: 0;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--meta);
    white-space: pre-wrap;
  }
  .peer-reply-full {
    margin: 0;
    white-space: pre-wrap;
    color: var(--fg);
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
    border-radius: 0;
    background: none;
    text-align: left;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--mute);
    user-select: none;
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
    font-weight: 500;
    color: var(--mute);
  }
  .run-head b.live, .run-head b.open { color: var(--fg); }
  .run-what {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
    color: var(--meta);
  }
  .run-head em.failed {
    flex-shrink: 0;
    font-style: normal;
    font-size: 11.5px;
    font-weight: 500;
    color: var(--rupture);
  }
  .run-body {
    margin-left: 7px;
    padding-left: 11px;
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
    border: 1px solid var(--fg);
    border-radius: 0;
    overflow: hidden;
    background: var(--raised);
    cursor: zoom-in;
    flex-shrink: 0;
  }
  .pic-btn img {
    display: block;
    height: 112px;
    width: auto;
  }
  .pic-ph {
    width: 96px;
    height: 112px;
    border-radius: 0;
    background: var(--recessed);
    border: 1px solid var(--rule);
    flex-shrink: 0;
  }
</style>
