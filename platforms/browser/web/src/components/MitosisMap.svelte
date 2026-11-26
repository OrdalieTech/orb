<script>
  import { onMount } from "svelte";
  import { orb } from "../lib/store.svelte.js";
  import { base, named, word } from "../lib/utils.js";

  let filter = $state("all"); // "all" | "live"
  let hoveredKey = $state(""); // machine id or session id
  let viewportEl = $state(null);

  let panX = $state(0);
  let panY = $state(0);
  let zoom = $state(1);
  let dragging = $state(false);
  let dragStart = null;

  // Deterministic lightweight hash -> [0, 1) for stable organic offsets.
  function hash01(str) {
    let h = 2166136261;
    for (let i = 0; i < str.length; i++) {
      h ^= str.charCodeAt(i);
      h = Math.imul(h, 16777619);
    }
    return ((h >>> 0) % 10000) / 10000;
  }

  // Smooth organic Bézier conduit between two anchor points.
  function conduitPath(x1, y1, x2, y2, seed, bend = 0.16) {
    const dx = x2 - x1;
    const dy = y2 - y1;
    const len = Math.hypot(dx, dy) || 1;
    const nx = -dy / len;
    const ny = dx / len;
    const dir = hash01(seed) > 0.5 ? 1 : -1;
    const mag = Math.min(95, len * bend) * (0.7 + 0.6 * hash01(seed + ":m")) * dir;
    const c1x = x1 + dx * 0.35 + nx * mag;
    const c1y = y1 + dy * 0.35 + ny * mag;
    const c2x = x1 + dx * 0.68 - nx * (mag * 0.4);
    const c2y = y1 + dy * 0.68 - ny * (mag * 0.4);
    const mx = 0.125 * x1 + 0.375 * c1x + 0.375 * c2x + 0.125 * x2;
    const my = 0.125 * y1 + 0.375 * c1y + 0.375 * c2y + 0.125 * y2;
    return {
      d: `M ${x1.toFixed(1)} ${y1.toFixed(1)} C ${c1x.toFixed(1)} ${c1y.toFixed(1)}, ${c2x.toFixed(1)} ${c2y.toFixed(1)}, ${x2.toFixed(1)} ${y2.toFixed(1)}`,
      mx,
      my,
    };
  }

  // Anchor point on a colony rectangle's perimeter facing target (tx, ty).
  function edgePoint(col, tx, ty) {
    const dx = tx - col.x;
    const dy = ty - col.y;
    if (Math.abs(dx) < 0.01 && Math.abs(dy) < 0.01) return { x: col.x, y: col.y };
    const hw = col.w / 2;
    const hh = col.h / 2;
    const sx = Math.abs(dx) > 0.01 ? hw / Math.abs(dx) : Infinity;
    const sy = Math.abs(dy) > 0.01 ? hh / Math.abs(dy) : Infinity;
    const t = Math.min(sx, sy);
    return { x: col.x + dx * t, y: col.y + dy * t };
  }

  let graph = $derived.by(() => {
    const rawMachines = orb.home.machines || [];
    const entries = orb.home.entries || [];
    const tabs = orb.state.tabs || [];
    const selfId = orb.state.self || rawMachines.find(m => m.self)?.id || "";

    const tabsByPeer = new Map();
    for (const t of tabs) {
      if (!tabsByPeer.has(t.peer)) tabsByPeer.set(t.peer, []);
      tabsByPeer.get(t.peer).push(t);
    }

    const selfMachine = rawMachines.find(m => m.self || m.id === selfId) || {
      id: selfId || "local",
      name: "this tab",
      self: true,
      connected: true,
      version: "",
      launch: false,
      running: [],
    };
    const peerMachines = rawMachines
      .filter(m => m.id !== selfMachine.id)
      .slice()
      .sort((a, b) => Number(!!b.connected) - Number(!!a.connected) || (a.name || "").localeCompare(b.name || ""));

    function collectSessions(m) {
      const seen = new Set();
      const out = [];

      for (const t of tabsByPeer.get(m.id) || []) {
        const entry = entries.find(e => e.tab === t.id);
        const sessId = entry?.key?.includes("|")
          ? entry.key.split("|")[1]
          : entry?.key?.startsWith("i:")
            ? entry.key.slice(2)
            : "";
        if (sessId) seen.add(sessId);
        const rows = orb.rowsByTab[t.id] || [];
        const subagents = [];
        for (const r of rows) {
          if (r.kind === "peer" && r.exchange?.scope === "subagent") {
            subagents.push({
              id: `sub:${t.id}:${r.k}`,
              who: r.exchange.who,
              status: r.exchange.status,
              live: !!r.exchange.live,
              failed: !!r.exchange.failed,
            });
          }
        }
        out.push({
          id: `tab:${t.id}`,
          tabId: t.id,
          sessionId: sessId,
          entryKey: entry?.key || "",
          peer: m.id,
          title: named(t.title),
          meta: t.ask ? (t.ask.title || "approval required") : base(t.cwd) || t.model?.split("/").slice(1).join("/") || "",
          status: t.ask ? "ASKS" : t.busy ? "WORK" : t.asleep ? "SLEEP" : "IDLE",
          busy: !!t.busy,
          asks: !!t.ask,
          open: true,
          live: true,
          asleep: !!t.asleep,
          context: t.context || 0,
          subagents: subagents.slice(-2),
        });
      }

      for (const r of m.running || []) {
        if (r.open || seen.has(r.instance)) continue;
        seen.add(r.instance);
        const parts = (r.label || "").split(" · ");
        const title = parts.length > 1 ? parts[parts.length - 1] : r.label || "running";
        out.push({
          id: `inst:${m.id}:${r.instance}`,
          tabId: "",
          sessionId: r.instance,
          entryKey: `i:${r.instance}`,
          peer: m.id,
          title: named(title),
          meta: parts.length > 2 ? parts[1] : "",
          status: r.busy ? "WORK" : "LIVE",
          busy: !!r.busy,
          asks: false,
          open: false,
          live: true,
          asleep: false,
          context: 0,
          subagents: [],
        });
      }

      if (filter === "all") {
        let addedRecent = 0;
        for (const e of entries) {
          if (e.machine !== m.id || e.open) continue;
          const sessId = e.key.includes("|") ? e.key.split("|")[1] : e.key.slice(2);
          if (seen.has(sessId)) continue;
          if (!e.live && !e.asks && addedRecent >= 2) continue;
          seen.add(sessId);
          if (!e.live && !e.asks) addedRecent++;
          out.push({
            id: `ent:${e.key}`,
            tabId: "",
            sessionId: sessId,
            entryKey: e.key,
            peer: m.id,
            title: named(e.title),
            meta: base(e.cwd) || "",
            status: e.asks ? "ASKS" : e.live ? "WORK" : "SLEEP",
            busy: !!e.live,
            asks: !!e.asks,
            open: false,
            live: !!e.live,
            asleep: !e.live,
            context: 0,
            subagents: [],
          });
        }
      }

      const list = filter === "live" ? out.filter(s => s.live || s.open || s.busy || s.asks) : out;
      // Put asks -> busy -> live -> asleep in priority order inside each machine colony.
      list.sort((a, b) => Number(b.asks) - Number(a.asks) || Number(b.busy) - Number(a.busy) || Number(b.live) - Number(a.live));
      return list;
    }

    function measureColony(sessList) {
      const w = 256;
      const headH = 44;
      if (sessList.length === 0) return { w, h: headH + 34 };
      let bodyH = 0;
      for (const s of sessList) {
        bodyH += 44 + s.subagents.length * 19;
      }
      return { w, h: headH + bodyH };
    }

    const colonies = [];
    const colonyByPeer = new Map();
    const sessionAnchor = new Map(); // sessionId or "tab:<id>" -> { colony, rowCenterY }

    // 00 · Nucleus (this device) at center
    const selfSessions = collectSessions(selfMachine);
    const selfDim = measureColony(selfSessions);
    const rootCol = {
      id: `m:${selfMachine.id}`,
      code: "00",
      peer: selfMachine.id,
      name: selfMachine.self ? "THIS TAB" : (selfMachine.name || "LOCAL").toUpperCase(),
      version: selfMachine.version ? selfMachine.version.split("-")[0] : "",
      self: true,
      connected: true,
      launch: !!selfMachine.launch,
      busy: selfSessions.some(s => s.busy),
      asks: selfSessions.some(s => s.asks),
      workingCount: selfSessions.filter(s => s.busy).length,
      liveCount: selfSessions.filter(s => s.live).length,
      sessions: selfSessions,
      raw: selfMachine,
      x: 0,
      y: 0,
      w: selfDim.w,
      h: selfDim.h,
    };
    colonies.push(rootCol);
    colonyByPeer.set(selfMachine.id, rootCol);

    // Organic elliptical ring of peer machine colonies around the nucleus.
    const nPeers = peerMachines.length;
    const step = nPeers > 0 ? (Math.PI * 2) / nPeers : 0;
    peerMachines.forEach((m, idx) => {
      const mSessions = collectSessions(m);
      const dim = measureColony(mSessions);
      const jitterA = (hash01(m.id + ":a") - 0.5) * Math.min(0.16, step * 0.18);
      const angle = -Math.PI / 2 + idx * step + jitterA;
      const rx = (m.connected ? 395 : 460) + (hash01(m.id + ":rx") - 0.5) * 44;
      const ry = (m.connected ? 275 : 320) + (hash01(m.id + ":ry") - 0.5) * 34;
      const col = {
        id: `m:${m.id}`,
        code: String(idx + 1).padStart(2, "0"),
        peer: m.id,
        name: (m.name || m.id.slice(-8)).toUpperCase(),
        version: m.version ? m.version.split("-")[0] : "",
        self: false,
        connected: !!m.connected,
        launch: !!m.launch,
        busy: mSessions.some(s => s.busy),
        asks: mSessions.some(s => s.asks),
        workingCount: mSessions.filter(s => s.busy).length,
        liveCount: mSessions.filter(s => s.live).length,
        sessions: mSessions,
        raw: m,
        x: Math.cos(angle) * rx,
        y: Math.sin(angle) * ry,
        w: dim.w,
        h: dim.h,
      };
      colonies.push(col);
      colonyByPeer.set(m.id, col);
    });

    // Deterministic AABB relaxation so machine colonies NEVER overlap while staying organically packed.
    const padGap = 42;
    for (let iter = 0; iter < 40; iter++) {
      let moved = false;
      for (let i = 0; i < colonies.length; i++) {
        const a = colonies[i];
        for (let j = i + 1; j < colonies.length; j++) {
          const b = colonies[j];
          const dx = b.x - a.x;
          const dy = b.y - a.y;
          const minX = (a.w + b.w) / 2 + padGap;
          const minY = (a.h + b.h) / 2 + padGap;
          const ox = minX - Math.abs(dx);
          const oy = minY - Math.abs(dy);
          if (ox > 0 && oy > 0) {
            moved = true;
            const wa = a.self ? 0 : 0.5;
            const wb = b.self ? 0 : a.self ? 1 : 0.5;
            if (ox < oy) {
              const dir = dx === 0 ? 1 : Math.sign(dx);
              a.x -= dir * ox * wa;
              b.x += dir * ox * wb;
            } else {
              const dir = dy === 0 ? 1 : Math.sign(dy);
              a.y -= dir * oy * wa;
              b.y += dir * oy * wb;
            }
          }
        }
      }
      if (!moved) break;
    }

    // Record exact session row Y-positions inside each settled colony for precise synapse anchors.
    for (const col of colonies) {
      let y = col.y - col.h / 2 + 44;
      for (const s of col.sessions) {
        const rowH = 44 + s.subagents.length * 19;
        const anchor = { col, y: y + 22, id: s.id };
        if (s.sessionId) sessionAnchor.set(s.sessionId, anchor);
        if (s.tabId) sessionAnchor.set(`tab:${s.tabId}`, anchor);
        y += rowH;
      }
    }

    // Build structural monochrome conduits between Nucleus (00) and each peer colony.
    const conduits = [];
    for (const col of colonies) {
      if (col.self) continue;
      const p1 = edgePoint(rootCol, col.x, col.y);
      const p2 = edgePoint(col, rootCol.x, rootCol.y);
      const curve = conduitPath(p1.x, p1.y, p2.x, p2.y, col.peer, 0.14);
      conduits.push({
        id: `c:${col.peer}`,
        from: rootCol.id,
        to: col.id,
        connected: col.connected,
        busy: col.busy,
        d: curve.d,
        x1: p1.x,
        y1: p1.y,
        x2: p2.x,
        y2: p2.y,
      });
    }

    // Build cross-colony multiplayer peer-to-peer message synapses (the ONLY signal accent lines!).
    const synapses = [];
    const seenSyn = new Set();
    for (const t of tabs) {
      const src = sessionAnchor.get(`tab:${t.id}`);
      if (!src) continue;
      const rows = orb.rowsByTab[t.id] || [];
      for (const r of rows) {
        if (r.kind !== "peer" || r.exchange?.scope !== "peer") continue;
        const ex = r.exchange;
        const dstAnchor = ex.session ? sessionAnchor.get(ex.session) : null;
        const dstCol = dstAnchor?.col || (ex.peer ? colonyByPeer.get(ex.peer) : null);
        if (!dstCol || dstCol.id === src.col.id) continue;

        const fromCol = ex.dir === "in" ? dstCol : src.col;
        const toCol = ex.dir === "in" ? src.col : dstCol;
        const fromY = ex.dir === "in" ? (dstAnchor?.y ?? dstCol.y) : src.y;
        const toY = ex.dir === "in" ? src.y : (dstAnchor?.y ?? dstCol.y);
        const fromSid = ex.dir === "in" ? (dstAnchor?.id ?? dstCol.id) : src.id;
        const toSid = ex.dir === "in" ? src.id : (dstAnchor?.id ?? dstCol.id);

        const key = `${fromSid}->${toSid}`;
        if (seenSyn.has(key)) continue;
        seenSyn.add(key);

        const horizOverlap = Math.abs(toCol.x - fromCol.x) < (fromCol.w + toCol.w) / 2 + 24;
        let x1, y1, x2, y2;
        if (horizOverlap) {
          // Vertically stacked colonies: exit and enter on the outer side facing away from center so lines never cut across a box.
          const side = (fromCol.x + toCol.x) >= 0 ? 1 : -1;
          x1 = fromCol.x + side * (fromCol.w / 2);
          y1 = fromY;
          x2 = toCol.x + side * (toCol.w / 2);
          y2 = toY;
        } else {
          const fromRight = toCol.x >= fromCol.x;
          x1 = fromCol.x + (fromRight ? fromCol.w / 2 : -fromCol.w / 2);
          y1 = fromY;
          x2 = toCol.x + (fromRight ? -toCol.w / 2 : toCol.w / 2);
          y2 = toY;
        }
        const curve = conduitPath(x1, y1, x2, y2, key, horizOverlap ? 0.3 : 0.2);

        synapses.push({
          id: `syn:${key}`,
          fromCol: fromCol.id,
          toCol: toCol.id,
          fromSession: fromSid,
          toSession: toSid,
          d: curve.d,
          x1,
          y1,
          x2,
          y2,
          mx: curve.mx,
          my: curve.my,
          live: !!ex.live,
          label: (ex.text || "").split("\n")[0].slice(0, 34),
        });
      }
    }

    const totalSessions = colonies.reduce((n, c) => n + c.sessions.length, 0);
    const totalWorking = colonies.reduce((n, c) => n + c.workingCount, 0);
    const totalAsks = colonies.reduce((n, c) => n + (c.asks ? 1 : 0), 0);

    return {
      colonies,
      conduits,
      synapses,
      connectedCount: colonies.filter(c => c.connected).length,
      totalSessions,
      totalWorking,
      totalAsks,
    };
  });

  // Clean hover focus: highlight the hovered colony/session and its direct links.
  let activeSet = $derived.by(() => {
    if (!hoveredKey) return null;
    const set = new Set([hoveredKey]);
    for (const c of graph.conduits) {
      if (c.from === hoveredKey || c.to === hoveredKey) {
        set.add(c.from);
        set.add(c.to);
        set.add(c.id);
      }
    }
    for (const s of graph.synapses) {
      if (s.fromCol === hoveredKey || s.toCol === hoveredKey || s.fromSession === hoveredKey || s.toSession === hoveredKey) {
        set.add(s.fromCol);
        set.add(s.toCol);
        set.add(s.fromSession);
        set.add(s.toSession);
        set.add(s.id);
      }
    }
    return set;
  });

  function fitView() {
    if (!viewportEl) {
      panX = 0;
      panY = 0;
      zoom = 1;
      return;
    }
    let minX = -160, maxX = 160, minY = -100, maxY = 100;
    for (const c of graph.colonies) {
      minX = Math.min(minX, c.x - c.w / 2);
      maxX = Math.max(maxX, c.x + c.w / 2);
      minY = Math.min(minY, c.y - c.h / 2);
      maxY = Math.max(maxY, c.y + c.h / 2);
    }
    const vw = viewportEl.clientWidth || 1200;
    const vh = viewportEl.clientHeight || 760;
    const pad = 56;
    const scale = Math.min(1.0, Math.max(0.45, Math.min((vw - pad * 2) / (maxX - minX), (vh - pad * 2) / (maxY - minY))));
    zoom = Number(scale.toFixed(2));
    panX = -((minX + maxX) / 2) * scale;
    panY = -((minY + maxY) / 2) * scale;
  }

  onMount(() => {
    requestAnimationFrame(fitView);
  });

  function onPointerDown(e) {
    if (e.button !== 0 || e.target.closest("button, .colony")) return;
    dragging = true;
    dragStart = { x: e.clientX - panX, y: e.clientY - panY };
    e.currentTarget.setPointerCapture?.(e.pointerId);
  }
  function onPointerMove(e) {
    if (!dragging || !dragStart) return;
    panX = e.clientX - dragStart.x;
    panY = e.clientY - dragStart.y;
  }
  function onPointerUp() {
    dragging = false;
    dragStart = null;
  }
  function onWheel(e) {
    e.preventDefault();
    if (e.ctrlKey || e.metaKey) {
      const factor = e.deltaY < 0 ? 1.09 : 0.92;
      zoom = Math.min(1.6, Math.max(0.4, Number((zoom * factor).toFixed(3))));
    } else {
      panX -= e.deltaX;
      panY -= e.deltaY;
    }
  }

  function openSession(s) {
    if (s.tabId) orb.focus(s.tabId);
    else if (s.entryKey) orb.openEntry(s.entryKey);
  }

  function startOnMachine(col, e) {
    e.stopPropagation();
    orb.aimAt(col.peer);
    orb.focus("");
  }

  function stopAllBusy() {
    let n = 0;
    for (const t of orb.state.tabs || []) {
      if (t.busy) {
        orb.send({ do: "abort", tab: t.id });
        n++;
      }
    }
    if (n > 0) orb.showToast(`stopped ${n} working ${n === 1 ? "agent" : "agents"}`);
  }
</script>

<div class="magi-map">
  <header class="map-bar">
    <div class="bar-left">
      <span class="bar-code">MAGI // MITOSIS</span>
      <span class="bar-jp">神経接続網</span>
      <span class="bar-stat">
        {graph.connectedCount}/{graph.colonies.length} UNITS · {graph.totalSessions} CELLS{#if graph.totalWorking > 0}{' · '}<b>{graph.totalWorking} ACTIVE</b>{/if}{#if graph.synapses.length > 0}{' · '}{graph.synapses.length} LINKS{/if}
      </span>
    </div>

    <div class="bar-right">
      {#if graph.totalWorking > 0}
        <button type="button" class="btn plain bar-abort" onclick={stopAllBusy}>
          HALT ALL ({graph.totalWorking})
        </button>
      {/if}
      <div class="bar-seg" role="tablist">
        <button type="button" class:on={filter === "all"} onclick={() => (filter = "all")}>ALL</button>
        <button type="button" class:on={filter === "live"} onclick={() => (filter = "live")}>LIVE</button>
      </div>
      <div class="bar-zoom">
        <button type="button" class="btn plain" onclick={() => (zoom = Math.max(0.45, Number((zoom - 0.12).toFixed(2))))}>−</button>
        <button type="button" class="btn plain" onclick={fitView}>{Math.round(zoom * 100)}%</button>
        <button type="button" class="btn plain" onclick={() => (zoom = Math.min(1.6, Number((zoom + 0.12).toFixed(2))))}>+</button>
      </div>
      <button type="button" class="btn plain" onclick={() => orb.toggleMitosisMap()}>
        EXIT <kbd>Esc</kbd>
      </button>
    </div>
  </header>

  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="map-stage"
    class:dragging
    bind:this={viewportEl}
    onpointerdown={onPointerDown}
    onpointermove={onPointerMove}
    onpointerup={onPointerUp}
    onpointercancel={onPointerUp}
    onwheel={onWheel}
  >
    <!-- Architectural registration frame marks -->
    <span class="reg tl" aria-hidden="true"></span>
    <span class="reg tr" aria-hidden="true"></span>
    <span class="reg bl" aria-hidden="true"></span>
    <span class="reg br" aria-hidden="true"></span>

    <div
      class="map-world"
      style="transform: translate(calc(50% + {panX}px), calc(50% + {panY}px)) scale({zoom});"
    >
      <svg class="map-svg" viewBox="-1800 -1200 3600 2400" aria-hidden="true">
        <defs>
          <marker id="syn-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
            <path d="M 0 1 L 7 4 L 0 7 z" fill="var(--rupture)" />
          </marker>
        </defs>

        <!-- Quiet structural elliptical axes -->
        <ellipse cx="0" cy="0" rx="395" ry="275" class="axis-ring" />
        <line x1="-1800" y1="0" x2="1800" y2="0" class="axis-line" />
        <line x1="0" y1="-1200" x2="0" y2="1200" class="axis-line" />

        <!-- Structural monochrome conduits: Nucleus (00) <-> Machine Colonies -->
        {#each graph.conduits as c (c.id)}
          {@const dim = activeSet && !activeSet.has(c.id)}
          <path
            d={c.d}
            class="conduit"
            class:offline={!c.connected}
            class:busy={c.busy}
            class:dim={dim}
          />
          <rect x={c.x1 - 2.5} y={c.y1 - 2.5} width="5" height="5" class="joint" class:dim={dim} />
          <rect x={c.x2 - 2.5} y={c.y2 - 2.5} width="5" height="5" class="joint" class:dim={dim} />
        {/each}

        <!-- Cross-colony peer-to-peer message synapses (sole signal accent: vermilion) -->
        {#each graph.synapses as syn (syn.id)}
          {@const dim = activeSet && !activeSet.has(syn.id)}
          <path
            d={syn.d}
            class="synapse"
            class:dim={dim}
            marker-end="url(#syn-arrow)"
          />
          <circle cx={syn.x1} cy={syn.y1} r="2.5" class="syn-origin" class:dim={dim} />
        {/each}
      </svg>

      <!-- Synapse inline labels (shown subtly on hover of linked colonies/sessions) -->
      {#each graph.synapses as syn (syn.id)}
        {#if activeSet?.has(syn.id) && syn.label}
          <div class="syn-tag" style="left: {syn.mx}px; top: {syn.my}px;">
            <span>→ {syn.label}</span>
          </div>
        {/if}
      {/each}

      <!-- Machine Colonies (each machine groups all its sessions inside one architectural cell) -->
      {#each graph.colonies as col (col.id)}
        {@const dim = activeSet && !activeSet.has(col.id)}
        <section
          class="colony"
          class:nucleus={col.self}
          class:offline={!col.connected}
          class:asks={col.asks}
          class:dim={dim}
          style="left: {col.x}px; top: {col.y}px; width: {col.w}px;"
          oncontextmenu={e => { e.preventDefault(); orb.openDeviceMenu(col.raw); }}
          onmouseenter={() => (hoveredKey = col.id)}
          onmouseleave={() => (hoveredKey = "")}
        >
          <header class="col-head">
            <span class="col-code">{col.code}</span>
            <span class="col-name">{col.name}</span>
            <span class="col-right">
              {#if !col.connected}
                <span class="col-state off">OFF</span>
              {:else if col.workingCount > 0}
                <span class="col-state work">{col.workingCount} ACT</span>
              {:else if col.version}
                <span class="col-state">{col.version}</span>
              {/if}
              {#if col.launch && col.connected}
                <button
                  type="button"
                  class="col-add"
                  title="New session on {col.name}"
                  onclick={e => startOnMachine(col, e)}
                >+</button>
              {/if}
            </span>
          </header>

          <div class="col-body">
            {#if col.sessions.length === 0}
              <div class="col-empty">{col.connected ? "STANDBY" : "UNREACHABLE"}</div>
            {:else}
              {#each col.sessions as s (s.id)}
                <button
                  type="button"
                  class="cell-row"
                  class:busy={s.busy}
                  class:asks={s.asks}
                  class:asleep={s.asleep}
                  onclick={() => openSession(s)}
                  oncontextmenu={e => {
                    e.preventDefault();
                    e.stopPropagation();
                    const tabObj = s.tabId ? orb.getTab(s.tabId) : null;
                    if (tabObj) orb.openTabMenu(tabObj);
                  }}
                  onmouseenter={() => (hoveredKey = s.id)}
                  onmouseleave={() => (hoveredKey = col.id)}
                >
                  <div class="cell-main">
                    <span class="cell-title">{s.title}</span>
                    <span class="cell-status" class:work={s.busy && !s.asks} class:asks={s.asks}>{s.status}</span>
                  </div>
                  {#if s.meta}
                    <div class="cell-meta">{s.meta}</div>
                  {/if}
                  {#if s.subagents.length > 0}
                    <div class="cell-subs">
                      {#each s.subagents as sub (sub.id)}
                        <div class="cell-sub" class:live={sub.live} class:failed={sub.failed}>
                          <span>↳ {sub.who}</span>
                          <span>{sub.status}</span>
                        </div>
                      {/each}
                    </div>
                  {/if}
                  {#if s.context > 0}
                    <span class="cell-ctx"><i class:alarm={s.context > 0.8} style="width:{Math.min(100, Math.round(s.context * 100))}%"></i></span>
                  {/if}
                </button>
              {/each}
            {/if}
          </div>
        </section>
      {/each}
    </div>
  </div>
</div>

<style>
  .magi-map {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    background: var(--bg);
    font-family: var(--font-mono);
  }
  .map-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 8px var(--margin);
    border-bottom: 1px solid var(--fg);
    flex-shrink: 0;
    flex-wrap: wrap;
  }
  .bar-left {
    display: flex;
    align-items: baseline;
    gap: 12px;
    min-width: 0;
  }
  .bar-code {
    font-size: 12px;
    font-weight: 800;
    letter-spacing: 0.1em;
    color: var(--fg);
  }
  .bar-jp {
    font-size: 10.5px;
    letter-spacing: 0.14em;
    color: var(--meta);
  }
  .bar-stat {
    font-size: 10.5px;
    letter-spacing: 0.06em;
    color: var(--meta);
  }
  .bar-stat b {
    color: var(--fg);
    font-weight: 700;
  }
  .bar-right {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .bar-abort {
    color: var(--rupture);
    font-weight: 700;
    letter-spacing: 0.06em;
    font-size: 10.5px;
  }
  .bar-seg {
    display: inline-flex;
    border: 1px solid var(--fg);
  }
  .bar-seg button {
    padding: 2px 9px;
    border: 0;
    background: none;
    color: var(--meta);
    font-family: inherit;
    font-size: 10.5px;
    font-weight: 700;
    letter-spacing: 0.08em;
    cursor: pointer;
  }
  .bar-seg button.on {
    background: var(--fg);
    color: var(--bg);
  }
  .bar-zoom {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    font-size: 10.5px;
  }

  .map-stage {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow: hidden;
    cursor: grab;
    user-select: none;
    background-image: radial-gradient(color-mix(in srgb, var(--fg) 13%, transparent) 0.75px, transparent 0.75px);
    background-size: 28px 28px;
    background-position: center;
  }
  .map-stage.dragging {
    cursor: grabbing;
  }
  .reg {
    position: absolute;
    width: 12px;
    height: 12px;
    pointer-events: none;
    z-index: 4;
    border-color: var(--fg);
    opacity: 0.35;
  }
  .reg.tl { top: 12px; left: 12px; border-top: 1px solid; border-left: 1px solid; }
  .reg.tr { top: 12px; right: 12px; border-top: 1px solid; border-right: 1px solid; }
  .reg.bl { bottom: 12px; left: 12px; border-bottom: 1px solid; border-left: 1px solid; }
  .reg.br { bottom: 12px; right: 12px; border-bottom: 1px solid; border-right: 1px solid; }

  .map-world {
    position: absolute;
    inset: 0;
    transform-origin: 0 0;
  }
  .map-svg {
    position: absolute;
    left: -1800px;
    top: -1200px;
    width: 3600px;
    height: 2400px;
    pointer-events: none;
    overflow: visible;
  }
  .axis-ring {
    fill: none;
    stroke: var(--rule);
    stroke-width: 0.75;
    stroke-dasharray: 2 8;
    opacity: 0.7;
  }
  .axis-line {
    stroke: var(--rule-soft);
    stroke-width: 0.75;
    opacity: 0.55;
  }
  .conduit {
    fill: none;
    stroke: var(--fg);
    stroke-width: 1;
    opacity: 0.24;
    transition: opacity 140ms var(--ease);
  }
  .conduit.busy {
    opacity: 0.52;
    stroke-width: 1.2;
  }
  .conduit.offline {
    stroke-dasharray: 3 5;
    opacity: 0.14;
  }
  .joint {
    fill: var(--fg);
    opacity: 0.45;
  }
  .synapse {
    fill: none;
    stroke: var(--rupture);
    stroke-width: 1.35;
    stroke-dasharray: 5 3;
    opacity: 0.82;
    transition: opacity 140ms var(--ease);
  }
  .syn-origin {
    fill: var(--rupture);
  }
  .conduit.dim, .joint.dim, .synapse.dim, .syn-origin.dim {
    opacity: 0.08;
  }
  .syn-tag {
    position: absolute;
    transform: translate(-50%, -50%);
    padding: 2px 7px;
    background: var(--rupture);
    color: #fff;
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.03em;
    white-space: nowrap;
    pointer-events: none;
    z-index: 6;
  }

  /* Machine colony: sharp Evangelion / MAGI architectural block */
  .colony {
    position: absolute;
    transform: translate(-50%, -50%);
    background: var(--bg);
    border: 1px solid var(--fg);
    border-radius: 0;
    transition: opacity 140ms var(--ease);
  }
  .colony.nucleus {
    border-width: 1.5px;
  }
  .colony.offline {
    border-style: dashed;
    border-color: var(--rule-strong);
    opacity: 0.55;
  }
  .colony.asks {
    border-color: var(--rupture);
  }
  .colony.dim {
    opacity: 0.26;
  }

  .col-head {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 36px;
    padding: 0 9px;
    background: var(--fg);
    color: var(--bg);
  }
  .colony.offline .col-head {
    background: var(--recessed);
    color: var(--meta);
    border-bottom: 1px dashed var(--rule-strong);
  }
  .col-code {
    font-size: 10px;
    font-weight: 800;
    letter-spacing: 0.08em;
    opacity: 0.6;
  }
  .col-name {
    flex: 1;
    min-width: 0;
    font-size: 11px;
    font-weight: 800;
    letter-spacing: 0.09em;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .col-right {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    flex-shrink: 0;
  }
  .col-state {
    font-size: 9.5px;
    font-weight: 700;
    letter-spacing: 0.08em;
    opacity: 0.72;
  }
  .col-state.work {
    opacity: 1;
  }
  .col-add {
    width: 18px;
    height: 18px;
    display: grid;
    place-items: center;
    padding: 0;
    border: 1px solid color-mix(in srgb, var(--bg) 40%, transparent);
    background: none;
    color: var(--bg);
    font-family: inherit;
    font-size: 12px;
    line-height: 1;
    cursor: pointer;
  }
  .col-add:hover {
    background: var(--bg);
    color: var(--fg);
  }

  .col-body {
    display: flex;
    flex-direction: column;
  }
  .col-empty {
    padding: 10px 10px;
    font-size: 10px;
    letter-spacing: 0.12em;
    color: var(--meta);
  }
  .cell-row {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 2px;
    width: 100%;
    padding: 8px 10px;
    border: 0;
    border-top: 1px solid var(--rule);
    background: var(--bg);
    color: var(--fg);
    text-align: left;
    font-family: inherit;
    cursor: pointer;
    transition: background-color 100ms var(--ease);
  }
  .cell-row:first-child {
    border-top: 0;
  }
  .cell-row:hover {
    background: var(--hover);
  }
  .cell-row.busy {
    background: var(--recessed);
  }
  .cell-row.asleep {
    opacity: 0.55;
  }
  .cell-row.asks {
    background: var(--rupture);
    color: #fff;
  }
  .cell-row.asks:hover {
    background: color-mix(in srgb, var(--rupture) 90%, #000);
  }
  .cell-row.asks .cell-meta,
  .cell-row.asks .cell-status,
  .cell-row.asks .cell-sub {
    color: #fff;
    opacity: 0.92;
  }
  .cell-main {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 8px;
  }
  .cell-title {
    flex: 1;
    min-width: 0;
    font-family: var(--font-sans, sans-serif);
    font-size: 12px;
    font-weight: 600;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .cell-status {
    flex-shrink: 0;
    font-size: 9.5px;
    font-weight: 700;
    letter-spacing: 0.08em;
    color: var(--meta);
  }
  .cell-status.work {
    color: var(--amber);
  }
  .cell-status.asks {
    color: #fff;
  }
  .cell-meta {
    font-size: 10px;
    color: var(--meta);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .cell-subs {
    margin-top: 3px;
    padding-top: 3px;
    border-top: 1px dashed var(--rule-soft);
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .cell-sub {
    display: flex;
    justify-content: space-between;
    gap: 6px;
    font-size: 10px;
    color: var(--meta);
  }
  .cell-sub.live {
    color: var(--fg);
    font-weight: 600;
  }
  .cell-sub.failed {
    color: var(--rupture);
  }
  .cell-ctx {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 1.5px;
    background: transparent;
  }
  .cell-ctx i {
    display: block;
    height: 100%;
    background: color-mix(in srgb, var(--fg) 35%, transparent);
  }
  .cell-ctx i.alarm {
    background: var(--rupture);
  }
  .cell-row.asks .cell-ctx i {
    background: rgba(255, 255, 255, 0.75);
  }
</style>
