<script>
  import { orb } from "../lib/store.svelte.js";
  import { collectLiveActivity } from "../lib/utils.js";

  let { currentTabId = "", defaultOpen = false } = $props();

  const urlExpand =
    typeof location !== "undefined" &&
    new URLSearchParams(location.search).get("expand") === "activity";

  let open = $state(false);
  let initialized = $state(false);
  let expandedProc = $state({});
  let procDetail = $state({});
  let procLoading = $state({});

  $effect(() => {
    if (!initialized) {
      initialized = true;
      if (defaultOpen || (urlExpand && orb.isBench)) {
        open = true;
      }
    }
  });

  let activity = $derived(collectLiveActivity(orb.state.tabs, orb.rowsByTab, orb.home));
  let hasActivity = $derived(
    activity.inputs.length > 0 || activity.agents.length > 0 || activity.remotes.length > 0,
  );
  let hasInputs = $derived(activity.inputs.length > 0);

  function procId(tabId, proc, idx) {
    return `${tabId}:${proc.key || idx}`;
  }

  async function toggleProcess(ag, proc, idx) {
    const id = procId(ag.tabId, proc, idx);
    const next = !expandedProc[id];
    expandedProc = { ...expandedProc, [id]: next };
    if (!next) return;
    if (proc.kind === "action" && proc.key && !procDetail[id] && !procLoading[id]) {
      procLoading = { ...procLoading, [id]: true };
      try {
        const res = await orb.ask("detail", { tab: ag.tabId, key: proc.key });
        if (res) {
          procDetail = { ...procDetail, [id]: res };
        }
      } catch {
        // Ignore detail fetch failures in transient states
      } finally {
        procLoading = { ...procLoading, [id]: false };
      }
    }
  }
</script>

{#if hasActivity}
  <div class="live-activity" class:open class:has-inputs={hasInputs}>
    <button
      type="button"
      class="la-toggle"
      aria-expanded={open}
      onclick={() => (open = !open)}
    >
      <i
        class="led"
        class:live={!hasInputs}
        class:asks={hasInputs}
        aria-hidden="true"
      ></i>
      <span class="la-summary" class:asks={hasInputs}>{activity.summary}</span>
      <span class="la-chev" class:open aria-hidden="true">›</span>
    </button>

    {#if open}
      <div class="la-panel scroll">
        {#each activity.inputs as inp (inp.tabId)}
          <div class="la-group">
            <button
              type="button"
              class="la-row la-input"
              class:current={inp.tabId === currentTabId}
              onclick={() => orb.focus(inp.tabId)}
            >
              <i class="led asks" aria-hidden="true"></i>
              <span class="la-tag asks">input</span>
              <span class="la-title">{inp.prompt}</span>
              {#if inp.where}
                <span class="la-where">{inp.where}</span>
              {/if}
              <span class="la-jump" aria-hidden="true">→</span>
            </button>
          </div>
        {/each}

        {#each activity.agents as ag (ag.tabId)}
          <div class="la-group">
            <button
              type="button"
              class="la-row la-agent"
              class:current={ag.tabId === currentTabId}
              onclick={() => orb.focus(ag.tabId)}
            >
              <i class="led live" style="--hue: var(--hue{ag.hue})" aria-hidden="true"></i>
              <span class="la-title">{ag.title}</span>
              {#if ag.where}
                <span class="la-where">{ag.where}</span>
              {/if}
              <span class="la-jump" aria-hidden="true">→</span>
            </button>

            {#if ag.processes.length > 0}
              <div class="la-procs">
                {#each ag.processes as proc, idx (procId(ag.tabId, proc, idx))}
                  {@const id = procId(ag.tabId, proc, idx)}
                  {@const canInspect = (proc.kind === "action" && !!proc.key) || (proc.kind === "subagent" && !!proc.detail)}
                  {@const isOpen = !!expandedProc[id]}
                  {@const det = procDetail[id]}
                  {#if canInspect}
                    <button
                      type="button"
                      class="la-proc clickable"
                      aria-expanded={isOpen}
                      onclick={() => toggleProcess(ag, proc, idx)}
                    >
                      <span class="la-verb">{proc.verb}</span>
                      {#if proc.target}
                        <span class="la-target">{proc.target}</span>
                      {/if}
                      <span class="la-proc-chev" class:open={isOpen} aria-hidden="true">›</span>
                    </button>
                    {#if isOpen}
                      <div class="la-detail">
                        {#if procLoading[id]}
                          <span class="la-detail-muted">loading…</span>
                        {:else if det}
                          {#if det.args}
                            <pre class="la-detail-pre">{det.args}</pre>
                          {/if}
                          {#if det.output}
                            <pre class="la-detail-pre">{det.output}</pre>
                          {/if}
                          {#if !det.args && !det.output && det.text}
                            <pre class="la-detail-pre">{det.text}</pre>
                          {/if}
                        {:else if proc.detail}
                          <pre class="la-detail-pre">{proc.detail}</pre>
                        {/if}
                      </div>
                    {/if}
                  {:else}
                    <div class="la-proc">
                      <span class="la-verb">{proc.verb}</span>
                      {#if proc.target}
                        <span class="la-target">{proc.target}</span>
                      {/if}
                    </div>
                  {/if}
                {/each}
              </div>
            {/if}
          </div>
        {/each}

        {#each activity.remotes as rem (rem.instance || `${rem.peer}:${rem.label}`)}
          <div class="la-group">
            <button
              type="button"
              class="la-row la-remote"
              onclick={() => orb.openEntry("i:" + rem.instance)}
            >
              <i class="led live" style="--hue: var(--hue{rem.hue})" aria-hidden="true"></i>
              <span class="la-title">{rem.label}</span>
              {#if rem.where}
                <span class="la-where">{rem.where}</span>
              {/if}
              <span class="la-jump" aria-hidden="true">→</span>
            </button>
          </div>
        {/each}
      </div>
    {/if}
  </div>
{/if}

<style>
  .live-activity {
    width: 100%;
    min-width: 0;
  }

  .la-toggle {
    display: flex;
    align-items: center;
    gap: 7px;
    width: 100%;
    min-height: 26px;
    padding: 3px 6px;
    margin: 0 -6px;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--fg);
    font-size: 12px;
    line-height: 1.3;
    text-align: left;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease);
  }
  .la-toggle:hover {
    background: var(--hover);
  }

  .la-summary {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
    color: var(--fg);
  }
  .la-summary.asks {
    color: var(--rupture);
  }

  .la-chev {
    flex-shrink: 0;
    color: var(--meta);
    font-size: 13px;
    line-height: 1;
    transition: transform var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .la-chev.open {
    transform: rotate(90deg);
    color: var(--fg);
  }

  .la-panel {
    margin: 2px 0 4px 4px;
    padding: 4px 0 4px 11px;
    border-left: 1px solid var(--rule);
    max-height: 220px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .la-group {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .la-row {
    display: flex;
    align-items: center;
    gap: 7px;
    width: 100%;
    padding: 3px 6px;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--fg);
    font-size: 12px;
    text-align: left;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease);
  }
  .la-row:hover {
    background: var(--hover);
  }
  .la-row.current .la-title {
    font-weight: 600;
  }

  .la-tag {
    flex-shrink: 0;
    font-size: 10.5px;
    font-weight: 600;
    letter-spacing: 0.02em;
    text-transform: lowercase;
  }
  .la-tag.asks {
    color: var(--rupture);
  }

  .la-title {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
    color: var(--fg);
  }

  .la-where {
    flex-shrink: 0;
    font-size: 11px;
    color: var(--meta);
  }

  .la-jump {
    flex-shrink: 0;
    font-size: 11.5px;
    color: var(--meta);
    opacity: 0.7;
    transition: opacity var(--t-fast) var(--ease), transform var(--t-fast) var(--ease);
  }
  .la-row:hover .la-jump {
    opacity: 1;
    color: var(--fg);
    transform: translateX(1px);
  }

  .la-procs {
    margin-left: 10px;
    padding-left: 10px;
    border-left: 1px solid var(--rule);
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .la-proc {
    display: flex;
    align-items: baseline;
    gap: 6px;
    width: 100%;
    padding: 2px 5px;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    text-align: left;
    font-size: 11.5px;
    line-height: 1.35;
    color: var(--meta);
  }
  .la-proc.clickable {
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease);
  }
  .la-proc.clickable:hover {
    background: var(--hover);
  }

  .la-verb {
    flex-shrink: 0;
    font-weight: 500;
    color: var(--fg);
  }

  .la-target {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: var(--mono);
    font-size: 11px;
    color: var(--meta);
  }

  .la-proc-chev {
    flex-shrink: 0;
    font-size: 11px;
    color: var(--meta);
    transition: transform var(--t-fast) var(--ease);
  }
  .la-proc-chev.open {
    transform: rotate(90deg);
  }

  .la-detail {
    margin: 2px 4px 4px 6px;
    padding: 6px 8px;
    border-left: 1px solid var(--rule);
    background: var(--hover);
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
    max-height: 120px;
    overflow: auto;
  }

  .la-detail-pre {
    margin: 0;
    font-family: var(--mono);
    font-size: 11px;
    line-height: 1.4;
    color: var(--fg);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .la-detail-pre + .la-detail-pre {
    margin-top: 5px;
    padding-top: 5px;
    border-top: 1px solid var(--rule);
  }

  .la-detail-muted {
    font-size: 11px;
    color: var(--meta);
  }
</style>
