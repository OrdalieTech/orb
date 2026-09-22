import { RemoteBridge, randomID } from "./bridge.js";
const $ = id => document.getElementById(id);
let worker, loaded = false, running = false, connected = false, pending = null;
let state = { messages: [], files: {} };
let events = [], count = 0;
const remote = new RemoteBridge(message => worker.postMessage(message));
let selected = "", remoteReady = false, remoteBusy = false, receipt = null, selection = 0;
let localDraft = "";
let renderedMessages = null, renderedCount = 0;

function showError(message) {
  $("error").textContent = message || "";
  $("error").hidden = !message;
  if (message) $("status").textContent = "Needs attention";
}
function controls() {
  $("bridge-connect").disabled = !loaded || remoteBusy;
  $("bridge-disconnect").disabled = !remote.connection;
  $("location").disabled = running || remoteBusy;
  $("bridge-agent").disabled = running;
  $("reset").disabled = selected ? !remoteReady || remoteBusy : false;
  $("send").disabled = !loaded || running || (selected && (!remoteReady || remoteBusy || ((!!receipt || !!remote.view?.info.target.execution_id) && !remote.view?.info.input)));
  $("prompt").disabled = !loaded;
  $("configuration").disabled = !loaded || running;
  $("cancel").hidden = selected ? !remoteReady || !remote.view?.info.target.execution_id : !running;
  $("export").disabled = running || !(selected ? remote.view?.messages.length : state.messages.length);
}
function text(content) {
  return typeof content === "string" ? content : (content || []).filter(b => b.type === "text").map(b => b.text).join("\n");
}
function renderMessages(messages, follow = true) {
  if (messages === renderedMessages && messages.length === renderedCount) return;
  renderedMessages = messages; renderedCount = messages.length;
  const transcript = $("transcript"), scroll = transcript.scrollTop;
  const following = transcript.scrollHeight - scroll - transcript.clientHeight < 80;
  const rows = messages.filter(m => ["user", "assistant"].includes(m.role) && text(m.content)).map(message => {
    const row = document.createElement("article");
    row.className = message.role;
    const label = document.createElement("strong");
    label.textContent = message.role === "user" ? "You" : "Orb";
    const body = document.createElement("pre");
    body.textContent = text(message.content);
    row.append(label, body);
    return row;
  });
  $("messages").replaceChildren(...rows);
  if (!rows.length) { const empty = document.createElement("p"); empty.className = "empty"; empty.textContent = "What would you like to try?"; $("messages").append(empty); }
  transcript.scrollTop = follow || following ? transcript.scrollHeight : scroll;
}
function render(snapshot, local = true) {
  const display = { messages: snapshot.messages || [], files: snapshot.files || {} };
  if (local) state = display;
  renderMessages(display.messages, local);
  $("files").replaceChildren(...Object.entries(display.files).sort().map(([path, content]) => {
    const row = document.createElement("details"), label = document.createElement("summary"), body = document.createElement("pre");
    label.textContent = path;
    body.textContent = content;
    row.append(label, body);
    return row;
  }));
  if (!Object.keys(display.files).length) $("files").textContent = "No files yet.";
  $("stream").hidden = true;
}
function clearEvents() {
  events = []; count = 0;
  $("count").textContent = "0 events";
  $("events").textContent = "No events yet.";
}
function idle() {
  running = false;
  $("status").textContent = "Ready";
  controls();
}
function prompt(value) {
  pending = null;
  $("prompt").value = "";
  $("settings-panel").open = false;
  renderMessages([...state.messages, { role: "user", content: value }]);
  $("stream").hidden = true;
  $("status").textContent = "Waiting for reply…";
  worker.postMessage({ type: "prompt", text: value, agentCalls: $("bridge-agent").checked });
}
function boot() {
  remote.reset(); selected = ""; remoteReady = false; selection++; receipt = null;
  $("location").value = ""; $("remote-controls").hidden = true; $("bridge-status").textContent = "Disconnected";
  worker?.terminate();
  loaded = running = connected = false; pending = null;
  $("status").textContent = "Loading…";
  controls(); showError(""); render({}); clearEvents();
  const current = worker = new Worker("worker.js");
  current.onerror = event => { loaded = running = false; controls(); showError(`${event.message}. Click New chat to restart.`); };
  current.onmessage = ({ data }) => {
    if (worker !== current) return;
    const message = JSON.parse(data);
    if (message.type.startsWith("bridge.")) {
      remote.receive(message);
      if (!remote.connection && message.type === "bridge.closed") {
        remoteReady = false; $("bridge-status").textContent = "Disconnected"; controls();
        if (selected) showError("Bridge disconnected. Reconnect to continue; remote execution is unaffected.");
      }
      return;
    }
    switch (message.type) {
      case "boot": loaded = true; idle(); break;
      case "ready":
        connected = true;
        render(message); clearEvents();
        if (pending !== null) prompt(pending);
        else idle();
        break;
      case "event": {
        const event = message.event;
        count++; events.push(event); if (events.length > 100) events.shift();
        $("count").textContent = `${count} events`;
        if ($("event-panel").open) renderEvents();
        if (event.type === "tool_execution_start") $("status").textContent = `Using ${event.toolName}…`;
        if (event.type === "message_update") {
          const transcript = $("transcript");
          const following = transcript.scrollHeight - transcript.scrollTop - transcript.clientHeight < 80;
          const reply = text(event.message.content);
          $("stream").textContent = reply;
          $("stream").hidden = !reply;
          if (following) transcript.scrollTop = transcript.scrollHeight;
          if (reply) $("status").textContent = "Replying…";
        }
        break;
      }
      case "settled": render(message); idle(); showError(message.error); $("prompt").focus(); break;
      case "error":
        if (pending !== null) { $("settings-panel").open = true; pending = null; }
        idle(); showError(message.error); break;
      case "fatal": loaded = running = false; controls(); showError(`${message.error}. Click New chat to restart.`); break;
    }
  };
}
function config() {
  return { api: $("provider").value, model: $("model").value.trim(), baseURL: $("endpoint").value.trim(), apiKey: $("key").value.trim() };
}
function configure() {
  connected = false;
  const value = config();
  $("model-label").textContent = value.model || "Configure a provider";
}
$("settings").oninput = configure;
$("settings").onsubmit = event => event.preventDefault();
$("provider").onchange = () => {
  const api = $("provider").value;
  $("endpoint").value = api === "openrouter" ? "https://openrouter.ai/api/v1" : api === "anthropic-messages" ? "https://api.anthropic.com" : "https://api.openai.com/v1";
  $("model").value = api === "openrouter" ? "openai/gpt-5.6-luna" : "";
  $("key").value = "";
  $("hint").textContent = "Enter your model and API key, then send. Keys stay in this tab. The endpoint must allow browser requests (CORS). Changing settings starts a new conversation.";
  configure();
};
$("prompt-form").onsubmit = event => {
  event.preventDefault();
  if (!loaded || running) return;
  const value = $("prompt").value;
  if (!value.trim() || new TextEncoder().encode(value).length > 16384) { showError("Enter a message of at most 16 KiB."); return; }
  if (selected) {
    const input = remote.view?.info.input;
    const args = input ? { id: input.id, execution_id: remote.view.info.target.execution_id, value } : { text: value };
    remoteCommand(input ? "input.reply" : "prompt", args).then(ok => { if (ok) $("prompt").value = ""; });
    return;
  }
  const settings = config();
  if (!connected) {
    for (const [id, message] of [["model", "Enter a model ID in Model settings."], ["key", "Enter an API key in Model settings. Your terminal login isn't available in the browser."], ["endpoint", "Enter an HTTP(S) API base URL."]]) {
      if (!$(id).value.trim() || (id === "endpoint" && !/^https?:\/\//.test(settings.baseURL))) {
        $("settings-panel").open = true; showError(message); $(id).focus(); return;
      }
    }
  }
  running = true; controls(); showError("");
  if (!connected) { pending = value; $("status").textContent = "Starting…"; worker.postMessage({ type: "start", config: settings }); }
  else prompt(value);
};
$("prompt").onkeydown = event => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    if (!running) $("prompt-form").requestSubmit();
  }
};
function renderEvents() { $("events").textContent = events.map(event => JSON.stringify(event)).join("\n"); }
$("event-panel").addEventListener("toggle", renderEvents);
$("cancel").onclick = () => { if (selected) { remoteCommand("cancel", { execution_id: remote.view?.info.target.execution_id }); return; } worker.postMessage({ type: "cancel" }); $("status").textContent = "Stopping…"; };
$("reset").onclick = () => selected ? remoteCommand("session.new") : boot();
$("export").onclick = () => {
  const url = URL.createObjectURL(new Blob([JSON.stringify(selected ? { messages: remote.view?.messages || [] } : state, null, 2)], { type: "application/json" }));
  const link = document.createElement("a"); link.href = url; link.download = "orb-browser-debug.json"; link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
};
function browserIdentity() {
  const saved = localStorage.getItem("orb.bridge.identity");
  if (saved) return JSON.parse(saved);
  const identity = { seed: btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(32)))).replaceAll("=", ""), agentID: randomID() };
  localStorage.setItem("orb.bridge.identity", JSON.stringify(identity));
  return identity;
}
async function refreshInstances() {
  const items = await remote.instances();
  $("location").replaceChildren(new Option("Local · this browser", ""), ...items.filter(i => i.available).map(i => new Option(i.alias || i.instance_id, i.instance_id)));
  if (selected && !items.some(i => i.instance_id === selected && i.available)) { selectConversation(""); showError("Remote instance is no longer available"); }
  $("location").value = selected;
  $("remote-controls").hidden = false;
  $("bridge-status").textContent = "Connected";
  $("bridge-notice").textContent = "Connected. Choose a remote conversation, or keep working locally.";
}
async function refreshSessions() {
  const instance = selected, version = selection;
  if (!instance || !remoteReady || !remote.view.info.methods.includes("session.list")) return;
  try {
    const items = await remote.sessions(instance);
    if (version !== selection) return;
    $("remote-sessions").replaceChildren(...items.map(i => new Option(i.name || i.session_id, i.session_id)));
    $("remote-sessions").value = remote.view.info.target.session_id;
  } catch (error) { if (version === selection) showError(error.message); }
}
function selectConversation(instance) {
  if (!selected) localDraft = $("prompt").value;
  if (instance) { $("settings-panel").open = false; $("bridge-panel").open = false; }
  selected = instance; selection++; receipt = null; remoteReady = false; remote.view = null;
  $("location").value = instance;
  $("remote-sessions-label").hidden = !instance;
  $("prompt").value = instance ? "" : localDraft;
  $("remote-question").hidden = true;
  showError(""); clearEvents();
  render(instance ? {} : state, false);
  $("status").textContent = instance ? "Loading remote session…" : "Ready";
  controls();
}
async function remoteCommand(method, args = {}) {
  if (!remoteReady || remoteBusy) return false;
  const version = selection;
  remoteBusy = true; controls(); showError("");
  try {
    const accepted = await remote.command(method, args);
    if (version === selection) { receipt = accepted; $("status").textContent = `Remote · ${accepted.status}`; }
    return true;
  } catch (error) {
    if (version === selection) showError(`${error.message}. The command was not retried automatically.`);
    return false;
  } finally { remoteBusy = false; controls(); }
}
async function pollRemote() {
  const instance = selected, version = selection;
  try {
    if (!instance || !remote.connection) return;
    const previousSession = remote.view?.info.target.session_id;
    const view = await remote.refresh(instance);
    if (version !== selection) return;
    const first = !remoteReady;
    remoteReady = true; showError("");
    render({ messages: view.messages }, false);
    $("stream").textContent = text(view.partial?.content); $("stream").hidden = !$("stream").textContent;
    $("remote-question").textContent = view.info.input ? JSON.stringify(view.info.input.presentation || view.info.input, null, 2) : "";
    $("remote-question").hidden = !view.info.input;
    $("status").textContent = view.info.input ? "Remote · reply to the pending question" : view.info.target.execution_id ? "Remote · running" : "Remote · ready";
    if (receipt) {
      const result = await remote.call("operations.get", { instance_id: instance, operation_id: receipt.operation_id });
      if (version !== selection) return;
      if (["failed", "cancelled", "outcome_unknown"].includes(result.status)) showError(`Remote operation ${result.status}${result.error ? ": " + result.error : ""}`);
      if (!["accepted", "running"].includes(result.status)) receipt = null;
    }
    if (first || previousSession !== view.info.target.session_id) await refreshSessions();
  } catch (error) {
    if (version === selection) { remoteReady = false; if (["unauthorized", "not_found"].includes(error.message)) { render({}, false); remote.view = null; } showError(error.message); }
  } finally { controls(); setTimeout(pollRemote, 700); }
}
$("bridge-connect").onclick = async () => {
  remoteBusy = true; controls();
  try {
    const invitation = JSON.parse($("bridge-invitation").value || localStorage.getItem("orb.bridge.remote") || "{}");
    const identity = browserIdentity();
    const result = await remote.connect(invitation, identity);
    localStorage.setItem("orb.bridge.remote", JSON.stringify({ locator: invitation.locator, peer_id: invitation.peer_id }));
    $("bridge-invitation").value = "";
    $("bridge-identity").textContent = `Browser identity: ${result.peer_id}\nLocal agent: ${result.agent_id}`;
    $("bridge-grant").textContent = JSON.stringify({ principal: { peer_id: result.peer_id, subject: { kind: "instance", instance_id: result.agent_id } }, group_id: "*", include_future: true, permissions: ["instance.inspect", "instance.prompt"] }, null, 2);
    $("bridge-status").textContent = "Awaiting approval";
    $("bridge-notice").textContent = "Approve this browser identity on the remote Bridge, then click Refresh.";
    $("remote-controls").hidden = false;
    try { await refreshInstances(); } catch (error) { if (error.message !== "unauthorized") throw error; }
  } catch (error) { $("bridge-notice").textContent = error.message; }
  finally { remoteBusy = false; controls(); }
};
$("bridge-disconnect").onclick = async () => { try { await remote.disconnect(); selectConversation(""); $("remote-controls").hidden = true; $("bridge-status").textContent = "Disconnected"; } catch (error) { showError(error.message); } controls(); };
$("bridge-refresh").onclick = () => refreshInstances().then(refreshSessions).catch(error => showError(error.message));
$("location").onchange = () => selectConversation($("location").value);
$("remote-sessions").onchange = () => remoteCommand("session.switch", { session_id: $("remote-sessions").value });
configure();
boot();
pollRemote();
