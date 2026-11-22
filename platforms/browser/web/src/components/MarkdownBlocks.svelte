<script>
  import MarkdownBlocks from "./MarkdownBlocks.svelte";

  let { blocks = [] } = $props();
  let copiedIdx = $state(-1);

  function copyCode(text, idx) {
    navigator.clipboard?.writeText(text).then(() => {
      copiedIdx = idx;
      setTimeout(() => {
        if (copiedIdx === idx) copiedIdx = -1;
      }, 1200);
    });
  }
</script>

{#each blocks as b, i}
  {@const indent = b.depth ? `margin-left:${b.depth * 2}ch` : undefined}
  {#if b.type === "h"}
    {#if b.level <= 2}
      <h3 class="md-h3" style={indent}>
        {#each b.spans || [] as s}
          {@render spanNode(s)}
        {/each}
      </h3>
    {:else}
      <h4 class="md-h4" style={indent}>
        {#each b.spans || [] as s}
          {@render spanNode(s)}
        {/each}
      </h4>
    {/if}
  {:else if b.type === "li"}
    <div class="md-li" style={indent}>
      <span class="md-mark">{b.mark}</span>
      <span class="md-li-body">
        {#each b.spans || [] as s}
          {@render spanNode(s)}
        {/each}
      </span>
    </div>
  {:else if b.type === "quote"}
    <blockquote class="md-quote" style={indent}>
      <MarkdownBlocks blocks={b.blocks || []} />
    </blockquote>
  {:else if b.type === "code" || b.type === "art"}
    <div class="md-pre-wrap" style={indent}>
      <div class="md-pre-head">
        <span class="md-lang">{b.lang || (b.type === "art" ? "diagram" : "code")}</span>
        <button class="md-copy" type="button" onclick={() => copyCode(b.text || "", i)}>
          {copiedIdx === i ? "copied" : "copy"}
        </button>
      </div>
      <pre class="md-pre" class:art={b.type === "art"}>{b.text}</pre>
    </div>
  {:else if b.type === "table"}
    <div class="md-table-wrap scroll" style={indent}>
      <table class="md-table">
        <tbody>
          {#each b.rows || [] as row, ri}
            <tr>
              {#each row || [] as cell}
                {#if ri === 0}
                  <th>
                    {#each cell || [] as s}
                      {@render spanNode(s)}
                    {/each}
                  </th>
                {:else}
                  <td>
                    {#each cell || [] as s}
                      {@render spanNode(s)}
                    {/each}
                  </td>
                {/if}
              {/each}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else if b.type === "rule"}
    <hr class="md-hr" />
  {:else}
    <p class="md-p" style={indent}>
      {#each b.spans || [] as s}
        {@render spanNode(s)}
      {/each}
    </p>
  {/if}
{/each}

{#snippet spanNode(s)}
  {#if s.h}
    <a class="md-link" href={s.h} target="_blank" rel="noreferrer">{@render styledText(s)}</a>
  {:else}
    {@render styledText(s)}
  {/if}
{/snippet}

{#snippet styledText(s)}
  {#if s.c}
    <code class="md-code">{s.t}</code>
  {:else if s.b && s.i}
    <strong><em>{s.t}</em></strong>
  {:else if s.b}
    <strong>{s.t}</strong>
  {:else if s.i}
    <em>{s.t}</em>
  {:else if s.s}
    <s>{s.t}</s>
  {:else}
    {s.t}
  {/if}
{/snippet}

<style>
  .md-p { white-space: pre-wrap; margin: 0; }
  .md-h3 { margin: 8px 0 2px; font-size: var(--title); font-weight: 600; letter-spacing: -.02em; }
  .md-h4 { margin: 6px 0 2px; font-size: var(--body); font-weight: 600; }
  .md-li { display: flex; gap: 1ch; }
  .md-mark { flex-shrink: 0; min-width: 1ch; font-weight: 600; color: var(--meta); }
  .md-li-body { flex: 1; min-width: 0; white-space: pre-wrap; }
  .md-quote {
    margin: 4px 0;
    padding: 2px 0 2px 14px;
    color: var(--mute);
    border-left: 2px solid var(--rule);
  }
  .md-code {
    padding: 0 5px;
    font-size: .92em;
    border-radius: 3px;
    background: var(--recessed);
    border: 1px solid var(--rule-soft);
  }
  .md-pre-wrap {
    margin: 6px 0;
    border: 1px solid var(--rule);
    border-radius: var(--pane);
    background: var(--raised);
    overflow: hidden;
  }
  .md-pre-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 4px 10px;
    background: var(--recessed);
    border-bottom: 1px solid var(--rule-soft);
  }
  .md-lang {
    font-size: var(--label);
    font-weight: 600;
    letter-spacing: .08em;
    text-transform: uppercase;
    color: var(--meta);
  }
  .md-copy {
    cursor: pointer;
    padding: 1px 7px;
    font-size: var(--label);
    font-weight: 600;
    letter-spacing: .04em;
    text-transform: uppercase;
    color: var(--mute);
    background: var(--raised);
    border: 1px solid var(--rule);
    border-radius: 3px;
  }
  @media (hover: hover) {
    .md-copy:hover { color: var(--fg); border-color: var(--fg); }
  }
  .md-pre {
    margin: 0;
    padding: 10px 12px;
    overflow-x: auto;
    font: inherit;
    font-size: var(--small);
    line-height: 1.5;
    white-space: pre-wrap;
    tab-size: 4;
  }
  .md-pre.art {
    white-space: pre;
    line-height: 1.1;
  }
  .md-table-wrap {
    margin: 6px 0;
    overflow-x: auto;
    border: 1px solid var(--rule);
    border-radius: var(--soft);
  }
  .md-table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--small);
  }
  .md-table th, .md-table td {
    padding: 6px 10px;
    text-align: left;
    vertical-align: top;
    border-bottom: 1px solid var(--rule-soft);
    border-right: 1px solid var(--rule-soft);
  }
  .md-table th:last-child, .md-table td:last-child { border-right: 0; }
  .md-table tr:last-child td { border-bottom: 0; }
  .md-table th {
    font-weight: 600;
    background: var(--recessed);
    font-size: var(--label);
    letter-spacing: .06em;
    text-transform: uppercase;
    color: var(--mute);
  }
  .md-link {
    color: inherit;
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .md-hr {
    margin: 8px 0;
    border: 0;
    height: 1px;
    background: var(--rule-soft);
  }
</style>
