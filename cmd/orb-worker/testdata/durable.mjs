// Runs the real Worker bundle (wasm_exec.js + fake-model.js + worker.mjs) and
// orb.wasm under Node with Map-backed Durable Object storage: a scripted
// write/read turn over HTTP NDJSON, then a fresh object over the same storage
// must resume the session and the file. Go's net/http uses fetch only when
// process.argv0 does not start with "node", so the Go test sets argv0.
import { registerHooks } from "node:module";
import { pathToFileURL } from "node:url";

registerHooks({
  load(url, context, nextLoad) {
    if (!url.endsWith(".wasm")) return nextLoad(url, context);
    // Workers import a .wasm file as a compiled WebAssembly.Module.
    const source = `export default await WebAssembly.compile((await import("node:fs")).readFileSync(new URL(${JSON.stringify(url)})));`;
    return { format: "module", source, shortCircuit: true };
  },
});

const { default: worker, OrbAgent } = await import(pathToFileURL(process.argv[2]).href);

function storage() {
  const map = new Map();
  const later = value => new Promise(resolve => setTimeout(() => resolve(value), 0));
  return {
    map,
    get: keys => later(new Map(keys.filter(key => map.has(key)).map(key => [key, structuredClone(map.get(key))]))),
    put: entries => later().then(() => Object.entries(entries).forEach(([key, value]) => map.set(key, structuredClone(value)))),
    delete: keys => later().then(() => keys.filter(key => map.delete(key)).length),
    list: ({ prefix = "" } = {}) => later(new Map([...map.keys()].sort().filter(key => key.startsWith(prefix)).map(key => [key, structuredClone(map.get(key))]))),
  };
}

const token = "durable-harness-token-0123456789";
const models = { providers: { fake: { baseUrl: "https://fake-model.invalid/v1", api: "openai-completions", apiKey: "FAKE_MODEL_KEY",
  models: [{ id: "scripted", name: "Scripted", contextWindow: 128000, maxTokens: 4096, input: ["text"] }] } } };
const stored = new Map();
let objects = new Map();
const env = {
  ORB_TOKEN: token, FAKE_MODEL_KEY: "fake-key", ORB_MODELS: JSON.stringify(models),
  ORB_SETTINGS: JSON.stringify({ defaultProvider: "fake", defaultModel: "scripted" }),
  ORB_AGENT: {
    idFromName: name => name,
    get: name => ({
      fetch: request => {
        if (!stored.has(name)) stored.set(name, storage());
        if (!objects.has(name)) objects.set(name, new OrbAgent({ storage: stored.get(name), getWebSockets: () => [] }, env));
        return objects.get(name).fetch(request);
      },
    }),
  },
};

function check(condition, message, detail) {
  if (!condition) throw new Error(`${message}${detail === undefined ? "" : `\n${JSON.stringify(detail, null, 2)}`}`);
}

async function call(path, init = {}) {
  return worker.fetch(new Request(`https://orb.test${path}`, { ...init, headers: { Authorization: `Bearer ${token}`, ...init.headers } }), env);
}

async function rpc(agent, ...commands) {
  const response = await call(`/agents/${agent}/rpc`, { method: "POST", body: commands.map(command => JSON.stringify(command)).join("\n") });
  check(response.status === 200, `rpc answered ${response.status}`, await response.clone().text());
  return (await response.text()).split("\n").filter(Boolean).map(line => JSON.parse(line));
}

const reply = (frames, id) => frames.find(frame => frame.type === "response" && frame.id === id);
const tools = frames => frames.filter(frame => frame.type === "tool_execution_end").map(frame => [frame.toolName, frame.isError, frame.result.content.map(part => part.text).join("")]);
const answer = frames => frames.filter(frame => frame.type === "message_end" && frame.message.role === "assistant").at(-1)?.message.content.map(part => part.text ?? "").join("");

check((await worker.fetch(new Request("https://orb.test/health"), env)).status === 200, "health");
check((await worker.fetch(new Request("https://orb.test/agents/a/rpc", { method: "POST" }), env)).status === 401, "an unauthenticated call was served");
check((await call("/agents/a/bridge")).status === 501, "the Bridge route is not reserved");

const first = await rpc("a", { id: "p1", type: "prompt", message: "orb-e2e write notes/hello.txt persisted in durable storage" });
check(reply(first, "p1")?.success, "prompt was refused", first.filter(frame => frame.type === "response"));
check(JSON.stringify(tools(first).map(([name, error]) => [name, error])) === '[["write",false],["read",false]]', "write then read did not run", tools(first));
check(answer(first) === "read back: persisted in durable storage", "wrong answer", answer(first));
check(first.at(-1).type === "agent_settled", "the stream ended before the run settled", first.at(-1));
const before = await (await call("/agents/a/stats")).json();
check(before.wasmMemoryBytes > 0 && before.go.heapAlloc > 0, "stats are missing", before);
check([...stored.get("a").map.values()].every(value => value instanceof Uint8Array), "stored a non-binary value");

objects = new Map();
const messages = reply(await rpc("a", { id: "m", type: "get_messages" }), "m")?.data.messages ?? [];
check(messages.length === 7 && messages[1].role === "user", "the restarted object lost its history", messages.map(message => message.role));
const resumed = await rpc("a", { id: "p2", type: "prompt", message: "orb-e2e read notes/hello.txt" });
check(JSON.stringify(tools(resumed).map(([name, error]) => [name, error])) === '[["read",false]]' && tools(resumed)[0][2].includes("persisted in durable storage"), "the file did not survive", tools(resumed));
check(answer(resumed) === "read back: persisted in durable storage", "wrong answer after restart", answer(resumed));
const after = await (await call("/agents/a/stats")).json();
check(after.bootId !== before.bootId, "the object was not restarted");
const other = await rpc("b", { id: "s", type: "get_state" });
check(reply(other, "s")?.data.messageCount === 0, "objects share a session", other);
console.log(`durable harness OK: wasm memory ${after.wasmMemoryBytes} bytes, Go heap ${after.go.heapAlloc} bytes`);
// The Go runtimes keep timers pending, which would hold the event loop open.
process.exit(0);
