<script>
  import { orb } from "../lib/store.svelte.js";
  import { ago, hasVisibleText, stripAnsi, tidy, tightestWindow, usageLabel, word } from "../lib/utils.js";
  import VirtualTranscript from "./VirtualTranscript.svelte";
  import PromptBox from "./PromptBox.svelte";

  let { tab, pane = "primary" } = $props();

  let transcriptRef = $state(null);
  let expandedSlots = $state({});

  let rows = $derived(orb.rowsByTab[tab.id] || []);
  let statusWord = $derived(word(tab));
  let tightWin = $derived(tightestWindow(tab.usage));
  let visibleSlots = $derived((tab.slots || []).filter(s => hasVisibleText(s.lines)));
  let isFocusedPane = $derived(orb.splitTab && orb.activePane === pane);

  function showPlan() {
    const u = tab.usage;
    if (!u?.windows?.length) return;
    orb.pick(
      [tab.model?.split("/")[0], u.plan, `read ${ago(u.at)}`].filter(Boolean).join(" · "),
      u.windows.map(w => ({
        label: `${w.name} · ${Math.round(w.left)}% left`,
        detail: w.resets
          ? `resets ${new Date(w.resets).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" })}`
          : "",
      })),
    );
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="chat-pane"
  class:split-focus={isFocusedPane}
  onmousedown={() => { if (orb.splitTab) orb.activePane = pane; }}
>
  <div class="strip" class:busy={tab.busy}>
    <span class="strip-where" title={tidy(tab.cwd || tab.where)}>
      {#if orb.splitTab}
        <i class="led" class:live={tab.busy} class:asks={!!tab.ask} style="--hue: var(--hue{orb.getMachine(tab.peer)?.hue || 0})"></i>
        <b class="strip-title">{tab.title || "new session"}</b>
      {/if}
      <span>{[tab.remote && tab.where, tab.cwd && tidy(tab.cwd), statusWord].filter(Boolean).join(" · ")}</span>
    </span>

    {#if tightWin}
      <button
        type="button"
        class="strip-pill"
        class:alarm={tightWin.left < 15}
        onclick={showPlan}
        title="Activity limit · click for all windows"
      >
        <span class="jp">活動限界</span>
        <span>{usageLabel(tab.usage)}</span>
      </button>
    {/if}

    {#if tab.cost > 0}
      <span class="strip-cost">${tab.cost.toFixed(2)}</span>
    {/if}

    {#if tab.remote}
      <button
        type="button"
        class="strip-term"
        class:on={orb.terminalTab === tab.id}
        onclick={() => (orb.terminalTab = orb.terminalTab === tab.id ? "" : tab.id)}
        title="Remote terminal (Alt+J)"
      >&gt;_</button>
    {/if}

    {#if pane === "secondary"}
      <button type="button" class="strip-close" onclick={() => orb.toggleSplit()} title="Close split (Alt+D)">×</button>
    {/if}
  </div>

  {#if tab.acting}
    <div class="acting">
      <b class="stretch">PATTERN BLUE</b>
      <p>パターン青 · another Orb is driving this tab</p>
      <button type="button" class="btn" onclick={() => orb.send({ do: "abort", tab: tab.id })}>stop</button>
    </div>
  {/if}

  <VirtualTranscript
    bind:this={transcriptRef}
    tabId={tab.id}
    {rows}
    earlier={!!tab.earlier}
    loaded={!!tab.loaded}
    asleep={!!tab.asleep}
    streaming={!!tab.streaming}
    status={tab.status || ""}
    onEarlier={() => orb.send({ do: "earlier", tab: tab.id })}
  />

  {#if visibleSlots.length > 0}
    <div class="slots">
      {#each visibleSlots as s (s.key)}
        {@const open = !!expandedSlots[s.key]}
        <button
          type="button"
          class="slot-line"
          class:alarm={s.alarm}
          class:folded={!open}
          onclick={() => (expandedSlots = { ...expandedSlots, [s.key]: !open })}
        >
          {stripAnsi(s.lines.join("\n"))}
        </button>
      {/each}
    </div>
  {/if}

  <PromptBox {tab} onSent={() => transcriptRef?.scrollToBottom()} />
</div>

<style>
  .chat-pane {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    position: relative;
  }
  .chat-pane.split-focus {
    box-shadow: inset 0 2px 0 var(--rupture);
  }
  .strip {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    max-width: calc(760px + 2 * var(--margin));
    margin: 0 auto;
    padding: 7px var(--margin) 6px;
    font-size: 11.5px;
    color: var(--meta);
    border-bottom: 1px solid var(--rule);
    flex-shrink: 0;
  }
  .strip.busy .strip-where { color: var(--fg); }
  .strip-where {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 8px;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .strip-where > span {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .strip-title {
    font-size: 12px;
    color: var(--fg);
    font-weight: 700;
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    flex-shrink: 0;
  }
  .strip-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 2px 7px;
    border: 1px solid var(--rule);
    border-radius: var(--soft);
    background: var(--raised);
    font-size: 11px;
    font-weight: 600;
    color: var(--fg);
    cursor: pointer;
    flex-shrink: 0;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .strip-pill .jp {
    font-size: 10px;
    letter-spacing: 0.04em;
    color: var(--meta);
  }
  .strip-pill.alarm {
    border-color: var(--rupture);
    color: var(--rupture);
  }
  .strip-cost {
    font-size: 11px;
    font-weight: 600;
    color: var(--mute);
    flex-shrink: 0;
  }
  .strip-term {
    padding: 2px 7px;
    border: 1px solid var(--fg);
    border-radius: var(--soft);
    background: var(--raised);
    font-size: 11.5px;
    font-weight: 700;
    color: var(--fg);
    cursor: pointer;
    flex-shrink: 0;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .strip-term.on {
    color: var(--bg);
    background: var(--fg);
  }
  .strip-close {
    padding: 0 5px;
    border: 0;
    border-radius: var(--soft);
    background: none;
    color: var(--mute);
    font-size: 15px;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .strip-pill:hover, .strip-term:hover:not(.on) {
      background: var(--fg);
      border-color: var(--fg);
      color: var(--bg);
    }
    .strip-pill:hover .jp { color: var(--bg); }
    .strip-close:hover { background: var(--hover); color: var(--fg); }
  }
  .acting {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 16px;
    flex-shrink: 0;
    padding: 10px var(--margin);
    color: var(--paper-ink);
    background: var(--blue);
    border-top: 4px solid;
    border-image: repeating-linear-gradient(45deg, #FFFFFF66 0 6px, transparent 6px 12px) 4;
    animation: drop-in var(--t-med) var(--ease);
  }
  .acting b { font-size: 14px; line-height: 20px; }
  .acting p {
    flex: 1;
    font-family: var(--font-mono);
    font-size: var(--label);
    letter-spacing: 0.08em;
    text-transform: uppercase;
    opacity: 0.85;
  }
  .acting .btn {
    color: var(--blue);
    background: var(--paper-ink);
    border-color: transparent;
  }
  .slots {
    width: 100%;
    max-width: 752px;
    margin: 0 auto;
    padding: 4px var(--margin) 0;
    flex-shrink: 0;
  }
  .slot-line {
    display: block;
    width: 100%;
    padding: 5px 10px;
    margin: 2px 0;
    border: 1px solid var(--rule-soft);
    border-radius: var(--soft);
    background: var(--recessed);
    text-align: left;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--mute);
    white-space: pre-wrap;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease);
  }
  .slot-line:hover { background: var(--hover); }
  .slot-line.folded {
    display: -webkit-box;
    -webkit-line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  @media (max-width: 600px) {
    .strip-pill .jp { display: none; }
  }
</style>
