import { indexedStorage, openPeer } from "./peer.js";
const $ = id => document.getElementById(id);
// notice: the one error line, moved to the screen it concerns, so pages that redraw keep hold of it.
const notice = document.getElementById("error");
// tab: this tab's own Orb (peer.wasm), one of the user's devices; word: what it does now; where: its model.
let tab = null, me = "", running = false, switching = false, messages = [], streamed = "", word = "", where = "";
// tabs: the conversations the bar shows, by session file, with their titles; current: the one shown.
let tabs = JSON.parse(localStorage.getItem("orb.tab.tabs") || "[]"), current = "";

function showError(message) {
  notice.textContent = message || "";
  notice.hidden = !message;
}
// show picks the one screen: the invitation, its approval, the conversation or Settings; an error
// reads at the end of what it concerns, as the apps note one.
function show(screen) {
  const opening = screen === "chat" && $("chat").hidden;
  $({ chat: "transcript", settings: "page" }[screen] || screen).append(notice);
  $("pair").hidden = screen === "chat" || screen === "settings";
  $("chat").hidden = screen !== "chat";
  $("settings").hidden = screen !== "settings";
  $("invite").hidden = screen !== "invite";
  $("approve").hidden = screen !== "approve";
  // A hidden transcript has no height to scroll: it opens at its end once shown.
  if (opening) $("transcript").scrollTop = $("transcript").scrollHeight;
}
function controls() {
  $("status").textContent = word || where;
  $("send").ariaLabel = running ? "Stop" : "Send";
  $("send").classList.toggle("stop", running);
  $("send").disabled = switching || !running && !$("prompt").value.trim();
  $("new").disabled = $("model").disabled = running || switching;
  renderTabs();
}
// The desktop's tab bar: a chip per conversation, its square pulsing while it works, its × on it
// when shown or under the pointer; one conversation runs at a time, so the others wait for it.
function renderTabs() {
  $("tabs").replaceChildren(...tabs.map(t => {
    const close = node("b", "×"), chip = node("button", node("i"), node("span", t.title || "new session"), close);
    chip.type = "button";
    chip.className = t.file === current ? (running ? "on live" : "on") : "";
    chip.disabled = (running || switching) && t.file !== current;
    chip.onclick = () => t.file === current || running || switching || go("switch_session", { sessionPath: t.file });
    close.onclick = event => {
      event.stopPropagation();
      if (running && t.file === current) return;
      const at = tabs.indexOf(t);
      tabs.splice(at, 1);
      if (t.file !== current) return save();
      if (tabs.length) go("switch_session", { sessionPath: tabs[Math.min(at, tabs.length - 1)].file });
      else go("new_session");
    };
    return chip;
  }));
}
function save() {
  localStorage.setItem("orb.tab.tabs", JSON.stringify(tabs));
  renderTabs();
}
// go changes the conversation; no prompt goes in until the change settles, which would end it.
function go(command, params) {
  return transition(() => tab.rpc(command, params).then(settle)).catch(error => showError(error.message));
}
async function transition(change) {
  switching = true; controls();
  try { await change(); } finally { switching = false; controls(); }
}
function text(content) {
  return typeof content === "string" ? content : (content || []).filter(b => b.type === "text").map(b => b.text).join("\n");
}
function node(tag, ...children) {
  const element = document.createElement(tag);
  element.append(...children);
  return element;
}
// markdown draws what the apps draw of a reply (headings, lists, tables, code blocks, inline
// code, bold, italic, links) as elements, never as HTML.
function markdown(source) {
  const blocks = [];
  let list = null, paragraph = null;
  const lines = source.split("\n"), cells = line => line.trim().replace(/^\||\|$/g, "").split("|").map(cell => node("td", ...inline(cell.trim())));
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i], item = /^\s*([-*+]|\d+[.)])\s+(.*)/.exec(line), heading = /^#{1,6}\s+(.*)/.exec(line), fence = line.startsWith("```");
    const table = line.trim().startsWith("|") && /^\s*\|?[\s:|-]+\|?\s*$/.test(lines[i + 1] || "") && lines[i + 1].includes("-");
    // A list goes on across blank lines, as models write numbered steps apart.
    if (!item && line.trim()) list = null;
    if (item || heading || fence || table || !line.trim()) paragraph = null;
    if (fence) {
      const code = [];
      while (++i < lines.length && !lines[i].startsWith("```")) code.push(lines[i]);
      blocks.push(node("pre", code.join("\n")));
    } else if (table) {
      const rows = [node("tr", ...cells(line).map(cell => node("th", ...cell.childNodes)))];
      for (i += 2; i < lines.length && lines[i].trim().startsWith("|"); i++) rows.push(node("tr", ...cells(lines[i])));
      i--;
      blocks.push(node("div", node("table", ...rows)));
    } else if (item) {
      const tag = /\d/.test(item[1]) ? "OL" : "UL";
      if (list?.tagName !== tag) blocks.push(list = Object.assign(node(tag), tag === "OL" ? { start: parseInt(item[1]) } : {}));
      list.append(node("li", ...inline(item[2])));
    } else if (heading) blocks.push(node("h3", ...inline(heading[1])));
    else if (paragraph) paragraph.append("\n", ...inline(line));
    else if (line.trim()) blocks.push(paragraph = node("p", ...inline(line)));
  }
  return blocks;
}
function inline(text) {
  return text.split(/(`[^`]+`|\*\*[^*]+\*\*|\*[^*\s][^*]*\*|\[[^\]]+\]\(https?:\/\/[^)\s]+\))/).map((part, i) => {
    if (i % 2 === 0) return part;
    if (part[0] === "[") {
      const [, label, href] = /^\[(.+)\]\((.+)\)$/.exec(part);
      return Object.assign(node("a", label), { href, target: "_blank", rel: "noreferrer" });
    }
    return part[0] === "`" ? node("code", part.slice(1, -1)) : part[1] === "*" ? node("strong", part.slice(2, -2)) : node("em", part.slice(1, -1));
  });
}
// What the person said sits at right on a soft ground; Orb just speaks, full width, its actions
// inline: a line per tool call (what ran, on what; failed in the rupture), which opens on what it
// was given and what came back.
function renderMessages(list) {
  const transcript = $("transcript"), results = new Map(list.filter(m => m.role === "toolResult").map(m => [m.toolCallId, m]));
  $("messages").replaceChildren(...list.flatMap(message => {
    if (message.role === "user") return text(message.content) ? [Object.assign(node("p", text(message.content)), { className: "user" })] : [];
    if (message.role !== "assistant") return [];
    const blocks = (typeof message.content === "string" ? [{ type: "text", text: message.content }] : message.content || []).flatMap(block =>
      block.type === "text" ? markdown(block.text) : block.type === "toolCall" ? [action(block, results.get(block.id))] : []);
    return blocks.length ? [Object.assign(node("div", ...blocks), { className: "assistant" })] : [];
  }));
  transcript.scrollTop = transcript.scrollHeight;
}
function action(call, result) {
  const args = call.arguments || {}, given = String(args.code ?? args.command ?? args.path ?? JSON.stringify(args));
  const line = node("summary", node("b", call.name), node("span", given.split("\n")[0]), ...(result?.isError ? [node("em", "failed")] : []));
  return Object.assign(node("details", line, node("pre", given), ...(result ? [node("pre", text(result.content))] : [])), { className: "action" });
}
function idle() {
  running = false; word = "";
  $("stream").hidden = true;
  for (const question of document.querySelectorAll(".interrupt")) question.remove();
  controls();
}
// RPC frames carry message_update as deltas (agent/rpc).
function onEvent(event) {
  if (event.type === "tool_execution_start") word = `using ${event.toolName}`;
  if (event.type === "message_start") streamed = "";
  if (event.type === "message_update") {
    const transcript = $("transcript");
    const following = transcript.scrollHeight - transcript.scrollTop - transcript.clientHeight < 80;
    const delta = event.assistantMessageEvent;
    const reply = event.message ? text(event.message.content) : (streamed += delta?.type === "text_delta" ? delta.delta : "");
    $("stream").replaceChildren(...markdown(reply));
    $("stream").hidden = !reply;
    if (following) transcript.scrollTop = transcript.scrollHeight;
    if (reply) word = "replying";
  }
  controls();
}
// A question the agent waits on stops the world, as in the apps (platforms/desktop's interrupt,
// Android's Interrupt, mitosis/view's walk): the red fills the page between warning stripes, the
// word wipes in, and each choice has its key. The questions plugin's series goes one question at
// a time and answers as one result, which the session checks.
async function ask() {
  const input = await tab.input().catch(() => null);
  if (!input || document.querySelector(".interrupt")) return;
  const asked = input.presentation?.kind === "questions" ? input.presentation.data.questions : null, answers = [];
  const owner = input.presentation?.kind === "approval" ? "permissions" : "questions";
  const done = reply => { document.querySelector(".interrupt")?.remove(); tab.input(input.id, reply).catch(error => showError(error.message)); };
  const step = () => {
    if (!asked) return { title: input.title, choices: input.choices || [], free: !input.choices?.length, answer: value => done(value ?? "") };
    const q = asked[answers.length], n = asked.length;
    return {
      title: q.question, message: [q.header, n > 1 && `${answers.length + 1} / ${n}`].filter(Boolean).join("  "), choices: (q.options || []).map(o => o.label), free: true,
      answer: value => {
        if (value === null) return done(JSON.stringify({ cancelled: true }));
        answers.push(q.options?.some(o => o.label === value) ? { id: q.id, selected: [value] } : { id: q.id, selected: [], custom: value });
        answers.length < n ? interrupt(owner, input.id + answers.length, step()) : done(JSON.stringify({ answers }));
      },
    };
  };
  interrupt(owner, input.id, step());
}
function interrupt(owner, id, ask) {
  const [word, caption] = { permissions: ["APPROVAL", "approval required"] }[owner] || ["QUESTION", "a question for you"];
  const [first, ...rest] = ask.title.split("\n"), detail = [...rest, ask.message || ""].join("\n").trim(), armed = Date.now() + 800;
  const keyed = ask.choices.map((choice, i) => {
    const own = /^([a-z]) (.+)/.exec(choice);
    return [own?.[1] ?? ({ allow: "y", deny: "n" }[choice] || (i < 9 ? String(i + 1) : "")), own?.[2] ?? choice, choice];
  });
  const field = Object.assign(node("input"), { placeholder: "type an answer", autocomplete: "off" });
  const answer = value => { removeEventListener("keydown", keys); box.remove(); ask.answer(value); };
  const keys = event => {
    if (event.key === "Escape") return answer(null);
    const hit = event.target !== field && Date.now() > armed && keyed.find(([key]) => key === event.key);
    if (hit) { event.preventDefault(); answer(hit[2]); }
  };
  const head = node("div", node("span", owner), node("span", "waiting on you"));
  const send = () => field.value.trim() && answer(field.value);
  field.onkeydown = event => event.key === "Enter" && send();
  const box = node("div", node("i"), node("div", head, node("hr"),
    Object.assign(node("h2", word), { className: word.length > 5 ? "stretch word squeezed" : "stretch word" }), Object.assign(node("p", caption), { className: "caption" }),
    Object.assign(node("p", first), { className: "first" }), ...(detail ? [Object.assign(node("p", detail), { className: "detail" })] : []),
    node("hr"), ...keyed.map(([key, text, choice]) => Object.assign(node("button", node("b", key), node("span", text)), { type: "button", className: choice === ask.choices[0] ? "choice first" : "choice", onclick: () => answer(choice) })),
    ...(ask.free ? [node("div", field, button("send", send, "inverted"))] : []),
    node("hr"), button("esc  ·  dismiss", () => answer(null))), node("i"));
  box.className = "interrupt";
  for (const old of document.querySelectorAll(".interrupt")) old.remove();
  document.body.append(box);
  addEventListener("keydown", keys);
  // The line takes the keys only when there is nothing to choose: a choice's key answers.
  if (ask.free && !keyed.length) field.focus();
}
$("prompt-form").onsubmit = event => {
  event.preventDefault();
  if (!tab || switching) return;
  if (running) { tab.rpc("abort").catch(error => showError(error.message)); return; }
  const value = $("prompt").value;
  if (!value.trim()) return;
  running = true; word = "waiting"; showError("");
  const shown = tabs.find(t => t.file === current);
  if (shown && !shown.title) shown.title = value.trim().split("\n")[0];
  $("prompt").value = "";
  controls();
  renderMessages([...messages, { role: "user", content: value }]);
  tab.rpc("prompt", { message: value }).catch(error => { idle(); showError(error.message); renderMessages(messages); });
};
$("prompt").oninput = controls;
$("prompt").onkeydown = event => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    if (!running && !switching) $("prompt-form").requestSubmit();
  }
};
$("new").onclick = () => go("new_session");
$("menu").onclick = () => openPage("general");
$("back").onclick = () => show("chat");
for (const button of $("pages").querySelectorAll("[data-page]")) button.onclick = () => openPage(button.dataset.page);
$("model").onclick = openModels;
$("filter").oninput = renderModels;
document.onkeydown = event => {
  if ((event.ctrlKey || event.metaKey) && event.key === "," && tab) { event.preventDefault(); openPage("general"); }
  else if (event.key === "Escape" && !$("settings").hidden) show("chat");
};
// deck: what the model deck shows, as the desktop's: the models the tab's accounts give, by
// provider, and the reasoning levels the one in use takes.
let deck = { models: [], levels: [], state: {} };
async function readDeck() {
  const [{ models }, state, { levels }] = await Promise.all([tab.rpc("get_available_models"), tab.rpc("get_state"), tab.rpc("get_available_thinking_levels")]);
  deck = { models, levels, state };
  const at = levels.indexOf(state.thinkingLevel);
  $("meter").replaceChildren(...levels.map((level, i) => Object.assign(node("i"), { className: i <= at && level !== "off" ? "lit" : "" })));
  where = state.model && state.model.provider !== "unknown" ? state.model.id.split("/").at(-1) : "";
  controls();
}
async function openModels() {
  await readDeck();
  $("filter").value = "";
  renderModels();
  $("models").showModal();
}
function renderModels() {
  const query = $("filter").value.trim().toLowerCase(), { model, thinkingLevel } = deck.state, rows = [];
  const pick = (command, params) => tab.rpc(command, params).then(readDeck).then(renderModels).catch(error => showError(error.message));
  $("levels").replaceChildren(...deck.levels.map(level => {
    const chip = Object.assign(node("button", level), { type: "button", className: level === thinkingLevel ? "on" : "" });
    chip.onclick = () => pick("set_thinking_level", { level });
    return chip;
  }));
  let provider = "";
  for (const m of deck.models.filter(m => `${m.provider} ${m.id} ${m.name || ""}`.toLowerCase().includes(query))) {
    if (m.provider !== provider) rows.push(Object.assign(node("p", provider = m.provider), { className: "label" }));
    const row = Object.assign(node("button", m.name || m.id), { type: "button", className: m.provider === model?.provider && m.id === model?.id ? "on" : "" });
    row.onclick = () => pick("set_model", { provider: m.provider, modelId: m.id }).then(() => $("models").close());
    rows.push(row);
  }
  $("list").replaceChildren(...rows);
}
// Settings, as the desktop's: its pages at left, the one shown at right.
const label = text => Object.assign(node("p", text), { className: "label" });
const note = text => Object.assign(node("p", text), { className: "meta" });
function row(name, sub, action) {
  return Object.assign(node("div", node("div", node("strong", name), note(sub)), ...(action ? [action] : [])), { className: "row" });
}
function button(text, onclick, className = "") {
  return Object.assign(node("button", text), { type: "button", onclick, className });
}
// What each plugin does, as agent/assembly's catalog says it.
const about = {
  titles: "Name each session after its first exchange", tasks: "Live session task list and todo tool",
  questions: "Ask the user questions with choices and custom answers", memory: "Bounded persistent remember, recall, replace, and forget tools",
  memtree: "Experimental: zoomable summary tree of each session for lossless compaction or fresh turns",
};
const pages = {
  async general() {
    await readDeck();
    const { model, thinkingLevel } = deck.state;
    return [node("h2", "General"), note("new conversations, keys"), label("new conversations"),
      row("default model", model ? [model.id.split("/").at(-1), thinkingLevel].filter(Boolean).join(" · ") : "none yet", button("change ›", openModels, "plain")),
      label("keys"), ...[["Enter", "send"], ["Shift+Enter", "new line"], ["Ctrl+,", "settings"], ["Esc", "back to the conversations"]].map(([key, what]) => row(key, what))];
  },
  async providers() {
    const counts = new Map();
    for (const m of (await tab.rpc("get_available_models")).models) counts.set(m.provider, (counts.get(m.provider) || 0) + 1);
    return [node("h2", "Providers"), note("the accounts your devices share with this tab"), label("accounts"),
      ...[...counts].map(([provider, n]) => row(provider, `${n} model${n === 1 ? "" : "s"}`)),
      note("Sign in on one of your devices: its accounts reach this tab, sealed to it.")];
  },
  async plugins() {
    const plugins = await tab.plugins();
    return [node("h2", "Plugins"), note("what this tab's Orb runs, from its next conversation"), ...Object.keys(plugins).sort().map(name =>
      row(name, about[name], button(plugins[name] ? "on" : "off", () => tab.plugins(name, !plugins[name]).then(() => openPage("plugins"), error => showError(error.message)), plugins[name] ? "inverted" : "")))];
  },
  async mitosis() {
    const [status, group] = await Promise.all([tab.admin("status"), tab.admin("sync")]);
    return [node("h2", "Mitosis"), note("peer to peer"), label("devices"),
      ...(group.members || []).filter(id => id !== me).map(id => row(group.names?.[id] || id.split(":").at(-1).slice(0, 8),
        [status.peer_states?.[id] === "connected" ? "connected" : "not connected", group.owners?.includes(id) && "owner"].filter(Boolean).join(" · "))),
      label("this tab"), note(me),
      note("Each device is approved by fingerprint on its own screen. Forgetting this tab clears it here; block it on a device to take it out of your group."),
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
// The tab's Orb over IndexedDB, which keeps its identity, received providers, memory and
// conversation across reloads. Starting its Mitosis makes it dial its devices.
async function openTab() {
  // One runtime owns the tab's storage for the page's life: its journals are read once and
  // appended in memory, so a second page writing the same database would overwrite them.
  const held = await new Promise(resolve => navigator.locks.request("orb-tab", { ifAvailable: true }, lock => { resolve(!!lock); return lock && new Promise(() => {}); }));
  if (!held) throw new Error("Your Orb is already open in another tab of this page.");
  await import("./wasm_exec.js");
  await import("./helpers.js");
  const response = await fetch("peer.wasm.gz");
  if (!response.ok) throw new Error(`Wasm download failed: ${response.status}`);
  const replies = new Map(), decoder = new TextDecoder();
  let next = 0;
  const peer = await openPeer({
    wasm: await new Response(response.body.pipeThrough(new DecompressionStream("gzip"))).arrayBuffer(), storage: indexedStorage("orb-tab"),
    env: { ORB_SETTINGS: JSON.stringify({ plugins: { memory: true } }) },
    emit: (array, length) => {
      const frame = JSON.parse(decoder.decode(array.slice(0, length)));
      if (frame.type === "response" && replies.has(frame.id)) { replies.get(frame.id)(frame); replies.delete(frame.id); return; }
      if (frame.type === "extension_ui_request") ask();
      else if (frame.type !== "response") onEvent(frame);
      if (frame.type === "agent_end") settle().catch(error => showError(error.message));
    },
    exit: () => { tab = null; idle(); showError("This tab's Orb stopped. Reload the page."); }, run,
  });
  tab = {
    admin: async (method, params = {}) => JSON.parse(await peer.mitosisAdmin(method, JSON.stringify(params), "")),
    input: async (...args) => JSON.parse(await peer.input(...args)),
    plugins: async (...args) => JSON.parse(await peer.plugins(...args)),
    rpc: (type, params = {}) => new Promise((resolve, reject) => {
      const id = `tab-${++next}`;
      replies.set(id, frame => (frame.success ? resolve(frame.data) : reject(new Error(frame.error))));
      const refused = peer.command(JSON.stringify({ id, type, ...params }));
      if (refused) { replies.delete(id); reject(new Error(refused)); }
    }),
  };
  ({ peer_id: me } = await tab.admin("self"));
  $("fingerprint").textContent = me.split(":").at(-1).slice(0, 8);
  $("self").textContent = me;
  await settle();
  refreshTab();
}
// settle shows the conversation the tab's Orb holds, and gives its chip its name (the titles plugin
// names it) or the first thing said in it; a chip left with nothing said goes.
async function settle() {
  ({ messages } = await tab.rpc("get_messages"));
  const { sessionFile, sessionName } = await tab.rpc("get_state");
  current = sessionFile;
  const title = sessionName || text(messages.find(m => m.role === "user")?.content).trim().split("\n")[0];
  tabs = tabs.filter(t => t.title || t.file === current);
  const shown = tabs.find(t => t.file === current);
  if (shown) shown.title = title;
  else tabs.push({ file: current, title });
  save();
  const last = messages.at(-1);
  showError(last?.role === "assistant" && last.stopReason === "error" ? last.errorMessage : "");
  renderMessages(messages); idle();
  readContext().catch(() => {});
}
// How full the conversation's context is, as the desktop's strip reads it: ten segments, red past
// 80 %.
async function readContext() {
  const share = ((await tab.rpc("get_session_stats")).contextUsage?.percent ?? 0) / 100;
  $("context").hidden = !(share > 0);
  $("context").classList.toggle("alarm", share > .8);
  $("gauge").replaceChildren(...Array.from({ length: 10 }, (_, i) => Object.assign(node("i"), { className: i < Math.round(share * 10) ? "lit" : "" })));
  $("context").lastChild.textContent = `${Math.round(share * 100)}%`;
}
// Provider names only: credentials never leave the tab's Orb. The first provider the device
// shares is its approval: the conversation replaces the fingerprint. Until then the tab asks to
// follow the device's sync group again, since the device may admit it after the tab first asked.
async function refreshTab() {
  try {
    const owner = localStorage.getItem("orb.tab.owner");
    const { models } = await tab.rpc("get_available_models");
    if (owner && !models.length) await tab.admin("sync", { join: owner }).catch(() => {});
    const { model } = await tab.rpc("get_state");
    // A session started before any provider arrived has no model; a new one takes the preferred one.
    if (models.length && (!model || model.provider === "unknown") && !running && !messages.length) {
      await transition(() => tab.rpc("new_session").then(settle));
    }
    if ($("settings").hidden) show(!owner ? "invite" : models.length ? "chat" : "approve");
    await readDeck();
  } catch (error) { showError(error.message); }
  finally { setTimeout(refreshTab, 3000); }
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
    if (!tab) await openTab();
    await tab.admin("join", invitation);
    await tab.admin("trust", { peer_id: invitation.peer_id });
    localStorage.setItem("orb.tab.owner", invitation.peer_id);
    show("approve");
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
if (localStorage.getItem("orb.tab.owner")) openTab().catch(error => showError(error.message));
else {
  show("invite");
  if (linked.startsWith("orb-mitosis:v1:")) join($("code").value = linked);
}
