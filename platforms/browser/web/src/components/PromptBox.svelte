<script>
  import { orb } from "../lib/store.svelte.js";
  import { base, HOME_COMMANDS, tidy } from "../lib/utils.js";

  let {
    tab = null, // null = Home prompt
    onSent = () => {},
  } = $props();

  let textarea = $state(null);
  let fileInput = $state(null);
  let tabKey = $derived(tab?.id || "");
  let draft = $state("");
  let dragging = $state(false);
  let uploading = $state(false);

  // History navigation index (-1 = current draft)
  let histIdx = -1;
  let savedDraftBeforeHist = "";

  // Palette state (slash commands & @ completions)
  let chosen = $state(-1);
  let dismissed = $state(null);
  let found = $state({ tab: null, token: null, items: [] });
  let findTimer = null;
  let cursorPos = $state(0);

  // Sync draft when switching conversations
  let loadedKey = null;
  $effect(() => {
    const k = tabKey;
    if (k !== loadedKey) {
      if (loadedKey !== null) orb.setDraft(loadedKey, draft);
      loadedKey = k;
      draft = orb.getDraft(k);
      histIdx = -1;
      dismissed = null;
    }
  });

  // Focus textarea when requested by keyboard shortcuts or tab switches
  $effect(() => {
    void orb.focusTick;
    if (orb.screen === "app" && !orb.overview && !orb.modelDeckOpen && !orb.paletteOpen && !orb.shortcutsOpen && !orb.explorerPeer) {
      if (!tab || orb.activeTabId() === tab.id) {
        textarea?.focus({ preventScroll: true });
      }
    }
  });

  // Current target machine & model metadata
  let aim = $derived.by(() => {
    void orb.aimVersion;
    void orb.home;
    void orb.state;
    return orb.getAim();
  });

  // Ensure defaults loaded for Home target machine
  $effect(() => {
    if (!tab && aim.machine?.id) {
      orb.ensureDefaults(aim.machine.id);
    }
  });

  let fresh = $derived(!tab && aim.machine ? orb.defaultsByPeer[aim.machine.id] : null);
  let modelInfo = $derived(
    tab || (fresh ? { model: fresh.default, levels: fresh.levels, thinking: fresh.thinking } : null),
  );
  let modelLabel = $derived.by(() => {
    const raw = modelInfo?.model;
    if (raw) return raw.split("/").slice(1).join("/") || raw;
    if (tab?.loaded && !tab.asleep && !tab.models?.length) return "connect a provider";
    if (!tab && aim.machine?.self) return "own model";
    return "model";
  });
  let shownLevels = $derived((modelInfo?.levels || []).filter(l => l !== "off"));
  let litCount = $derived(shownLevels.indexOf(modelInfo?.thinking) + 1);
  let targetPeer = $derived(tab ? tab.peer : aim.machine?.id || orb.state.self);
  let targetHue = $derived(orb.getMachine(targetPeer)?.hue || 0);
  let aimLabel = $derived.by(() => {
    if (tab) return tab.remote ? tab.where : "this tab";
    if (!aim.machine) return "no device";
    const name = aim.machine.self ? "this tab" : aim.machine.name;
    return `${name}${aim.cwd ? ` · ${base(aim.cwd)}` : ""}`;
  });

  // Compute slash / @ palette choices
  let paletteChoices = $derived.by(() => {
    if (draft === dismissed) return [];
    const typed = /^\/(\S*)$/.exec(draft)?.[1];
    if (typed !== undefined) {
      const all = tab
        ? [...(orb.state.commands || []), ...(tab.commands || [])]
        : (orb.state.commands || []).filter(c => HOME_COMMANDS.has(c.name));
      const starts = all.filter(c => c.name.startsWith(typed));
      const contains = all.filter(c => !c.name.startsWith(typed) && c.name.includes(typed));
      return [...starts, ...contains].slice(0, 28).map(c => ({
        label: `/${c.name}`,
        detail: c.hint || "",
        pick: () => {
          draft = `/${c.name}${c.now ? "" : " "}`;
          orb.setDraft(tabKey, draft);
          if (c.now) submit();
          else textarea?.focus();
        },
      }));
    }
    const token = /(?:^|\s)(@\S*)$/.exec(draft.slice(0, cursorPos))?.[1];
    if (token && tab) {
      if (found.tab === tab.id && found.token === token) {
        return found.items.map(f => ({
          label: f.text.startsWith("/skill:") ? `◆ ${f.label}` : f.label,
          detail: f.detail || "",
          pick: () => {
            const after = draft.slice(cursorPos);
            const end = cursorPos + Math.max(0, after.search(/\s|$/));
            const gap = /\/"?$/.test(f.text) ? "" : " ";
            draft = draft.slice(0, cursorPos - token.length) + f.text + gap + draft.slice(end);
            orb.setDraft(tabKey, draft);
            const caret = cursorPos - token.length + f.text.length + gap.length - (f.text.endsWith('/"') ? 1 : 0);
            requestAnimationFrame(() => {
              textarea?.focus();
              textarea?.setSelectionRange(caret, caret);
              cursorPos = caret;
            });
          },
        }));
      }
    }
    return [];
  });

  // Debounced @ completion fetch
  $effect(() => {
    const token = /(?:^|\s)(@\S*)$/.exec(draft.slice(0, cursorPos))?.[1];
    if (!token || !tab) return;
    if (found.tab === tab.id && found.token === token) return;
    clearTimeout(findTimer);
    const tid = tab.id;
    findTimer = setTimeout(() => {
      orb
        .ask("complete", { tab: tid, text: token.slice(1) })
        .then(items => {
          found = { tab: tid, token, items: items || [] };
        })
        .catch(() => {});
    }, 100);
  });

  $effect(() => {
    if (paletteChoices.length === 0) chosen = -1;
    else if (chosen < 0 || chosen >= paletteChoices.length) chosen = /^\/\S*$/.test(draft) ? 0 : -1;
  });

  async function submit(mode = "send") {
    const text = draft.trim();
    if (tab?.busy && !text) {
      orb.send({ do: "abort", tab: tab.id });
      return;
    }
    if (!text) return;
    draft = "";
    orb.setDraft(tabKey, "");
    histIdx = -1;
    dismissed = null;
    onSent();
    try {
      await orb.submitMessage(tab?.id || "", text, mode);
    } catch {
      if (!draft) {
        draft = text;
        orb.setDraft(tabKey, text);
      }
    }
  }

  function onInput(e) {
    draft = e.currentTarget.value;
    cursorPos = e.currentTarget.selectionStart;
    orb.setDraft(tabKey, draft);
    dismissed = null;
    histIdx = -1;
  }

  function onKeyUp(e) {
    cursorPos = e.currentTarget.selectionStart;
  }

  function onKeyDown(e) {
    if (paletteChoices.length > 0) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        chosen = (chosen + (e.key === "ArrowDown" ? 1 : -1) + paletteChoices.length) % paletteChoices.length;
        return;
      }
      if (e.key === "Tab" || (e.key === "Enter" && !e.shiftKey && chosen >= 0)) {
        e.preventDefault();
        paletteChoices[Math.max(chosen, 0)].pick();
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        dismissed = draft;
        return;
      }
    }

    // History recall with ↑ / ↓
    const hist = orb.getHistory();
    if (e.key === "ArrowUp" && hist.length > 0 && (draft === "" || histIdx >= 0)) {
      if (e.currentTarget.selectionStart === 0) {
        e.preventDefault();
        if (histIdx < 0) {
          savedDraftBeforeHist = draft;
          histIdx = hist.length - 1;
        } else if (histIdx > 0) {
          histIdx--;
        }
        draft = hist[histIdx];
        orb.setDraft(tabKey, draft);
        return;
      }
    }
    if (e.key === "ArrowDown" && histIdx >= 0) {
      e.preventDefault();
      if (histIdx < hist.length - 1) {
        histIdx++;
        draft = hist[histIdx];
      } else {
        histIdx = -1;
        draft = savedDraftBeforeHist;
      }
      orb.setDraft(tabKey, draft);
      return;
    }

    // Alt+Enter or Ctrl/Cmd+Enter queues when busy (or sends)
    if (e.key === "Enter" && (e.altKey || e.ctrlKey || e.metaKey) && !e.isComposing) {
      e.preventDefault();
      submit(tab?.busy ? "queue" : "send");
      return;
    }
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      submit("send");
    }
  }

  async function chooseWhere() {
    const self = orb.getMachine(orb.state.self);
    const now = aim;
    const options = [];
    for (const m of orb.launchers()) {
      const at = now.machine?.id === m.id && tidy(now.cwd);
      options.push({
        label: m.name,
        detail: "home folder",
        on: at === "" || at === "~",
        value: () => orb.aimAt(m.id),
      });
      for (const f of (m.folders || []).filter(f => tidy(f.cwd) !== "~").slice(0, 5)) {
        options.push({
          label: tidy(f.cwd),
          detail: `${m.name} · ${f.threads} thread${f.threads === 1 ? "" : "s"}`,
          on: at === tidy(f.cwd),
          value: () => orb.aimAt(m.id, f.cwd),
        });
      }
      options.push({
        label: `browse folders on ${m.name} ›`,
        detail: "interactive explorer",
        value: () => {
          orb.explorerPeer = m.id;
        },
      });
    }
    for (const m of orb.home.machines || []) {
      for (const r of m.running || []) {
        if (!r.open && !m.self) {
          options.push({
            label: r.label,
            detail: r.busy ? "working · open it" : "running · open it",
            value: () => orb.openEntry(`i:${r.instance}`),
          });
        }
      }
    }
    if (self) {
      options.push({
        label: "this tab",
        detail: "its own Orb · sandboxed js + workspace",
        on: now.machine?.self,
        value: () => orb.aimAt(orb.state.self),
      });
    }
    const picked = await orb.pick("run on", options);
    picked?.();
  }

  async function handleFiles(fileList) {
    const files = [...(fileList || [])];
    if (!files.length) return;
    uploading = true;
    try {
      const paths = await orb.attachFiles(targetPeer, files);
      if (paths.length) {
        const prefix = draft && !draft.endsWith(" ") ? " " : "";
        draft = `${draft}${prefix}${paths.map(p => `@${p}`).join(" ")} `;
        orb.setDraft(tabKey, draft);
        textarea?.focus();
      }
    } catch (err) {
      orb.showError(err.message);
    } finally {
      uploading = false;
    }
  }

  function onDrop(e) {
    e.preventDefault();
    dragging = false;
    if (e.dataTransfer?.files?.length) handleFiles(e.dataTransfer.files);
  }

  function onPaste(e) {
    const files = [...(e.clipboardData?.files || [])];
    if (files.length > 0) {
      e.preventDefault();
      handleFiles(files);
    }
  }

  let busy = $derived(!!tab?.busy);
  let hasText = $derived(draft.trim().length > 0);
</script>

<form
  class="prompt-deck"
  class:dragging
  onsubmit={e => { e.preventDefault(); submit("send"); }}
  ondragover={e => { e.preventDefault(); dragging = true; }}
  ondragleave={() => (dragging = false)}
  ondrop={onDrop}
>
  {#if paletteChoices.length > 0}
    <div class="palette scroll" role="listbox">
      {#each paletteChoices as ch, i}
        <button
          type="button"
          class="pal-row"
          class:on={i === chosen}
          onmousedown={e => e.preventDefault()}
          onclick={ch.pick}
        >
          <b>{ch.label}</b>
          <span>{ch.detail}</span>
        </button>
      {/each}
    </div>
  {/if}

  <div class="input-row">
    <span class="prompt-sigil" aria-hidden="true">&gt;</span>
    <textarea
      bind:this={textarea}
      class="prompt-input scroll"
      rows="2"
      maxlength="16384"
      value={draft}
      oninput={onInput}
      onkeyup={onKeyUp}
      onkeydown={onKeyDown}
      onpaste={onPaste}
      placeholder={tab ? (busy ? "steer or queue a message" : "command or prompt...") : "start session..."}
      aria-label="Message"
    ></textarea>
  </div>

  <div class="deck-bar">
    <button
      type="button"
      class="deck-chip aim"
      disabled={!!tab}
      onclick={chooseWhere}
      title={tab ? tidy(tab.cwd || tab.where) : "Where new sessions run"}
    >
      <i class="led" style="--hue: var(--hue{targetHue})"></i>
      <span>{aimLabel}{!tab ? " ▾" : ""}</span>
    </button>

    <button
      type="button"
      class="deck-chip model"
      onclick={() => orb.openModelDeck(tab)}
      title="Model and reasoning (Alt+M)"
    >
      <span class="model-name">{modelLabel} ▾</span>
      {#if shownLevels.length > 0}
        <span class="meter" aria-hidden="true">
          {#each shownLevels as _, i}
            <i class:lit={i < litCount}></i>
          {/each}
        </span>
      {/if}
    </button>

    <input
      bind:this={fileInput}
      type="file"
      multiple
      hidden
      onchange={e => { handleFiles(e.currentTarget.files); e.currentTarget.value = ""; }}
    />

    <button
      type="button"
      class="deck-icon"
      onclick={() => fileInput?.click()}
      disabled={uploading}
      title="Attach file or drop anywhere in box"
      aria-label="Attach file"
    >
      {uploading ? "…" : "@"}
    </button>

    {#if busy && hasText}
      <button type="button" class="deck-queue" onclick={() => submit("queue")} title="Queue for after this turn (Alt+Enter)">
        queue
      </button>
    {/if}

    <button
      type="submit"
      class="te-action"
      class:stop={busy && !hasText}
      disabled={!hasText && !busy}
      aria-label={busy && !hasText ? "Stop" : busy ? "Steer" : "Send"}
      title={busy && !hasText ? "Stop (Alt+.)" : busy ? "Steer (Enter)" : "Send (Enter · Shift+Enter newline)"}
    ></button>
  </div>
</form>

<style>
  .prompt-deck {
    position: relative;
    flex-shrink: 0;
    width: calc(100% - 2 * var(--margin));
    max-width: 780px;
    margin: 6px auto max(12px, env(safe-area-inset-bottom));
    padding: 0;
    border: 1px solid var(--fg);
    border-radius: 0;
    background: var(--raised);
    transition: border-color var(--t-fast) var(--ease);
  }
  .prompt-deck:focus-within {
    border-color: var(--fg);
  }
  .prompt-deck:focus-within .prompt-sigil {
    color: var(--rupture);
  }
  .prompt-deck.dragging {
    border-color: var(--rupture);
  }
  .input-row {
    display: flex;
    align-items: flex-start;
    padding: 10px 12px 6px 10px;
    gap: 8px;
  }
  .prompt-sigil {
    font-weight: 700;
    line-height: 1.5;
    color: var(--meta);
    user-select: none;
    transition: color var(--t-fast) var(--ease);
  }
  .prompt-input {
    display: block;
    flex: 1;
    width: 100%;
    min-height: 2.5em;
    max-height: min(14lh, 30dvh);
    field-sizing: content;
    padding: 0 0 4px;
    border: 0;
    resize: none;
    background: none;
    font-size: var(--body);
    line-height: 1.5;
  }
  .prompt-input::placeholder { color: var(--meta); }
  .prompt-input:focus-visible { outline: 0; }
  @media (max-width: 600px) {
    .prompt-input { font-size: 16px; }
  }

  .deck-bar {
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 5px 8px;
    border-top: 1px solid var(--rule);
    background: var(--bg);
  }
  .deck-chip {
    display: flex;
    align-items: center;
    gap: 7px;
    min-width: 0;
    height: 26px;
    padding: 0 8px;
    border: 1px solid transparent;
    border-radius: 0;
    background: none;
    cursor: pointer;
    font-size: 12px;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .deck-chip.aim {
    flex: 0 1 auto;
    font-weight: 700;
    color: var(--fg);
  }
  .deck-chip.aim:disabled { opacity: 1; cursor: default; }
  .deck-chip.aim span {
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .deck-chip.model {
    flex: 1 1 auto;
    justify-content: flex-start;
    font-size: 12px;
    color: var(--mute);
  }
  .model-name {
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .meter {
    display: inline-flex;
    align-items: flex-end;
    gap: 2px;
    flex-shrink: 0;
  }
  .meter i {
    width: 3px;
    background: var(--rule);
    transition: background-color var(--t-med) var(--ease);
  }
  .meter i:nth-child(1) { height: 4px; }
  .meter i:nth-child(2) { height: 6px; }
  .meter i:nth-child(3) { height: 8px; }
  .meter i:nth-child(4) { height: 10px; }
  .meter i:nth-child(5) { height: 12px; }
  .meter i:nth-child(6) { height: 14px; }
  .meter i.lit { background: var(--rupture); }

  .deck-icon {
    display: grid;
    place-items: center;
    width: 26px;
    height: 26px;
    flex-shrink: 0;
    border: 1px solid var(--rule);
    border-radius: 0;
    background: var(--raised);
    color: var(--fg);
    font-size: 13px;
    font-weight: 700;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .deck-queue {
    padding: 4px 9px;
    border: 1px solid var(--fg);
    border-radius: 0;
    background: var(--raised);
    color: var(--fg);
    font-size: 11.5px;
    font-weight: 700;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  @media (hover: hover) {
    .deck-chip:hover:enabled {
      background: var(--fg);
      color: var(--bg);
    }
    .deck-icon:hover:enabled,
    .deck-queue:hover {
      background: var(--fg);
      border-color: var(--fg);
      color: var(--bg);
    }
  }

  /* The ONE circular element in the entire console: iconic TE Orange hardware record/send key */
  .te-action {
    display: grid;
    place-items: center;
    width: var(--round);
    height: var(--round);
    flex-shrink: 0;
    margin-left: 4px;
    padding: 0;
    border: 0;
    border-radius: 50%;
    cursor: pointer;
    color: #FFFFFF;
    background: var(--rupture);
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .te-action:hover:enabled {
    background: var(--fg);
    color: var(--bg);
  }
  .te-action:disabled {
    opacity: 1;
    background: var(--recessed);
    color: var(--meta);
    border: 1px solid var(--rule);
    cursor: default;
  }
  .te-action.stop {
    background: var(--rupture);
    color: #FFFFFF;
  }
  .te-action::before {
    content: "";
    width: 52%;
    height: 52%;
    background: currentColor;
    mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cpath d='M8 13.5V3.5M2.75 8.25 8 3l5.25 5.25' fill='none' stroke='black' stroke-width='3' stroke-miterlimit='8'/%3E%3C/svg%3E") center / contain no-repeat;
  }
  .te-action.stop::before {
    width: 34%;
    height: 34%;
    border-radius: 0;
    mask: none;
  }

  .palette {
    position: absolute;
    left: -1px;
    right: -1px;
    bottom: calc(100% + 4px);
    max-height: 260px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    padding: 0;
    border: 1px solid var(--fg);
    border-radius: 0;
    background: var(--raised);
    z-index: 6;
    animation: drop-in 90ms var(--ease);
  }
  .pal-row {
    display: flex;
    align-items: baseline;
    gap: 12px;
    padding: 6px 10px;
    border: 0;
    border-bottom: 1px solid var(--rule-soft);
    background: none;
    text-align: left;
    cursor: pointer;
    font-size: var(--small);
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .pal-row:last-child { border-bottom: 0; }
  .pal-row b { font-weight: 700; flex-shrink: 0; }
  .pal-row span { color: var(--meta); overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  .pal-row.on { background: var(--fg); color: var(--bg); }
  .pal-row.on span { color: color-mix(in srgb, var(--bg) 72%, transparent); }
  @media (hover: hover) {
    .pal-row:hover:not(.on) { background: var(--hover); }
  }
</style>
