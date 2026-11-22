<script>
  import { onDestroy, onMount } from "svelte";
  import { orb } from "../lib/store.svelte.js";
  import { tidy } from "../lib/utils.js";

  let { tab, onClose } = $props();

  let hostEl = $state(null);
  let status = $state("connecting…");
  let error = $state("");

  let term = null;
  let fitAddon = null;
  let resizeObs = null;
  let termId = "";
  let closed = false;

  function themeColors() {
    const cs = getComputedStyle(document.documentElement);
    const get = name => cs.getPropertyValue(name).trim();
    return {
      background: get("--recessed") || "#161817",
      foreground: get("--fg") || "#E2E6DF",
      cursor: get("--rupture") || "#D94326",
      cursorAccent: get("--bg") || "#161817",
      selectionBackground: "rgba(128,128,128,0.28)",
    };
  }

  async function remoteCall(method, params) {
    return orb.rawTab.admin("remote", { peer_id: tab.peer, method, params });
  }

  onMount(async () => {
    try {
      const [{ Terminal }, { FitAddon }] = await Promise.all([
        import("@xterm/xterm"),
        import("@xterm/addon-fit"),
        import("@xterm/xterm/css/xterm.css"),
      ]);
      if (closed || !hostEl) return;

      term = new Terminal({
        fontFamily: '"Ubuntu Sans Mono", ui-monospace, monospace',
        fontSize: 13,
        lineHeight: 1.3,
        cursorBlink: true,
        cursorStyle: "bar",
        theme: themeColors(),
      });
      fitAddon = new FitAddon();
      term.loadAddon(fitAddon);
      term.open(hostEl);
      fitAddon.fit();

      const cols = Math.max(40, term.cols || 80);
      const rows = Math.max(10, term.rows || 24);
      const opened = await remoteCall("host.terminal.open", { cwd: tab.cwd || "", cols, rows });
      termId = opened.terminal_id;
      status = `${tab.where} · ${tidy(tab.cwd || "~")}`;

      term.onData(str => {
        if (!termId || closed) return;
        const bytes = new TextEncoder().encode(str);
        let bin = "";
        for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
        remoteCall("host.terminal.write", { terminal_id: termId, data: btoa(bin) }).catch(() => {});
      });

      term.onResize(({ cols: c, rows: r }) => {
        if (!termId || closed) return;
        remoteCall("host.terminal.resize", { terminal_id: termId, cols: c, rows: r }).catch(() => {});
      });

      resizeObs = new ResizeObserver(() => {
        try { fitAddon?.fit(); } catch {}
      });
      resizeObs.observe(hostEl);
      term.focus();

      // Long-poll read loop
      let offset = 0;
      while (!closed && termId) {
        const res = await remoteCall("host.terminal.read", { terminal_id: termId, offset });
        if (closed) break;
        if (res?.data) {
          const raw = atob(res.data);
          const bytes = new Uint8Array(raw.length);
          for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
          term.write(bytes);
        }
        offset = res?.offset ?? offset;
        if (res?.done) {
          status = "session exited";
          break;
        }
      }
    } catch (err) {
      if (!closed) error = err.message || "terminal unavailable";
    }
  });

  onDestroy(() => {
    closed = true;
    resizeObs?.disconnect();
    term?.dispose();
    if (termId) {
      remoteCall("host.terminal.close", { terminal_id: termId }).catch(() => {});
    }
  });
</script>

<div class="term-drawer">
  <div class="term-head">
    <div class="hud-label">
      <span class="idx">&gt;_</span>
      <span>terminal</span>
      <span class="term-sub">{status}</span>
    </div>
    <button type="button" class="btn plain" onclick={onClose} title="Close terminal (Alt+J)">
      close <kbd>Alt+J</kbd>
    </button>
  </div>
  {#if error}
    <p class="term-err alarm">{error}</p>
  {/if}
  <div class="term-body" bind:this={hostEl}></div>
</div>

<style>
  .term-drawer {
    height: min(44dvh, 380px);
    flex-shrink: 0;
    display: flex;
    flex-direction: column;
    background: var(--recessed);
    border-top: 1px solid var(--rule-strong);
    box-shadow: 0 -8px 28px var(--shade);
    animation: drawer-in 210ms var(--ease);
  }
  .term-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 6px 14px;
    background: var(--raised);
    border-bottom: 1px solid var(--rule);
  }
  .term-sub {
    font-weight: 400;
    text-transform: none;
    letter-spacing: 0;
    color: var(--meta);
  }
  .term-err {
    padding: 8px 14px;
    font-size: var(--small);
  }
  .term-body {
    flex: 1;
    min-height: 0;
    padding: 8px 12px;
    overflow: hidden;
  }
</style>
