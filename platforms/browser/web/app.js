import { indexedStorage, openPeer } from "./peer.js";
const $ = id => document.getElementById(id);
// tab: this tab's own Orb (peer.wasm), one of the user's devices; word: what it does now; where: its model.
let tab = null, me = "", running = false, messages = [], streamed = "", word = "", where = "";

function showError(message) {
  $("error").textContent = message || "";
  $("error").hidden = !message;
}
// show picks the one screen: the invitation, its approval, or the conversation; an error reads
// at the end of what it concerns, as the apps note one.
function show(screen) {
  const opening = screen === "chat" && $("chat").hidden;
  $(screen === "chat" ? "transcript" : screen).append($("error"));
  $("pair").hidden = screen === "chat";
  $("chat").hidden = screen !== "chat";
  $("invite").hidden = screen !== "invite";
  $("approve").hidden = screen !== "approve";
  // A hidden transcript has no height to scroll: it opens at its end once shown.
  if (opening) $("transcript").scrollTop = $("transcript").scrollHeight;
}
function controls() {
  $("status").textContent = word || where;
  $("send").textContent = running ? "■" : "↑";
  $("send").classList.toggle("stop", running);
  $("send").disabled = !running && !$("prompt").value.trim();
  $("reset").disabled = running;
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
// What the person said sits at right on a soft ground; Orb just speaks, full width.
function renderMessages(list) {
  const transcript = $("transcript");
  $("messages").replaceChildren(...list.filter(m => ["user", "assistant"].includes(m.role) && text(m.content)).map(message => {
    const row = message.role === "user" ? node("p", text(message.content)) : node("div", ...markdown(text(message.content)));
    row.className = message.role;
    return row;
  }));
  transcript.scrollTop = transcript.scrollHeight;
}
function idle() {
  running = false; word = "";
  $("stream").hidden = true;
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
$("prompt-form").onsubmit = event => {
  event.preventDefault();
  if (!tab) return;
  if (running) { tab.rpc("abort").catch(error => showError(error.message)); return; }
  const value = $("prompt").value;
  if (!value.trim()) return;
  running = true; word = "waiting"; showError("");
  $("prompt").value = "";
  controls();
  renderMessages([...messages, { role: "user", content: value }]);
  tab.rpc("prompt", { message: value }).catch(error => { idle(); showError(error.message); renderMessages(messages); });
};
$("prompt").oninput = controls;
$("prompt").onkeydown = event => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    if (!running) $("prompt-form").requestSubmit();
  }
};
$("reset").onclick = () => tab.rpc("new_session").then(settle, error => showError(error.message));
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
      if (frame.type !== "response") onEvent(frame);
      if (frame.type === "agent_end") settle().catch(error => showError(error.message));
    },
    exit: () => { tab = null; idle(); showError("This tab's Orb stopped. Reload the page."); },
  });
  tab = {
    admin: async (method, params = {}) => JSON.parse(await peer.mitosisAdmin(method, JSON.stringify(params), "")),
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
async function settle() {
  ({ messages } = await tab.rpc("get_messages"));
  const last = messages.at(-1);
  showError(last?.role === "assistant" && last.stopReason === "error" ? last.errorMessage : "");
  renderMessages(messages); idle();
}
// Provider names only: credentials never leave the tab's Orb. The first provider the device
// shares is its approval: the conversation replaces the fingerprint. Until then the tab asks to
// follow the device's sync group again, since the device may admit it after the tab first asked.
async function refreshTab() {
  try {
    const owner = localStorage.getItem("orb.tab.owner");
    const { models } = await tab.rpc("get_available_models");
    if (owner && !models.length) await tab.admin("sync", { join: owner }).catch(() => {});
    let { model } = await tab.rpc("get_state");
    // A session started before any provider arrived has no model; a new one takes the preferred one.
    if (models.length && (!model || model.provider === "unknown") && !running && !messages.length) {
      await tab.rpc("new_session");
      ({ model } = await tab.rpc("get_state"));
    }
    show(!owner ? "invite" : models.length ? "chat" : "approve");
    where = model && model.provider !== "unknown" ? model.id.split("/").at(-1) : "";
    controls();
  } catch (error) { showError(error.message); }
  finally { setTimeout(refreshTab, 3000); }
}
$("join").onclick = async () => {
  $("join").disabled = true; showError("");
  try {
    // The code orb mitosis shows (orb-mitosis:v1:<base64url JSON>), or the invitation JSON itself.
    const value = $("code").value.trim(), code = value.split("orb-mitosis:v1:")[1]?.split(/\s/)[0];
    const invitation = JSON.parse(code ? atob(code.replace(/-/g, "+").replace(/_/g, "/")) : value || "{}");
    if (!invitation.peer_id || !invitation.token) throw new Error("Paste the code orb mitosis shows.");
    if (!tab) await openTab();
    await tab.admin("join", invitation);
    await tab.admin("trust", { peer_id: invitation.peer_id });
    localStorage.setItem("orb.tab.owner", invitation.peer_id);
    show("approve");
  } catch (error) { showError(error.message); }
  finally { $("join").disabled = false; }
};
if (localStorage.getItem("orb.tab.owner")) openTab().catch(error => showError(error.message));
else show("invite");
