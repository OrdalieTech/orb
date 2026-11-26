import { HOME_COMMANDS, named, quotePath, shortPeer, tabName, toBase64 } from "./utils.js";
import { createMockPeer } from "./mock.js";
import {
  closeTile as closeTileNode,
  deserializeTree,
  equalizeTree,
  findLeaf,
  findLeafByTab,
  isLeaf,
  listLeaves,
  makeLeaf,
  pruneTree,
  serializeTree,
  setSplitRatio as setSplitRatioNode,
  setTileTab,
  splitTile as splitTileNode,
  visibleTabIds,
} from "./tiles.js";

// Code-mode sandboxed iframe + worker (opaque origin, 2-min timeout, nested tool calls).
function sandboxWorker() {
  const out = [];
  const show = v => (typeof v === "string" ? v : JSON.stringify(v, null, 2) ?? String(v));
  const waiting = new Map();
  for (const k of ["log", "info", "warn", "error"]) {
    console[k] = (...a) => out.push(a.map(show).join(" "));
  }
  let next = 0;
  const call = (name, args) =>
    new Promise((ok, no) => {
      waiting.set(++next, { ok, no });
      postMessage({ call: { k: next, name, args } });
    });
  onmessage = async ({ data }) => {
    if (data.reply) {
      const { ok, no } = waiting.get(data.reply.k);
      waiting.delete(data.reply.k);
      return data.reply.failed ? no(new Error(data.reply.text)) : ok(data.reply.text);
    }
    const tools = Object.fromEntries(data.names.map(name => [name, args => call(name, args ?? {})]));
    try {
      const value = await (0, eval)("(async tools => {" + data.code + "\n})")(tools);
      postMessage({ out, value: value === undefined ? undefined : show(value) });
    } catch (error) {
      postMessage({ out, error: String(error?.stack || error) });
    }
  };
}

function sandboxFrame(source) {
  const runs = new Map();
  onmessage = ({ data }) => {
    if (data.reply) return runs.get(data.id)?.postMessage({ reply: data.reply });
    const worker = new Worker(URL.createObjectURL(new Blob([source])));
    const end = reply => {
      clearTimeout(timer);
      worker.terminate();
      runs.delete(data.id);
      parent.postMessage({ id: data.id, ...reply }, "*");
    };
    const timer = setTimeout(() => end({ error: "stopped after 2 min" }), 120000);
    runs.set(data.id, worker);
    worker.onmessage = event =>
      event.data.call ? parent.postMessage({ id: data.id, call: event.data.call }, "*") : end(event.data);
    worker.onerror = event => {
      event.preventDefault();
      end({ error: event.message });
    };
    worker.postMessage({ code: data.code, names: data.names });
  };
}

let sandboxEl = null;
let sandboxReady = null;
let nextSandboxRun = 0;
const sandboxRuns = new Map();

function runCodeMode(code, names, call) {
  if (!sandboxEl) {
    sandboxEl = Object.assign(document.createElement("iframe"), {
      hidden: true,
      srcdoc: `<script>(${sandboxFrame})(${JSON.stringify(`(${sandboxWorker})()`)})<\/script>`,
    });
    sandboxEl.setAttribute("sandbox", "allow-scripts");
    sandboxReady = new Promise(resolve => (sandboxEl.onload = resolve));
    document.body.append(sandboxEl);
  }
  return sandboxReady.then(
    () =>
      new Promise(resolve => {
        const id = ++nextSandboxRun;
        sandboxRuns.set(id, { resolve, call });
        sandboxEl.contentWindow.postMessage({ id, code, names: JSON.parse(names) }, "*");
      }),
  );
}

if (typeof window !== "undefined") {
  window.addEventListener("message", ({ source, data }) => {
    const ongoing = source === sandboxEl?.contentWindow && sandboxRuns.get(data?.id);
    if (!ongoing) return;
    if (data.call) {
      const reply = answer =>
        sandboxEl.contentWindow.postMessage({ id: data.id, reply: { k: data.call.k, ...answer } }, "*");
      ongoing
        .call(data.call.name, JSON.stringify(data.call.args))
        .then(
          answer => JSON.parse(answer),
          error => ({ text: error.message, failed: true }),
        )
        .then(reply);
      return;
    }
    ongoing.resolve(JSON.stringify({ out: data.out || [], value: data.value, error: data.error }));
    sandboxRuns.delete(data.id);
  });
}

function createOrbStore() {
  // Core reactive state (shallow / raw for heavy collections to avoid Svelte deep proxy overhead).
  let screen = $state("boot"); // boot | invite | approve | app | settings | elsewhere
  let elsewhereText = $state("");
  let me = $state("");
  let error = $state("");
  let state = $state.raw({ self: "", up: false, tabs: [], commands: [], summary: "", launching: "", login: null, latest: "" });
  let home = $state.raw({ machines: [], entries: [] });
  // Map tabId -> raw array of immutable Row objects; replaced by shallow copy on patch so only
  // the affected tab's VirtualTranscript re-evaluates.
  let rowsByTab = $state({});

  // UI Navigation & Layout state (recursive binary split-tile tree, aligned with Desktop)
  const initialLeaf = makeLeaf("");
  let tileTree = $state(initialLeaf);
  let activeTileId = $state(initialLeaf.id);
  let zoomedTileId = $state("");
  let shown = $state(""); // active tile's conversation id, "" = Home
  let restoreLayout = (() => {
    try {
      return JSON.parse(localStorage.getItem("orb.tab.layout") || "null");
    } catch {
      return null;
    }
  })();
  let overview = $state(false);
  let mitosisMap = $state(typeof location !== "undefined" && new URLSearchParams(location.search).get("swarm") === "1");
  let deviceFilter = $state("");
  let dismissedLaunch = $state("");
  const initialTheme = localStorage.getItem("orb.tab.theme") || "auto";
  let theme = $state(initialTheme); // auto | light | dark

  // Settings navigation
  let settingsPage = $state({ name: "general", peer: "", vendor: "" });
  let latestRelease = $state("");

  // Modals & Drawers
  let modelDeckOpen = $state(false);
  let modelDeckTab = $state(null); // null = use active conversation or Home aim
  let paletteOpen = $state(false);
  let shortcutsOpen = $state(false);
  let explorerPeer = $state("");
  let terminalTab = $state("");
  let viewerImage = $state(null); // { src, tabId, ref }
  let pickerState = $state(null); // { title, options, field, value, hint, resolve }
  let inlinePaletteOpen = $state(false);
  let toolsToggleSeq = $state(0);
  let toolsDefaultExpanded = $state(false);

  // Toast
  let toastState = $state(null); // { text, undo }
  let toastTimer = null;

  // Caches (kept reactive via plain objects replaced shallowly)
  let defaultsByPeer = $state.raw({});
  let providersByPeer = $state.raw({});
  let accountsByPeer = $state.raw({});

  // Drafts & Prompt history (non-reactive map for zero keystroke re-render churn, plus reactive mirror for active)
  const drafts = new Map();
  let promptHistory = [];
  let reopenStack = [];
  let focusTick = $state(0);

  // Worker peer handle & RPC correlation
  let tab = null;
  let askedSeq = 0;
  const replies = new Map();
  const waitingOnline = [];
  let restoreTab = localStorage.getItem("orb.tab.shown") || "";
  let joinedAt = 0;
  let isBench = false;

  // rAF-coalesced batching for high-frequency streaming row patches from peer.wasm worker.
  // State and home frames apply synchronously in wire order so reply callbacks (start/open/waitOnline)
  // always observe the updated tab list before focus() runs.
  const pendingRows = new Map(); // tabId -> list of { at, rows }
  let flushScheduled = false;

  // BroadcastChannel for single-tab storage ownership (WhatsApp Web pattern)
  const moves = typeof BroadcastChannel !== "undefined" ? new BroadcastChannel("orb-tab") : null;

  function applyTheme(next) {
    theme = next;
    localStorage.setItem("orb.tab.theme", next);
    if (next === "auto") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.setAttribute("data-theme", next);
  }
  applyTheme(initialTheme);

  function showError(msg) {
    error = msg || "";
  }

  function showToast(text, undo = null) {
    clearTimeout(toastTimer);
    toastState = { text, undo };
    toastTimer = setTimeout(() => {
      toastState = null;
    }, undo ? 3200 : 2200);
  }

  function dismissToast() {
    clearTimeout(toastTimer);
    toastState = null;
  }

  function send(intent) {
    if (!tab) return;
    tab.app(JSON.stringify(intent));
  }

  function ask(name, args = {}) {
    const id = `p${++askedSeq}`;
    return new Promise((resolve, reject) => {
      replies.set(id, m => (m.error ? reject(new Error(m.error)) : resolve(m.result)));
      send({ do: name, id, ...args });
    });
  }

  function flushRows() {
    flushScheduled = false;
    if (pendingRows.size === 0) return;
    const nextMap = { ...rowsByTab };
    for (const [tabId, patches] of pendingRows) {
      let arr = nextMap[tabId] ? nextMap[tabId].slice() : [];
      for (const p of patches) {
        const incoming = p.rows || [];
        if (p.at <= arr.length) arr.length = p.at;
        for (let i = 0; i < incoming.length; i++) arr.push(incoming[i]);
      }
      nextMap[tabId] = arr;
    }
    pendingRows.clear();
    rowsByTab = nextMap;
  }

  function applyState(m) {
    const next = { ...m, tabs: m.tabs || [] };
    state = next;

    // Prune rows for closed tabs
    const tabIds = new Set(next.tabs.map(t => t.id));
    let pruned = false;
    for (const id of Object.keys(rowsByTab)) {
      if (!tabIds.has(id)) {
        delete rowsByTab[id];
        pendingRows.delete(id);
        pruned = true;
      }
    }
    if (pruned) rowsByTab = { ...rowsByTab };

    if (restoreLayout) {
      const restored = deserializeTree(restoreLayout);
      restoreLayout = null;
      if (restored) {
        const prunedTree = pruneTree(restored, tabIds);
        const leaves = listLeaves(prunedTree);
        if (leaves.some(l => l.tab)) {
          tileTree = prunedTree;
          activeTileId = (leaves.find(l => l.tab === restoreTab) || leaves[0]).id;
          shown = findLeaf(tileTree, activeTileId)?.tab || "";
          restoreTab = "";
          syncVisibleTabs();
        }
      }
    }
    if (restoreTab && tabIds.has(restoreTab)) {
      focus(restoreTab);
    }
    restoreTab = "";
    const prunedTree = pruneTree(tileTree, tabIds);
    if (prunedTree !== tileTree) {
      tileTree = prunedTree;
      persistLayout();
    }
    if (!findLeaf(tileTree, activeTileId)) {
      activeTileId = listLeaves(tileTree)[0]?.id || "";
    }
    if (zoomedTileId && !findLeaf(tileTree, zoomedTileId)) zoomedTileId = "";
    shown = findLeaf(tileTree, activeTileId)?.tab || "";
    if (terminalTab && !tabIds.has(terminalTab)) terminalTab = "";

    for (const w of waitingOnline.splice(0)) {
      const found = next.tabs.find(t => t.id === w.id);
      if (found?.online) w.resolve();
      else waitingOnline.push(w);
    }

    updateTitle();
  }

  function heard(m) {
    if (m.t === "reply") {
      flushRows();
      const cb = replies.get(m.id);
      replies.delete(m.id);
      cb?.(m);
      return;
    }
    if (m.t === "alert") {
      notify(m);
      return;
    }
    if (m.t === "state") {
      applyState(m);
      return;
    }
    if (m.t === "home") {
      home = m;
      return;
    }
    if (m.t === "rows") {
      let q = pendingRows.get(m.tab);
      if (!q) {
        q = [];
        pendingRows.set(m.tab, q);
      }
      q.push({ at: m.at, rows: m.rows || [] });
      if (!flushScheduled) {
        flushScheduled = true;
        requestAnimationFrame(flushRows);
      }
    }
  }

  function notify(m) {
    if (typeof Notification === "undefined" || Notification.permission !== "granted") return;
    const n = new Notification(m.title, { body: m.text, tag: m.tab, icon: "ordalie.svg" });
    n.onclick = () => {
      window.focus();
      focus(m.tab);
      n.close();
    };
  }

  function updateTitle() {
    const t = getTab(activeTabId());
    const unread = (state.tabs || []).reduce((n, c) => n + (c.unread || 0), 0);
    const hasAsk = (state.tabs || []).some(c => c.ask);
    document.title = `${hasAsk ? "● " : unread ? `(${unread}) ` : ""}${t ? named(t.title) : "Orb"}`;
  }

  function getTab(id) {
    return (state.tabs || []).find(t => t.id === id) || null;
  }

  function getMachine(id) {
    return (home.machines || []).find(m => m.id === id) || null;
  }

  function activeTabId() {
    return findLeaf(tileTree, activeTileId)?.tab || shown || "";
  }

  function isSplit() {
    return !isLeaf(tileTree);
  }

  function persistLayout() {
    try {
      localStorage.setItem("orb.tab.layout", JSON.stringify(serializeTree(tileTree)));
      localStorage.setItem("orb.tab.shown", activeTabId());
    } catch {}
  }

  function syncVisibleTabs() {
    if (overview || mitosisMap) {
      send({ do: "show", tabs: (state.tabs || []).map(t => t.id) });
      return;
    }
    const visible = visibleTabIds(tileTree);
    send({ do: "show", tabs: visible });
  }

  function focusTile(tileId) {
    const leaf = findLeaf(tileTree, tileId);
    if (!leaf) return;
    activeTileId = leaf.id;
    shown = leaf.tab || "";
    updateTitle();
    persistLayout();
  }

  function cycleTile(delta) {
    const leaves = listLeaves(tileTree);
    if (leaves.length < 2) return;
    const at = Math.max(0, leaves.findIndex(l => l.id === activeTileId));
    const next = leaves[(at + delta + leaves.length) % leaves.length];
    if (next) {
      if (zoomedTileId) zoomedTileId = next.id;
      focusTile(next.id);
    }
  }

  function focus(id, tileId = "") {
    showError("");
    if (toastState?.undo) dismissToast();
    overview = false;
    mitosisMap = false;
    if (screen === "settings") screen = "app";
    // If clicking a tab already shown in another tile (and no explicit tileId target), jump to that tile.
    if (id && !tileId && !isLeaf(tileTree)) {
      const existing = findLeafByTab(tileTree, id);
      if (existing) {
        activeTileId = existing.id;
        if (zoomedTileId) zoomedTileId = existing.id;
        shown = existing.tab;
        syncVisibleTabs();
        persistLayout();
        updateTitle();
        return;
      }
    }
    const targetId = tileId && findLeaf(tileTree, tileId) ? tileId : activeTileId;
    tileTree = setTileTab(tileTree, targetId, id || "");
    activeTileId = targetId;
    shown = id || "";
    syncVisibleTabs();
    persistLayout();
    updateTitle();
  }

  function toggleOverview() {
    if (toastState?.undo) dismissToast();
    if (screen === "settings") screen = "app";
    mitosisMap = false;
    overview = !overview;
    syncVisibleTabs();
  }

  function toggleMitosisMap() {
    if (toastState?.undo) dismissToast();
    if (screen === "settings") screen = "app";
    overview = false;
    mitosisMap = !mitosisMap;
    syncVisibleTabs();
  }

  function splitActiveTile(side = "right", targetTab = "", tileId = "") {
    const fromId = tileId && findLeaf(tileTree, tileId) ? tileId : activeTileId;
    let pickTab = targetTab;
    if (!pickTab) {
      const shownSet = new Set(visibleTabIds(tileTree));
      pickTab = (state.tabs || []).find(t => !shownSet.has(t.id))?.id || "";
    }
    const res = splitTileNode(tileTree, fromId, side, pickTab);
    if (!res.createdId) {
      showToast("maximum split tiles reached");
      return;
    }
    tileTree = res.tree;
    activeTileId = res.createdId;
    zoomedTileId = "";
    shown = pickTab;
    overview = false;
    syncVisibleTabs();
    persistLayout();
    updateTitle();
  }

  function closeTileById(tileId = "") {
    const targetId = tileId && findLeaf(tileTree, tileId) ? tileId : activeTileId;
    const res = closeTileNode(tileTree, targetId);
    tileTree = res.tree;
    activeTileId = res.nextActiveId;
    if (zoomedTileId === targetId || isLeaf(tileTree)) zoomedTileId = "";
    shown = findLeaf(tileTree, activeTileId)?.tab || "";
    syncVisibleTabs();
    persistLayout();
    updateTitle();
  }

  function toggleSplit(targetId = "") {
    if (!targetId && !isLeaf(tileTree)) {
      closeTileById(activeTileId);
      return;
    }
    splitActiveTile("right", targetId);
  }

  function setSplitRatioById(splitId, ratio) {
    tileTree = setSplitRatioNode(tileTree, splitId, ratio);
    persistLayout();
  }

  function equalizeTiles() {
    if (isLeaf(tileTree)) return;
    tileTree = equalizeTree(tileTree);
    persistLayout();
  }

  function toggleZoomTile(tileId = "") {
    if (isLeaf(tileTree)) return;
    const targetId = tileId || activeTileId;
    zoomedTileId = zoomedTileId === targetId ? "" : targetId;
  }

  function dropTabOnTile(tabId, tileId, zone = "center") {
    if (!tabId || !findLeaf(tileTree, tileId)) return;
    if (zone === "center") {
      focus(tabId, tileId);
      return;
    }
    splitActiveTile(zone, tabId, tileId);
  }

  function launchers() {
    return (home.machines || []).filter(m => !m.self && m.launch && m.connected);
  }

  function getAim() {
    let saved = {};
    try {
      saved = JSON.parse(localStorage.getItem("orb.tab.aim") || "{}");
    } catch {}
    const all = launchers();
    const self = getMachine(state.self);
    if (saved.machine === state.self && self) return { machine: self, cwd: "" };
    const m =
      all.find(d => d.id === saved.machine) ||
      all.find(d => d.id === localStorage.getItem("orb.tab.owner")) ||
      all[0];
    return m ? { machine: m, cwd: m.id === saved.machine ? saved.cwd || "" : "" } : { machine: self, cwd: "" };
  }

  let aimVersion = $state(0);
  function aimAt(id, cwd = "") {
    localStorage.setItem("orb.tab.aim", JSON.stringify({ machine: id, cwd }));
    aimVersion++;
    ensureDefaults(id);
  }

  function defaultPeer() {
    return getTab(activeTabId())?.peer || getAim().machine?.id || state.self;
  }

  function waitOnline(id) {
    if (getTab(id)?.online) return Promise.resolve();
    return new Promise(resolve => waitingOnline.push({ id, resolve }));
  }

  async function openEntry(key, pane = "primary") {
    try {
      const r = await ask("open", { key });
      if (r?.tab) focus(r.tab, pane);
    } catch (err) {
      if (err.message !== state.launching) showError(err.message);
    }
  }

  async function openExchange(ex) {
    if (!ex || ex.scope !== "peer") return;
    const entries = home.entries || [];
    const key = ex.session && ex.peer
      ? (entries.find(e => e.key === `i:${ex.session}`)?.key || `t:${ex.peer}|${ex.session}`)
      : ex.session
        ? `i:${ex.session}`
        : "";
    if (key) await openEntry(key);
  }

  async function startSession(text) {
    const { machine: m, cwd } = getAim();
    if (!m) throw new Error("no device to start on yet");
    if (!m.self) {
      const r = await ask("start", { machine: m.id, cwd, text });
      if (r?.tab) focus(r.tab);
      return;
    }
    const own = m.running?.[0];
    if (!own) throw new Error("this tab's Orb is still starting");
    const { tab: id } = await ask("open", { key: `i:${own.instance}` });
    focus(id);
    await waitOnline(id);
    await ask("send", { tab: id, text: `/new ${text}` });
  }

  function closeTab(c) {
    if (!c) return;
    const list = state.tabs || [];
    const at = list.findIndex(x => x.id === c.id);
    const entry = (home.entries || []).find(e => e.tab === c.id);
    send({ do: "close", tab: c.id });
    state = { ...state, tabs: list.filter(x => x.id !== c.id) };
    const shownElsewhere = new Set(visibleTabIds(tileTree));
    shownElsewhere.delete(c.id);
    const replacement =
      [list[at + 1], list[at - 1], ...list].find(x => x && x.id !== c.id && !shownElsewhere.has(x.id))?.id || "";
    for (const leaf of listLeaves(tileTree)) {
      if (leaf.tab === c.id) {
        if (!isLeaf(tileTree) && !replacement) {
          const res = closeTileNode(tileTree, leaf.id);
          tileTree = res.tree;
          if (activeTileId === leaf.id) activeTileId = res.nextActiveId;
        } else {
          tileTree = setTileTab(tileTree, leaf.id, replacement);
        }
      }
    }
    if (zoomedTileId && !findLeaf(tileTree, zoomedTileId)) zoomedTileId = "";
    shown = findLeaf(tileTree, activeTileId)?.tab || "";
    syncVisibleTabs();
    persistLayout();
    updateTitle();
    if (!entry) return;
    const again = () =>
      ask("open", { key: entry.key }).then(
        r => {
          if (r?.tab) {
            send({ do: "move", tab: r.tab, to: at });
            focus(r.tab);
          }
        },
        err => showError(err.message),
      );
    reopenStack.push(again);
    if (reopenStack.length > 20) reopenStack.shift();
    showToast(`closed ${named(c.title)}`, () => {
      reopenStack = reopenStack.filter(f => f !== again);
      again();
    });
  }

  function reopenClosed() {
    const fn = reopenStack.pop();
    if (fn) fn();
  }

  function stepTab(by) {
    const list = (state.tabs || []).map(c => c.id);
    if (list.length === 0) return;
    const cur = activeTabId();
    const at = list.indexOf(cur);
    let nextId;
    if (at < 0) {
      nextId = by >= 0 ? list[0] : list[list.length - 1];
    } else {
      nextId = list[(at + by + list.length) % list.length];
    }
    focus(nextId, activeTileId);
  }

  function getDraft(id) {
    return drafts.get(id || "") || "";
  }

  function setDraft(id, val) {
    drafts.set(id || "", val);
  }

  function pushHistory(text) {
    promptHistory = [...promptHistory.filter(h => h !== text), text].slice(-50);
  }

  function getHistory() {
    return promptHistory;
  }

  async function submitMessage(tabId, text, mode = "send") {
    try {
      const t = tabId ? getTab(tabId) : null;
      const trimmed = (text || "").trim();
      if (t?.busy && !trimmed) {
        send({ do: "abort", tab: t.id });
        return true;
      }
      if (!trimmed) return false;
      if (!tab) throw new Error("Orb is still connecting — try again in a moment");
      showError("");
      pushHistory(trimmed);
      if (typeof Notification !== "undefined" && Notification.permission === "default") {
        try {
          Promise.resolve(Notification.requestPermission()).catch(() => {});
        } catch {}
      }
      const name = /^\/(\S+)/.exec(trimmed)?.[1];
      if (t?.busy && mode !== "queue" && !(state.commands || []).some(c => c.name === name)) {
        send({ do: "steer", tab: t.id, text: trimmed });
        return true;
      }
      if (!t && name && name !== "new" && HOME_COMMANDS.has(name)) {
        handleOutcome(await ask("send", { text: trimmed }));
        return true;
      }
      if (!t) {
        await startSession(name === "new" ? trimmed.slice(4).trim() : trimmed);
        return true;
      }
      handleOutcome(await ask("send", { tab: t.id, text: trimmed }));
      return true;
    } catch (err) {
      showError(err?.message || String(err));
      throw err;
    }
  }

  function handleOutcome(r) {
    if (r?.copy) {
      try {
        Promise.resolve(navigator.clipboard?.writeText(r.copy))
          .then(() => showToast("copied the last answer"))
          .catch(() => {});
      } catch {}
    }
    if (r?.nav) {
      if (r.nav === "model") openModelDeck();
      else if (r.nav === "home") focus("");
      else openSettings({ providers: "providers", plugins: "plugins", pair: "mitosis" }[r.nav] || "general", defaultPeer());
    }
  }

  // File & Image attachment (supports remote machines via host.attach AND this tab's /workspace).
  async function attachFiles(peerId, files) {
    const paths = [];
    for (const file of files) {
      if (file.size > 12 * 1024 * 1024) {
        showError(`${file.name} is over 12 MiB`);
        continue;
      }
      const buf = new Uint8Array(await file.arrayBuffer());
      const r = await ask("attach", {
        machine: peerId || state.self,
        name: file.name || "attachment",
        data: toBase64(buf),
      });
      if (r?.path) paths.push(quotePath(r.path));
    }
    return paths;
  }

  // Defaults / Providers / Accounts caching
  async function loadTabDefaults() {
    if (!tab) return null;
    const [d, accts] = await Promise.all([tab.models(), tab.accounts().catch(() => [])]);
    if (d?.models?.length) {
      const st = await tab.rpc("get_state").catch(() => null);
      if (!st?.model || st.model.provider === "unknown") await tab.rpc("new_session").catch(() => {});
    }
    defaultsByPeer = { ...defaultsByPeer, [state.self]: d || { models: [] } };
    accountsByPeer = { ...accountsByPeer, [state.self]: accts || [] };
    return d;
  }

  function ensureDefaults(peerId) {
    if (!peerId || !tab) return;
    if (peerId in defaultsByPeer) return;
    defaultsByPeer = { ...defaultsByPeer, [peerId]: null };
    if (peerId === state.self) {
      loadTabDefaults().catch(() => {
        const copy = { ...defaultsByPeer };
        delete copy[peerId];
        defaultsByPeer = copy;
      });
    } else {
      ask("models", { machine: peerId }).then(
        d => {
          defaultsByPeer = { ...defaultsByPeer, [peerId]: d || { models: [] } };
        },
        () => {
          const copy = { ...defaultsByPeer };
          delete copy[peerId];
          defaultsByPeer = copy;
        },
      );
    }
  }

  async function refreshPeerMeta(peerId) {
    if (!peerId || !tab) return;
    if (peerId === state.self) {
      await loadTabDefaults();
      return;
    }
    ask("models", { machine: peerId }).then(d => {
      if (d) defaultsByPeer = { ...defaultsByPeer, [peerId]: d };
    }, () => {});
    ask("providers", { machine: peerId }).then(p => {
      if (p) providersByPeer = { ...providersByPeer, [peerId]: p };
    }, () => {});
    ask("accounts", { machine: peerId }).then(a => {
      if (a) accountsByPeer = { ...accountsByPeer, [peerId]: a };
    }, () => {});
  }

  async function setDefaultModel(peerId, name, level = "") {
    try {
      if (peerId === state.self) {
        const [provider, ...rest] = name.split("/");
        const d = await tab.models(provider, rest.join("/"), level);
        if (d) defaultsByPeer = { ...defaultsByPeer, [state.self]: d };
      } else {
        await ask("default", { machine: peerId, name, level });
        const d = await ask("models", { machine: peerId }).catch(() => null);
        if (d) defaultsByPeer = { ...defaultsByPeer, [peerId]: d };
      }
    } catch (err) {
      showError(err.message);
    }
  }

  async function switchAccount(peerId, provider, accountId) {
    try {
      if (peerId === state.self) {
        const list = await tab.accounts(provider, accountId);
        accountsByPeer = { ...accountsByPeer, [state.self]: list || [] };
      } else {
        await ask("account", { machine: peerId, provider, account: accountId });
        const list = await ask("accounts", { machine: peerId }).catch(() => null);
        if (list) accountsByPeer = { ...accountsByPeer, [peerId]: list };
      }
    } catch (err) {
      showError(err.message);
    }
  }

  function cycleReasoning() {
    const t = getTab(activeTabId());
    if (t) {
      const levels = t.levels || [];
      if (levels.length < 2) return;
      const next = levels[(levels.indexOf(t.thinking) + 1) % levels.length];
      send({ do: "thinking", tab: t.id, name: next });
      showToast(`reasoning · ${next}`);
      return;
    }
    const { machine: m } = getAim();
    const def = m ? defaultsByPeer[m.id] : null;
    const levels = def?.levels || [];
    if (!m || !def?.default || levels.length < 2) return;
    const next = levels[(levels.indexOf(def.thinking) + 1) % levels.length];
    setDefaultModel(m.id, def.default, next);
    showToast(`default reasoning · ${next}`);
  }

  function toggleToolsExpanded() {
    toolsDefaultExpanded = !toolsDefaultExpanded;
    toolsToggleSeq++;
    showToast(toolsDefaultExpanded ? "tool outputs expanded" : "tool outputs collapsed");
  }

  function openModelDeck(explicitTab = null) {
    modelDeckTab = explicitTab;
    modelDeckOpen = true;
    const t = explicitTab || getTab(activeTabId());
    const peer = t ? t.peer : getAim().machine?.id;
    if (peer) refreshPeerMeta(peer);
  }

  function openSettings(name = "general", peer = "", vendor = "") {
    showError("");
    settingsPage = { name, peer: peer || defaultPeer(), vendor };
    screen = "settings";
    if (name === "mitosis" && !latestRelease && !isBench) {
      fetch("https://api.github.com/repos/OrdalieTech/orb/releases/latest", { headers: { Accept: "application/vnd.github+json" } })
        .then(r => (r.ok ? r.json() : null))
        .then(data => {
          if (data?.tag_name) latestRelease = data.tag_name.replace(/^v/, "");
        })
        .catch(() => {});
    }
  }

  function pick(title, options, opts = {}) {
    return new Promise(resolve => {
      pickerState = {
        title,
        options,
        field: !!opts.field,
        value: opts.value || "",
        hint: opts.hint || "",
        resolve: val => {
          pickerState = null;
          resolve(val);
        },
      };
    });
  }

  async function renamePrompt(title, apply) {
    const name = (await pick("rename", [], { field: true, value: title }))?.trim();
    if (name) apply(name);
  }

  async function openTabMenu(c) {
    const inSplit = !!findLeafByTab(tileTree, c.id) && !isLeaf(tileTree);
    const what = await pick(named(c.title), [
      { label: "rename" },
      ...(c.remote ? [{ label: "open terminal" }] : []),
      { label: "split right" },
      { label: "split down" },
      ...(inSplit ? [{ label: "close tile" }] : []),
      { label: "close conversation" },
    ]);
    if (what === "rename") renamePrompt(c.title, name => send({ do: "rename", tab: c.id, name }));
    if (what === "open terminal") terminalTab = c.id;
    if (what === "split right") splitActiveTile("right", c.id);
    if (what === "split down") splitActiveTile("bottom", c.id);
    if (what === "close tile") {
      const leaf = findLeafByTab(tileTree, c.id);
      if (leaf) closeTileById(leaf.id);
    }
    if (what === "close conversation") closeTab(c);
  }

  async function openThreadMenu(e) {
    const c = getTab(e.tab);
    const what = await pick(named(e.title), [
      { label: "rename" },
      ...(c ? [{ label: "close conversation" }] : []),
      ...(e.deletable ? [{ label: "delete forever" }] : []),
    ]);
    if (what === "rename") {
      renamePrompt(e.title, name => ask("rename", { key: e.key, name }).catch(err => showError(err.message)));
    }
    if (what === "close conversation") closeTab(c);
    if (
      what === "delete forever" &&
      (await pick(`delete ${named(e.title)}? it cannot come back`, [{ label: "keep it" }, { label: "delete forever" }])) ===
        "delete forever"
    ) {
      send({ do: "delete", key: e.key });
    }
  }

  async function openDeviceMenu(m) {
    const what = await pick(m.self ? "this tab" : m.name, [
      ...(m.launch && m.connected ? [{ label: "browse folders" }] : []),
      { label: "providers" },
      { label: "plugins" },
      ...(m.self ? [] : [{ label: "rename" }, { label: "forget", detail: "takes it out of this tab's view" }]),
    ]);
    if (what === "browse folders") explorerPeer = m.id;
    if (what === "providers" || what === "plugins") openSettings(what, m.id);
    if (what === "rename") renamePrompt(m.name, name => send({ do: "rename", machine: m.id, name }));
    if (
      what === "forget" &&
      (await pick(`forget ${m.name}?`, [{ label: "keep it" }, { label: "forget" }])) === "forget"
    ) {
      ask("forget", { machine: m.id }).catch(err => showError(err.message));
    }
  }

  // Boot & Pairing lifecycle
  async function boot() {
    const params = new URLSearchParams(location.search);
    if (params.has("bench")) {
      isBench = true;
      const rowCount = parseInt(params.get("rows") || "2000", 10) || 2000;
      const mock = createMockPeer({
        rowCount,
        emit: text => heard(JSON.parse(text)),
      });
      tab = {
        admin: async (method, p = {}) => JSON.parse(await mock.mitosisAdmin(method, JSON.stringify(p), "")),
        app: line => mock.app(line),
        plugins: async (...args) => JSON.parse(await mock.plugins(...args)),
        models: async (...args) => JSON.parse(await mock.models(...args)),
        accounts: async (...args) => JSON.parse(await mock.accounts(...args)),
        rpc: async (type, p = {}) => {
          let ans;
          await mock.command(JSON.stringify({ id: "b1", type, ...p }));
          return ans || { models: [{ provider: "anthropic", id: "claude-sonnet-4-6" }] };
        },
      };
      ({ peer_id: me } = await tab.admin("self"));
      latestRelease = "0.27.0";
      send({ do: "hello", budget: 6 });
      send({ do: "visible", on: true });
      screen = "app";
      if (params.get("open") === "heavy") {
        setTimeout(() => focus("tab-heavy"), 30);
      }
      return true;
    }

    const take = sessionStorage.getItem("orb.take");
    sessionStorage.removeItem("orb.take");
    if (take) {
      showError("taking your Orb over from the other tab…");
      moves?.postMessage("take");
    }
    let releaseLock = null;
    const held = await new Promise(resolve =>
      navigator.locks.request("orb-tab", take ? {} : { ifAvailable: true }, lock => {
        resolve(!!lock);
        return lock && new Promise(done => { releaseLock = done; });
      }),
    );
    if (!held) {
      elsewhereText = "Your Orb is open in another tab of this page.";
      screen = "elsewhere";
      return false;
    }
    try {
    showError("");
    if (moves) {
      moves.onmessage = ({ data }) => {
        if (data === "take") {
          sessionStorage.setItem("orb.moved", "1");
          location.reload();
        }
      };
    }

    const peerURL = new URL("peer.js", document.baseURI).href;
    const [{ spawnPeer }, response] = await Promise.all([
      import(/* @vite-ignore */ peerURL),
      fetch("peer.wasm.gz"),
    ]);
    if (!response.ok) throw new Error(`Wasm download failed: ${response.status}`);
    const answers = new Map();
    let rpcSeq = 0;

    const peer = spawnPeer({
      wasm: await new Response(response.body.pipeThrough(new DecompressionStream("gzip"))).arrayBuffer(),
      storage: "orb-tab",
      name: tabName(),
      env: { ORB_SETTINGS: JSON.stringify({ plugins: { memory: true } }) },
      emit: text => {
        const frame = JSON.parse(text);
        if (frame.t) return heard(frame);
        if (frame.type === "response" && answers.has(frame.id)) {
          answers.get(frame.id)(frame);
          answers.delete(frame.id);
        }
      },
      exit: () => {
        tab = null;
        releaseLock?.();
        showError("This tab's Orb stopped. Reload the page.");
      },
      run: runCodeMode,
    });

    tab = {
      admin: async (method, p = {}) => JSON.parse(await peer.mitosisAdmin(method, JSON.stringify(p), "")),
      app: line => peer.app(line),
      plugins: async (...args) => JSON.parse(await peer.plugins(...args)),
      models: async (...args) => JSON.parse(await peer.models(...args)),
      accounts: async (...args) => JSON.parse(await peer.accounts(...args)),
      rpc: (type, p = {}) =>
        new Promise((resolve, reject) => {
          const id = `tab-${++rpcSeq}`;
          answers.set(id, frame => (frame.success ? resolve(frame.data) : reject(new Error(frame.error))));
          peer.command(JSON.stringify({ id, type, ...p })).then(refused => {
            if (refused) {
              answers.delete(id);
              reject(new Error(refused));
            }
          }, reject);
        }),
    };

    ({ peer_id: me } = await tab.admin("self"));
    approval();
    return true;
    } catch (err) {
      releaseLock?.();
      throw err;
    }
  }

  async function approval() {
    try {
      const owner = localStorage.getItem("orb.tab.owner");
      if (!owner) {
        screen = "invite";
        return;
      }
      const synced = await tab.admin("sync").catch(() => null);
      if (!synced?.group_id) {
        await tab.admin("trust", { peer_id: owner }).catch(() => {});
      } else if (!joinedAt) {
        joinedAt = Date.now();
      }
      const { models } = await tab.rpc("get_available_models");
      const ready =
        !!localStorage.getItem("orb.tab.approved") ||
        !!synced?.in_sync ||
        models.length > 0 ||
        (!!synced?.group_id &&
          Date.now() - joinedAt > 12000 &&
          (await tab
            .admin("remote", { peer_id: owner, method: "agent.describe", params: {} })
            .then(() => true, () => false)));
      if (!ready) {
        screen = "approve";
        setTimeout(approval, 2000);
        return;
      }
      localStorage.setItem("orb.tab.approved", "1");
      if (models.length) {
        const { model } = await tab.rpc("get_state");
        if (!model || model.provider === "unknown") await tab.rpc("new_session");
      }
      send({ do: "hello", budget: 6 });
      send({ do: "visible", on: !document.hidden });
      syncVisibleTabs();
      if (screen !== "settings") screen = "app";
    } catch (err) {
      showError(err.message);
      setTimeout(approval, 3000);
    }
  }

  function parseInvitationCode(text) {
    try {
      const value = (text || "").trim();
      if (!value) return null;
      const code = value.split("orb-mitosis:v1:")[1]?.split(/\s/)[0];
      const inv = JSON.parse(code ? atob(code.replace(/-/g, "+").replace(/_/g, "/")) : value);
      if (!inv?.peer_id || !inv?.token) return null;
      return inv;
    } catch {
      return null;
    }
  }

  async function joinInvitation(text, additional = false) {
    showError("");
    const inv = parseInvitationCode(text);
    if (!inv) throw new Error("Paste the code orb mitosis --browser shows.");
    if (!/^wss?:\/\//.test(inv.locator ?? "")) {
      throw new Error("That code is for the apps. For this tab, run orb mitosis --browser.");
    }
    if (!tab && !(await boot())) return;
    await tab.admin("join", inv);
    await tab.admin("trust", { peer_id: inv.peer_id });
    if (additional) {
      showToast("claimed · approve this tab on that device");
      return;
    }
    localStorage.setItem("orb.tab.owner", inv.peer_id);
    screen = "approve";
    approval();
  }

  async function forgetTab() {
    if (
      (await pick("forget this tab? its conversations here go with it", [
        { label: "keep it" },
        { label: "forget this tab" },
      ])) !== "forget this tab"
    ) {
      return;
    }
    await Promise.race([
      tab?.admin("sync", { remove: me }).catch(() => {}),
      new Promise(r => setTimeout(r, 3000)),
    ]);
    localStorage.setItem("orb.tab.forget", "1");
    location.reload();
  }

  function takeOverTab() {
    sessionStorage.setItem("orb.take", "1");
    location.reload();
  }

  function consumeHash() {
    const raw = location.hash.slice(1);
    if (!raw) return "";
    let decoded = raw;
    try {
      decoded = decodeURIComponent(raw);
    } catch {}
    history.replaceState(null, "", location.pathname + location.search);
    return decoded;
  }

  async function init() {
    if (localStorage.getItem("orb.tab.forget")) {
      for (const key of Object.keys(localStorage)) {
        if (key.startsWith("orb.tab.")) localStorage.removeItem(key);
      }
      await new Promise(resolve =>
        Object.assign(indexedDB.deleteDatabase("orb-tab"), {
          onsuccess: resolve,
          onerror: resolve,
          onblocked: resolve,
        }),
      );
    }

    document.addEventListener("visibilitychange", () => send({ do: "visible", on: !document.hidden }));
    window.addEventListener("hashchange", () => {
      const code = consumeHash();
      if (code.startsWith("orb-mitosis:v1:") && !localStorage.getItem("orb.tab.owner")) {
        screen = "invite";
        joinInvitation(code).catch(err => showError(err.message));
      }
    });

    const params = new URLSearchParams(location.search);
    if (params.has("bench")) {
      boot().catch(err => showError(err.message));
      return;
    }

    const linked = consumeHash();
    if (sessionStorage.getItem("orb.moved")) {
      sessionStorage.removeItem("orb.moved");
      elsewhereText = "Your Orb moved to another tab of this page.";
      screen = "elsewhere";
    } else if (localStorage.getItem("orb.tab.owner")) {
      boot().catch(err => showError(err.message));
    } else {
      screen = "invite";
      if (linked.startsWith("orb-mitosis:v1:")) {
        joinInvitation(linked).catch(err => showError(err.message));
      }
    }
  }

  return {
    get screen() { return screen; },
    set screen(v) { screen = v; },
    get elsewhereText() { return elsewhereText; },
    get me() { return me; },
    get fingerprint() { return shortPeer(me); },
    get error() { return error; },
    get state() { return state; },
    get home() { return home; },
    get rowsByTab() { return rowsByTab; },
    get tileTree() { return tileTree; },
    get activeTileId() { return activeTileId; },
    get zoomedTileId() { return zoomedTileId; },
    get isSplit() { return isSplit(); },
    get visibleTabs() { return visibleTabIds(tileTree); },
    get shown() { return shown; },
    get splitTab() {
      const leaves = listLeaves(tileTree);
      return leaves.length > 1 ? (leaves.find(l => l.id !== activeTileId)?.tab || leaves[1]?.tab || "") : "";
    },
    get overview() { return overview; },
    get mitosisMap() { return mitosisMap; },
    get deviceFilter() { return deviceFilter; },
    set deviceFilter(v) { deviceFilter = v; },
    get dismissedLaunch() { return dismissedLaunch; },
    set dismissedLaunch(v) { dismissedLaunch = v; },
    get theme() { return theme; },
    get settingsPage() { return settingsPage; },
    get latestRelease() { return latestRelease || state.latest || ""; },
    get modelDeckOpen() { return modelDeckOpen; },
    set modelDeckOpen(v) { modelDeckOpen = v; },
    get modelDeckTab() { return modelDeckTab; },
    get paletteOpen() { return paletteOpen; },
    set paletteOpen(v) { paletteOpen = v; },
    get shortcutsOpen() { return shortcutsOpen; },
    set shortcutsOpen(v) { shortcutsOpen = v; },
    get explorerPeer() { return explorerPeer; },
    set explorerPeer(v) { explorerPeer = v; },
    get terminalTab() { return terminalTab; },
    set terminalTab(v) { terminalTab = v; },
    get viewerImage() { return viewerImage; },
    set viewerImage(v) { viewerImage = v; },
    get pickerState() { return pickerState; },
    get inlinePaletteOpen() { return inlinePaletteOpen; },
    set inlinePaletteOpen(v) { inlinePaletteOpen = v; },
    get toolsToggleSeq() { return toolsToggleSeq; },
    get toolsDefaultExpanded() { return toolsDefaultExpanded; },
    get toastState() { return toastState; },
    get defaultsByPeer() { return defaultsByPeer; },
    get providersByPeer() { return providersByPeer; },
    get accountsByPeer() { return accountsByPeer; },
    get aimVersion() { return aimVersion; },
    get focusTick() { return focusTick; },
    get rawTab() { return tab; },

    init,
    applyTheme,
    showError,
    showToast,
    dismissToast,
    send,
    ask,
    getTab,
    getMachine,
    activeTabId,
    focus,
    focusTile,
    cycleTile,
    toggleOverview,
    toggleMitosisMap,
    toggleSplit,
    splitActiveTile,
    closeTile: closeTileById,
    setSplitRatio: setSplitRatioById,
    equalizeTiles,
    toggleZoomTile,
    dropTabOnTile,
    launchers,
    getAim,
    aimAt,
    defaultPeer,
    openEntry,
    openExchange,
    closeTab,
    reopenClosed,
    stepTab,
    getDraft,
    setDraft,
    getHistory,
    submitMessage,
    attachFiles,
    loadTabDefaults,
    ensureDefaults,
    refreshPeerMeta,
    setDefaultModel,
    switchAccount,
    cycleReasoning,
    toggleToolsExpanded,
    openModelDeck,
    openSettings,
    pick,
    renamePrompt,
    openTabMenu,
    openThreadMenu,
    openDeviceMenu,
    parseInvitationCode,
    joinInvitation,
    forgetTab,
    takeOverTab,
    bumpFocus() { focusTick++; },
    setProviders(peer, list) { providersByPeer = { ...providersByPeer, [peer]: list }; },
    setAccounts(peer, list) { accountsByPeer = { ...accountsByPeer, [peer]: list }; },
    clearAccounts(peer) {
      const copy = { ...accountsByPeer };
      delete copy[peer];
      accountsByPeer = copy;
    },
  };
}

export const orb = createOrbStore();
