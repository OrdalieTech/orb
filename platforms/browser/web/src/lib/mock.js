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

export function createMockPeer({ emit, rowCount = 2000 }) {
  const selfId = "orb:ed25519:TABBENCH00000000000000000000000000000000001";
  const macId = "orb:ed25519:MACBOOKP00000000000000000000000000000000002";
  const boxId = "orb:ed25519:SERVERBX00000000000000000000000000000000003";

  const now = Date.now();
  const heavyRows = generateHeavyRows(rowCount, "heavy");
  const lightRows = generateHeavyRows(48, "light");

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

  let streamTimer = null;

  function emitState() {
    emit(JSON.stringify({
      t: "state",
      self: selfId,
      up: true,
      tabs,
      summary: "1 working · Deploy pipeline check",
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
        if (p.method === "host.terminal.open") return JSON.stringify({ terminal_id: "term-1" });
        if (p.method === "host.terminal.read") {
          const off = p.params?.offset || 0;
          if (off === 0) {
            const msg = "\x1b[1;32morb@macbook\x1b[0m:\x1b[1;34m~/orb\x1b[0m$ ";
            return JSON.stringify({ data: btoa(msg), offset: msg.length, done: false });
          }
          await new Promise(r => setTimeout(r, 800));
          return JSON.stringify({ data: "", offset: off, done: false });
        }
        if (p.method === "host.terminal.write") return JSON.stringify({});
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
        emitRows("tab-heavy", 0, heavyRows);
        emitRows("tab-live", 0, lightRows);
        return;
      }
      if (m.do === "open") {
        const found = tabs.find(t => `t:${t.id}` === m.key);
        reply({ tab: found ? found.id : "tab-heavy" });
        return;
      }
      if (m.do === "detail") {
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
          { name: "memory", on: true, about: "Persistent memory tools", choices: [] },
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
