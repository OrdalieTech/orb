<script>
  import { onDestroy } from "svelte";
  import { orb } from "../lib/store.svelte.js";

  let { tab, ask } = $props();

  let freeText = $state("");
  let ticked = $state([]);
  let armedAt = 0;
  let lastId = "";
  let inputEl = $state(null);

  let word = $derived(ask.owner === "permissions" ? "APPROVAL" : "QUESTION");
  let caption = $derived(ask.owner === "permissions" ? "approval required" : "a question for you");
  let titleLines = $derived((ask.title || "").split("\n"));
  let firstLine = $derived(titleLines[0] || "");
  let detail = $derived([...titleLines.slice(1), ask.message || ""].join("\n").trim());

  let keyedChoices = $derived(
    (ask.choices || []).map((choice, i) => {
      const own = /^([a-z]) (.+)/.exec(choice);
      const key = own?.[1] ?? ({ allow: "y", deny: "n" }[choice] || (i < 9 ? String(i + 1) : ""));
      return { key, label: own?.[2] ?? choice, value: choice };
    }),
  );

  $effect(() => {
    if (ask.id !== lastId) {
      lastId = ask.id;
      freeText = "";
      ticked = [];
      armedAt = Date.now() + 800;
      if (ask.free && keyedChoices.length === 0) {
        requestAnimationFrame(() => inputEl?.focus());
      }
    }
  });

  function sendAnswer(value, pickedList) {
    orb.send({ do: "answer", tab: tab.id, value, ticked: pickedList });
  }

  function choose(choice) {
    if (!ask.multi) {
      sendAnswer(choice);
      return;
    }
    if (ticked.includes(choice)) ticked = ticked.filter(x => x !== choice);
    else ticked = [...ticked, choice];
  }

  function submitForm() {
    const trimmed = freeText.trim();
    if (ask.multi) {
      if (ticked.length > 0 || trimmed) sendAnswer(trimmed, ticked);
    } else if (trimmed) {
      sendAnswer(trimmed);
    }
  }

  function onWindowKeyDown(e) {
    if (e.key === "Escape") {
      e.preventDefault();
      sendAnswer(null);
      return;
    }
    if (e.target === inputEl || Date.now() < armedAt) return;
    const hit = keyedChoices.find(c => c.key === e.key);
    if (hit) {
      e.preventDefault();
      choose(hit.value);
    }
  }

  window.addEventListener("keydown", onWindowKeyDown);
  onDestroy(() => window.removeEventListener("keydown", onWindowKeyDown));
</script>

<div class="interrupt" role="alertdialog" aria-modal="true" aria-label={word}>
  <i class="hazard"></i>
  <div class="inner">
    <div class="top-meta">
      <span>{ask.owner} · {tab.where}</span>
      <span>waiting on you</span>
    </div>
    <hr />

    <h2 class="stretch word" class:squeezed={word.length > 5}>{word}</h2>
    <p class="caption">{caption}</p>
    <p class="first">{firstLine}</p>
    {#if detail}
      <p class="detail scroll">{detail}</p>
    {/if}

    <hr class="push" />

    {#each keyedChoices as item, n (item.value)}
      {@const isSelected = ask.multi ? ticked.includes(item.value) : n === 0}
      <button
        type="button"
        class="choice"
        class:lit={isSelected}
        style="--n:{n}"
        onclick={() => choose(item.value)}
      >
        <b>{ask.multi ? (isSelected ? "■" : "□") : item.key}</b>
        <span>{item.label}</span>
      </button>
    {/each}

    {#if ask.free || ask.multi}
      <div class="free-row">
        {#if ask.free}
          <input
            bind:this={inputEl}
            bind:value={freeText}
            placeholder={ask.multi ? "optional note" : "type an answer"}
            autocomplete="off"
            enterkeyhint="send"
            onkeydown={e => { if (e.key === "Enter") { e.preventDefault(); submitForm(); } }}
          />
        {/if}
        <button
          type="button"
          class="int-send"
          disabled={ask.multi ? ticked.length === 0 && !freeText.trim() : !freeText.trim()}
          onclick={submitForm}
        >
          {ask.multi ? `confirm (${ticked.length})` : "send"}
        </button>
      </div>
    {/if}

    <hr />
    <button type="button" class="dismiss" onclick={() => sendAnswer(null)}>
      esc · dismiss
    </button>
  </div>
  <i class="hazard"></i>
</div>

<style>
  .interrupt {
    --ink: #141413;
    position: fixed;
    inset: 0;
    z-index: 40;
    display: flex;
    flex-direction: column;
    overflow-y: auto;
    padding: env(safe-area-inset-top) 0 env(safe-area-inset-bottom);
    color: var(--ink);
    background: var(--rupture);
    animation: rise 260ms var(--ease) backwards;
  }
  @keyframes rise {
    from { opacity: 0; transform: translateY(16px); }
    to { opacity: 1; transform: translateY(0); }
  }
  .hazard {
    display: block;
    flex-shrink: 0;
    height: 14px;
    background: repeating-linear-gradient(45deg, var(--ink) 0 6px, transparent 6px 12px);
  }
  .inner {
    display: flex;
    flex: 1 0 auto;
    flex-direction: column;
    width: 100%;
    max-width: 900px;
    margin: 0 auto;
  }
  hr {
    flex-shrink: 0;
    width: 100%;
    height: 1px;
    margin: 0;
    border: 0;
    background: var(--ink);
  }
  hr.push { margin-top: auto; }
  .top-meta {
    display: flex;
    justify-content: space-between;
    padding: 14px var(--margin);
    font-size: 12px;
    font-weight: 600;
  }
  .word {
    --h: 142px;
    height: var(--h);
    margin: 20px var(--margin) calc(var(--h) * .26);
    font-size: calc(var(--h) * .75);
    line-height: var(--h);
    animation: wipe 260ms var(--ease) backwards;
  }
  .word.squeezed { --squeeze: .6; }
  .caption {
    margin: 0 var(--margin) 16px;
    font-size: 12px;
    font-weight: 600;
    animation: appear 240ms 120ms var(--ease) backwards;
  }
  .first {
    padding: 0 var(--margin);
    font-size: 20px;
    font-weight: 600;
  }
  .detail {
    max-height: 220px;
    overflow-y: auto;
    padding: 10px var(--margin);
    font-size: 14px;
    font-weight: 500;
    white-space: pre-wrap;
  }
  .choice {
    display: flex;
    align-items: center;
    width: 100%;
    padding: 14px 20px;
    border: 0;
    background: transparent;
    text-align: left;
    font-size: var(--body);
    color: var(--ink);
    cursor: pointer;
    animation: enter 280ms calc(30ms + var(--n) * 24ms) var(--ease) backwards;
  }
  @keyframes enter {
    from { opacity: 0; transform: translateY(40%); }
  }
  .choice b { width: 36px; flex-shrink: 0; font-weight: 700; }
  .choice.lit {
    color: var(--rupture);
    background: var(--ink);
  }
  @media (hover: hover) {
    .choice:not(.lit):hover {
      background: color-mix(in srgb, var(--ink) 12%, transparent);
    }
  }
  .free-row {
    display: flex;
    gap: 8px;
    padding: 14px var(--margin);
  }
  .free-row input {
    flex: 1;
    min-width: 0;
    padding: 10px 14px;
    color: var(--ink);
    border: 1px solid var(--ink);
    border-radius: 0;
    background: none;
    outline: 0;
  }
  .free-row input::placeholder { color: color-mix(in srgb, var(--ink) 62%, transparent); }
  .int-send {
    padding: 10px 16px;
    border: 1px solid var(--ink);
    border-radius: 0;
    background: var(--ink);
    color: var(--rupture);
    font-weight: 700;
    font-size: var(--small);
    cursor: pointer;
  }
  .dismiss {
    padding: 14px var(--margin);
    border: 0;
    background: none;
    text-align: left;
    font-size: 12px;
    font-weight: 600;
    color: var(--ink);
    cursor: pointer;
  }
</style>
