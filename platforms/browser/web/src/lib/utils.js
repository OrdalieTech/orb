// Pure formatting, tokenizing, ANSI stripping, and keyboard helper utilities.

const ANSI_RE = /\x1b\[[0-9;?]*[ -/]*[@-~]|\[[0-9;]+m/g;
const TOKEN_RE = /(?:^|\s)(\/skill:[\w.-]+|@[^\s]+|#[\w-]+)/g;

export function stripAnsi(text) {
  return (text || "").replace(ANSI_RE, "");
}

export function hasVisibleText(lines) {
  return /[\p{L}\p{N}]/u.test(stripAnsi((lines || []).join("")));
}

export function named(title) {
  return (title || "new session").replace(/^./, c => c.toUpperCase());
}

export function base(path) {
  return (path || "").split("/").filter(Boolean).at(-1) || path || "";
}

export function tidy(path) {
  return (path || "").replace(/^\/(Users|home)\/[^/]+/, "~");
}

export function shortPeer(id) {
  return id?.split(":").at(-1)?.slice(0, 8) || id || "";
}

export function quotePath(p) {
  return /\s/.test(p) ? `"${p}"` : p;
}

export function ago(ms) {
  if (!ms) return "";
  const m = (Date.now() - ms) / 60000;
  if (m < 1) return "now";
  if (m < 60) return `${m | 0}m`;
  if (m < 1440) return `${(m / 60) | 0}h`;
  if (m < 7 * 1440) return `${(m / 1440) | 0}d`;
  const d = new Date(ms);
  return d.getFullYear() === new Date().getFullYear()
    ? d.toLocaleDateString([], { month: "short", day: "numeric" })
    : d.toLocaleDateString([], { month: "short", year: "numeric" });
}

export function resetsAt(ms) {
  if (!ms || ms <= 0) return "";
  const at = new Date(ms);
  const hours = (ms - Date.now()) / 3600000;
  if (hours < 20) return at.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  if (hours < 6 * 24) return at.toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit", hour12: false });
  return at.toLocaleDateString([], { day: "numeric", month: "short" });
}

export function word(t) {
  if (!t) return "";
  if (t.ask) return "asks";
  if (t.busy) return "working";
  if (t.asleep) return "asleep";
  if (!t.online) return t.remote ? "offline" : "waking…";
  return "";
}

export function peerActivity(machine) {
  const list = machine?.running || [];
  let working = 0;
  let open = 0;
  for (const r of list) {
    if (r?.busy) working++;
    else open++;
  }
  let label = "";
  if (working > 0 && open > 0) label = `${working} working · ${open} open`;
  else if (working > 0) label = `${working} working`;
  else if (open > 0) label = `${open} open`;
  return { working, open, label };
}

export function busyActivity(tab, rows = []) {
  if (!tab?.busy) return "";
  for (let i = rows.length - 1; i >= 0; i--) {
    const r = rows[i];
    if (r?.kind === "run" && r.live) {
      if (r.now) return r.now;
      const actions = r.actions || [];
      for (let j = actions.length - 1; j >= 0; j--) {
        if (actions[j]?.live && actions[j].verb) {
          return `${actions[j].verb} · running`;
        }
      }
    }
    if (r?.kind === "peer" && r.live && r.exchange?.who) {
      return r.exchange.status ? `${r.exchange.who} · ${r.exchange.status}` : `${r.exchange.who} · working`;
    }
  }
  if (tab.status) return tab.status;
  return "working";
}

function clipText(str, max = 48) {
  const s = (str || "").replace(/\s+/g, " ").trim();
  if (!s || s.length <= max) return s;
  return s.slice(0, max - 1) + "…";
}

export function collectLiveActivity(tabs = [], rowsByTab = {}, home = {}) {
  const machines = home?.machines || [];
  const machineById = new Map();
  for (const m of machines) {
    if (m?.id) machineById.set(m.id, m);
  }

  const inputs = [];
  const agents = [];
  const remotes = [];

  for (const t of tabs || []) {
    if (!t) continue;
    const m = machineById.get(t.peer);
    const hue = m?.hue ?? 0;
    const where = t.where || m?.name || "";

    if (t.ask) {
      const prompt = t.ask.title || t.ask.question || t.ask.text || "needs input";
      inputs.push({
        tabId: t.id,
        peer: t.peer || "",
        where,
        title: named(t.title),
        prompt,
        hue,
      });
    }

    if (t.busy) {
      const rows = (rowsByTab && rowsByTab[t.id]) || [];
      const processes = [];
      for (const r of rows) {
        if (!r) continue;
        if (r.kind === "run" && r.live) {
          for (const a of r.actions || []) {
            if (a?.live) {
              processes.push({
                key: a.k || "",
                verb: a.verb || "tool",
                target: a.target || "",
                detail: a.result || "",
                kind: "action",
              });
            }
          }
        } else if (r.kind === "action" && r.action?.live) {
          const a = r.action;
          processes.push({
            key: a.k || r.k || "",
            verb: a.verb || "tool",
            target: a.target || "",
            detail: a.result || "",
            kind: "action",
          });
        } else if (r.kind === "peer" && r.exchange?.live) {
          const ex = r.exchange;
          processes.push({
            key: r.k || "",
            verb: ex.who || (ex.scope === "subagent" ? "subagent" : "peer"),
            target: ex.text || ex.status || "",
            detail: ex.steps || "",
            kind: ex.scope === "subagent" ? "subagent" : "peer",
          });
        }
      }
      if (processes.length === 0) {
        processes.push({
          key: "",
          verb: "thinking",
          target: t.status || "working…",
          detail: "",
          kind: "status",
        });
      }
      agents.push({
        tabId: t.id,
        peer: t.peer || "",
        where,
        title: named(t.title),
        status: t.status || "",
        hue,
        processes,
      });
    }
  }

  for (const m of machines) {
    for (const r of m?.running || []) {
      if (r?.busy && !r.open) {
        remotes.push({
          instance: r.instance || "",
          peer: m.id || "",
          where: m.name || "",
          label: r.label || "running agent",
          hue: m.hue ?? 0,
        });
      }
    }
  }

  const totalRunning = agents.length + remotes.length;
  const parts = [];
  if (totalRunning > 0) parts.push(`${totalRunning} running`);
  if (inputs.length > 0) parts.push(`${inputs.length} ${inputs.length === 1 ? "input" : "inputs"}`);

  let highlight = "";
  for (const ag of agents) {
    const p = ag.processes.find(x => x.kind === "action" || x.kind === "subagent" || x.kind === "peer");
    if (p) {
      const target = clipText(p.target, 44);
      highlight = target ? `${p.verb}: ${target}` : p.verb;
      break;
    }
  }
  if (!highlight && inputs.length > 0) {
    highlight = clipText(inputs[0].prompt, 44);
  }
  if (!highlight && agents.length > 0) {
    highlight = clipText(agents[0].processes[0]?.target || agents[0].title, 44);
  }
  if (!highlight && remotes.length > 0) {
    highlight = clipText(remotes[0].label, 44);
  }
  if (highlight) parts.push(highlight);

  return {
    inputs,
    agents,
    remotes,
    totalRunning,
    summary: parts.join(" · "),
  };
}

export function tightestWindow(usage) {
  if (!usage?.windows?.length) return null;
  let min = usage.windows[0];
  for (let i = 1; i < usage.windows.length; i++) {
    if (usage.windows[i].left < min.left) min = usage.windows[i];
  }
  return min;
}

export function usageLabel(usage) {
  const w = tightestWindow(usage);
  if (!w) return "";
  const reset = w.resets ? ` · ${new Date(w.resets).toTimeString().slice(0, 5)}` : "";
  return `${w.name} ${Math.round(w.left)}%${reset}`;
}

export function newerVersion(a, b) {
  if (!a || !b) return false;
  const parse = v =>
    v.replace(/^v/, "").split("-")[0].split(".").slice(0, 3).map(n => parseInt(n, 10) || 0);
  const x = parse(a), y = parse(b);
  for (let i = 0; i < 3; i++) {
    if ((x[i] || 0) !== (y[i] || 0)) return (x[i] || 0) > (y[i] || 0);
  }
  return false;
}

// Split user message text into plain and highlighted token segments (/skill:…, @file, #ref).
export function tokenizeText(text) {
  const out = [];
  let at = 0;
  TOKEN_RE.lastIndex = 0;
  let m;
  while ((m = TOKEN_RE.exec(text)) !== null) {
    const tok = m[1];
    const start = m.index + m[0].length - tok.length;
    if (start > at) out.push({ kind: "text", text: text.slice(at, start) });
    const kind = tok.startsWith("/skill:") ? "skill" : tok.startsWith("@") ? "file" : "ref";
    out.push({ kind, text: tok });
    at = start + tok.length;
  }
  if (at < text.length) out.push({ kind: "text", text: text.slice(at) });
  return out;
}

// Tail preview lines of a conversation for Overview / Home live cards.
export function tailPreview(list, maxLines = 10) {
  if (!list?.length) return "";
  const slice = list.slice(-8);
  const parts = [];
  for (const r of slice) {
    let s = "";
    if (r.kind === "you") s = `› ${r.text || ""}`;
    else if (r.kind === "md") s = blockPlain(r.block);
    else if (r.kind === "run") s = `· ${r.now || r.what || ""}`;
    else if (r.kind === "note") s = stripAnsi(r.text || "");
    s = s.trim();
    if (s) parts.push(s);
  }
  return parts.join("\n").split("\n").slice(-maxLines).join("\n");
}

function blockPlain(b) {
  if (!b) return "";
  const bits = [];
  if (b.text) bits.push(b.text);
  if (b.spans?.length) bits.push(b.spans.map(s => s.t).join(""));
  if (b.blocks?.length) bits.push(b.blocks.map(blockPlain).join("\n"));
  if (b.rows?.length) bits.push(b.rows.map(r => r.map(c => c.map(s => s.t).join("")).join("  ")).join("\n"));
  return bits.filter(Boolean).join("\n");
}

export function crumbs(p) {
  if (!p) return [];
  const t = tidy(p);
  const out = [];
  let rest = "";
  let at = "";
  if (t.startsWith("~")) {
    at = p.slice(0, p.length - t.length + 1);
    out.push({ label: "~", path: at });
    rest = t.slice(1).replace(/^\/+/, "");
  } else {
    out.push({ label: "/", path: "/" });
    rest = p.replace(/^\/+/, "");
  }
  if (!rest) return out;
  for (const seg of rest.split("/").filter(Boolean)) {
    at = at === "/" ? `/${seg}` : `${at}/${seg}`;
    out.push({ label: seg, path: at });
  }
  return out;
}

export function toBase64(bytes) {
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}

export function tabName() {
  const ua = navigator.userAgent || "";
  if (/iPhone|iPad/.test(ua)) return "Browser (iOS)";
  if (/Android/.test(ua)) return "Browser (Android)";
  if (/Mac OS X|Macintosh/.test(ua)) return "Browser (Mac)";
  if (/Windows/.test(ua)) return "Browser (Windows)";
  if (/Linux/.test(ua)) return "Browser (Linux)";
  return "Browser";
}

export const REASONING = {
  off: "no thinking",
  minimal: "barely",
  low: "light",
  medium: "balanced",
  high: "thorough",
  xhigh: "very thorough",
  max: "as long as it takes",
};

export const PLUGIN_ABOUT = {
  titles: "Name each session after its first exchange",
  tasks: "Live session task list and todo tool",
  questions: "Ask the user questions with choices and custom answers",
  memory: "Bounded persistent remember, recall, replace, and forget tools",
  memtree: "Experimental: zoomable summary tree of each session for lossless compaction or fresh turns",
};

export const HOME_COMMANDS = new Set(["new", "model", "sessions", "pair", "login", "plugins"]);

export const SHORTCUT_GROUPS = [
  {
    index: "01",
    title: "TUI Core",
    items: [
      ["Ctrl + P / ⌘K", "Command palette · jump anywhere"],
      ["Ctrl + M / Alt + M", "Model & reasoning deck"],
      ["Shift + Tab", "Cycle reasoning / thinking level"],
      ["Ctrl + O", "Expand / collapse all tool outputs"],
      ["Esc", "Close overlay → stop turn → Home"],
      ["Ctrl + N / Alt + N", "New session (Home)"],
      ["Ctrl + R / Alt + R", "Rename active session"],
      ["Ctrl + X", "Copy last reply (when nothing selected)"],
      ["Alt + Enter", "Queue follow-up after active turn"],
    ],
  },
  {
    index: "02",
    title: "Workspace, Tabs & Splits",
    items: [
      ["Shift + ← / →", "Previous / next conversation (outside input)"],
      ["Ctrl + Tab · ⌘⇧[ / ]", "Next / previous conversation"],
      ["Alt + 1 … 8 / 9", "Switch to tab 1–8 / last tab"],
      ["⌘/Alt + D · ⇧D", "Split right · split down (drag tabs onto edges too)"],
      ["⌘ + [  /  ⌘ + ]", "Focus previous / next split tile"],
      ["⌘ + ⇧ + Enter", "Zoom / restore active split tile"],
      ["Alt + ⇧ + W", "Close active split tile"],
      ["Alt + ⇧ + =", "Even out split tiles (or double-click divider)"],
      ["Ctrl + G / Alt + G", "Toggle Overview live grid"],
      ["Ctrl + J / Alt + J", "Toggle remote terminal"],
      ["Alt + H / Alt + 0", "Home"],
      ["Alt + W / Alt+Shift+T", "Close tab / reopen closed tab"],
      ["Ctrl/⌘ + ,", "Settings"],
    ],
  },
  {
    index: "03",
    title: "Composer & Transcript",
    items: [
      ["Enter", "Send message · steer running turn"],
      ["Shift + Enter", "Insert newline"],
      ["Tab", "Complete /command or @file / skill"],
      ["↑  (empty box)", "Recall previous sent prompts"],
      ["Alt + L", "Focus prompt box"],
      ["PageUp / PageDown", "Scroll transcript (detaches follow)"],
      ["End", "Jump to bottom & re-pin follow"],
      ["?", "Open this shortcuts reference"],
    ],
  },
];
