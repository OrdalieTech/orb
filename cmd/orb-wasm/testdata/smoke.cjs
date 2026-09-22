const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const pending = [], received = [];
// Exercise the real provider's Fetch path in a browser-like context without Node's filesystem/process.
const sandbox = vm.createContext({
  crypto: require("node:crypto").webcrypto, performance, TextEncoder, TextDecoder,
  WebAssembly, Uint8Array, Uint8ClampedArray, ArrayBuffer, setTimeout, clearTimeout, console, Headers, AbortController, WebSocket,
  postMessage(raw) {
    const message = JSON.parse(raw);
    received.push(message);
    const index = pending.findIndex(waiter => waiter.type === message.type);
    if (index >= 0) pending.splice(index, 1)[0].resolve(message);
  },
  async fetch(url, options) {
    assert.equal(url, "https://fixture.invalid/chat/completions");
    assert.equal(options.headers.get("Authorization"), "Bearer test-key");
    const request = JSON.parse(new TextDecoder().decode(options.body));
    const last = request.messages.at(-1);
    let delta, reason;
    if (last.role === "tool") {
      delta = { content: "Tool completed." }; reason = "stop";
    } else {
      delta = { role: "assistant", tool_calls: [{ index: 0, id: "write-1", type: "function", function: { name: "write", arguments: JSON.stringify({ path: "note.txt", content: typeof last.content === "string" ? last.content : last.content.map(block => block.text || "").join("") }) } }] };
      reason = "tool_calls";
    }
    const frame = { id: "fixture", object: "chat.completion.chunk", model: "test-model", choices: [{ index: 0, delta, finish_reason: reason }] };
    return new Response(`data: ${JSON.stringify(frame)}\n\ndata: [DONE]\n\n`, { headers: { "Content-Type": "text/event-stream" } });
  }
});
vm.runInContext(fs.readFileSync(process.argv[2], "utf8"), sandbox);
function next(type) { return new Promise(resolve => pending.push({ type, resolve })); }
function send(value) { return sandbox.orbDispatch(JSON.stringify(value)); }
const config = { api: "openrouter", model: "test-model", baseURL: "https://fixture.invalid", apiKey: "test-key" };
const deadline = setTimeout(() => { console.error("Wasm lifecycle timed out"); process.exit(1); }, 20000);
(async () => {
  const boot = next("boot");
  const go = new sandbox.Go();
  const { instance } = await WebAssembly.instantiate(fs.readFileSync(process.argv[3]), go.importObject);
  go.run(instance).catch(error => { console.error(error); process.exit(1); });
  await boot;
  assert.match(send({ type: "prompt", text: "too soon" }), /start a session/);
  assert.match(send({ type: "start", config: { api: "demo" } }), /unsupported/);
  let ready = next("ready");
  assert.equal(send({ type: "start", config }), "");
  await ready;
  let settled = next("settled");
  assert.equal(send({ type: "prompt", text: "Wasm <script> stays text" }), "");
  assert.match(send({ type: "prompt", text: "overlap" }), /already running/);
  const result = await settled;
  assert.equal(result.error, null);
  assert.equal(result.files["/workspace/note.txt"], "Wasm <script> stays text");
  assert.deepEqual(received.filter(m => m.type === "event" && m.event.type === "tool_execution_end").map(m => m.event.toolName), ["write"]);
  assert.ok(result.messages.some(m => m.role === "assistant" && m.stopReason === "stop"));
  assert.match(send({ type: "prompt", text: " " }), /prompt must/);
  settled = next("settled");
  assert.equal(send({ type: "prompt", text: "cancel this immediately" }), "");
  assert.equal(send({ type: "cancel" }), "");
  const cancelled = await settled;
  assert.ok(cancelled.messages.some(m => m.role === "assistant" && m.stopReason === "aborted"));
  ready = next("ready");
  assert.equal(send({ type: "start", config }), "");
  assert.deepEqual((await ready).files, {});
  const fixture = JSON.parse(process.env.ORB_TEST_BRIDGE);
  async function rpc(type, fields = {}) {
    const reply = next("bridge.result");
    const id = require("node:crypto").randomBytes(16).toString("base64url");
    assert.equal(send({ type, id, ...fields }), "");
    const result = await reply;
    assert.equal(result.id, id);
    return result;
  }
  const connected = await rpc("bridge.connect", fixture);
  assert.equal(connected.error, "");
  const connection = connected.result.connection;
  const catalog = await rpc("bridge.call", { connection, method: "instances.list", params: {} });
  assert.equal(catalog.error, "");
  assert.equal(catalog.result.items[0].instance_id, fixture.instance);
  const inspect = await rpc("bridge.call", { connection, method: "instances.describe", params: { instance_id: fixture.instance } });
  assert.equal(inspect.result.wasm_bridge, true);
  const stale = await rpc("bridge.call", { connection: "wrong", method: "instances.list", params: {} });
  assert.equal(stale.error, "unavailable");
  const blocked = await rpc("bridge.call", { connection, method: "status", params: {} });
  assert.equal(blocked.error, "method_not_found");
  const disconnected = await rpc("bridge.disconnect", { connection });
  assert.equal(disconnected.error, "");
  const reconnected = await rpc("bridge.connect", fixture);
  assert.equal(reconnected.error, "");
  assert.notEqual(reconnected.result.connection, connection);
  assert.equal((await rpc("bridge.disconnect", { connection: reconnected.result.connection })).error, "");
  clearTimeout(deadline);
  console.log("Wasm lifecycle OK: API Fetch, tools, events, overlap, cancellation, reset, live WebSocket Bridge, authorization, reconnect");
  process.exit(0);
})().catch(error => { console.error(error); process.exit(1); });
