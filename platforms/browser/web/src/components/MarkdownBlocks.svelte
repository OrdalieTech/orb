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
  .md-p { white-space: pre-wrap; margin: 0 0 6px; line-height: 1.62; }
  .md-p:last-child { margin-bottom: 0; }
  .md-h3 { margin: 14px 0 5px; font-size: 17px; font-weight: 600; letter-spacing: -0.025em; }
  .md-h4 { margin: 10px 0 4px; font-size: 15px; font-weight: 600; letter-spacing: -0.015em; }
  .md-li { display: flex; gap: 8px; margin: 2px 0; line-height: 1.58; }
  .md-mark { flex-shrink: 0; min-width: 14px; font-family: var(--font-mono); font-size: 13px; font-weight: 600; color: var(--meta); }
  .md-li-body { flex: 1; min-width: 0; white-space: pre-wrap; }
  .md-quote {
    margin: 8px 0;
    padding: 4px 0 4px 14px;
    color: var(--mute);
    border-left: 2px solid var(--rupture);
  }
  .md-code {
    padding: 1px 6px;
    font-family: var(--font-mono);
    font-size: 0.88em;
    border-radius: 5px;
    background: var(--recessed);
    border: 1px solid var(--rule-soft);
  }
  .md-pre-wrap {
    margin: 10px 0;
    border: 1px solid var(--rule);
    border-radius: 10px;
    background: var(--raised);
    box-shadow: 0 1px 3px var(--shade);
    overflow: hidden;
  }
  .md-pre-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 6px 12px;
    background: var(--recessed);
    border-bottom: 1px solid var(--rule-soft);
  }
  .md-lang {
    font-family: var(--font-mono);
    font-size: var(--label);
    font-weight: 500;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--meta);
  }
  .md-copy {
    cursor: pointer;
    padding: 2px 9px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    font-weight: 500;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--mute);
    background: var(--raised);
    border: 1px solid var(--rule);
    border-radius: 5px;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .md-copy:hover { color: var(--fg); border-color: var(--rule-strong); }
  }
  .md-pre {
    margin: 0;
    padding: 12px 14px;
    overflow-x: auto;
    font-family: var(--font-mono);
    font-size: 12.5px;
    line-height: 1.55;
    white-space: pre-wrap;
    tab-size: 4;
  }
  .md-pre.art {
    white-space: pre;
    line-height: 1.15;
  }
  .md-table-wrap {
    margin: 10px 0;
    overflow-x: auto;
    border: 1px solid var(--rule);
    border-radius: 10px;
    background: var(--raised);
  }
  .md-table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--small);
  }
  .md-table th, .md-table td {
    padding: 8px 12px;
    text-align: left;
    vertical-align: top;
    border-bottom: 1px solid var(--rule-soft);
    border-right: 1px solid var(--rule-soft);
  }
  .md-table th:last-child, .md-table td:last-child { border-right: 0; }
  .md-table tr:last-child td { border-bottom: 0; }
  .md-table th {
    font-family: var(--font-mono);
    font-weight: 600;
    background: var(--recessed);
    font-size: var(--label);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--mute);
  }
  .md-link {
    color: inherit;
    text-decoration: underline;
    text-decoration-color: var(--rule-strong);
    text-underline-offset: 3px;
    transition: text-decoration-color var(--t-fast) var(--ease);
  }
  .md-link:hover { text-decoration-color: var(--rupture); }
  .md-hr {
    margin: 12px 0;
    border: 0;
    height: 1px;
    background: var(--rule);
  }
</style>
