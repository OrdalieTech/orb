// Synthetic Mitosis peer mock for `?bench=1` and automated headless performance benchmarking.
// Exposes realistic multi-device state, heavy conversations (2,000–10,000 rows), high-frequency
// streaming simulation, folder browsing, models, providers, accounts, plugins, and remote terminal.

const BUILTIN_COMMANDS = [
  { name: "new", hint: "fresh session · optional first message" },
  { name: "compact", hint: "summarize to free context · optional focus" },
  { name: "name", hint: "name this session" },
  { name: "model", hint: "model and reasoning", now: true },
  { name: "copy", hint: "copy the last answer", now: true },
  { name: "sessions", hint: "all sessions", now: true },
  { name: "pair", hint: "Mitosis: scan or share a code", now: true },
  { name: "login", hint: "providers and accounts", now: true },
  { name: "plugins", hint: "turn plugins on and off", now: true },
];

export function generateHeavyRows(count = 2000, prefix = "r") {
  const rows = [];
  const langs = ["go", "ts", "sql", "bash"];
  for (let i = 0; i < count; i++) {
    const mod = i % 6;
    if (mod === 0) {
      rows.push({
        k: `${prefix}-${i}`,
        kind: "you",
        text: `Turn #${(i / 6) | 0}: inspect @platforms/browser/web/src/App.svelte and /skill:perf-audit for frame budget regressions (#${i})`,
      });
    } else if (mod === 1) {
      rows.push({
        k: `${prefix}-${i}`,
        kind: "run",
        live: false,
        what: "read 3 files, ran 1 check",
        now: "",
        failed: i % 36 === 1 ? 1 : 0,
        actions: [
          { k: `${prefix}-${i}-t`, verb: "thought", target: "planning virtualizer height cache and prefix sums", result: "142 tokens", live: false, failed: false },
          { k: `${prefix}-${i}-a1`, verb: "read", target: "platforms/browser/web/src/components/VirtualTranscript.svelte", result: "194 lines", live: false, failed: false },
          { k: `${prefix}-${i}-a2`, verb: "edit", target: "platforms/browser/web/src/lib/store.svelte.js", result: "+14 -6", live: false, failed: false },
          { k: `${prefix}-${i}-a3`, verb: "bash", target: "go test ./mitosis/view/...", result: i % 36 === 1 ? "exit 1" : "ok 0.42s", live: false, failed: i % 36 === 1 },
        ],
      });
    } else if (mod === 2) {
      rows.push({
        k: `${prefix}-${i}`,
        kind: "md",
        block: {
          type: "h",
          level: 2,
          spans: [{ t: `Analysis step ${i}` }],
        },
      });
    } else if (mod === 3) {
      rows.push({
        k: `${prefix}-${i}`,
        kind: "md",
        block: {
          type: "p",
          spans: [
            { t: "Measured layout pass on " },
            { t: "VirtualTranscript.svelte", c: true },
            { t: " with " },
            { t: `${count} rows`, b: true },
            { t: ": keeping DOM nodes bounded below 30 ensures steady 120 Hz scrolling and sub-millisecond streaming updates across " },
            { t: "Mitosis", i: true },
            { t: " frames." },
          ],
        },
      });
    } else if (mod === 4) {
      const lang = langs[i % langs.length];
      rows.push({
        k: `${prefix}-${i}`,
        kind: "md",
        block: {
          type: "code",
          lang,
          text: `// benchmark sample block ${i}\nfunction measureRange(scrollTop, viewportHeight, offsets) {\n  const start = lowerBound(offsets, scrollTop) - 6;\n  const end = lowerBound(offsets, scrollTop + viewportHeight) + 8;\n  return [Math.max(0, start), Math.min(offsets.length - 1, end)];\n}`,
        },
      });
    } else {
      rows.push({
        k: `${prefix}-${i}`,
        kind: "md",
        block: {
          type: "table",
          rows: [
            [[{ t: "Metric" }], [{ t: "Baseline" }], [{ t: "Svelte Virtual" }]],
            [[{ t: "DOM nodes" }], [{ t: `${count * 4}` }], [{ t: "24" }]],
            [[{ t: "Frame P95" }], [{ t: "48.2 ms" }], [{ t: "2.1 ms" }]],
          ],
        },
      });
    }
  }
  return rows;
}

export function createMockPeer({ emit, rowCount = 2000, demo = "" }) {
  const search = typeof location !== "undefined" ? new URLSearchParams(location.search) : null;
  const swarmMode = search?.get("swarm") === "1";
  const runningMode = (demo || search?.get("demo") || "") === "running";
  const selfId = "orb:ed25519:TABBENCH00000000000000000000000000000000001";
  const macId = "orb:ed25519:MACBOOKP00000000000000000000000000000000002";
  const boxId = "orb:ed25519:SERVERBX00000000000000000000000000000000003";
  const labId = "orb:ed25519:ORDLAB0300000000000000000000000000000000004";
  const fedId = "orb:ed25519:FEDORAWK00000000000000000000000000000000005";
  const phnId = "orb:ed25519:ONEPLS1500000000000000000000000000000000006";
  const botId = "orb:ed25519:GROKVM0000000000000000000000000000000000007";

  const now = Date.now();
  const heavyRows = generateHeavyRows(swarmMode ? 12 : rowCount, "heavy");
  const lightRows = generateHeavyRows(swarmMode ? 12 : 48, "light");

  const machines = [
    {
      id: selfId,
      name: "this tab",
      self: true,
      connected: true,
      version: "0.27.0",
      launch: false,
      hue: 0,
      running: [{ instance: "inst-tab", label: "scratch", busy: false, open: false }],
      folders: [],
    },
    {
      id: macId,
      name: "MacBook Pro",
      self: false,
      connected: true,
      version: "0.27.0",
      launch: true,
      hue: 1,
      running: [
        { instance: "inst-1", label: "orb · heavy benchmark", busy: false, open: true },
        { instance: "inst-2", label: "ordalie-back · deploy", busy: true, open: true },
      ],
      folders: [
        { cwd: "/Users/baudouin/Documents/GitHub/orb", threads: 24, modified: now - 120000, live: true },
        { cwd: "/Users/baudouin/Documents/GitHub/ordalie-back", threads: 11, modified: now - 3600000, live: false },
        { cwd: "/Users/baudouin/Documents/GitHub/mygo", threads: 6, modified: now - 86400000, live: false },
      ],
    },
    {
      id: boxId,
      name: "hetzner-01",
      self: false,
      connected: true,
      version: "0.26.0",
      launch: true,
      hue: 2,
      running: [],
      folders: [
        { cwd: "/srv/agents/buzz", threads: 8, modified: now - 7200000, live: false },
      ],
    },
  ];

  const tabs = [
    {
      id: "tab-heavy",
      title: `Heavy conversation (${rowCount} rows)`,
      peer: macId,
      where: "MacBook Pro",
      remote: true,
      cwd: "/Users/baudouin/Documents/GitHub/orb",
      busy: false,
      acting: false,
      online: true,
      asleep: false,
      loaded: true,
      earlier: true,
      streaming: false,
      status: "",
      model: "anthropic/claude-sonnet-4-6",
      thinking: "high",
      levels: ["off", "low", "medium", "high", "max"],
      context: 0.64,
      cost: 1.42,
      usage: {
        plan: "Max 20x",
        at: now - 60000,
        windows: [
          { name: "5h", left: 74, resets: now + 3 * 3600000 },
          { name: "7d", left: 41, resets: now + 4 * 86400000 },
        ],
      },
      ask: null,
      slots: [{ key: "tasks", lines: ["[x] Profile virtualizer", "[ ] Measure streaming P99"], alarm: false }],
      unread: 0,
      commands: [{ name: "review", hint: "code review current diff" }],
      models: [
        "anthropic/claude-sonnet-4-6",
        "anthropic/claude-opus-4-6",
        "openai/gpt-5.4",
        "openai-codex/gpt-5.3-codex",
        "google/gemini-3-pro",
      ],
    },
    {
      id: "tab-live",
      title: "Deploy pipeline check",
      peer: macId,
      where: "MacBook Pro",
      remote: true,
      cwd: "/Users/baudouin/Documents/GitHub/ordalie-back",
      busy: true,
      acting: false,
      online: true,
      asleep: false,
      loaded: true,
      earlier: false,
      streaming: true,
      status: "running tests…",
      model: "openai-codex/gpt-5.3-codex",
      thinking: "medium",
      levels: ["off", "low", "medium", "high"],
      context: 0.86,
      cost: 0.38,
      usage: {
        plan: "Pro",
        at: now - 120000,
        windows: [{ name: "5h", left: 12, resets: now + 90 * 60000 }],
      },
      ask: null,
      slots: [],
      unread: 2,
      commands: [],
      models: ["openai-codex/gpt-5.3-codex", "anthropic/claude-sonnet-4-6"],
    },
  ];

  const entries = [
    { key: "t:tab-heavy", title: tabs[0].title, machine: macId, cwd: tabs[0].cwd, modified: now - 60000, live: false, asks: false, open: true, tab: "tab-heavy", deletable: false },
    { key: "t:tab-live", title: tabs[1].title, machine: macId, cwd: tabs[1].cwd, modified: now - 30000, live: true, asks: false, open: true, tab: "tab-live", deletable: false },
    ...Array.from({ length: 120 }, (_, i) => ({
      key: `s:thread-${i}`,
      title: `Archived session #${i + 1} · ${["refactor", "mitosis", "tui", "wasm", "deploy", "mcp"][i % 6]}`,
      machine: i % 3 === 0 ? boxId : macId,
      cwd: i % 3 === 0 ? "/srv/agents/buzz" : "/Users/baudouin/Documents/GitHub/orb",
      modified: now - (i + 2) * 3600000,
      live: false,
      asks: false,
      open: false,
      tab: "",
      deletable: true,
    })),
  ];

  const rowMap = new Map([
    ["tab-heavy", heavyRows],
    ["tab-live", lightRows],
  ]);

  if (swarmMode) {
    machines.length = 0;
    tabs.length = 0;
    entries.length = 0;
    rowMap.clear();

    const makeTab = (id, title, peer, where, cwd, opts = {}) => ({
      id,
      title,
      peer,
      where,
      remote: peer !== selfId,
      cwd,
      busy: !!opts.busy,
      acting: false,
      online: true,
      asleep: !!opts.asleep,
      loaded: true,
      earlier: false,
      streaming: !!opts.busy,
      status: opts.status || "",
      model: opts.model || "google/gemini-4-neon",
      thinking: "high",
      levels: ["off", "low", "medium", "high"],
      context: opts.context || 0.28,
      cost: opts.cost || 0.24,
      usage: null,
      ask: opts.ask || null,
      slots: [],
      unread: opts.unread || 0,
      commands: [],
      models: ["google/gemini-4-neon", "anthropic/claude-opus-5-5"],
    });

    machines.push(
      { id: selfId, name: "this tab", self: true, connected: true, version: "0.28.0", launch: false, hue: 0, running: [], folders: [] },
      { id: macId, name: "MacBook Pro", self: false, connected: true, version: "0.28.0", launch: true, hue: 1, running: [
        { instance: "s-mac-1", label: "orb · Mitosis protocol perf", busy: true, open: true },
        { instance: "s-mac-2", label: "orb · TUI layout cleanup", busy: true, open: true },
        { instance: "s-mac-3", label: "longboy · ingest worker", busy: false, open: true },
        { instance: "s-mac-4", label: "ordalie-front · drawer polish", busy: false, open: false },
      ], folders: [{ cwd: "/Users/baudouin/Documents/GitHub/orb", threads: 24, modified: now - 30000, live: true }] },
      { id: labId, name: "ordalie-lab-3", self: false, connected: true, version: "0.28.0", launch: true, hue: 2, running: [
        { instance: "s-lab-1", label: "ordalie-back · staging deploy", busy: true, open: true },
        { instance: "s-lab-2", label: "ordalie-back · sqlc migration 042", busy: true, open: true },
        { instance: "s-lab-3", label: "longboy · clickhouse rollup", busy: false, open: true },
        { instance: "s-lab-4", label: "ops · cert renewal", busy: false, open: false },
      ], folders: [{ cwd: "/home/ordalie/ordalie-back", threads: 18, modified: now - 40000, live: true }] },
      { id: fedId, name: "fedora", self: false, connected: true, version: "0.28.0", launch: true, hue: 3, running: [
        { instance: "s-fed-1", label: "orb · linux e2e matrix", busy: true, open: true },
        { instance: "s-fed-2", label: "orb · fixtures-check", busy: false, open: true },
        { instance: "s-fed-3", label: "bench · tailcat throughput", busy: true, open: false },
      ], folders: [{ cwd: "/home/b-arbaretier/orb", threads: 12, modified: now - 90000, live: true }] },
      { id: phnId, name: "OnePlus 15", self: false, connected: true, version: "0.28.0", launch: true, hue: 4, running: [
        { instance: "s-phn-1", label: "notes · client brief Ledoux", busy: false, open: true },
        { instance: "s-phn-2", label: "android · APK smoke test", busy: true, open: false },
      ], folders: [{ cwd: "/data/data/tech.ordalie.orb/files/linux/files/home", threads: 5, modified: now - 180000, live: false }] },
      { id: botId, name: "grok-bot-vm", self: false, connected: false, version: "0.27.0", launch: true, hue: 5, running: [], folders: [{ cwd: "/srv/bot", threads: 4, modified: now - 3600000, live: false }] },
    );

    const swarmTabs = [
      makeTab("tab-coord", "Swarm coordinator", selfId, "this tab", "/workspace", { busy: true, context: 0.34 }),
      makeTab("tab-mac-1", "Mitosis protocol perf", macId, "MacBook Pro", "/Users/baudouin/Documents/GitHub/orb", { busy: true, context: 0.72 }),
      makeTab("tab-mac-2", "TUI layout cleanup", macId, "MacBook Pro", "/Users/baudouin/Documents/GitHub/orb", { busy: true, context: 0.51 }),
      makeTab("tab-mac-3", "Longboy ingest worker", macId, "MacBook Pro", "/Users/baudouin/Documents/GitHub/longboy", { busy: false, context: 0.22 }),
      makeTab("tab-lab-1", "Staging deploy", labId, "ordalie-lab-3", "/home/ordalie/ordalie-back", { busy: true, context: 0.84, ask: { id: "a1", owner: "permissions", title: "Run docker compose up on staging?", choices: ["approve", "deny"] } }),
      makeTab("tab-lab-2", "SQLC migration 042", labId, "ordalie-lab-3", "/home/ordalie/ordalie-back", { busy: true, context: 0.61 }),
      makeTab("tab-lab-3", "ClickHouse rollup", labId, "ordalie-lab-3", "/home/ordalie/longboy", { busy: false, context: 0.19 }),
      makeTab("tab-fed-1", "Linux e2e matrix", fedId, "fedora", "/home/b-arbaretier/orb", { busy: true, context: 0.48 }),
      makeTab("tab-fed-2", "Fixtures check", fedId, "fedora", "/home/b-arbaretier/orb", { busy: false, context: 0.15 }),
      makeTab("tab-phn-1", "Client brief Ledoux", phnId, "OnePlus 15", "/data/data/tech.ordalie.orb/files/linux/files/home", { busy: false, context: 0.12 }),
    ];
    tabs.push(...swarmTabs);

    const sessByTab = {
      "tab-coord": "s-coord",
      "tab-mac-1": "s-mac-1",
      "tab-mac-2": "s-mac-2",
      "tab-mac-3": "s-mac-3",
      "tab-lab-1": "s-lab-1",
      "tab-lab-2": "s-lab-2",
      "tab-lab-3": "s-lab-3",
      "tab-fed-1": "s-fed-1",
      "tab-fed-2": "s-fed-2",
      "tab-phn-1": "s-phn-1",
    };
    for (const t of swarmTabs) {
      entries.push({
        key: `t:${t.peer}|${sessByTab[t.id]}`,
        title: t.title,
        machine: t.peer,
        cwd: t.cwd,
        modified: now - 15000,
        live: t.busy,
        asks: !!t.ask,
        open: true,
        tab: t.id,
        deletable: false,
      });
    }
    // Add a couple of asleep/archived threads on offline & online peers
    entries.push(
      { key: `t:${botId}|s-bot-1`, title: "Nightly scraper run", machine: botId, cwd: "/srv/bot", modified: now - 7200000, live: false, asks: false, open: false, tab: "", deletable: false },
      { key: `t:${botId}|s-bot-2`, title: "Digest summary", machine: botId, cwd: "/srv/bot", modified: now - 14400000, live: false, asks: false, open: false, tab: "", deletable: false },
      { key: `t:${macId}|s-mac-old`, title: "Bench relay throughput", machine: macId, cwd: "/Users/baudouin/Documents/GitHub/orb", modified: now - 3600000, live: false, asks: false, open: false, tab: "", deletable: false },
    );

    // Populate peer-to-peer exchanges & local subagents so synapses and micro-spores light up!
    rowMap.set("tab-coord", [
      { k: "c1", kind: "peer", text: "Run benchmarks on protocol.Write", live: true, exchange: { dir: "out", scope: "peer", who: "MacBook Pro · Mitosis protocol perf", peer: macId, session: "s-mac-1", text: "Run benchmarks on protocol.Write", status: "sent", live: true } },
      { k: "c2", kind: "peer", text: "Verify staging deploy once migration 042 lands", live: false, exchange: { dir: "out", scope: "peer", who: "ordalie-lab-3 · Staging deploy", peer: labId, session: "s-lab-1", text: "Verify staging deploy once migration 042 lands", status: "queued" } },
      { k: "c3", kind: "peer", text: "Kick off Linux e2e matrix", live: false, exchange: { dir: "out", scope: "peer", who: "fedora · Linux e2e matrix", peer: fedId, session: "s-fed-1", text: "Kick off Linux e2e matrix", status: "sent" } },
    ]);
    rowMap.set("tab-mac-1", [
      { k: "m1", kind: "peer", text: "Audit sync.Pool races in protocol.go", live: true, exchange: { dir: "out", scope: "subagent", who: "reviewer", text: "Audit sync.Pool races in protocol.go", status: "working", live: true } },
      { k: "m2", kind: "peer", text: "Profile Validate() allocations", live: false, exchange: { dir: "out", scope: "subagent", who: "scout", text: "Profile Validate() allocations", status: "3 steps", reply: "0 B/op confirmed across 128B–256KiB." } },
      { k: "m3", kind: "peer", text: "Run fixtures-check against new protocol.go", live: false, exchange: { dir: "out", scope: "peer", who: "fedora · Fixtures check", peer: fedId, session: "s-fed-2", text: "Run fixtures-check against new protocol.go", status: "sent" } },
    ]);
    rowMap.set("tab-lab-2", [
      { k: "l1", kind: "peer", text: "Migration 042 verified, safe to deploy", live: true, exchange: { dir: "out", scope: "peer", who: "ordalie-lab-3 · Staging deploy", peer: labId, session: "s-lab-1", text: "Migration 042 verified, safe to deploy", status: "sent", live: true } },
      { k: "l2", kind: "peer", text: "Check index lock duration", live: true, exchange: { dir: "out", scope: "subagent", who: "worker", text: "Check index lock duration", status: "working", live: true } },
    ]);
    rowMap.set("tab-fed-1", [
      { k: "f1", kind: "peer", text: "Run F9 case-sensitive suite", live: true, exchange: { dir: "out", scope: "subagent", who: "runner-f9", text: "Run F9 case-sensitive suite", status: "working", live: true } },
      { k: "f2", kind: "peer", text: "Run Wasm portability checks", live: false, exchange: { dir: "out", scope: "subagent", who: "runner-wasm", text: "Run Wasm portability checks", status: "done", reply: "All Wasm packages pass." } },
    ]);
    rowMap.set("tab-phn-1", [
      { k: "p1", kind: "peer", text: "Summary pushed to MacBook memory", live: false, exchange: { dir: "out", scope: "peer", who: "MacBook Pro · Longboy ingest worker", peer: macId, session: "s-mac-3", text: "Summary pushed to MacBook memory", status: "sent" } },
    ]);
  }

  let summaryText = "1 working · Deploy pipeline check";

  if (runningMode) {
    machines.length = 0;
    tabs.length = 0;
    entries.length = 0;
    rowMap.clear();

    machines.push(
      {
        id: selfId,
        name: "this tab",
        self: true,
        connected: true,
        version: "0.28.0",
        launch: false,
        hue: 0,
        running: [],
        folders: [],
      },
      {
        id: labId,
        name: "ordalie-lab-3",
        self: false,
        connected: true,
        version: "0.28.0",
        launch: true,
        hue: 2,
        running: [
          { instance: "inst-lab-1", label: "orb · Refactor auth middleware", busy: true, open: true },
          { instance: "inst-lab-2", label: "orb · Docs cleanup", busy: false, open: true },
        ],
        folders: [
          { cwd: "/home/ordalie/orb", threads: 16, modified: now - 25000, live: true },
          { cwd: "/home/ordalie/ordalie-back", threads: 9, modified: now - 720000, live: false },
        ],
      },
      {
        id: macId,
        name: "macbook-pro",
        self: false,
        connected: true,
        version: "0.28.0",
        launch: true,
        hue: 1,
        running: [
          { instance: "inst-mac-1", label: "ordalie-back · Schema notes", busy: false, open: true },
          { instance: "inst-mac-remote", label: "Deploy worker", busy: true, open: false },
        ],
        folders: [
          { cwd: "/Users/baudouin/Documents/GitHub/orb", threads: 14, modified: now - 7200000, live: false },
        ],
      },
    );

    tabs.push(
      {
        id: "tab-running",
        title: "Refactor auth middleware",
        peer: labId,
        where: "ordalie-lab-3",
        remote: true,
        cwd: "/home/ordalie/orb",
        busy: true,
        acting: false,
        online: true,
        asleep: false,
        loaded: true,
        earlier: false,
        streaming: false,
        status: "",
        model: "anthropic/claude-sonnet-4-6",
        thinking: "high",
        levels: ["off", "low", "medium", "high", "max"],
        context: 0.42,
        cost: 0.56,
        usage: {
          plan: "Max 20x",
          at: now - 60000,
          windows: [{ name: "5h", left: 78, resets: now + 3 * 3600000 }],
        },
        ask: null,
        slots: [],
        unread: 0,
        commands: [],
        models: ["anthropic/claude-sonnet-4-6", "anthropic/claude-opus-4-6"],
      },
      {
        id: "tab-bg",
        title: "Audit SQL migrations",
        peer: labId,
        where: "ordalie-lab-3",
        remote: true,
        cwd: "/home/ordalie/ordalie-back",
        busy: true,
        acting: false,
        online: true,
        asleep: false,
        loaded: true,
        earlier: false,
        streaming: false,
        status: "analyzing diff…",
        model: "openai-codex/gpt-5.3-codex",
        thinking: "medium",
        levels: ["off", "low", "medium", "high"],
        context: 0.31,
        cost: 0.19,
        usage: null,
        ask: null,
        slots: [],
        unread: 1,
        commands: [],
        models: ["openai-codex/gpt-5.3-codex", "anthropic/claude-sonnet-4-6"],
      },
      {
        id: "tab-ask",
        title: "Release preflight",
        peer: macId,
        where: "macbook-pro",
        remote: true,
        cwd: "/Users/baudouin/Documents/GitHub/orb",
        busy: false,
        acting: false,
        online: true,
        asleep: false,
        loaded: true,
        earlier: false,
        streaming: false,
        status: "",
        model: "anthropic/claude-sonnet-4-6",
        thinking: "medium",
        levels: ["off", "low", "medium", "high"],
        context: 0.24,
        cost: 0.14,
        usage: null,
        ask: {
          title: "Approve migration lock check?",
          question: "Run ALTER TABLE dry-run on staging?",
          options: [
            { label: "Approve dry-run", description: "Run read-only lock check against staging" },
            { label: "Skip", description: "Continue without staging dry-run" },
          ],
        },
        slots: [],
        unread: 1,
        commands: [],
        models: ["anthropic/claude-sonnet-4-6"],
      },
      {
        id: "tab-idle",
        title: "Schema notes",
        peer: macId,
        where: "macbook-pro",
        remote: true,
        cwd: "/Users/baudouin/Documents/GitHub/orb",
        busy: false,
        acting: false,
        online: true,
        asleep: false,
        loaded: true,
        earlier: false,
        streaming: false,
        status: "",
        model: "anthropic/claude-sonnet-4-6",
        thinking: "medium",
        levels: ["off", "low", "medium", "high"],
        context: 0.18,
        cost: 0.08,
        usage: null,
        ask: null,
        slots: [],
        unread: 0,
        commands: [],
        models: ["anthropic/claude-sonnet-4-6"],
      },
    );

    entries.push(
      { key: "t:tab-running", title: "Refactor auth middleware", machine: labId, cwd: "/home/ordalie/orb", modified: now - 15000, live: true, asks: false, open: true, tab: "tab-running", deletable: false },
      { key: "t:tab-bg", title: "Audit SQL migrations", machine: labId, cwd: "/home/ordalie/ordalie-back", modified: now - 45000, live: true, asks: false, open: true, tab: "tab-bg", deletable: false },
      { key: "t:tab-ask", title: "Release preflight", machine: macId, cwd: "/Users/baudouin/Documents/GitHub/orb", modified: now - 25000, live: false, asks: true, open: true, tab: "tab-ask", deletable: false },
      { key: "t:tab-idle", title: "Schema notes", machine: macId, cwd: "/Users/baudouin/Documents/GitHub/orb", modified: now - 12 * 60000, live: false, asks: false, open: true, tab: "tab-idle", deletable: false },
      { key: "s:resting-1", title: "Tighten WebSocket reconnect backoff", machine: labId, cwd: "/home/ordalie/orb", modified: now - 12 * 60000, live: false, asks: false, open: false, tab: "", deletable: true },
      { key: "s:resting-2", title: "Migrate session store to WAL mode", machine: macId, cwd: "/Users/baudouin/Documents/GitHub/orb", modified: now - 2 * 3600000, live: false, asks: false, open: false, tab: "", deletable: true },
      { key: "s:resting-3", title: "Fix mobile viewport safe-area padding", machine: macId, cwd: "/Users/baudouin/Documents/GitHub/orb", modified: now - 5 * 3600000, live: false, asks: false, open: false, tab: "", deletable: true },
    );

    rowMap.set("tab-running", [
      {
        k: "run-1",
        kind: "you",
        text: "Refactor the JWT middleware to validate key rotation without blocking active requests, then run the full auth test suite.",
      },
      {
        k: "run-2",
        kind: "md",
        block: {
          type: "p",
          spans: [
            { t: "I've updated " },
            { t: "internal/auth/middleware.go", c: true },
            { t: " to swap JWKS snapshots atomically with " },
            { t: "atomic.Pointer", c: true },
            { t: " and removed the read-path mutex contention. Running the auth package tests now." },
          ],
        },
      },
      {
        k: "run-3",
        kind: "run",
        live: true,
        what: "read 2 files, edited 1 file",
        now: "bash · running",
        failed: 0,
        actions: [
          { k: "run-3-a1", verb: "read", target: "internal/auth/middleware.go", result: "184 lines", live: false, failed: false },
          { k: "run-3-a2", verb: "edit", target: "internal/auth/middleware.go", result: "+28 -19", live: false, failed: false },
          { k: "run-3-a3", verb: "bash", target: "go test -race ./internal/auth/...", result: "", live: true, failed: false },
        ],
      },
      {
        k: "run-4",
        kind: "peer",
        live: true,
        exchange: {
          scope: "subagent",
          dir: "out",
          who: "scout",
          status: "scanning locks",
          text: "inspect middleware read-path locks",
          steps: "read internal/auth/middleware.go\ngrep sync.RWMutex ./internal/auth",
          live: true,
          failed: false,
        },
      },
    ]);

    rowMap.set("tab-ask", [
      { k: "ask-1", kind: "you", text: "Run release preflight checks before tagging v0.28.1." },
    ]);

    rowMap.set("tab-bg", [
      { k: "bg-1", kind: "you", text: "Check migration 042 for any table locks on orders." },
    ]);

    rowMap.set("tab-idle", [
      { k: "idle-1", kind: "you", text: "Summarize the current index layout." },
      { k: "idle-2", kind: "md", block: { type: "p", spans: [{ t: "All primary indexes are covering and aligned with the query planner." }] } },
    ]);

    summaryText = "2 working · Refactor auth middleware, Audit SQL migrations";
  }

  let streamTimer = null;

  function emitState() {
    emit(JSON.stringify({
      t: "state",
      self: selfId,
      up: true,
      tabs,
      summary: summaryText,
      latest: "0.27.0",
      commands: BUILTIN_COMMANDS,
    }));
  }

  function emitHome() {
    emit(JSON.stringify({ t: "home", machines, entries }));
  }

  function emitRows(tabId, at = 0, list = rowMap.get(tabId) || []) {
    emit(JSON.stringify({ t: "rows", tab: tabId, at, rows: list }));
  }

  // Expose benchmark hooks on window so bench.mjs can drive streaming bursts directly.
  window.__orbBench = {
    rowCount,
    startStreamBurst(tabId = "tab-heavy", chunks = 120, intervalMs = 16) {
      return new Promise(resolve => {
        const list = rowMap.get(tabId) || [];
        const baseIdx = list.length;
        let text = "";
        let n = 0;
        tabs[0].busy = true;
        tabs[0].streaming = true;
        emitState();
        const frameTimes = [];
        let last = performance.now();
        clearInterval(streamTimer);
        streamTimer = setInterval(() => {
          const t0 = performance.now();
          frameTimes.push(t0 - last);
          last = t0;
          text += ` token_${n} streaming chunk from benchmark harness;`;
          const row = {
            k: `stream-${baseIdx}`,
            kind: "md",
            block: { type: "p", spans: [{ t: text }] },
          };
          list[baseIdx] = row;
          emitRows(tabId, baseIdx, [row]);
          n++;
          if (n >= chunks) {
            clearInterval(streamTimer);
            tabs[0].busy = false;
            tabs[0].streaming = false;
            emitState();
            resolve({ chunks: n, frameTimes });
          }
        }, intervalMs);
      });
    },
    triggerInterrupt(multi = false) {
      tabs[0].ask = {
        id: `ask-${Date.now()}`,
        owner: multi ? "questions" : "permissions",
        title: multi ? "Which build targets should we verify?" : "Allow bash command?\ngit push origin main",
        message: multi ? "Select one or more platforms" : "Working directory: ~/Documents/GitHub/orb",
        choices: multi ? ["web (svelte)", "desktop (mygo)", "android (kotlin)", "ios (swift)"] : ["y allow once", "a allow session", "n deny"],
        free: true,
        multi,
      };
      emitState();
    },
    clearInterrupt() {
      tabs[0].ask = null;
      emitState();
    },
  };

  const closedTabs = new Map();
  const archivedEntries = [];
  let termBuf = "\x1b[1;32morb@macbook\x1b[0m:\x1b[1;34m~/orb\x1b[0m$ ";
  let termLine = "";
  let termWaiters = [];
  function pushTerm(chunk) {
    termBuf += chunk;
    const ws = termWaiters.splice(0);
    for (const w of ws) w();
  }

  return {
    async mitosisAdmin(method, paramsJSON) {
      const p = JSON.parse(paramsJSON || "{}");
      if (method === "self") return JSON.stringify({ peer_id: selfId });
      if (method === "sync") {
        return JSON.stringify({
          group_id: "grp-bench",
          in_sync: true,
          owners: [macId],
          members: [macId, boxId, selfId],
          names: { [macId]: "MacBook Pro", [boxId]: "hetzner-01", [selfId]: "Browser (Bench)" },
        });
      }
      if (method === "remote") {
        if (p.method === "host.terminal.open") {
          termBuf = "\x1b[1;32morb@macbook\x1b[0m:\x1b[1;34m~/orb\x1b[0m$ ";
          termLine = "";
          return JSON.stringify({ terminal_id: "term-1" });
        }
        if (p.method === "host.terminal.read") {
          const off = p.params?.offset || 0;
          if (off < termBuf.length) {
            const chunk = termBuf.slice(off);
            return JSON.stringify({ data: btoa(chunk), offset: termBuf.length, done: false });
          }
          await new Promise(resolve => {
            const timer = setTimeout(resolve, 600);
            termWaiters.push(() => { clearTimeout(timer); resolve(); });
          });
          if (off < termBuf.length) {
            const chunk = termBuf.slice(off);
            return JSON.stringify({ data: btoa(chunk), offset: termBuf.length, done: false });
          }
          return JSON.stringify({ data: "", offset: termBuf.length, done: false });
        }
        if (p.method === "host.terminal.write") {
          const raw = p.params?.data ? atob(p.params.data) : "";
          for (const ch of raw) {
            if (ch === "\r" || ch === "\n") {
              const cmd = termLine.trim();
              termLine = "";
              let reply = "\r\n";
              if (cmd) reply += `(demo shell) ${cmd}\r\n`;
              reply += "\x1b[1;32morb@macbook\x1b[0m:\x1b[1;34m~/orb\x1b[0m$ ";
              pushTerm(reply);
            } else if (ch === "\x7f" || ch === "\b") {
              if (termLine.length > 0) {
                termLine = termLine.slice(0, -1);
                pushTerm("\b \b");
              }
            } else if (ch === "\x03") {
              termLine = "";
              pushTerm("^C\r\n\x1b[1;32morb@macbook\x1b[0m:\x1b[1;34m~/orb\x1b[0m$ ");
            } else if (ch >= " ") {
              termLine += ch;
              pushTerm(ch);
            }
          }
          return JSON.stringify({});
        }
        if (p.method === "host.terminal.resize") return JSON.stringify({});
        if (p.method === "host.terminal.close") return JSON.stringify({});
      }
      return JSON.stringify({});
    },
    app(line) {
      const m = JSON.parse(line);
      const reply = (result, error) => {
        if (!m.id) return;
        emit(JSON.stringify({ t: "reply", id: m.id, ...(error ? { error } : { result }) }));
      };
      if (m.do === "hello") {
        emitHome();
        emitState();
        for (const [id, list] of rowMap) {
          emitRows(id, 0, list);
        }
        return;
      }
      if (m.do === "open") {
        let found = tabs.find(t => `t:${t.id}` === m.key);
        if (!found) {
          const entry = entries.find(e => e.key === m.key);
          const id = entry?.tab || m.key.replace(/^t:/, "");
          found = closedTabs.get(id);
          if (found) {
            closedTabs.delete(id);
            tabs.push(found);
            if (entry) entry.open = true;
            emitState();
            emitHome();
          }
        }
        reply({ tab: found ? found.id : tabs[0]?.id || "" });
        return;
      }
      if (m.do === "close") {
        const idx = tabs.findIndex(t => t.id === m.tab);
        if (idx >= 0) {
          const [removed] = tabs.splice(idx, 1);
          closedTabs.set(removed.id, removed);
          const entry = entries.find(e => e.tab === removed.id);
          if (entry) entry.open = false;
          emitState();
          emitHome();
        }
        return;
      }
      if (m.do === "move") {
        const from = tabs.findIndex(t => t.id === m.tab);
        const to = Math.max(0, Math.min(tabs.length - 1, Number(m.to) || 0));
        if (from >= 0 && from !== to) {
          const [moved] = tabs.splice(from, 1);
          tabs.splice(to, 0, moved);
          emitState();
        }
        return;
      }
      if (m.do === "rename") {
        const target = tabs.find(t => t.id === m.tab);
        if (target && m.name) {
          target.title = m.name;
          const entry = entries.find(e => e.tab === target.id);
          if (entry) entry.title = m.name;
          emitState();
          emitHome();
        }
        return;
      }
      if (m.do === "archive") {
        const archived = m.archived !== false;
        const matchesEntry = e => {
          if (m.key && e.key === m.key) return true;
          if (m.machine && m.session_id) {
            return (
              e.machine === m.machine &&
              (e.key === m.session_id || e.key.endsWith(`|${m.session_id}`) || e.key.endsWith(`:${m.session_id}`))
            );
          }
          return false;
        };
        if (archived) {
          const idx = entries.findIndex(matchesEntry);
          if (idx >= 0) {
            const [entry] = entries.splice(idx, 1);
            archivedEntries.unshift({ ...entry, open: false, live: false });
            emitHome();
          }
        } else {
          const idx = archivedEntries.findIndex(matchesEntry);
          if (idx >= 0) {
            const [entry] = archivedEntries.splice(idx, 1);
            entries.push(entry);
            emitHome();
          }
        }
        reply({ ok: true });
        return;
      }
      if (m.do === "threads.archived") {
        reply({ entries: [...archivedEntries] });
        return;
      }
      if (m.do === "detail") {
        if (m.key === "run-3-a3") {
          reply({
            args: JSON.stringify({ command: "go test -race ./internal/auth/...", timeout: 120 }, null, 2),
            output: "=== RUN   TestMiddlewareKeyRotation\n=== RUN   TestMiddlewareConcurrentReads\n--- PASS: TestMiddlewareKeyRotation (0.14s)\nrunning TestMiddlewareConcurrentReads…",
            diff: "",
          });
          return;
        }
        reply({
          text: "Considering virtualizer bounds, prefix sums, and zero-overhead Svelte 5 $state.raw arrays.",
          args: JSON.stringify({ path: "platforms/browser/web/src/components/VirtualTranscript.svelte" }, null, 2),
          output: "ok  github.com/OrdalieTech/orb/mitosis/view  0.412s",
          diff: m.key.endsWith("-a2")
            ? "@@ -12,4 +12,6 @@\n- let rows = $state([]);\n+ let rows = $state.raw([]);\n+ // avoid deep proxying thousands of immutable rows"
            : "",
        });
        return;
      }
      if (m.do === "dirs") {
        const cwd = m.cwd || "/Users/baudouin";
        reply({
          path: cwd,
          parent: cwd === "/" ? "" : cwd.split("/").slice(0, -1).join("/") || "/",
          dirs: [
            { name: "Documents", git: false },
            { name: "orb", git: true },
            { name: "ordalie-back", git: true },
            { name: "mygo", git: true },
          ],
        });
        return;
      }
      if (m.do === "models") {
        reply({
          default: "anthropic/claude-sonnet-4-6",
          thinking: "high",
          levels: ["off", "low", "medium", "high", "max"],
          models: tabs[0].models,
        });
        return;
      }
      if (m.do === "providers") {
        reply([
          { id: "anthropic", name: "Anthropic", ready: true, holds: "account", status: "oauth", models: 4, methods: [{ auth: "oauth", label: "Claude Pro / Max", about: "OAuth sign-in", account: true }, { auth: "api_key", label: "API key", about: "console.anthropic.com" }] },
          { id: "openai-codex", name: "OpenAI Codex", ready: true, holds: "account", status: "oauth", models: 3, methods: [{ auth: "oauth", label: "ChatGPT Plus / Pro", about: "OAuth sign-in", account: true }] },
          { id: "google", name: "Google Gemini", ready: false, models: 5, methods: [{ auth: "api_key", label: "API key", about: "aistudio.google.com" }] },
        ]);
        return;
      }
      if (m.do === "accounts") {
        reply([
          { id: "acc-1", provider: "anthropic", provider_name: "Anthropic (Claude)", name: "work@ordalie.com", active: true, plan: "max", windows: [{ name: "5h", left: 74, resets: now + 3 * 3600000 }, { name: "7d", left: 41, resets: now + 4 * 86400000 }] },
          { id: "acc-2", provider: "openai-codex", provider_name: "OpenAI Codex", name: "personal", active: true, plan: "pro", windows: [{ name: "5h", left: 12, resets: now + 90 * 60000 }] },
        ]);
        return;
      }
      if (m.do === "plugins") {
        reply([
          { name: "memory", on: true, about: "OptChat summary tree and curated user profile with one unified memory tool", choices: [] },
          { name: "tasks", on: true, about: "Live task list", choices: [] },
          { name: "permissions", on: true, about: "Tool approval policy", choices: [{ key: "mode", value: "auto", values: ["enforce", "auto", "log"] }] },
        ]);
        return;
      }
      if (m.do === "complete") {
        reply([
          { text: "/skill:perf-audit", label: "perf-audit", detail: "Profile browser rendering" },
          { text: "@platforms/browser/web/src/App.svelte", label: "App.svelte", detail: "platforms/browser/web/src" },
        ]);
        return;
      }
      if (m.do === "earlier") {
        const extra = generateHeavyRows(120, `earlier-${Date.now()}`);
        const cur = rowMap.get(m.tab) || [];
        const next = extra.concat(cur);
        rowMap.set(m.tab, next);
        emitRows(m.tab, 0, next);
        return;
      }
      if (m.do === "thinking") {
        const t = tabs.find(x => x.id === m.tab);
        if (t && m.name) {
          t.thinking = m.name;
          emitState();
        }
        return;
      }
      if (m.do === "model") {
        const t = tabs.find(x => x.id === m.tab);
        if (t && m.name) {
          t.model = m.name;
          emitState();
        }
        return;
      }
      if (m.do === "answer") {
        tabs[0].ask = null;
        emitState();
        return;
      }
      if (m.do === "send") {
        const list = rowMap.get(m.tab) || [];
        const at = list.length;
        const msg = { k: `sent-${at}`, kind: "you", text: m.text };
        list.push(msg);
        emitRows(m.tab, at, [msg]);
        reply({});
        return;
      }
      if (m.do === "attach") {
        reply({ path: `/tmp/orb-attach/${m.name || "file"}` });
        return;
      }
      if (m.do === "spread.info") {
        reply({
          machine: macId,
          machine_name: "MacBook Pro",
          ssh_hosts: ["ordalie-lab-3", "hetzner-01"],
          clients: [
            { id: "claude", name: "Claude Code" },
            { id: "codex", name: "Codex" },
            { id: "pi", name: "Pi" },
          ],
        });
        return;
      }
      if (m.do === "spread.ssh") {
        reply({
          ok: true,
          target: m.target || "",
          output: "joined · Mitosis running",
        });
        return;
      }
      if (m.do === "invite") {
        reply({
          code: "orb-mitosis:v1:DEMO-INVITE-CODE-0001",
        });
        return;
      }
      reply({});
    },
    async plugins() {
      return JSON.stringify({ memory: true, tasks: true, titles: true, questions: true, memtree: false });
    },
    async models() {
      return JSON.stringify({
        default: "anthropic/claude-sonnet-4-6",
        thinking: "high",
        levels: ["off", "low", "medium", "high", "max"],
        models: tabs[0].models,
      });
    },
    async accounts() {
      return JSON.stringify([
        { id: "acc-1", provider: "anthropic", provider_name: "Anthropic", name: "work@ordalie.com", auth: "oauth", active: true },
      ]);
    },
    async command(raw) {
      const f = JSON.parse(raw);
      emit(JSON.stringify({ type: "response", id: f.id, success: true, data: { models: [{ provider: "anthropic", id: "claude-sonnet-4-6" }], model: { provider: "anthropic", id: "claude-sonnet-4-6" } } }));
      return null;
    },
  };
}
