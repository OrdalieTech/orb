<script>
  import MarkdownBlocks from "./MarkdownBlocks.svelte";
  import { tableToMarkdown } from "../lib/markdownCopy.js";

  let { blocks = [] } = $props();
  let copiedKey = $state("");

  function copyText(text, key) {
    if (!text) return;
    navigator.clipboard?.writeText(text).then(() => {
      copiedKey = key;
      setTimeout(() => {
        if (copiedKey === key) copiedKey = "";
      }, 1200);
    });
  }
</script>

{#each blocks as b, i}
  {@const indent = b.depth ? `margin-left:${b.depth * 2}ch` : undefined}
  {#if b.type === "h"}
    {#if b.level <= 2}
      <h3 class="md-h3" data-md-level={b.level || 2} style={indent}>
        {#each b.spans || [] as s}
          {@render spanNode(s)}
        {/each}
      </h3>
    {:else}
      <h4 class="md-h4" data-md-level={b.level || 3} style={indent}>
        {#each b.spans || [] as s}
          {@render spanNode(s)}
        {/each}
      </h4>
    {/if}
  {:else if b.type === "li"}
    <div class="md-li" data-md-mark={b.mark || "·"} data-md-depth={b.depth || 0} style={indent}>
      <span class="md-mark" data-no-copy>{b.mark}</span>
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
      <div class="md-pre-head" data-no-copy>
        <span class="md-lang">{b.lang || (b.type === "art" ? "diagram" : "code")}</span>
        <button class="md-copy" type="button" onclick={() => copyText(b.text || "", `code:${i}`)}>
          {copiedKey === `code:${i}` ? "copied" : "copy"}
        </button>
      </div>
      <pre class="md-pre" class:art={b.type === "art"} data-md-lang={b.lang || (b.type === "art" ? "text" : "")}>{b.text}</pre>
    </div>
  {:else if b.type === "table"}
    {@const tRows = b.rows || []}
    <div class="md-table-box" style={indent}>
      <button
        class="md-table-copy"
        type="button"
        data-no-copy
        onclick={() => copyText(tableToMarkdown(tRows), `table:${i}`)}
        title="Copy table as Markdown"
      >
        {copiedKey === `table:${i}` ? "copied" : "copy"}
      </button>
      <div class="md-table-wrap scroll">
        <table class="md-table">
          <tbody>
            {#each tRows as row, ri}
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
  .md-mark { flex-shrink: 0; min-width: 14px; font-family: var(--font-mono); font-size: 13px; font-weight: 600; color: var(--meta); user-select: none; }
  .md-li-body { flex: 1; min-width: 0; white-space: pre-wrap; }
  .md-quote {
    margin: 8px 0;
    padding: 2px 0 2px 14px;
    color: var(--mute);
    border-left: 2px solid var(--rule);
  }
  .md-code {
    padding: 1px 5px;
    font-size: 0.91em;
    border-radius: var(--radius-sm);
    background: var(--recessed);
  }
  .md-pre-wrap {
    margin: 10px 0;
    border-radius: var(--radius-md);
    background: var(--recessed);
    overflow: hidden;
  }
  .md-pre-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 6px 12px 2px;
    color: var(--meta);
    user-select: none;
  }
  .md-lang {
    font-size: 11px;
    font-weight: 500;
    color: var(--meta);
  }
  .md-copy {
    cursor: pointer;
    padding: 1px 6px;
    font-size: 11px;
    font-weight: 500;
    color: var(--meta);
    background: transparent;
    border: 0;
    border-radius: var(--radius-sm);
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .md-copy:hover { background: var(--hover); color: var(--fg); }
  }
  .md-pre {
    margin: 0;
    padding: 6px 12px 11px;
    overflow-x: auto;
    font-size: 12.5px;
    line-height: 1.5;
    white-space: pre-wrap;
    tab-size: 4;
  }
  .md-pre.art {
    white-space: pre;
    line-height: 1.15;
  }
  .md-table-box {
    position: relative;
    margin: 12px 0;
  }
  .md-table-copy {
    position: absolute;
    top: 4px;
    right: 4px;
    z-index: 2;
    cursor: pointer;
    padding: 2px 7px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--meta);
    background: var(--bg);
    border: 0;
    border-radius: var(--radius-sm);
    opacity: 0;
    user-select: none;
    transition: opacity var(--t-fast) var(--ease), color var(--t-fast) var(--ease), background-color var(--t-fast) var(--ease);
  }
  .md-table-box:hover .md-table-copy,
  .md-table-copy:focus-visible {
    opacity: 1;
  }
  .md-table-copy:hover {
    color: var(--fg);
    background: var(--hover);
  }
  .md-table-wrap {
    overflow-x: auto;
  }
  .md-table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--small);
  }
  .md-table th, .md-table td {
    padding: 7px 12px 7px 0;
    text-align: left;
    vertical-align: top;
    border-bottom: 1px solid var(--rule-soft);
  }
  .md-table th:last-child, .md-table td:last-child { padding-right: 0; }
  .md-table tr:last-child td { border-bottom: 0; }
  .md-table th {
    font-weight: 600;
    font-size: var(--small);
    color: var(--mute);
    border-bottom: 1px solid var(--rule);
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
