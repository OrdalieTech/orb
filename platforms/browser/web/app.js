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
let asked = 0, dirty = 0;
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
    if (shown && !conversation()) focus("");
  } else if (m.t === "home") home = m;
  else if (m.t === "rows") {
    const list = rows.get(m.tab) || [];
    list.length = Math.min(m.at, list.length);
    rows.set(m.tab, list.concat(m.rows));
  } else return;
  dirty ||= requestAnimationFrame(render);
}
const conversation = (id = shown) => state.tabs.find(t => t.id === id);
const machine = id => home.machines.find(m => m.id === id);
const named = title => (title || "new session").replace(/^./, c => c.toUpperCase());
function focus(id) {
  shown = id;
  $("prompt").value = "";
  send({ do: "show", tabs: id ? [id] : [] });
  dirty ||= requestAnimationFrame(render);
  $("prompt").focus({ preventScroll: true });
}
function open(key) {
  ask("open", { key }).then(r => r?.tab && focus(r.tab), error => showError(error.message));
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
    close.onclick = event => { event.stopPropagation(); send({ do: "close", tab: c.id }); if (c.id === shown) focus(""); };
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
  if (!changed("home", home, state.tabs, state.summary, state.launching, state.up, device)) return;
  const machines = [...home.machines].sort((x, y) => !!y.self - !!x.self);
  $("machines").replaceChildren(...machines.map(m => {
    const up = m.self ? state.up : m.connected;
    const card = button("", () => { device = device === m.id ? "" : m.id; render(); }, `${device === m.id ? "on" : ""}${up ? "" : " down"}`);
    card.append(node("span", where(m.hue), m.self ? "this tab" : m.name), node("span", up ? `${m.running?.length || 0} running` : "offline", node("b", up ? "承認" : "否決")));
    return card;
  }));
  $("summary").textContent = state.summary || "";
  $("summary").classList.toggle("asks", (state.summary || "").startsWith("needs"));
  $("launching").textContent = state.launching || "";
  const entries = home.entries.filter(e => !device || e.machine === device);
  $("entries").replaceChildren(...entries.map(e => {
    const m = machine(e.machine), followed = conversation(e.tab), age = followed ? word(followed) : "";
    const row = button("", () => followed ? focus(followed.id) : open(e.key), e.open ? "open" : "");
    row.append(where(m?.hue, e.live, e.asks), node("span", named(e.title)),
      el("small", e.asks ? "asks" : "", [!device && m && (m.self ? "this tab" : m.name), e.asks ? "asks" : e.live ? "working" : age || (e.unstored ? "open" : ago(e.modified))].filter(Boolean).join(" · ")));
    return row;
  }), ...(!home.machines.length ? [el("p", "meta", "reading conversations…")] : !entries.length ? [el("p", "meta", "no conversations here yet")] : []));
}
// Where a new conversation starts: a device that lets this tab start Orb there, the one picked last
// or the one this tab paired with; with none, this tab's own Orb.
function launchers() {
  return home.machines.filter(m => !m.self && m.launch && m.connected);
}
function target() {
  const all = launchers(), picked = localStorage.getItem("orb.tab.where");
  return all.find(m => m.id === picked) || all.find(m => m.id === localStorage.getItem("orb.tab.owner")) || all[0] || machine(state.self);
}
async function start(text) {
  const m = target();
  if (!m) return;
  if (!m.self) return ask("start", { machine: m.id, text }).then(r => focus(r.tab));
  // This tab's Orb holds one conversation at a time: a new one replaces it.
  const own = m.running?.[0];
  if (!own) throw new Error("this tab's Orb is still starting");
  const { tab: id } = await ask("open", { key: `i:${own.instance}` });
  focus(id);
  await ask("send", { tab: id, text: `/new ${text}` });
}

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
  $("earlier").hidden = !t.earlier;
  $("empty").hidden = list.length > 0;
  $("empty").textContent = t.asleep ? "asleep · a message wakes it" : t.loaded ? "" : "loading…";
  $("strip").replaceChildren(...[[t.remote && t.where, word(t)].filter(Boolean).join(" · "), t.usage && usage(t.usage), t.cost > 0 && `$${t.cost.toFixed(2)}`]
    .filter(Boolean).map(text => node("span", text)));
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
// The reply is followed while the reader stays at its end: scrolling up lets go, and coming back
// to the end takes hold again.
const end = () => requestAnimationFrame(() => ($("transcript").scrollTop = $("transcript").scrollHeight));
let lastTop = 0;
$("transcript").addEventListener("wheel", event => { if (event.deltaY < 0) pinned = false; }, { passive: true });
$("transcript").addEventListener("touchmove", () => (pinned = false), { passive: true });
$("transcript").addEventListener("scroll", ({ target: t }) => {
  if (t.scrollTop > lastTop && t.scrollHeight - t.scrollTop - t.clientHeight < 4) pinned = true;
  lastTop = t.scrollTop;
}, { passive: true });
$("earlier").onclick = () => { pinned = false; send({ do: "earlier", tab: shown }); };

function drawRow(r, t, old) {
  if (r.kind === "you") {
    return el("div", "you", ...(r.via ? [el("small", "via", "from a peer")] : []), ...pictures(r.images, t), ...(r.text.trim() ? [el("p", "", r.text)] : []));
  }
  if (r.kind === "md") return el("div", "said", ...blocks([r.block]));
  if (r.kind === "note") return el("p", r.alarm ? "note alarm" : "note", r.text);
  // A run of actions folds into one line, as the TUI folds exploration, naming what runs now.
  const one = r.actions?.length === 1, line = one ? action(r.actions[0], t) : el("details", "action run");
  const opened = !!old?.querySelector("details")?.open;
  if (one) line.firstChild.open = opened;
  else {
    line.open = opened;
    line.append(summary(r.live ? "working" : "worked", [r.what, !line.open && r.now].filter(Boolean).join(" · "), r.failed ? `${r.failed} failed` : "", r.live, r.failed > 0),
      ...r.actions.map(a => action(a, t)));
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
    const img = Object.assign(node("img"), { alt: "", onclick: () => ask("image", { tab: t.id, ref, px: 0 }).then(r => open64(r.data)) });
    ask("image", { tab: t.id, ref, px: 384 }).then(r => (img.src = `data:image/png;base64,${r.data}`), () => {});
    return img;
  }))];
}
function open64(data) {
  const w = window.open();
  if (w) w.document.body.append(Object.assign(w.document.createElement("img"), { src: `data:image/png;base64,${data}` }));
}
// The blocks the view parsed of what Orb wrote, drawn as elements, never as HTML.
function blocks(list) {
  return list.map(b => {
    const indent = b.depth ? { style: `margin-left: ${b.depth * 2}ch` } : {};
    switch (b.type) {
      case "h": return Object.assign(node(b.level <= 2 ? "h3" : "h4", ...spans(b.spans)), indent);
      case "li": return Object.assign(el("div", "li", el("span", "mark", b.mark), node("span", ...spans(b.spans))), indent);
      case "quote": return Object.assign(node("blockquote", ...blocks(b.blocks || [])), indent);
      case "code": case "art": return Object.assign(el("pre", b.type, ...(b.lang ? [el("small", "", b.lang)] : []), b.text), indent);
      case "table": return Object.assign(el("div", "table", node("table", ...(b.rows || []).map((r, i) => node("tr", ...r.map(cell => node(i ? "td" : "th", ...spans(cell))))))), indent);
      case "rule": return node("hr");
      default: return Object.assign(node("p", ...spans(b.spans)), indent);
    }
  });
}
function spans(list = []) {
  return list.map(s => {
    let n = s.t;
    if (s.c) n = node("code", n);
    if (s.b) n = node("strong", n);
    if (s.i) n = node("em", n);
    if (s.s) n = node("s", n);
    if (s.h) n = Object.assign(node("a", n), { href: s.h, target: "_blank", rel: "noreferrer" });
    return n;
  });
}

// The prompt box, as the apps': on Home it starts a conversation where the chip says; in one, it
// sends, steers a run with what is typed, or stops it.
function controls() {
  const t = conversation(), text = $("prompt").value.trim(), m = !t && target();
  $("send").ariaLabel = t?.busy && !text ? "Stop" : "Send";
  $("send").classList.toggle("stop", !!t?.busy && !text);
  $("send").disabled = !text && !t?.busy;
  $("prompt").placeholder = t ? "What next?" : "What should we work on?";
  $("where").textContent = t ? [t.model?.split("/").at(-1), t.remote && t.where].filter(Boolean).join(" · ") || "model" : m ? (m.self ? "this tab" : m.name) : "no device";
  $("model").firstChild.style = `--hue: var(--hue${(t ? machine(t.peer) : m)?.hue || 0})`;
  const levels = t?.levels || [], at = levels.indexOf(t?.thinking);
  $("meter").replaceChildren(...levels.filter(l => l !== "off").map((l, i) => el("i", i < at ? "lit" : "")));
  palette();
}
$("prompt-form").onsubmit = event => {
  event.preventDefault();
  const t = conversation(), text = $("prompt").value.trim();
  if (t?.busy && !text) return send({ do: "abort", tab: t.id });
  if (!text) return;
  $("prompt").value = "";
  pinned = true;
  showError("");
  controls();
  if (!t) return start(text).catch(error => { $("prompt").value = text; showError(error.message); });
  if (t.busy) return send({ do: "steer", tab: t.id, text });
  // What only the page can do comes back: a screen to open, or text to copy.
  ask("send", { tab: t.id, text }).then(r => {
    if (r?.copy) navigator.clipboard.writeText(r.copy);
    if (r?.nav) go(r.nav);
  }, error => showError(error.message));
};
function go(nav) {
  if (nav === "model") openModels();
  else if (nav === "home") focus("");
  else openPage({ providers: "providers", plugins: "plugins", pair: "mitosis" }[nav] || "general");
}
$("prompt").oninput = controls;
$("prompt").onkeydown = event => {
  const first = $("palette").querySelector("button");
  if (event.key === "Tab" && first && !event.shiftKey) { event.preventDefault(); first.click(); }
  else if (event.key === "Enter" && !event.shiftKey && !event.isComposing) { event.preventDefault(); $("prompt-form").requestSubmit(); }
};
// The slash palette: the app's commands, then the conversation's Orb's, as typed so far.
function palette() {
  const text = $("prompt").value, typed = /^\/(\S*)$/.exec(text)?.[1];
  const all = [...(state.commands || []), ...(conversation()?.commands || [])].filter(c => typed !== undefined && c.name.startsWith(typed));
  $("palette").hidden = !all.length;
  $("palette").replaceChildren(...all.slice(0, 8).map(c => {
    const pick = button("", () => {
      $("prompt").value = `/${c.name}${c.now ? "" : " "}`;
      if (c.now) $("prompt-form").requestSubmit();
      else { $("prompt").focus(); controls(); }
    });
    pick.append(node("b", `/${c.name}`), node("span", c.hint || ""));
    return pick;
  }));
}

// The model deck, as the apps': in a conversation, the models its Orb has and the reasoning levels
// the one in use takes; on Home, the devices a conversation can start on and each one's default.
const deck = $("models");
deck.onclick = event => {
  const r = deck.getBoundingClientRect();
  if (event.clientX < r.left || event.clientX > r.right || event.clientY < r.top || event.clientY > r.bottom) deck.close();
};
$("model").onclick = openModels;
$("filter").oninput = () => drawDeck();
let defaults = null;
async function openModels() {
  $("filter").value = "";
  defaults = null;
  drawDeck();
  deck.showModal();
  const m = !conversation() && target();
  if (m && !m.self) {
    defaults = await ask("models", { machine: m.id }).catch(error => ({ models: [], error: error.message }));
    drawDeck();
  }
}
function drawDeck() {
  const t = conversation(), m = !t && target(), query = $("filter").value.trim().toLowerCase();
  $("places").hidden = !!t;
  $("places").replaceChildren(...(t ? [] : launchers()).map(d => button(d.name, () => {
    localStorage.setItem("orb.tab.where", d.id);
    openModels();
    controls();
  }, d.id === m?.id ? "on" : "")));
  const levels = t ? t.levels || [] : defaults?.levels || [], level = t ? t.thinking : defaults?.thinking;
  $("levels").replaceChildren(...levels.map(l => button(l, () => t ? send({ do: "thinking", tab: t.id, name: l }) :
    ask("default", { machine: m.id, name: defaults.default, level: l }).then(openModels, error => showError(error.message)), l === level ? "on" : "")));
  const models = t ? t.models || [] : defaults?.models || [], using = t ? t.model : defaults?.default, list = [];
  let provider = "";
  for (const id of models.filter(id => id.toLowerCase().includes(query))) {
    const [p, ...rest] = id.split("/");
    if (p !== provider) list.push(el("p", "label", (provider = p)));
    list.push(button(rest.join("/"), () => t ? (send({ do: "model", tab: t.id, name: id }), deck.close()) :
      ask("default", { machine: m.id, name: id }).then(() => deck.close(), error => showError(error.message)), id === using ? "on" : ""));
  }
  if (!list.length) list.push(el("p", "meta", defaults?.error || (!t && m && !m.self && !defaults ? "reading its models…" : m?.self ? "this tab's Orb uses its own default" : "no models")));
  $("list").replaceChildren(...list);
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
  const answer = value => { leave(); send({ do: "answer", tab: t.id, value }); };
  const keys = event => {
    if (event.key === "Escape") return answer(null);
    const hit = event.target !== field && Date.now() > box.dataset.armed && keyed.find(([key]) => key === event.key);
    if (hit) { event.preventDefault(); answer(hit[2]); }
  };
  const submit = () => field.value.trim() && answer(field.value);
  field.onkeydown = event => event.key === "Enter" && submit();
  box.replaceChildren(node("i"), node("div", node("div", node("span", `${ask.owner} · ${t.where}`), node("span", "waiting on you")), node("hr"),
    el("h2", word.length > 5 ? "stretch word squeezed" : "stretch word", word), el("p", "caption", caption),
    el("p", "first", first), ...(detail ? [el("p", "detail", detail)] : []),
    node("hr"), ...keyed.map(([key, text, choice], n) => {
      const pick = Object.assign(button("", () => answer(choice), n ? "choice" : "choice first"), { style: `--n: ${n}` });
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
$("menu").onclick = () => openPage("general");
$("back").onclick = () => { show("app"); render(); };
for (const item of $("pages").querySelectorAll("[data-page]")) item.onclick = () => openPage(item.dataset.page);
document.onkeydown = event => {
  if ((event.ctrlKey || event.metaKey) && event.key === "," && tab) { event.preventDefault(); openPage("general"); }
  else if (event.key === "Escape" && !$("settings").hidden) $("back").onclick();
  else if (event.key === "Escape" && !deck.open && !document.querySelector(".interrupt") && shown) focus("");
};
document.onvisibilitychange = () => send({ do: "visible", on: !document.hidden });

// Settings, as the desktop's: its pages at left, the one shown at right. They are this tab's: its
// Orb's accounts, plugins and devices.
const label = text => el("p", "label", text);
const note = text => el("p", "meta", text);
function row(name, sub, action) {
  return el("div", "row", node("div", node("strong", name), note(sub)), ...(action ? [action] : []));
}
// What each plugin does, as agent/assembly's catalog says it.
const about = {
  titles: "Name each session after its first exchange", tasks: "Live session task list and todo tool",
  questions: "Ask the user questions with choices and custom answers", memory: "Bounded persistent remember, recall, replace, and forget tools",
  memtree: "Experimental: zoomable summary tree of each session for lossless compaction or fresh turns",
};
const pages = {
  general() {
    // A phone has no keys to list.
    const keys = matchMedia("(hover: hover)").matches;
    return [node("h2", "General"), note("this tab, one of your devices"), ...(keys ? [label("keys"), ...[["Enter", "send"], ["Shift+Enter", "new line"], ["Tab", "complete a / command"],
      ["Ctrl+,", "settings"], ["Esc", "back to Home"]].map(([key, what]) => row(key, what))] : [])];
  },
  async providers() {
    const counts = new Map();
    for (const m of (await tab.rpc("get_available_models")).models) counts.set(m.provider, (counts.get(m.provider) || 0) + 1);
    return [node("h2", "Providers"), note("the accounts your devices share with this tab"), label("accounts"),
      ...[...counts].map(([provider, n]) => row(provider, `${n} model${n === 1 ? "" : "s"}`)),
      note("Sign in on one of your devices: its accounts reach this tab, sealed to it. A conversation on a device runs on that device's own accounts.")];
  },
  async plugins() {
    const plugins = await tab.plugins();
    return [node("h2", "Plugins"), note("what this tab's Orb runs, from its next conversation"), ...Object.keys(plugins).sort().map(name =>
      row(name, about[name], button(plugins[name] ? "on" : "off", () => tab.plugins(name, !plugins[name]).then(() => openPage("plugins"), error => showError(error.message)), plugins[name] ? "inverted" : "")))];
  },
  mitosis() {
    return [node("h2", "Mitosis"), note("peer to peer"), label("devices"),
      ...home.machines.filter(m => !m.self).map(m => row(m.name, [m.connected ? "connected" : "not connected", m.version, m.launch ? "starts conversations for this tab" : "follows only"].filter(Boolean).join(" · "))),
      label("this tab"), note(me),
      note("Each device is approved by fingerprint on its own screen. A device you paired this tab with lets it follow and start its conversations; run  orb mitosis trust <this tab>  on another one to let it do so too. Forgetting this tab clears it here; block it on a device to take it out of your group."),
      button("forget this tab", () => { if (confirm("Forget this tab? Its conversations here go with it.")) { localStorage.setItem("orb.tab.forget", "1"); location.reload(); } })];
  },
};
async function openPage(name) {
  show("settings");
  for (const item of $("pages").querySelectorAll("[data-page]")) item.className = item.dataset.page === name ? "on" : "";
  try { $("page").replaceChildren(...await pages[name]()); } catch (error) { showError(error.message); }
  $("page").append(notice);
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
// The tab's Orb over IndexedDB, which keeps its identity, received providers, memory, conversation
// and the view's open tabs across reloads. Starting its Mitosis makes it dial its devices.
async function boot() {
  // One runtime owns the tab's storage for the page's life: its journals are read once and
  // appended in memory, so a second page writing the same database would overwrite them.
  const held = await new Promise(resolve => navigator.locks.request("orb-tab", { ifAvailable: true }, lock => { resolve(!!lock); return lock && new Promise(() => {}); }));
  if (!held) throw new Error("Your Orb is already open in another tab of this page.");
  const response = await fetch("peer.wasm.gz");
  if (!response.ok) throw new Error(`Wasm download failed: ${response.status}`);
  const answers = new Map();
  let next = 0;
  // In a worker, so the page stays responsive however much Orb computes. Its frames are the RPC
  // frames of its own session and, told apart by their "t", what the view sends.
  const peer = spawnPeer({
    wasm: await new Response(response.body.pipeThrough(new DecompressionStream("gzip"))).arrayBuffer(), storage: "orb-tab",
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
}
// The first provider a device shares is the tab's approval: the app replaces the fingerprint. Until
// then the tab asks to follow the device's sync group again, since the device may admit it after
// the tab first asked. Credentials never leave the tab's Orb: it reads provider names only.
async function approval() {
  try {
    const owner = localStorage.getItem("orb.tab.owner");
    const { models } = await tab.rpc("get_available_models");
    if (owner && !models.length) await tab.admin("sync", { join: owner }).catch(() => {});
    if (!owner) return show("invite");
    if (!models.length) { show("approve"); return setTimeout(approval, 3000); }
    // A session started before any provider arrived has no model; a new one takes the preferred one.
    const { model } = await tab.rpc("get_state");
    if (!model || model.provider === "unknown") await tab.rpc("new_session");
    send({ do: "hello", budget: 4 });
    send({ do: "visible", on: !document.hidden });
    send({ do: "show", tabs: shown ? [shown] : [] });
    if ($("settings").hidden) show("app");
    render();
  } catch (error) { showError(error.message); setTimeout(approval, 3000); }
}
$("join").onclick = () => join($("code").value);
async function join(text) {
  $("join").disabled = true; showError("");
  try {
    // The code orb mitosis --browser shows (orb-mitosis:v1:<base64url JSON>), or the invitation JSON itself.
    const value = text.trim(), code = value.split("orb-mitosis:v1:")[1]?.split(/\s/)[0];
    const invitation = JSON.parse(code ? atob(code.replace(/-/g, "+").replace(/_/g, "/")) : value || "{}");
    if (!invitation.peer_id || !invitation.token) throw new Error("Paste the code orb mitosis --browser shows.");
    if (!/^wss?:\/\//.test(invitation.locator ?? "")) throw new Error("That code is for the apps. For this tab, run orb mitosis --browser.");
    if (!tab) await boot();
    await tab.admin("join", invitation);
    await tab.admin("trust", { peer_id: invitation.peer_id });
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
if (localStorage.getItem("orb.tab.owner")) boot().catch(error => showError(error.message));
else {
  show("invite");
  if (linked.startsWith("orb-mitosis:v1:")) join($("code").value = linked);
}
