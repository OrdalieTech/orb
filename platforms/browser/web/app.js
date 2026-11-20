import { spawnPeer } from "./peer.js";
const $ = id => document.getElementById(id);
// notice: the one error line, moved to the screen it concerns, so pages that redraw keep hold of it.
const notice = $("error");
// tab: this tab's own Orb (peer.wasm), one of the user's devices. The page draws what the view every
// Orb app draws (mitosis/view, run by that Orb) last sent: its state, Home (the machines of the group
// and their threads) and the rows of each conversation it follows. shown: the conversation on
// screen, "" for Home; device: the machine Home narrows to.
let tab = null, me = "", state = { tabs: [], commands: [] }, home = { machines: [], entries: [] }, shown = "", device = "";
const rows = new Map(), replies = new Map();
let asked = 0, dirty = 0, restore = localStorage.getItem("orb.tab.shown") || "";
// What each part last drew from: a part is drawn again only when that changed, so a list streaming
// beside it never replaces the button under a click.
const drewFrom = {};
function changed(part, ...from) {
  const key = JSON.stringify(from);
  return drewFrom[part] !== key && (drewFrom[part] = key);
}

function showError(message) {
  notice.textContent = message || "";
  notice.hidden = !message;
}
// show picks the one screen: the invitation, its approval, the app or Settings; an error reads at
// the end of what it concerns, as the apps note one.
function show(screen) {
  if (screen === "app") $("prompt-form").before(notice);
  else $(screen === "settings" ? "page" : screen).append(notice);
  $("pair").hidden = screen === "app" || screen === "settings";
  $("app").hidden = screen !== "app";
  $("settings").hidden = screen !== "settings";
  $("invite").hidden = screen !== "invite";
  $("approve").hidden = screen !== "approve";
}
function node(tag, ...children) {
  const element = document.createElement(tag);
  element.append(...children);
  return element;
}
const el = (tag, className, ...children) => Object.assign(node(tag, ...children), { className });
function button(text, onclick, className = "") {
  return Object.assign(node("button", text), { type: "button", onclick, className });
}

// The view's wire: intents go in as {do, …}; one with an id gets its reply.
const send = intent => tab?.app(JSON.stringify(intent));
function ask(name, args = {}) {
  const id = `p${++asked}`;
  return new Promise((resolve, reject) => {
    replies.set(id, m => (m.error ? reject(new Error(m.error)) : resolve(m.result)));
    send({ do: name, id, ...args });
  });
}
function heard(m) {
  if (m.t === "reply") return replies.get(m.id)?.(m), replies.delete(m.id);
  if (m.t === "state") {
    state = { ...m, tabs: m.tabs || [] };
    for (const id of rows.keys()) if (!state.tabs.some(t => t.id === id)) rows.delete(id);
    // A reload opens on the conversation shown before it, once the view brings its tab back.
    if (restore && state.tabs.some(c => c.id === restore)) focus(restore);
    restore = "";
    if (shown && !conversation()) focus("");
    for (const w of waiting.splice(0)) conversation(w.id)?.online ? w.resolve() : waiting.push(w);
  } else if (m.t === "home") home = m;
  else if (m.t === "alert") return notify(m);
  else if (m.t === "rows") {
    const list = rows.get(m.tab) || [];
    list.length = Math.min(m.at, list.length);
    rows.set(m.tab, list.concat(m.rows));
  } else return;
  dirty ||= requestAnimationFrame(render);
}
// ready resolves once a conversation's Orb has described it, so what is sent there reaches it.
const waiting = [];
const ready = id => conversation(id)?.online ? Promise.resolve() : new Promise(resolve => waiting.push({ id, resolve }));
// A conversation that finished or asks while the page is out of sight says so, once allowed to.
function notify(m) {
  if (window.Notification?.permission !== "granted") return;
  const n = new Notification(m.title, { body: m.text, tag: m.tab, icon: "ordalie.svg" });
  n.onclick = () => { window.focus(); focus(m.tab); n.close(); };
}
const conversation = (id = shown) => state.tabs.find(t => t.id === id);
const machine = id => home.machines.find(m => m.id === id);
const named = title => (title || "new session").replace(/^./, c => c.toUpperCase());
// Each conversation keeps its draft, and Home its own, while another shows.
const drafts = new Map();
function focus(id) {
  showError("");
  localStorage.setItem("orb.tab.shown", id);
  drafts.set(shown, $("prompt").value);
  shown = id;
  $("prompt").value = drafts.get(id) || "";
  send({ do: "show", tabs: id ? [id] : [] });
  dirty ||= requestAnimationFrame(render);
  $("prompt").focus({ preventScroll: true });
}
function open(key) {
  // A device that refuses says why on Home's launching line, which says it once.
  ask("open", { key }).then(r => r?.tab && focus(r.tab), error => error.message !== state.launching && showError(error.message));
}
// A conversation's state in a word, as the apps say it; empty awake and at rest.
function word(t) {
  if (t.ask) return "asks";
  if (t.busy) return "working";
  if (t.asleep) return "asleep";
  if (!t.online) return t.remote ? "offline" : "waking…";
  return "";
}
// Where a conversation runs, as a square: the ink for this tab, a device's own hue, red while it
// asks; it pulses while it works.
function where(hue, live, asks) {
  return Object.assign(el("i", `where${live ? " live" : ""}${asks ? " asks" : ""}`), { style: `--hue: var(--hue${hue || 0})` });
}
function ago(ms) {
  const m = (Date.now() - ms) / 60000;
  return m < 1 ? "now" : m < 60 ? `${m | 0}m` : m < 1440 ? `${(m / 60) | 0}h` : `${(m / 1440) | 0}d`;
}

function render() {
  dirty = 0;
  const t = conversation();
  renderBar(t);
  $("home").hidden = !!t;
  $("transcript").hidden = $("strip").hidden = !t;
  if (t) renderChat(t);
  else { renderHome(); $("slots").replaceChildren(); }
  // Another Orb driving this tab's: blue fills space here and nowhere else.
  $("acting").hidden = !t?.acting;
  const unread = state.tabs.reduce((n, c) => n + (c.unread || 0), 0);
  document.title = `${state.tabs.some(c => c.ask) ? "● " : unread ? `(${unread}) ` : ""}${t ? named(t.title) : "Orb"}`;
  if (!$("settings").hidden && page.vendor && changed("login", state.login)) openPage(page.name, page.peer, page.vendor);
  controls();
  if (deck.open && changed("deck", t)) drawDeck();
  // A question the conversation on screen waits on stops the world; answered or gone, it sinks.
  const box = document.querySelector(".interrupt:not(.leaving)");
  if (t?.ask && box?.dataset.ask !== t.ask.id) interrupt(t, t.ask);
  else if (!t?.ask && box) leave();
}
// The desktop's tab bar: a chip per open conversation, its square in its machine's hue, pulsing
// while it works, red while it asks, its unread answers counted; its × on it when shown or under
// the pointer.
function renderBar(t) {
  if (!changed("bar", state.tabs, shown, home.machines.map(m => m.hue))) return;
  $("tabs").replaceChildren(...state.tabs.map(c => {
    const close = node("b", "×"), chip = button("", () => focus(c.id), c.id === shown ? "on" : "");
    chip.append(where(machine(c.peer)?.hue, c.busy, !!c.ask), node("span", named(c.title)), ...(c.unread ? [el("small", "unread", String(c.unread))] : []), close);
    chip.title = [c.where, word(c)].filter(Boolean).join(" · ");
    close.onclick = event => { event.stopPropagation(); closeTab(c); };
    // A middle click closes it, a right click or a double click offers its menu, a drag moves it.
    chip.onauxclick = event => event.button === 1 && closeTab(c);
    chip.oncontextmenu = chip.ondblclick = event => { event.preventDefault(); tabMenu(c); };
    chip.draggable = true;
    chip.ondragstart = event => event.dataTransfer.setData("text/orb-tab", c.id);
    chip.ondragover = event => event.preventDefault();
    chip.ondrop = event => {
      const id = event.dataTransfer.getData("text/orb-tab");
      if (id && id !== c.id) send({ do: "move", tab: id, to: state.tabs.indexOf(c) });
    };
    return chip;
  }));
  $("orb").classList.toggle("on", !t);
  // How full the conversation's context is: ten segments, as the desktop's strip, red past 80 %.
  const share = t?.context || 0;
  $("context").hidden = !(share > 0);
  $("context").classList.toggle("alarm", share > .8);
  $("gauge").replaceChildren(...Array.from({ length: 10 }, (_, i) => el("i", i < Math.round(share * 10) ? "lit" : "")));
  $("context").lastChild.textContent = `${Math.round(share * 100)}%`;
}

// Home, as the apps': the machines of the group put to the vote, as NERV's MAGI (this tab first,
// then each device, connected or not, and how many Orbs run there; a card narrows the list to it),
// then every thread on every machine, newest first, where it lives a detail of its row.
function renderHome() {
  if (!changed("home", home, state.tabs, state.summary, state.launching, state.up, device, gone)) return;
  // This tab's own Orb is a fallback, for when no device lets it start one: it stays out of sight
  // while one does, unless a conversation was started on it.
  const own = !launchers().length || aim().machine?.self;
  const machines = [...home.machines].filter(m => own || !m.self).sort((x, y) => !!y.self - !!x.self);
  $("machines").replaceChildren(...machines.map(m => {
    const up = m.self ? state.up : m.connected;
    const card = button("", () => { device = device === m.id ? "" : m.id; render(); }, `${device === m.id ? "on" : ""}${up ? "" : " down"}`);
    card.oncontextmenu = event => { event.preventDefault(); deviceMenu(m); };
    card.append(node("span", where(m.hue), m.self ? "this tab" : m.name), node("span", up ? `${m.running?.length || 0} running` : "offline", node("b", up ? "承認" : "否決")));
    return card;
  }));
  $("summary").textContent = state.summary || "";
  $("summary").classList.toggle("asks", (state.summary || "").startsWith("needs"));
  // What starting Orb on a device is doing; why it failed, until clicked away.
  const launching = state.launching || "", starting = launching.startsWith("starting");
  $("launching").textContent = launching;
  $("launching").hidden = !launching || launching === gone;
  $("launching").className = starting ? "" : "alarm";
  $("launching").onclick = () => { if (!starting) { gone = launching; render(); } };
  const entries = home.entries.filter(e => (!device || e.machine === device) && (own || e.machine !== state.self || e.open));
  // A device connected but not read yet says so, rather than an empty list filling at once.
  const reading = home.machines.filter(m => m.reading && (!device || m.id === device));
  $("entries").replaceChildren(...entries.map(e => {
    const m = machine(e.machine), followed = conversation(e.tab), age = followed ? word(followed) : "";
    const row = button("", () => followed ? focus(followed.id) : open(e.key), e.open ? "open" : "");
    row.oncontextmenu = event => { event.preventDefault(); threadMenu(e); };
    row.append(where(m?.hue, e.live, e.asks), node("span", named(e.title)),
      el("small", e.asks ? "asks" : "", [!device && m && (m.self ? "this tab" : m.name), e.asks ? "asks" : e.live ? "working" : age || (e.unstored ? "open" : ago(e.modified))].filter(Boolean).join(" · ")));
    return row;
  }), ...(!home.machines.length ? [el("p", "meta reading", "reading conversations…")] : reading.length ? [el("p", "meta reading", `reading ${reading.map(m => m.name).join(", ")}…`)] : !entries.length ? [el("p", "meta", "no conversations here yet")] : []));
}
// Where Home's next message goes: the device and folder picked last, one that lets this tab start
// Orb there, else the one this tab paired with; with none, this tab's own Orb.
function launchers() {
  return home.machines.filter(m => !m.self && m.launch && m.connected);
}
function aim() {
  const saved = JSON.parse(localStorage.getItem("orb.tab.aim") || "{}"), all = launchers(), self = machine(state.self);
  if (saved.machine === state.self && self) return { machine: self, cwd: "" };
  const m = all.find(d => d.id === saved.machine) || all.find(d => d.id === localStorage.getItem("orb.tab.owner")) || all[0];
  return m ? { machine: m, cwd: m.id === saved.machine ? saved.cwd || "" : "" } : { machine: self, cwd: "" };
}
const aimAt = (id, cwd = "") => { localStorage.setItem("orb.tab.aim", JSON.stringify({ machine: id, cwd })); controls(); };
const base = path => path.split("/").filter(Boolean).at(-1) || path;
const tidy = path => path.replace(/^\/(Users|home)\/[^/]+/, "~");
// Where to run: a device, in its home folder, one of the folders worked in last or any other, the
// one chosen lit; an Orb running somewhere (it opens); this tab.
async function chooseWhere() {
  const self = machine(state.self), now = aim(), options = [];
  for (const m of launchers()) {
    const at = now.machine?.id === m.id && tidy(now.cwd);
    options.push({ label: m.name, detail: "home folder", on: at === "" || at === "~", value: () => aimAt(m.id) });
    for (const f of (m.folders || []).filter(f => tidy(f.cwd) !== "~").slice(0, 5)) options.push({ label: tidy(f.cwd), detail: `${m.name} · ${f.threads} thread${f.threads === 1 ? "" : "s"}`, on: at === tidy(f.cwd), value: () => aimAt(m.id, f.cwd) });
    if (at && at !== "~" && !options.some(o => o.on)) options.push({ label: at, detail: m.name, on: true, value: () => aimAt(m.id, now.cwd) });
    options.push({ label: "another folder…", detail: `${m.name} · type its path`, value: () => pick(`a folder on ${m.name}`, [], { field: true, hint: "~/code/project" }).then(cwd => cwd?.trim() && aimAt(m.id, cwd.trim())) });
  }
  for (const m of home.machines) for (const r of m.running || []) if (!r.open && !m.self) options.push({ label: r.label, detail: r.busy ? "working · open it" : "running · open it", value: () => open(`i:${r.instance}`) });
  if (self) options.push({ label: "this tab", detail: "its own Orb, one conversation at a time", on: now.machine?.self, value: () => aimAt(state.self) });
  (await pick("run on", options))?.();
}
async function start(text) {
  const { machine: m, cwd } = aim();
  if (!m) throw new Error("no device to start on yet");
  if (!m.self) return ask("start", { machine: m.id, cwd, text }).then(r => focus(r.tab));
  // This tab's Orb holds one conversation at a time: a new one replaces it, once its Orb is ready.
  const own = m.running?.[0];
  if (!own) throw new Error("this tab's Orb is still starting");
  const { tab: id } = await ask("open", { key: `i:${own.instance}` });
  focus(id);
  await ready(id);
  await ask("send", { tab: id, text: `/new ${text}` });
}

// Closing a conversation stops following it; its thread stays where it runs, so for a few seconds
// an undo (or Alt+Shift+T) opens it again in its place. The one shown gives way to its neighbour.
let reopen = [], gone = "";
function closeTab(c) {
  const at = state.tabs.findIndex(x => x.id === c.id), entry = home.entries.find(e => e.tab === c.id);
  send({ do: "close", tab: c.id });
  if (c.id === shown) { const next = state.tabs[at + 1] || state.tabs[at - 1]; focus(next ? next.id : ""); }
  if (!entry) return;
  const again = () => ask("open", { key: entry.key }).then(r => { send({ do: "move", tab: r.tab, to: at }); focus(r.tab); }, error => showError(error.message));
  reopen.push(again);
  toast(`closed ${named(c.title)}`, again);
}
function toast(text, undo) {
  const box = $("toast");
  box.firstChild.textContent = text;
  box.hidden = false;
  box.lastChild.hidden = !undo;
  box.lastChild.onclick = () => { box.hidden = true; reopen = reopen.filter(f => f !== undo); undo(); };
  const later = () => { clearTimeout(box.timer); box.timer = setTimeout(() => (box.hidden = true), 10000); };
  box.onmouseenter = () => clearTimeout(box.timer);
  box.onmouseleave = later;
  later();
}
async function rename(title, apply) {
  const name = (await pick("rename", [], { field: true, value: title }))?.trim();
  if (name) apply(name);
}
async function tabMenu(c) {
  const what = await pick(named(c.title), [{ label: "rename" }, { label: "close conversation" }]);
  if (what === "rename") rename(c.title, name => send({ do: "rename", tab: c.id, name }));
  if (what === "close conversation") closeTab(c);
}
// A Home row's menu: its tab's, and deleting a thread no Orb has open, which asks first.
async function threadMenu(e) {
  const c = conversation(e.tab), what = await pick(named(e.title), [{ label: "rename" }, ...(c ? [{ label: "close conversation" }] : []), ...(e.deletable ? [{ label: "delete forever" }] : [])]);
  if (what === "rename") rename(e.title, name => ask("rename", { key: e.key, name }).catch(error => showError(error.message)));
  if (what === "close conversation") closeTab(c);
  if (what === "delete forever" && await pick(`delete ${named(e.title)}? it cannot come back`, [{ label: "keep it" }, { label: "delete forever" }]) === "delete forever") send({ do: "delete", key: e.key });
}
async function deviceMenu(m) {
  const what = await pick(m.self ? "this tab" : m.name, [...(m.launch && m.connected ? [{ label: "start here" }] : []), { label: "providers" }, { label: "plugins" }, ...(m.self ? [] : [{ label: "rename" }, { label: "forget", detail: "takes it out of this tab's view" }])]);
  if (what === "start here") chooseWhere();
  if (what === "providers" || what === "plugins") openPage(what, m.id);
  if (what === "rename") rename(m.name, name => send({ do: "rename", machine: m.id, name }));
  if (what === "forget" && await pick(`forget ${m.name}?`, [{ label: "keep it" }, { label: "forget" }]) === "forget") ask("forget", { machine: m.id }).catch(error => showError(error.message));
}
// Go to, as the apps' Ctrl+K: Home, the conversations open, then the threads that are not.
function goTo() {
  pick("go to", [{ label: "home", value: () => focus("") }, ...state.tabs.map(c => ({ label: named(c.title), detail: c.where, value: () => focus(c.id) })),
    ...home.entries.filter(e => !e.open && (e.machine !== state.self || !launchers().length)).map(e => ({ label: named(e.title), detail: machine(e.machine)?.name, value: () => open(e.key) }))]).then(go => go?.());
}

// pick is the apps' picker: a sheet of choices, typed to filter, arrows and Enter, a click outside
// or Esc to leave; with field, it takes a line instead. It resolves to the choice's value (its
// label when it has none), the line, or null.
const sheet = $("sheet");
function pick(title, options, { field = false, value = "", hint = "" } = {}) {
  return new Promise(resolve => {
    let at = Math.max(0, options.findIndex(o => o.on)), shown = [];
    const input = $("sheet-input");
    const done = v => { sheet.onclose = null; sheet.close(); resolve(v); };
    const draw = () => {
      const q = input.value.trim().toLowerCase();
      shown = field ? [] : options.filter(o => `${o.label} ${o.detail || ""}`.toLowerCase().includes(q));
      at = Math.max(0, Math.min(at, shown.length - 1));
      $("sheet-list").replaceChildren(...shown.map((o, i) => {
        const b = button("", () => done(o.value ?? o.label), i === at ? "on" : "");
        b.append(node("span", o.label), ...(o.detail ? [node("small", o.detail)] : []));
        return b;
      }));
    };
    $("sheet-title").textContent = title;
    Object.assign(input, { value, placeholder: hint || (field ? "" : "type to filter"), hidden: !field && options.length < 8 });
    input.oninput = () => { at = 0; draw(); };
    sheet.onkeydown = event => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") { event.preventDefault(); at += event.key === "ArrowDown" ? 1 : -1; draw(); }
      else if (event.key === "Enter") { event.preventDefault(); field ? done(input.value) : shown[at] && done(shown[at].value ?? shown[at].label); }
    };
    // The close event comes after its close(): a sheet opened from a choice of the last one is open again.
    sheet.onclose = () => !sheet.open && resolve(null);
    draw();
    sheet.showModal();
    // The keys go to the sheet, whose rows are drawn again as they move.
    (input.hidden ? sheet : input).focus();
  });
}
// A dialog closes on a click outside it, as the apps' sheets do.
for (const dialog of document.querySelectorAll("dialog")) dialog.addEventListener("click", event => {
  const r = dialog.getBoundingClientRect();
  if (event.target === dialog && (event.clientX < r.left || event.clientX > r.right || event.clientY < r.top || event.clientY > r.bottom)) dialog.close();
});
$("viewer").addEventListener("click", () => $("viewer").close());

// The conversation, as the view folds it into rows: what the person said sits at right on a soft
// ground; Orb just speaks, full width, its actions inline. A row is drawn again only when it
// changed, and the browser lays out only what is on screen (CSS).
let drawn = { tab: "", rows: new Map() }, pinned = true;
function renderChat(t) {
  if (drawn.tab !== t.id) { drawn = { tab: t.id, rows: new Map() }; pinned = true; }
  const list = rows.get(t.id) || [], next = new Map();
  const nodes = list.map(r => {
    const old = drawn.rows.get(r.k), element = old?.row === r ? old.element : drawRow(r, t, old?.element);
    next.set(r.k, { row: r, element });
    return element;
  });
  drawn.rows = next;
  const box = $("messages");
  if (nodes.length !== box.childElementCount || nodes.some((n, i) => box.children[i] !== n)) box.replaceChildren(...nodes);
  if (keep && list.length) { $("transcript").scrollTop = $("transcript").scrollHeight - keep; keep = 0; }
  $("earlier").hidden = !t.earlier;
  $("empty").hidden = list.length > 0;
  $("empty").textContent = t.asleep ? "asleep · a message wakes it" : t.loaded ? "" : "loading…";
  $("strip").replaceChildren(node("span", [t.remote && t.where, word(t)].filter(Boolean).join(" · ")),
    ...(t.usage?.windows?.length ? [button(usage(t.usage), () => plan(t), "plain")] : []), ...(t.cost > 0 ? [node("span", `$${t.cost.toFixed(2)}`)] : []));
  $("strip").classList.toggle("busy", !!t.busy);
  $("caret").hidden = !t.streaming;
  $("note").textContent = t.status || "";
  // What its plugins show beside it: the task list, a status, the last notice.
  $("slots").replaceChildren(...(t.slots || []).map(s => el("pre", s.alarm ? "alarm" : "", s.lines.join("\n"))));
  if (pinned) end();
}
// 活動限界, the activity limit an EVA's battery counts down to: the plan's window nearest its end.
function usage({ windows }) {
  const w = [...windows].sort((x, y) => x.left - y.left)[0];
  return w && `活動限界 ${w.name} ${Math.round(w.left)}%${w.resets ? ` · ${new Date(w.resets).toTimeString().slice(0, 5)}` : ""}`;
}
// The plan's windows: what is left of each and when it resets, as its Orb last read them.
function plan(t) {
  const u = t.usage;
  pick([t.model?.split("/")[0], u.plan, `read ${ago(u.at)} ago`].filter(Boolean).join(" · "),
    u.windows.map(w => ({ label: `${w.name} · ${Math.round(w.left)}% left`, detail: w.resets ? `resets ${new Date(w.resets).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" })}` : "" })));
}
// The reply is followed while the reader stays at its end: scrolling up lets go, and coming back
// to the end takes hold again.
const end = () => requestAnimationFrame(() => ($("transcript").scrollTop = $("transcript").scrollHeight));
let lastTop = 0;
$("transcript").addEventListener("wheel", event => { if (event.deltaY < 0) pinned = false; }, { passive: true });
$("transcript").addEventListener("touchmove", () => (pinned = false), { passive: true });
$("transcript").addEventListener("keydown", event => { if (["ArrowUp", "PageUp", "Home"].includes(event.key)) pinned = false; });
$("transcript").addEventListener("scroll", ({ target: t }) => {
  if (t.scrollTop > lastTop && t.scrollHeight - t.scrollTop - t.clientHeight < 4) pinned = true;
  lastTop = t.scrollTop;
}, { passive: true });
// Earlier messages load above; the ones read so far stay where they are.
let keep = 0;
$("earlier").onclick = () => { pinned = false; keep = $("transcript").scrollHeight - $("transcript").scrollTop; send({ do: "earlier", tab: shown }); };

function drawRow(r, t, old) {
  if (r.kind === "you") {
    return el("div", "you", ...(r.via ? [el("small", "via", "from a peer")] : []), ...pictures(r.images, t), ...(r.text?.trim() ? [el("p", "", r.text)] : []));
  }
  if (r.kind === "md") return el("div", "said", ...blocks([r.block]));
  if (r.kind === "note") return el("p", r.alarm ? "note alarm" : "note", r.text);
  // A run of actions folds into one line, as the TUI folds exploration, naming what runs now.
  const one = r.actions?.length === 1, line = one ? action(r.actions[0], t) : el("details", "action run");
  // What the reader opened stays open as the run goes on: the run, and each action by its key.
  const opened = !!old?.querySelector("details")?.open, kept = new Set([...(old?.querySelectorAll("details[open]") ?? [])].map(d => d.dataset.k));
  if (one) line.firstChild.open = opened;
  else {
    line.open = opened;
    line.append(summary(r.live ? "working" : "worked", [r.what, !line.open && r.now].filter(Boolean).join(" · "), r.failed ? `${r.failed} failed` : "", r.live, r.failed > 0),
      ...r.actions.map(a => { const row = action(a, t); row.firstChild.open = kept.has(a.k); return row; }));
  }
  return el("div", "said", line, ...pictures(r.images, t));
}
function summary(verb, target, result, live, failed) {
  return node("summary", el("b", live ? "live" : "", verb), node("span", target), ...(result ? [el("em", failed ? "failed" : "", result)] : []));
}
// A thought or a tool call: one line that opens onto what it thought, or what it was given and
// returned, read from the view when it opens.
function action(a, t) {
  const details = el("details", "action", summary(a.verb, a.target, a.result, a.live, a.failed));
  details.dataset.k = a.k;
  details.ontoggle = () => details.open && ask("detail", { tab: t.id, key: a.k }).then(d => {
    const pane = el("div", "pane");
    if (a.verb === "thought") pane.append(el("pre", "", d?.text || ""));
    else if (d?.diff) pane.append(el("pre", "", ...d.diff.trimEnd().split("\n").map(l => el("span", l[0] === "+" ? "add" : l[0] === "-" ? "del" : "", `${l}\n`))));
    else pane.append(...(d?.args?.trim() ? [el("small", "", "input"), el("pre", "", d.args)] : []), el("small", "", "output"), el("pre", a.failed ? "alarm" : "", d?.output ?? "…"));
    details.querySelector(".pane")?.remove();
    details.append(pane);
  }, () => {});
  return el("div", "", details);
}
// Thumbnails, fetched small from the Orb that holds them; a click opens one whole.
function pictures(images = [], t) {
  if (!images?.length) return [];
  return [el("div", "pictures", ...images.map(ref => {
    const img = Object.assign(node("img"), { alt: "", onclick: () => ask("image", { tab: t.id, ref, px: 0 }).then(r => { $("picture").src = `data:image/png;base64,${r.data}`; $("viewer").showModal(); }) });
    ask("image", { tab: t.id, ref, px: 384 }).then(r => (img.src = `data:image/png;base64,${r.data}`), () => {});
    return img;
  }))];
}
// The blocks the view parsed of what Orb wrote, drawn as elements, never as HTML.
function blocks(list) {
  return list.map(b => {
    const indent = b.depth ? { style: `margin-left: ${b.depth * 2}ch` } : {};
    switch (b.type) {
      case "h": return Object.assign(node(b.level <= 2 ? "h3" : "h4", ...spans(b.spans)), indent);
      case "li": return Object.assign(el("div", "li", el("span", "mark", b.mark), node("span", ...spans(b.spans))), indent);
      case "quote": return Object.assign(node("blockquote", ...blocks(b.blocks || [])), indent);
      case "code": case "art": {
        const copy = button("copy", () => navigator.clipboard.writeText(b.text).then(() => { copy.textContent = "copied"; setTimeout(() => (copy.textContent = "copy"), 1200); }), "copy");
        return Object.assign(el("pre", b.type, copy, ...(b.lang ? [el("small", "", b.lang)] : []), b.text), indent);
      }
      case "table": return Object.assign(el("div", "table", node("table", ...(b.rows || []).map((r, i) => node("tr", ...(r || []).map(cell => node(i ? "td" : "th", ...spans(cell))))))), indent);
      case "rule": return node("hr");
      default: return Object.assign(node("p", ...spans(b.spans)), indent);
    }
  });
}
function spans(list) {
  return (list || []).map(s => {
    let n = s.t;
    if (s.c) n = node("code", n);
    if (s.b) n = node("strong", n);
    if (s.i) n = node("em", n);
    if (s.s) n = node("s", n);
    if (s.h) n = Object.assign(node("a", n), { href: s.h, target: "_blank", rel: "noreferrer" });
    return n;
  });
}

// The prompt box, as the apps': on Home it starts a conversation where its chip says; in one, it
// sends, and while a run goes it steers it (Enter) or queues for after it, or stops it.
function controls() {
  const t = conversation(), text = $("prompt").value.trim(), { machine: m, cwd } = aim();
  $("send").ariaLabel = t?.busy && !text ? "Stop" : t?.busy ? "Steer" : "Send";
  $("send").classList.toggle("stop", !!t?.busy && !text);
  $("send").disabled = !text && !t?.busy;
  $("queue").hidden = !(t?.busy && text);
  $("prompt").placeholder = t ? (t.busy ? "steer or queue a message" : "What next?") : "What should we work on?";
  // Where it runs is chosen on Home; a conversation stays where it started.
  $("aim").disabled = !!t;
  $("aim").lastChild.textContent = t ? (t.remote ? t.where : "this tab") : m ? `${m.self ? "this tab" : m.name}${cwd ? ` · ${base(cwd)}` : ""} ▾` : "no device";
  $("aim").firstChild.style = `--hue: var(--hue${(t ? machine(t.peer) : m)?.hue || 0})`;
  // On Home, what the device's next conversation starts on: its default, read once and on change.
  const fresh = !t && m ? defaultsOf.get(m.id) : null;
  if (!t && m && !defaultsOf.has(m.id)) {
    defaultsOf.set(m.id, null);
    if (m.self) loadTabDefaults().then(() => controls(), () => defaultsOf.delete(m.id));
    else ask("models", { machine: m.id }).then(d => { defaultsOf.set(m.id, d); controls(); }, () => defaultsOf.delete(m.id));
  }
  const on = t || (fresh && { model: fresh.default, levels: fresh.levels, thinking: fresh.thinking });
  $("where").textContent = `${on?.model?.split("/").slice(1).join("/") || (t?.loaded && !t.asleep && !t.models?.length ? "connect a provider" : m?.self && !t ? "own model" : "model")} ▾`;
  const shownLevels = (on?.levels || []).filter(l => l !== "off"), lit = shownLevels.indexOf(on?.thinking) + 1;
  $("meter").replaceChildren(...shownLevels.map((_, i) => el("i", i < lit ? "lit" : "")));
  palette();
}
// Home takes the app's commands that open a screen, and /new; a conversation takes them all.
const homeCommands = new Set(["new", "model", "sessions", "pair", "login", "plugins"]);
let past = [];
async function submit(mode) {
  const t = conversation(), text = $("prompt").value.trim();
  if (t?.busy && !text) return send({ do: "abort", tab: t.id });
  if (!text) return;
  $("prompt").value = "";
  pinned = true;
  showError("");
  past = [...past.filter(h => h !== text), text].slice(-50);
  controls();
  if (window.Notification?.permission === "default") Notification.requestPermission();
  // Refused, the text comes back to the box, unless a new draft took its place.
  const unsent = error => { if (!$("prompt").value) $("prompt").value = text; showError(error.message); controls(); };
  const name = /^\/(\S+)/.exec(text)?.[1];
  try {
    // During a run the text steers it, but the app's own commands still open their screen.
    if (t?.busy && mode !== "queue" && !state.commands?.some(c => c.name === name)) return send({ do: "steer", tab: t.id, text });
    if (!t && name && name !== "new" && homeCommands.has(name)) return outcome(await ask("send", { text }));
    if (!t) return await start(name === "new" ? text.slice(4).trim() : text);
    outcome(await ask("send", { tab: t.id, text }));
  } catch (error) { unsent(error); }
}
// What only the page can do comes back from the view: a screen to open, or text to copy.
function outcome(r) {
  if (r?.copy) navigator.clipboard.writeText(r.copy).then(() => toast("copied the last answer"));
  if (r?.nav) go(r.nav);
}
$("prompt-form").onsubmit = event => { event.preventDefault(); submit(); };
$("queue").onclick = () => submit("queue");
$("aim").onclick = chooseWhere;
$("unblue").onclick = () => shown && send({ do: "abort", tab: shown });
function defaultPeer() {
  return conversation()?.peer || aim().machine?.id || state.self;
}
function go(nav) {
  if (nav === "model") openModels();
  else if (nav === "home") focus("");
  else openPage({ providers: "providers", plugins: "plugins", pair: "mitosis" }[nav] || "general", defaultPeer());
}
$("prompt").oninput = controls;
$("prompt").onkeyup = event => ["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key) && palette();
$("prompt").onkeydown = event => {
  const box = $("prompt");
  if (choices.length) {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") { event.preventDefault(); chosen = (chosen + (event.key === "ArrowDown" ? 1 : -1) + choices.length) % choices.length; return drawPalette(); }
    if (event.key === "Tab" || event.key === "Enter" && !event.shiftKey && chosen >= 0) { event.preventDefault(); return choices[Math.max(chosen, 0)].pick(); }
    if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); dismissed = box.value; return palette(); }
  }
  // Up in an empty box brings back what was sent last, as a terminal does.
  if (event.key === "ArrowUp" && !box.value && past.length) { event.preventDefault(); box.value = past.at(-1); return controls(); }
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) { event.preventDefault(); submit(); }
};
// The palette over the box: typing / lists the app's commands and the conversation's Orb's,
// prefix matches first; an @ token asks that Orb what it completes to, skills then files, as the
// TUI does. Arrows choose, Tab or Enter takes, Esc puts it away until the text changes.
let choices = [], chosen = -1, dismissed = null, found = { tab: null, token: null, items: [] }, finding = 0;
function palette() {
  const box = $("prompt"), text = box.value, t = conversation(), cursor = box.selectionStart;
  const typed = /^\/(\S*)$/.exec(text)?.[1], token = /(?:^|\s)(@\S*)$/.exec(text.slice(0, cursor))?.[1];
  const before = choices.map(c => c.label).join();
  choices = [];
  if (text === dismissed) return drawPalette();
  dismissed = null;
  if (typed !== undefined) {
    const all = t ? [...state.commands || [], ...t.commands || []] : (state.commands || []).filter(c => homeCommands.has(c.name));
    choices = [...all.filter(c => c.name.startsWith(typed)), ...all.filter(c => !c.name.startsWith(typed) && c.name.includes(typed))].slice(0, 30).map(c => ({
      label: `/${c.name}`, detail: c.hint, pick: () => {
        box.value = `/${c.name}${c.now ? "" : " "}`;
        if (c.now) submit();
        else { box.focus(); controls(); }
      },
    }));
  } else if (token && t) {
    if (found.token !== token || found.tab !== t.id) {
      clearTimeout(finding);
      finding = setTimeout(() => ask("complete", { tab: t.id, text: token.slice(1) }).then(items => { found = { tab: t.id, token, items: items || [] }; palette(); }, () => {}), 120);
    } else choices = found.items.map(f => ({
      label: f.text.startsWith("/skill:") ? `◆ ${f.label}` : f.label, detail: f.detail, pick: () => {
        // A folder keeps the token open, so completion goes on inside it.
        const end = cursor + Math.max(0, text.slice(cursor).search(/\s|$/)), gap = /\/"?$/.test(f.text) ? "" : " ";
        box.value = text.slice(0, cursor - token.length) + f.text + gap + text.slice(end);
        const caret = cursor - token.length + f.text.length + gap.length - (f.text.endsWith('/"') ? 1 : 0);
        box.setSelectionRange(caret, caret);
        box.focus();
        controls();
      },
    }));
  }
  if (choices.map(c => c.label).join() !== before) chosen = typed !== undefined && choices.length ? 0 : -1;
  drawPalette();
}
function drawPalette() {
  $("palette").hidden = !choices.length;
  $("palette").replaceChildren(...choices.map((c, i) => {
    const b = button("", c.pick, i === chosen ? "on" : "");
    b.onmousedown = event => event.preventDefault(); // the box keeps the focus
    b.append(node("b", c.label), node("span", c.detail || ""));
    return b;
  }));
  $("palette").querySelector(".on")?.scrollIntoView({ block: "nearest" });
}

// The model deck, as the apps': the model and its reasoning, every model the Orb running the
// conversation has, those in use first, then by provider, said with how it is paid (a subscription,
// an API key), its own provider first; the accounts of the model's provider there. On Home it
// chooses the default a device's new conversations start on, and the device.
const deck = $("models"), accountsOf = new Map(), defaultsOf = new Map();
let deckAt = -1;
deck.addEventListener("close", () => $("prompt").focus({ preventScroll: true }));
$("model").onclick = openModels;
$("filter").oninput = () => { deckAt = $("filter").value.trim() ? 0 : -1; drawDeck(); };
deck.addEventListener("keydown", event => {
  const buttons = [...$("list").querySelectorAll("button[data-model]")];
  if (!buttons.length) return;
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    deckAt = deckAt < 0 ? (event.key === "ArrowDown" ? 0 : buttons.length - 1) : (deckAt + (event.key === "ArrowDown" ? 1 : -1) + buttons.length) % buttons.length;
    buttons.forEach((b, i) => b.classList.toggle("hi", i === deckAt));
    buttons[deckAt]?.scrollIntoView({ block: "nearest" });
  } else if (event.key === "Enter" && deckAt >= 0 && buttons[deckAt]) {
    event.preventDefault();
    buttons[deckAt].click();
  }
});
async function loadTabDefaults() {
  if (!tab) return null;
  const [d, accts] = await Promise.all([tab.models(), tab.accounts().catch(() => [])]);
  // A session created before synced credentials arrived sits on unknown/unknown: replace it once a model exists.
  if (d?.models?.length) {
    const st = await tab.rpc("get_state").catch(() => null);
    if (!st?.model || st.model.provider === "unknown") await tab.rpc("new_session").catch(() => {});
  }
  defaultsOf.set(state.self, d || { models: [] });
  accountsOf.set(state.self, accts || []);
  return d;
}
function openModels() {
  if (!deck.open) { $("filter").value = ""; deckAt = -1; }
  const t = conversation(), peer = t ? t.peer : aim().machine?.id;
  drawDeck();
  if (!deck.open) deck.showModal();
  if (!peer) return;
  const refresh = () => { if (deck.open) drawDeck(); controls(); };
  if (peer === state.self) {
    loadTabDefaults().then(refresh, () => {});
    return;
  }
  // Load models, providers and accounts independently so slow quota fetches never hold up the deck.
  ask("models", { machine: peer }).then(d => { if (d) defaultsOf.set(peer, d); refresh(); }, () => {});
  ask("providers", { machine: peer }).then(p => { if (p) listed.set(peer, p); refresh(); }, () => {});
  ask("accounts", { machine: peer }).then(a => { if (a) accountsOf.set(peer, a); refresh(); }, () => {});
}
const REASONING = { off: "no thinking", minimal: "barely", low: "light", medium: "balanced", high: "thorough", xhigh: "very thorough", max: "as long as it takes" };
function drawDeck() {
  const t = conversation(), m = t ? machine(t.peer) : aim().machine, peer = m?.id, def = defaultsOf.get(peer), query = $("filter").value.trim().toLowerCase();
  const s = t || (def && { model: def.default, models: def.models, levels: def.levels, thinking: def.thinking }) || { models: [] };
  const where = m ? (m.self ? "this tab" : m.name) : "";
  const fail = error => showError(error.message);
  const setDefault = (name, level = "") => {
    if (peer === state.self) {
      const [provider, ...rest] = name.split("/");
      return tab.models(provider, rest.join("/"), level)
        .then(d => { if (d) defaultsOf.set(state.self, d); if (deck.open) drawDeck(); controls(); }, fail);
    }
    return ask("default", { machine: peer, name, level }).then(() =>
      ask("models", { machine: peer }).then(d => { if (d) defaultsOf.set(peer, d); if (deck.open) drawDeck(); controls(); }, () => defaultsOf.delete(peer)), fail);
  };
  const choose = id => { t ? send({ do: "model", tab: t.id, name: id }) : setDefault(id); deck.close(); };
  $("deck-head").replaceChildren(node("div", el("p", "label", t ? "model" : `default · ${where}`), node("strong", s.model?.split("/").slice(1).join("/") || "none")),
    t && def?.default && t.model && t.model !== def.default ? button("make default", () => setDefault(t.model, t.thinking), "plain") : note([s.model?.split("/")[0], t && where].filter(Boolean).join(" · ")));
  const places = [...launchers(), ...(machine(state.self) ? [machine(state.self)] : [])];
  $("places").hidden = !!t || places.length < 2;
  $("places").replaceChildren(...(t ? [] : places).map(d => button(d.self ? "this tab" : d.name, () => { aimAt(d.id); openModels(); }, d.id === peer ? "on" : "")));
  const providers = listed.get(peer) || [], info = id => providers.find(p => p.id === id);
  const match = id => !query || `${id} ${info(id.split("/")[0])?.name || ""}`.toLowerCase().includes(query);
  const models = (s.models || []).filter(match), mine = s.model?.split("/")[0];
  const used = [...new Set([def?.default, ...state.tabs.filter(c => c.peer === peer).map(c => c.model)])].filter(id => id && models.includes(id));
  const groups = Map.groupBy(models, id => id.split("/")[0]);
  // Its own provider first, then those a subscription pays for, then the rest by name.
  const order = [...groups.keys()].sort((x, y) => (y === mine) - (x === mine) || !!info(y)?.methods?.some(a => a.account) - !!info(x)?.methods?.some(a => a.account) || x.localeCompare(y));
  let idx = 0;
  const line = (id, full) => {
    const i = idx++;
    const b = button("", () => choose(id), `${id === s.model ? "on" : ""}${i === deckAt ? " hi" : ""}`.trim());
    b.dataset.model = id;
    b.append(node("span", full ? id : id.split("/").slice(1).join("/")), ...(id === def?.default ? [el("small", "", "default")] : []), ...(id === s.model ? [el("i", "")] : []));
    return b;
  };
  $("list").replaceChildren(...(used.length ? [el("p", "label", "in use"), ...used.map(id => line(id, true))] : []),
    ...order.flatMap(p => [el("p", "label", [info(p)?.name || p, info(p)?.holds, `${groups.get(p).length}`].filter(Boolean).join(" · ")), ...groups.get(p).map(id => line(id))]),
    ...(!models.length ? [note(query ? "no model matches" : m && !def && !t ? "reading its models…" : "no models")] : []),
    ...(peer && peer !== state.self ? [button("+ connect a provider", () => { deck.close(); openPage("providers", peer); }, "plain add")] : []));
  // The accounts of the model's provider on that device: the one in use marked, the others a click away.
  const accounts = (accountsOf.get(peer) || []).filter(a => a.provider === mine);
  const switchAccount = a => {
    if (peer === state.self) {
      return tab.accounts(a.provider, a.id).then(list => { accountsOf.set(state.self, list || []); if (deck.open) drawDeck(); controls(); }, fail);
    }
    return ask("account", { machine: peer, provider: a.provider, account: a.id }).then(() =>
      ask("accounts", { machine: peer }).then(list => { if (list) accountsOf.set(peer, list); if (deck.open) drawDeck(); controls(); }, () => {}), fail);
  };
  $("accounts").replaceChildren(...(accounts.length ? [el("p", "label", `account · ${where}`), ...accounts.map(a => {
    const w = [...(a.windows || [])].sort((x, y) => x.left - y.left)[0], b = button("", () => switchAccount(a), a.active ? "on" : "");
    b.append(node("span", a.name), ...(w ? [el("small", w.left < 15 ? "alarm" : "", `${w.name} ${Math.round(w.left)}% left`)] : []));
    return b;
  })] : []));
  // Reasoning, what changes most often, last: the levels this model takes, the one chosen in words.
  const levels = s.levels || [];
  $("levels").replaceChildren(...(levels.length ? [node("p", el("span", "label", "reasoning"), el("small", "", REASONING[s.thinking] || s.thinking || "")),
    el("div", "levels", ...levels.map(l => button(l.replace("minimal", "min").replace("medium", "med"), () => t ? send({ do: "thinking", tab: t.id, name: l }) : setDefault(s.model, l), l === s.thinking ? "on" : "")))] : []));
}

// A question a conversation waits on stops the world, as in the apps (platforms/desktop's
// interrupt, Android's Interrupt): the red fills the page between warning stripes, the word wipes
// in, and each choice has its key. The view goes through a series one question at a time. Keys
// answer once it has been on screen a moment, so typing meant for the prompt cannot.
function interrupt(t, ask) {
  const [word, caption] = ask.owner === "permissions" ? ["APPROVAL", "approval required"] : ["QUESTION", "a question for you"];
  const [first, ...rest] = ask.title.split("\n"), detail = [...rest, ask.message || ""].join("\n").trim();
  const keyed = (ask.choices || []).map((choice, i) => {
    const own = /^([a-z]) (.+)/.exec(choice);
    return [own?.[1] ?? ({ allow: "y", deny: "n" }[choice] || (i < 9 ? String(i + 1) : "")), own?.[2] ?? choice, choice];
  });
  let box = document.querySelector(".interrupt:not(.leaving)");
  const again = !!box;
  if (!box) box = document.body.appendChild(el("div", "interrupt"));
  Object.assign(box.dataset, { ask: ask.id, armed: Date.now() + 800 });
  box.classList.toggle("again", again);
  const field = Object.assign(node("input"), { placeholder: "type an answer", autocomplete: "off", enterKeyHint: "send" });
  const answer = (value, ticked) => { leave(); send({ do: "answer", tab: t.id, value, ticked }); };
  // With several to choose, a choice ticks; send answers with what is ticked.
  const ticked = new Set(), choose = (choice, row) => {
    if (!ask.multi) return answer(choice);
    ticked.has(choice) ? ticked.delete(choice) : ticked.add(choice);
    row.classList.toggle("ticked", ticked.has(choice));
  };
  const keys = event => {
    if (event.key === "Escape") return answer(null);
    const hit = event.target !== field && Date.now() > box.dataset.armed && keyed.find(([key]) => key === event.key);
    if (hit) { event.preventDefault(); choose(hit[2], box.querySelectorAll(".choice")[keyed.indexOf(hit)]); }
  };
  const submit = () => ask.multi ? (ticked.size || field.value.trim()) && answer(field.value.trim(), [...ticked]) : field.value.trim() && answer(field.value);
  field.onkeydown = event => event.key === "Enter" && submit();
  box.replaceChildren(node("i"), node("div", node("div", node("span", `${ask.owner} · ${t.where}`), node("span", "waiting on you")), node("hr"),
    el("h2", word.length > 5 ? "stretch word squeezed" : "stretch word", word), el("p", "caption", caption),
    el("p", "first", first), ...(detail ? [el("p", "detail", detail)] : []),
    node("hr"), ...keyed.map(([key, text, choice], n) => {
      const pick = Object.assign(button("", () => choose(choice, pick), n || ask.multi ? "choice" : "choice first"), { style: `--n: ${n}` });
      pick.append(node("b", key), node("span", text));
      return pick;
    }),
    ...(ask.free ? [node("div", field, button("send", submit, "inverted"))] : []),
    node("hr"), button(matchMedia("(hover: hover)").matches ? "esc  ·  dismiss" : "dismiss", () => answer(null))), node("i"));
  removeEventListener("keydown", box.keys);
  addEventListener("keydown", box.keys = keys);
  if (ask.free && !keyed.length) field.focus();
}
// leave sinks the interrupt away, as Android's does.
function leave() {
  const box = document.querySelector(".interrupt:not(.leaving)");
  removeEventListener("keydown", box?.keys);
  box?.classList.add("leaving");
  setTimeout(() => box?.remove(), 260);
}

$("orb").onclick = $("new").onclick = () => focus("");
$("menu").onclick = () => openPage("general", defaultPeer());
$("back").onclick = () => { show("app"); render(); };
for (const item of $("pages").querySelectorAll("[data-page]")) item.onclick = () => openPage(item.dataset.page, page.peer);
// The apps' keys, on Alt where the browser keeps Ctrl for itself (the physical key, so a Mac's
// Option letters still work).
document.onkeydown = event => {
  if (!tab || document.querySelector("dialog[open]")) return;
  const code = event.code, alt = event.altKey && !event.ctrlKey && !event.metaKey, mod = (event.ctrlKey || event.metaKey) && !event.altKey;
  const keys = {
    Comma: mod && (() => openPage("general", defaultPeer())), KeyK: mod && goTo, KeyN: alt && (() => focus("")), KeyW: alt && shown && (() => closeTab(conversation())),
    KeyT: alt && event.shiftKey && (() => reopen.pop()?.()), KeyM: alt && openModels, KeyL: alt && (() => $("prompt").focus()),
    Period: alt && shown && (() => send({ do: "abort", tab: shown })), BracketRight: alt && (() => step(1)), BracketLeft: alt && (() => step(-1)),
  };
  const digit = alt && /^Digit([1-9])$/.exec(code)?.[1];
  const act = keys[code] || (digit && (() => { const c = digit === "9" ? state.tabs.at(-1) : state.tabs[digit - 1]; if (c) focus(c.id); }));
  if (act && !$("app").hidden) { event.preventDefault(); return act(); }
  if (event.key !== "Escape") return;
  if (!$("settings").hidden) $("back").onclick();
  else if (shown && !document.querySelector(".interrupt")) focus("");
};
// Home and the open conversations in a ring, as the apps' tab switching.
function step(by) {
  const ids = ["", ...state.tabs.map(c => c.id)], at = ids.indexOf(shown);
  focus(ids[(at + by + ids.length) % ids.length]);
}
document.onvisibilitychange = () => send({ do: "visible", on: !document.hidden });

// Settings, as the desktop's: its pages at left, the one shown at right. Providers and Plugins are
// a device's, as on Android: this tab's own (the accounts its devices share with it, the plugins its
// Orb runs), or another device's, read and changed there.
const label = text => el("p", "label", text);
const note = text => el("p", "meta", text);
function row(name, sub, ...actions) {
  return el("div", "row", node("div", node("strong", name), note(sub)), ...actions);
}
// The devices a page can be about: connected devices first, then this tab.
function devices(name, peer) {
  const list = [...home.machines.filter(m => !m.self && m.connected), ...home.machines.filter(m => m.self)];
  return el("div", "chips", ...list.map(m => button(m.self ? "this tab" : m.name, () => openPage(name, m.id), m.id === peer ? "on" : "")));
}
// What each plugin does, as agent/assembly's catalog says it.
const about = {
  titles: "Name each session after its first exchange", tasks: "Live session task list and todo tool",
  questions: "Ask the user questions with choices and custom answers", memory: "Bounded persistent remember, recall, replace, and forget tools",
  memtree: "Experimental: zoomable summary tree of each session for lossless compaction or fresh turns",
};
const listed = new Map(); // each device's providers, as it last listed them
const retry = (name, peer, vendor) => () => openPage(name, peer, vendor);
const shortPeer = id => id?.split(":").at(-1)?.slice(0, 8) || id;
function renderRemoteProviders(m, providers, accounts, loadingAccounts) {
  const peer = m.id;
  const section = (name, list) => list.length ? [label(name), ...list.map(p => Object.assign(row(p.name, p.ready ? p.holds : p.methods.map(x => (x.account ? "account" : "api key")).join(" · "),
    button(p.ready ? `${p.models} model${p.models === 1 ? "" : "s"} ›` : p.methods.some(x => x.account) ? "sign in ›" : "add key ›", () => openPage("providers", peer, p.id), "plain")), { className: p.ready ? "row on" : "row" }))] : [];
  const ready = (providers || []).filter(p => p.ready), rest = (providers || []).filter(p => !p.ready);
  return [node("h2", "Providers"), devices("providers", peer),
    note(providers ? `${m.name} · ${ready.length} ready · ${providers.length} to choose from` : `${m.name} · loading…`),
    ...((accounts || []).length ? [label("accounts · quota left"), ...accounts.map(a => el("div", "account", row(a.name, [a.provider_name?.split(" (")[0], a.plan].filter(Boolean).join(" · "),
      button(a.active ? "in use" : "use ›", () => ask("account", { machine: peer, provider: a.provider, account: a.id }).then(() => { accountsOf.delete(peer); openPage("providers", peer); }, error => showError(error.message)), a.active ? "plain on" : "plain")),
      ...(a.windows || []).map(quota)))] : loadingAccounts ? [label("accounts"), note("reading accounts and quotas…")] : []),
    ...section("ready", ready), ...section("your subscription", rest.filter(p => p.methods.some(x => x.account))), ...section("api key", rest.filter(p => !p.methods.some(x => x.account)))];
}
const pages = {
  general() {
    // A phone has no keys to list.
    const keys = matchMedia("(hover: hover)").matches;
    return [node("h2", "General"), note("this tab, one of your devices"), ...(keys ? [label("keys"), ...[["Enter", "send · steer a run"], ["Shift+Enter", "new line"], ["Tab", "complete a / command or an @ file"],
      ["↑", "the last message again"], ["Ctrl+K", "go to a conversation"], ["Alt+N", "Home"], ["Alt+1…9", "a conversation"], ["Alt+[  Alt+]", "the previous, the next"],
      ["Alt+W", "close the conversation"], ["Alt+Shift+T", "reopen it"], ["Alt+M", "models"], ["Alt+.", "stop"], ["Ctrl+,", "settings"], ["Esc", "back to Home"]].map(([key, what]) => row(key, what))] : [])];
  },
  async providers(peer, vendor, seq) {
    const m = machine(peer);
    if (m?.self || !m) {
      const [rawAccounts, avail] = await Promise.all([tab.accounts().catch(() => []), tab.rpc("get_available_models")]);
      const accounts = [...(rawAccounts || [])].sort((a, b) => (a.provider_name || a.provider).localeCompare(b.provider_name || b.provider) || Number(b.active) - Number(a.active) || a.name.localeCompare(b.name));
      accountsOf.set(state.self, accounts);
      const counts = new Map();
      for (const model of avail.models || []) counts.set(model.provider, (counts.get(model.provider) || 0) + 1);
      const listedProviders = new Set(accounts.map(a => a.provider));
      const extraProviders = [...counts].filter(([p]) => !listedProviders.has(p));
      const use = a => tab.accounts(a.provider, a.id).then(list => { accountsOf.set(state.self, list || []); openPage("providers", peer); }, error => showError(error.message));
      const connected = home.machines.filter(d => !d.self && d.connected);
      return [node("h2", "Providers"), devices("providers", peer), note("accounts synced to this tab"),
        ...(accounts.length ? [label("accounts"), ...accounts.map(a => row(a.name, [a.provider_name?.split(" (")[0] || a.provider, a.auth === "oauth" ? "account" : "api key", counts.get(a.provider) ? `${counts.get(a.provider)} model${counts.get(a.provider) === 1 ? "" : "s"}` : ""].filter(Boolean).join(" · "),
          button(a.active ? "in use" : "use ›", () => !a.active && use(a), a.active ? "plain on" : "plain")))] : [label("accounts"), note("no shared accounts yet")]),
        ...(extraProviders.length ? [label("other providers"), ...extraProviders.map(([p, n]) => row(p, `${n} model${n === 1 ? "" : "s"}`))] : []),
        note(connected.length ? "Shared accounts sync from your devices. Local-only accounts (such as Claude Code) run on the device that holds them — pick that device above to manage them." : "Sign in on one of your devices to share accounts with this tab.")];
    }
    if (vendor) return provider(m, vendor);
    // Draw cached providers immediately, then update independently as providers and accounts (quota
    // fetches) finish, so slow quota endpoints never stall the page.
    let providers = listed.get(peer) || null, accounts = accountsOf.get(peer) || null, loadingAccounts = !accounts;
    const paint = () => {
      if (seq !== pageSeq || page.name !== "providers" || page.peer !== peer || page.vendor) return;
      $("page").replaceChildren(...renderRemoteProviders(m, providers, accounts, loadingAccounts), notice);
    };
    ask("providers", { machine: peer }).then(p => { providers = p || []; listed.set(peer, providers); paint(); }, error => { if (seq === pageSeq) showError(error.message); });
    ask("accounts", { machine: peer }).then(a => { accounts = a || []; accountsOf.set(peer, accounts); loadingAccounts = false; paint(); }, () => { loadingAccounts = false; paint(); });
    return renderRemoteProviders(m, providers, accounts, loadingAccounts);
  },
  async plugins(peer) {
    const m = machine(peer);
    if (m?.self || !m) {
      const plugins = await tab.plugins();
      return [node("h2", "Plugins"), devices("plugins", peer), note("what this tab's Orb runs, from its next conversation"), ...Object.keys(plugins).sort().map(name =>
        row(name, about[name], button(plugins[name] ? "on" : "off", () => tab.plugins(name, !plugins[name]).then(retry("plugins", peer), error => showError(error.message)), plugins[name] ? "inverted" : "")))];
    }
    const change = args => ask("plugin", { machine: peer, ...args }).then(retry("plugins", peer), error => showError(error.message));
    const list = await ask("plugins", { machine: peer }) || [];
    return [node("h2", "Plugins"), devices("plugins", peer), note(`${m.name} · ${list.filter(x => x.on).length} of ${list.length} on`), ...list.map(x => el("div", "plugin",
      row(x.name, x.about, button(x.on ? "on" : "off", () => change({ name: x.name, on: !x.on }), x.on ? "inverted" : "")),
      ...(x.on ? (x.choices || []).map(c => el("div", "chips", node("small", c.key), ...c.values.map(v => button(v, () => v !== c.value && change({ name: x.name, key: c.key, value: v }), v === c.value ? "on" : "")))) : []))),
      note("An Orb reads its plugins when it starts: those open here reopen with a change at their next message.")];
  },
  async mitosis(_peer, _vendor, seq) {
    const syncGroup = await tab.admin("sync").catch(() => null);
    const pairAnother = async () => {
      const text = (await pick("pair a device", [], { field: true, hint: "paste code from orb mitosis --browser" }))?.trim();
      if (!text) return;
      await join(text, true);
      if (seq === pageSeq) openPage("mitosis");
    };
    const forgetTab = async () => {
      if (await pick("forget this tab? its conversations here go with it", [{ label: "keep it" }, { label: "forget this tab" }]) === "forget this tab") {
        localStorage.setItem("orb.tab.forget", "1");
        location.reload();
      }
    };
    const syncNow = () => tab.admin("sync").then(() => { toast("synced"); openPage("mitosis"); }, error => showError(error.message));
    const members = syncGroup?.members || [], owners = new Set(syncGroup?.owners || []), names = syncGroup?.names || {};
    return [node("h2", "Mitosis"), note("peer to peer"),
      label("paired devices"),
      ...(home.machines.filter(m => !m.self).length ? home.machines.filter(m => !m.self).map(m => row(m.name, [m.connected ? "connected" : "offline", m.version && `orb ${m.version}`, m.launch ? "starts conversations" : "follows only"].filter(Boolean).join(" · "),
        button("rename", () => rename(m.name, name => { send({ do: "rename", machine: m.id, name }); setTimeout(retry("mitosis"), 300); }), "plain"))) : [note("no devices paired yet")]),
      el("div", "actions", button("+ pair a device", pairAnother)),
      ...(syncGroup?.group_id ? [label(`sync group · ${syncGroup.in_sync ? "in sync" : "syncing"} · ${members.length} device${members.length === 1 ? "" : "s"}`),
        ...members.map(id => {
          const m = machine(id);
          const display = id === me ? (names[id] || tabName()) : (m?.name || names[id] || shortPeer(id));
          const tags = [id === me && "this tab", owners.has(id) && "owner", m && !m.self && (m.connected ? "connected" : "offline")].filter(Boolean);
          return row(display, tags.join(" · ") || "sync member");
        }),
        el("div", "actions", button("sync now", syncNow))] : []),
      label("this tab"),
      row(shortPeer(me), me, button("copy", () => navigator.clipboard.writeText(me).then(() => toast("peer id copied")), "plain")),
      note("Each device is approved by fingerprint on its own screen. Forgetting this tab clears it here; block it on a device to take it out of your group."),
      el("div", "actions", button("forget this tab", forgetTab))];
  },
};
// One plan-limit window: the share left as a bar, in the rupture red once it runs low, and its reset.
function quota(w) {
  const low = w.left < 15;
  return el("div", low ? "quota alarm" : "quota", node("small", w.name), Object.assign(el("i", "", node("b")), { style: `--left: ${Math.max(0, Math.min(100, w.left))}%` }),
    node("small", `${Math.round(w.left)}%`), node("small", w.resets ? new Date(w.resets).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" }) : ""));
}
// One provider of a device: its sign-in methods as /login offers them, and the sign-in running,
// which the view drives on that device (a browser page, a code, a question).
function provider(m, id) {
  const p = listed.get(m.id)?.find(x => x.id === id) || { id, name: id, methods: [] }, f = state.login?.machine === m.id && state.login.provider === id ? state.login : null;
  const answer = text => send({ do: "login.answer", text }), cancel = () => send({ do: "login.cancel" });
  const back = button("‹ providers", () => { if (f && !["done", "failed"].includes(f.state)) cancel(); openPage("providers", m.id); }, "plain");
  const field = (prompt, secret) => {
    const input = Object.assign(node("input"), { type: secret ? "password" : "text", placeholder: prompt.placeholder || (secret ? "paste it here" : ""), autocomplete: "off" });
    input.onkeydown = event => event.key === "Enter" && input.value.trim() && answer(input.value.trim());
    return el("div", "answer", input, button("save", () => input.value.trim() && answer(input.value.trim()), "inverted"));
  };
  const head = [back, node("h2", p.name), note([m.name, p.ready ? `ready through ${p.holds} · ${p.models} model${p.models === 1 ? "" : "s"}` : "not signed in"].join(" · "))];
  if (f && f.state !== "failed") {
    const link = f.url && Object.assign(node("a", "open the sign-in page"), { href: f.url, target: "_blank", rel: "noreferrer", className: "inverted" });
    const flow = {
      starting: () => [note(f.detail || "starting…")],
      browser: () => [node("h3", "Continue in the browser"), link, note("Finish on the page, then paste the code it shows or the final redirect URL here."), ...(f.prompt ? [field(f.prompt, false)] : [])],
      code: () => [label("enter this code"), button(f.code, () => navigator.clipboard.writeText(f.code).then(() => toast("code copied")), "code"), link, ...(f.detail ? [note(f.detail)] : [])],
      asking: () => [note(f.prompt?.message || ""), ...(f.prompt?.options?.length ? f.prompt.options.map((o, i) => button(o.label, () => answer(o.id), i ? "" : "inverted")) : [field(f.prompt || {}, f.prompt?.kind === "secret")])],
      done: () => [node("h3", "Signed in"), note(`${m.name} holds the new credential; its conversations can use the models now.`), button("done", () => { cancel(); openPage("providers", m.id); }, "inverted")],
    }[f.state]?.() || [note(f.state)];
    return [...head, el("div", "flow", ...flow.filter(Boolean), ...(f.state === "done" ? [] : [button("cancel", cancel)]))];
  }
  return [...head, ...(f?.state === "failed" ? [el("p", "meta alarm", f.detail || "sign-in failed")] : []), ...p.methods.map(x => el("div", "method", label(x.account ? "account" : "api key"), node("strong", x.label), note(x.about),
    button(x.account ? (p.ready ? "sign in again" : "sign in") : (p.ready ? "replace key" : "add key"), () => send({ do: "login", machine: m.id, provider: id, auth: x.auth }), p.ready ? "" : "inverted"))),
    ...(p.ready && (p.status === "oauth" || p.source === "stored") ? [button(p.status === "oauth" ? "sign out" : "remove key", () => ask("logout", { machine: m.id, provider: id }).then(retry("providers", m.id, id), error => showError(error.message)))] : [])];
}
let page = { name: "general", peer: "", vendor: "" }, pageSeq = 0;
async function openPage(name, peer = page.peer, vendor = "") {
  const seq = ++pageSeq;
  page = { name, peer: peer || defaultPeer(), vendor };
  showError("");
  show("settings");
  for (const item of $("pages").querySelectorAll("[data-page]")) item.className = item.dataset.page === name ? "on" : "";
  if (name === "plugins" || name === "mitosis") {
    $("page").replaceChildren(node("h2", name[0].toUpperCase() + name.slice(1)), ...(name === "plugins" ? [devices("plugins", page.peer)] : []), note("loading…"), notice);
  }
  try {
    const content = await pages[name](page.peer, vendor, seq);
    if (seq !== pageSeq) return;
    $("page").replaceChildren(...content, notice);
  } catch (error) {
    if (seq === pageSeq) showError(error.message);
  }
}
// Code mode's runtime: a frame of opaque origin (sandboxed without same-origin), so what runs reaches
// neither this page, its storage nor the synced credentials. Each run is a worker in it, ended when it
// settles or after 2 min; its code gets tools.<name>(args), the session's other tools, each call
// relayed out to the tab's Orb and its text back. These two functions run there, not here.
function sandboxWorker() {
  const out = [], show = v => (typeof v === "string" ? v : JSON.stringify(v, null, 2) ?? String(v)), waiting = new Map();
  for (const k of ["log", "info", "warn", "error"]) console[k] = (...a) => out.push(a.map(show).join(" "));
  let next = 0;
  const call = (name, args) => new Promise((ok, no) => { waiting.set(++next, { ok, no }); postMessage({ call: { k: next, name, args } }); });
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
    } catch (error) { postMessage({ out, error: String(error?.stack || error) }); }
  };
}
function sandboxFrame(source) {
  const runs = new Map();
  onmessage = ({ data }) => {
    if (data.reply) return runs.get(data.id)?.postMessage({ reply: data.reply });
    const worker = new Worker(URL.createObjectURL(new Blob([source])));
    const end = reply => { clearTimeout(timer); worker.terminate(); runs.delete(data.id); parent.postMessage({ id: data.id, ...reply }, "*"); };
    const timer = setTimeout(() => end({ error: "stopped after 2 min" }), 120000);
    runs.set(data.id, worker);
    worker.onmessage = event => (event.data.call ? parent.postMessage({ id: data.id, call: event.data.call }, "*") : end(event.data));
    worker.onerror = event => { event.preventDefault(); end({ error: event.message }); };
    worker.postMessage({ code: data.code, names: data.names });
  };
}
let frame, framed, nextRun = 0;
const runs = new Map();
function run(code, names, call) {
  if (!frame) {
    frame = Object.assign(document.createElement("iframe"), { hidden: true, srcdoc: `<script>(${sandboxFrame})(${JSON.stringify(`(${sandboxWorker})()`)})<\/script>` });
    frame.setAttribute("sandbox", "allow-scripts");
    framed = new Promise(resolve => (frame.onload = resolve));
    document.body.append(frame);
  }
  return framed.then(() => new Promise(resolve => {
    const id = ++nextRun;
    runs.set(id, { resolve, call });
    frame.contentWindow.postMessage({ id, code, names: JSON.parse(names) }, "*");
  }));
}
addEventListener("message", ({ source, data }) => {
  const ongoing = source === frame?.contentWindow && runs.get(data?.id);
  if (!ongoing) return;
  if (data.call) {
    const reply = answer => frame.contentWindow.postMessage({ id: data.id, reply: { k: data.call.k, ...answer } }, "*");
    ongoing.call(data.call.name, JSON.stringify(data.call.args)).then(answer => JSON.parse(answer), error => ({ text: error.message, failed: true })).then(reply);
    return;
  }
  ongoing.resolve(JSON.stringify({ out: data.out || [], value: data.value, error: data.error }));
  runs.delete(data.id);
});
function tabName() {
  const ua = navigator.userAgent || "";
  if (/iPhone|iPad/.test(ua)) return "Browser (iOS)";
  if (/Android/.test(ua)) return "Browser (Android)";
  if (/Mac OS X|Macintosh/.test(ua)) return "Browser (Mac)";
  if (/Windows/.test(ua)) return "Browser (Windows)";
  if (/Linux/.test(ua)) return "Browser (Linux)";
  return "Browser";
}
// The tab's Orb over IndexedDB, which keeps its identity, received providers, memory, conversation
// and the view's open tabs across reloads. Starting its Mitosis makes it dial its devices.
async function boot() {
  // One runtime owns the tab's storage for the page's life: its journals are read once and
  // appended in memory, so a second page writing the same database would overwrite them. Another
  // page takes it over as WhatsApp Web does: told to, the one holding it reloads, which lets go.
  const take = sessionStorage.getItem("orb.take");
  sessionStorage.removeItem("orb.take");
  if (take) { showError("taking your Orb over from the other tab…"); moves.postMessage("take"); }
  const held = await new Promise(resolve => navigator.locks.request("orb-tab", take ? {} : { ifAvailable: true }, lock => { resolve(!!lock); return lock && new Promise(() => {}); }));
  if (!held) return elsewhere("Your Orb is open in another tab of this page."), false;
  showError("");
  moves.onmessage = ({ data }) => data === "take" && (sessionStorage.setItem("orb.moved", "1"), location.reload());
  const response = await fetch("peer.wasm.gz");
  if (!response.ok) throw new Error(`Wasm download failed: ${response.status}`);
  const answers = new Map();
  let next = 0;
  // In a worker, so the page stays responsive however much Orb computes. Its frames are the RPC
  // frames of its own session and, told apart by their "t", what the view sends.
  const peer = spawnPeer({
    wasm: await new Response(response.body.pipeThrough(new DecompressionStream("gzip"))).arrayBuffer(), storage: "orb-tab", name: tabName(),
    env: { ORB_SETTINGS: JSON.stringify({ plugins: { memory: true } }) },
    emit: text => {
      const frame = JSON.parse(text);
      if (frame.t) return heard(frame);
      if (frame.type === "response" && answers.has(frame.id)) { answers.get(frame.id)(frame); answers.delete(frame.id); }
    },
    exit: () => { tab = null; showError("This tab's Orb stopped. Reload the page."); }, run,
  });
  tab = {
    admin: async (method, params = {}) => JSON.parse(await peer.mitosisAdmin(method, JSON.stringify(params), "")),
    app: line => peer.app(line),
    plugins: async (...args) => JSON.parse(await peer.plugins(...args)),
    models: async (...args) => JSON.parse(await peer.models(...args)),
    accounts: async (...args) => JSON.parse(await peer.accounts(...args)),
    rpc: (type, params = {}) => new Promise((resolve, reject) => {
      const id = `tab-${++next}`;
      answers.set(id, frame => (frame.success ? resolve(frame.data) : reject(new Error(frame.error))));
      peer.command(JSON.stringify({ id, type, ...params })).then(refused => {
        if (refused) { answers.delete(id); reject(new Error(refused)); }
      }, reject);
    }),
  };
  ({ peer_id: me } = await tab.admin("self"));
  $("fingerprint").textContent = me.split(":").at(-1).slice(0, 8);
  $("self").textContent = me;
  approval();
  return true;
}
const moves = new BroadcastChannel("orb-tab");
function elsewhere(text) {
  showError(text);
  notice.append(" ", button("use here", () => { sessionStorage.setItem("orb.take", "1"); location.reload(); }, "plain"));
}
// Once the inviting device approves this tab (sharing a provider, admitting it to the sync group,
// or answering agent.describe), the app replaces the fingerprint screen. Join the sync group only
// when not already in one, so polling never resets sync state.
async function approval() {
  try {
    const owner = localStorage.getItem("orb.tab.owner");
    if (!owner) return show("invite");
    let synced = await tab.admin("sync").catch(() => null);
    if (!synced?.group_id) synced = await tab.admin("sync", { join: owner }).catch(() => null);
    const { models } = await tab.rpc("get_available_models");
    const approved = !!localStorage.getItem("orb.tab.approved") || models.length > 0 || !!synced?.in_sync ||
      await tab.admin("remote", { peer_id: owner, method: "agent.describe", params: {} }).then(() => true, () => false);
    if (!approved) { show("approve"); return setTimeout(approval, 3000); }
    localStorage.setItem("orb.tab.approved", "1");
    // A session started before any provider arrived has no model; a new one takes the preferred one.
    if (models.length) {
      const { model } = await tab.rpc("get_state");
      if (!model || model.provider === "unknown") await tab.rpc("new_session");
    }
    send({ do: "hello", budget: 4 });
    send({ do: "visible", on: !document.hidden });
    send({ do: "show", tabs: shown ? [shown] : [] });
    if ($("settings").hidden) show("app");
    render();
  } catch (error) { showError(error.message); setTimeout(approval, 3000); }
}
$("join").onclick = () => join($("code").value);
async function join(text, additional = false) {
  $("join").disabled = true; showError("");
  try {
    // The code orb mitosis --browser shows (orb-mitosis:v1:<base64url JSON>), or the invitation JSON itself.
    const value = text.trim(), code = value.split("orb-mitosis:v1:")[1]?.split(/\s/)[0];
    const invitation = JSON.parse(code ? atob(code.replace(/-/g, "+").replace(/_/g, "/")) : value || "{}");
    if (!invitation.peer_id || !invitation.token) throw new Error("Paste the code orb mitosis --browser shows.");
    if (!/^wss?:\/\//.test(invitation.locator ?? "")) throw new Error("That code is for the apps. For this tab, run orb mitosis --browser.");
    if (!tab && !await boot()) return;
    await tab.admin("join", invitation);
    await tab.admin("trust", { peer_id: invitation.peer_id });
    if (additional) {
      toast("claimed · approve this tab on that device");
      return;
    }
    localStorage.setItem("orb.tab.owner", invitation.peer_id);
    show("approve");
    approval();
  } catch (error) { showError(error.message); }
  finally { $("join").disabled = false; }
}
// Forgetting the tab waits for the reload, when no runtime holds its database any more.
if (localStorage.getItem("orb.tab.forget")) {
  for (const key of Object.keys(localStorage)) if (key.startsWith("orb.tab.")) localStorage.removeItem(key);
  await new Promise(resolve => Object.assign(indexedDB.deleteDatabase("orb-tab"), { onsuccess: resolve, onerror: resolve, onblocked: resolve }));
}
// The link orb mitosis --browser opens carries its code after #, which the browser never sends.
const linked = decodeURIComponent(location.hash.slice(1));
if (linked) history.replaceState(null, "", location.pathname + location.search);
if (sessionStorage.getItem("orb.moved")) { sessionStorage.removeItem("orb.moved"); elsewhere("Your Orb moved to another tab of this page."); }
else if (localStorage.getItem("orb.tab.owner")) boot().catch(error => showError(error.message));
else {
  show("invite");
  if (linked.startsWith("orb-mitosis:v1:")) join($("code").value = linked);
}
