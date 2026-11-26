<script>
  import { untrack } from "svelte";
  import { orb } from "../lib/store.svelte.js";

  let mode = $state("spread"); // spread | join
  let info = $state(null);
  let code = $state("");
  let loading = $state(false);
  let loadError = $state("");

  let sshTarget = $state("");
  let sshBusy = $state(false);
  let sshStatus = $state("");
  let sshError = $state("");

  let joinCode = $state("");
  let joinBusy = $state(false);
  let joinError = $state("");

  let copiedKey = $state("");
  let copiedTimer = null;

  let sshHosts = $derived(Array.isArray(info?.ssh_hosts) ? info.ssh_hosts : []);
  let clients = $derived(
    (Array.isArray(info?.clients) ? info.clients : []).map(c =>
      typeof c === "string" ? { id: c, name: c } : { id: c.id || c.name, name: c.name || c.id },
    ),
  );

  $effect(() => {
    if (!orb.spreadModalOpen) return;
    untrack(() => {
      mode = "spread";
      sshTarget = "";
      sshBusy = false;
      sshStatus = "";
      sshError = "";
      joinCode = "";
      joinBusy = false;
      joinError = "";
      copiedKey = "";
      loadSpreadData();
    });
  });

  async function loadSpreadData() {
    loading = true;
    loadError = "";
    try {
      const existing = typeof orb.state?.invitation === "string" ? orb.state.invitation : "";
      if (existing) code = existing;

      const [infoRes, invRes] = await Promise.all([
        orb.ask("spread.info", {}).catch(() => null),
        existing ? Promise.resolve(null) : orb.ask("invite", {}).catch(() => null),
      ]);

      if (infoRes && typeof infoRes === "object") {
        info = infoRes;
      }
      const minted =
        invRes?.code ||
        invRes?.invitation ||
        (typeof invRes === "string" ? invRes : "") ||
        orb.state?.invitation ||
        "";
      if (minted) code = minted;
    } catch (err) {
      loadError = err?.message || String(err);
    } finally {
      loading = false;
    }
  }

  function close() {
    orb.spreadModalOpen = false;
    orb.bumpFocus();
  }

  function installJoinCmd() {
    return `command -v orb >/dev/null || curl -fsSL https://raw.githubusercontent.com/OrdalieTech/orb/main/scripts/install.sh | sh; orb mitosis join ${code}`;
  }

  function browserLink() {
    return `https://netapy.com/orb/#${code}`;
  }

  function clientPrompt(clientName) {
    return `Install Orb (if not already installed) and join my Mitosis mesh using ${clientName}:\n\n${installJoinCmd()}`;
  }

  async function copyText(key, text, label) {
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      copiedKey = key;
      clearTimeout(copiedTimer);
      copiedTimer = setTimeout(() => {
        if (copiedKey === key) copiedKey = "";
      }, 1600);
      orb.showToast(`copied ${label}`);
    } catch {
      orb.showToast("clipboard unavailable");
    }
  }

  async function runSshSpread(e) {
    e?.preventDefault();
    const target = sshTarget.trim();
    if (!target || sshBusy) return;
    sshBusy = true;
    sshStatus = "spreading…";
    sshError = "";
    try {
      const res = await orb.ask("spread.ssh", {
        machine: info?.machine || "",
        target,
        code,
      });
      if (res?.ok === false) {
        sshStatus = "";
        sshError = res.error || res.output || "spread failed";
      } else {
        sshStatus = res?.output || "joined · Mitosis running";
        orb.showToast(`spread to ${target}`);
      }
    } catch (err) {
      sshStatus = "";
      sshError = err?.message || String(err);
    } finally {
      sshBusy = false;
    }
  }

  async function runJoin(e) {
    e?.preventDefault();
    const raw = joinCode.trim();
    if (!raw || joinBusy) return;
    joinBusy = true;
    joinError = "";
    try {
      await orb.joinInvitation(raw, true);
      close();
    } catch (err) {
      joinError = err?.message || String(err);
    } finally {
      joinBusy = false;
    }
  }
</script>

{#if orb.spreadModalOpen}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="spread-backdrop" onclick={e => { if (e.target === e.currentTarget) close(); }}>
    <div class="spread-panel" role="dialog" aria-modal="true" aria-label="Spread and pair Mitosis">
      <div class="spread-head">
        <div class="spread-title-wrap">
          <h2>Spread &amp; Pair</h2>
          {#if info?.machine_name}
            <span class="spread-from">from {info.machine_name}</span>
          {/if}
        </div>
        <div class="spread-tabs" role="tablist">
          <button
            type="button"
            role="tab"
            aria-selected={mode === "spread"}
            class="spread-tab"
            class:on={mode === "spread"}
            onclick={() => (mode = "spread")}
          >Spread</button>
          <button
            type="button"
            role="tab"
            aria-selected={mode === "join"}
            class="spread-tab"
            class:on={mode === "join"}
            onclick={() => (mode = "join")}
          >Join</button>
        </div>
        <button type="button" class="spread-close" onclick={close} aria-label="Close">×</button>
      </div>

      <div class="spread-body">
        {#if mode === "spread"}
          {#if loadError}
            <p class="spread-err">{loadError}</p>
          {/if}

          <!-- 01 · SSH Spread -->
          <section class="spread-sec">
            <div class="hud-label">
              <span class="idx">01</span>
              <span>ssh spread</span>
              {#if sshBusy}<span class="tail">running…</span>{/if}
            </div>
            <form class="ssh-row" onsubmit={runSshSpread}>
              <input
                type="text"
                class="spread-input"
                bind:value={sshTarget}
                placeholder="ssh user@host or ~/.ssh/config alias"
                disabled={sshBusy}
                autocomplete="off"
                spellcheck="false"
              />
              <button
                type="submit"
                class="btn inverted spread-go"
                disabled={sshBusy || !sshTarget.trim()}
              >
                {sshBusy ? "spreading…" : "spread"}
              </button>
            </form>

            {#if sshHosts.length > 0}
              <div class="chip-row" aria-label="SSH config hosts">
                {#each sshHosts as host (host)}
                  <button
                    type="button"
                    class="chip"
                    class:on={sshTarget.trim() === host}
                    onclick={() => { sshTarget = host; sshError = ""; }}
                  >{host}</button>
                {/each}
              </div>
            {/if}

            {#if sshStatus}
              <p class="spread-ok">{sshStatus}</p>
            {/if}
            {#if sshError}
              <p class="spread-err">{sshError}</p>
            {/if}
          </section>

          <!-- 02 · Any Surface Quick Copy -->
          <section class="spread-sec">
            <div class="hud-label">
              <span class="idx">02</span>
              <span>any surface</span>
              {#if loading && !code}<span class="tail">minting code…</span>{/if}
            </div>
            <div class="copy-row">
              <button
                type="button"
                class="copy-btn"
                disabled={!code}
                onclick={() => copyText("install", installJoinCmd(), "install + join")}
              >
                {copiedKey === "install" ? "copied ✓" : "copy install + join"}
              </button>
              <button
                type="button"
                class="copy-btn"
                disabled={!code}
                onclick={() => copyText("link", browserLink(), "browser link")}
              >
                {copiedKey === "link" ? "copied ✓" : "copy browser link"}
              </button>
              <button
                type="button"
                class="copy-btn"
                disabled={!code}
                onclick={() => copyText("code", code, "code")}
              >
                {copiedKey === "code" ? "copied ✓" : "copy code"}
              </button>
            </div>
          </section>

          <!-- 03 · Detected LLM Clients -->
          {#if clients.length > 0}
            <section class="spread-sec">
              <div class="hud-label">
                <span class="idx">03</span>
                <span>detected llm clients</span>
              </div>
              <div class="chip-row">
                {#each clients as c (c.id)}
                  <button
                    type="button"
                    class="chip quiet"
                    disabled={!code}
                    onclick={() => copyText(`client-${c.id}`, clientPrompt(c.name), `prompt for ${c.name}`)}
                  >
                    {copiedKey === `client-${c.id}` ? `copied for ${c.name} ✓` : `copy for ${c.name}`}
                  </button>
                {/each}
              </div>
            </section>
          {/if}
        {:else}
          <!-- Join an existing device -->
          <section class="spread-sec">
            <div class="hud-label">
              <span class="idx">01</span>
              <span>join an existing device</span>
            </div>
            <p class="spread-hint">
              Paste an invitation code from another device running <code>orb mitosis --browser</code>.
            </p>
            <form class="ssh-row" onsubmit={runJoin}>
              <input
                type="text"
                class="spread-input"
                bind:value={joinCode}
                placeholder="orb-mitosis:v1:…"
                disabled={joinBusy}
                autocomplete="off"
                spellcheck="false"
              />
              <button
                type="submit"
                class="btn inverted spread-go"
                disabled={joinBusy || !joinCode.trim()}
              >
                {joinBusy ? "joining…" : "join"}
              </button>
            </form>
            {#if joinError}
              <p class="spread-err">{joinError}</p>
            {/if}
          </section>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .spread-backdrop {
    position: fixed;
    inset: 0;
    z-index: 25;
    display: grid;
    place-items: center;
    padding: 16px;
    background: color-mix(in srgb, var(--bg) 72%, transparent);
    backdrop-filter: blur(4px);
    animation: backdrop-in var(--t-fast) var(--ease);
  }
  .spread-panel {
    width: 100%;
    max-width: 560px;
    max-height: min(86vh, 640px);
    display: flex;
    flex-direction: column;
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 10px;
    box-shadow: 0 18px 48px color-mix(in srgb, #000 22%, transparent);
    overflow: hidden;
    animation: drop-in var(--t-fast) var(--ease);
  }
  .spread-head {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 14px;
    border-bottom: 1px solid var(--rule-soft);
    background: var(--raised);
  }
  .spread-title-wrap {
    display: flex;
    align-items: baseline;
    gap: 8px;
    min-width: 0;
  }
  .spread-head h2 {
    margin: 0;
    font-size: 14px;
    font-weight: 700;
    letter-spacing: -0.01em;
  }
  .spread-from {
    font-size: 11.5px;
    color: var(--meta);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .spread-tabs {
    margin-left: auto;
    display: inline-flex;
    gap: 2px;
    padding: 2px;
    border: 1px solid var(--rule-soft);
    border-radius: var(--radius-sm);
    background: var(--bg);
  }
  .spread-tab {
    padding: 3px 9px;
    border: 0;
    border-radius: 4px;
    background: transparent;
    color: var(--meta);
    font-size: 11.5px;
    font-weight: 600;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .spread-tab.on {
    background: var(--fg);
    color: var(--bg);
  }
  .spread-close {
    padding: 2px 6px;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--meta);
    font-size: 17px;
    line-height: 1;
    cursor: pointer;
  }
  .spread-close:hover {
    background: var(--hover);
    color: var(--fg);
  }

  .spread-body {
    padding: 14px 16px 18px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .spread-sec {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .ssh-row {
    display: flex;
    gap: 8px;
  }
  .spread-input {
    flex: 1;
    min-width: 0;
    padding: 7px 10px;
    border: 1px solid var(--rule);
    border-radius: var(--radius-sm);
    background: var(--raised);
    color: var(--fg);
    font-size: 12.5px;
    font-family: var(--mono);
  }
  .spread-input:focus-visible {
    outline: 0;
    border-color: var(--rule-focus);
  }
  .spread-go {
    flex-shrink: 0;
    padding: 6px 14px;
    font-size: 12px;
    font-weight: 600;
  }

  .chip-row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .chip {
    padding: 4px 9px;
    border: 1px solid var(--rule-soft);
    border-radius: var(--radius-sm);
    background: var(--raised);
    color: var(--mute);
    font-size: 11.5px;
    font-weight: 500;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease), color var(--t-fast) var(--ease);
  }
  .chip:hover:not(:disabled) {
    background: var(--hover);
    border-color: var(--rule);
    color: var(--fg);
  }
  .chip.on {
    background: var(--recessed);
    border-color: var(--fg);
    color: var(--fg);
  }
  .chip.quiet {
    background: transparent;
  }
  .chip:disabled {
    opacity: 0.5;
    cursor: default;
  }

  .copy-row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .copy-btn {
    flex: 1 1 130px;
    padding: 7px 10px;
    border: 1px solid var(--rule);
    border-radius: var(--radius-sm);
    background: var(--raised);
    color: var(--fg);
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
    transition: background-color var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease);
  }
  .copy-btn:hover:not(:disabled) {
    background: var(--hover);
    border-color: var(--rule-focus);
  }
  .copy-btn:disabled {
    opacity: 0.5;
    cursor: default;
  }

  .spread-hint {
    margin: 0;
    font-size: 12px;
    color: var(--meta);
    line-height: 1.45;
  }
  .spread-hint code {
    font-family: var(--mono);
    font-size: 11.5px;
    color: var(--fg);
  }
  .spread-ok {
    margin: 0;
    font-size: 12px;
    color: var(--fg);
    font-family: var(--mono);
  }
  .spread-err {
    margin: 0;
    font-size: 12px;
    color: var(--rupture);
  }
</style>
