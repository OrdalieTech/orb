// Shared pieces of the Worker end-to-end checks: the scripted model served
// over local HTTP, dev-server lifecycle for workerd and Celld, the RPC
// transports, and frame assertions.
import { spawn } from "node:child_process";
import { once } from "node:events";
import http from "node:http";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

export const PATH = "notes/hello.txt";
export const CONTENT = "persisted across restarts";
export const MODELS = baseUrl => JSON.stringify({
  providers: {
    fake: {
      baseUrl, api: "openai-completions", apiKey: "FAKE_MODEL_KEY",
      models: [{ id: "scripted", name: "Scripted", contextWindow: 128000, maxTokens: 4096, input: ["text"] }],
    },
  },
});
// bridge-agent-calls gives the object's agent bridge_call (the Bridge scenario).
export const SETTINGS = JSON.stringify({ defaultProvider: "fake", defaultModel: "scripted", plugins: { "bridge-agent-calls": true } });

export function fail(message) {
  throw new Error(message);
}

export function check(condition, message, detail) {
  if (!condition) fail(`${message}${detail === undefined ? "" : `\n${typeof detail === "string" ? detail : JSON.stringify(detail, null, 2)}`}`);
}

// --- scripted model over local HTTP ---------------------------------------
await import(join(here, "fake-model.js"));
export const modelStats = { requests: 0 };
export async function startModel() {
  const server = http.createServer(async (req, res) => {
    modelStats.requests++;
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const response = await globalThis.orbFakeModel.handle(
      new Request(`http://127.0.0.1${req.url}`, { method: req.method, headers: { "Content-Type": "application/json" }, body: Buffer.concat(chunks) }),
    );
    res.writeHead(response.status, Object.fromEntries(response.headers));
    for await (const chunk of response.body) res.write(chunk);
    res.end();
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  return server;
}

// --- dev servers -----------------------------------------------------------
export async function freePort() {
  const server = http.createServer().listen(0, "127.0.0.1");
  await once(server, "listening");
  const { port } = server.address();
  server.close();
  return port;
}

export function launch(runtime, port, fresh, { dir, celld }) {
  const command =
    runtime === "celld"
      ? [celld, ["dev", dir, "--port", String(port), "--no-watch", ...(fresh ? ["--clean"] : [])]]
      : [join(dir, "node_modules", ".bin", "wrangler"), ["dev", "--port", String(port), "--ip", "127.0.0.1", "--persist-to", join(dir, ".e2e-state"), "--show-interactive-dev-session=false"]];
  const child = spawn(command[0], command[1], { cwd: dir, detached: true, stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, NO_COLOR: "1" } });
  child.log = "";
  const collect = chunk => {
    child.log += chunk;
    if (process.env.ORB_E2E_VERBOSE) process.stderr.write(chunk);
  };
  child.stdout.on("data", collect);
  child.stderr.on("data", collect);
  return child;
}

export async function stop(child) {
  if (child.exitCode !== null) return;
  const exited = once(child, "exit");
  process.kill(-child.pid, "SIGINT");
  const timer = setTimeout(() => process.kill(-child.pid, "SIGKILL"), 10000);
  await exited;
  clearTimeout(timer);
}

export async function waitHealthy(base, child) {
  const deadline = Date.now() + 180000;
  while (Date.now() < deadline) {
    if (child && child.exitCode !== null) fail(`dev server exited early:\n${child.log}`);
    try {
      const response = await fetch(`${base}/health`);
      if (response.ok) return;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 250));
  }
  fail(`dev server did not become healthy:\n${child?.log ?? ""}`);
}

// --- transports ------------------------------------------------------------
export function headers(token) {
  return { Authorization: `Bearer ${token}` };
}

// overSocket sends one command and collects frames until the run settles.
export async function overSocket(base, agent, token, command) {
  const socket = new WebSocket(`${base.replace(/^http/, "ws")}/agents/${agent}/rpc`, { headers: headers(token) });
  const frames = [];
  const started = performance.now();
  let firstFrameMs;
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`WebSocket run timed out; frames: ${JSON.stringify(frames.slice(-5))}`)), 120000);
    socket.onopen = () => socket.send(JSON.stringify(command));
    socket.onerror = event => reject(new Error(`WebSocket error: ${event.message ?? event.type}`));
    socket.onclose = event => reject(new Error(`WebSocket closed early: ${event.code} ${event.reason}`));
    socket.onmessage = event => {
      firstFrameMs ??= performance.now() - started;
      const frame = JSON.parse(event.data);
      frames.push(frame);
      if (frame.type === "worker_error") reject(new Error(frame.error));
      if (frame.type === "agent_settled" || (frame.type === "response" && frame.id === command.id && !frame.success)) {
        clearTimeout(timer);
        resolve();
      }
    };
  });
  socket.onclose = null;
  socket.close();
  return { frames, firstFrameMs, totalMs: performance.now() - started };
}

// overHTTP posts NDJSON commands and reads frames until the stream ends.
export async function overHTTP(base, agent, token, commands) {
  const started = performance.now();
  const response = await fetch(`${base}/agents/${agent}/rpc`, {
    method: "POST", headers: { ...headers(token), "Content-Type": "application/x-ndjson" },
    body: commands.map(command => JSON.stringify(command)).join("\n"),
  });
  check(response.ok, `POST /rpc answered ${response.status}`, await response.clone().text());
  check(response.headers.get("content-type")?.startsWith("application/x-ndjson"), "POST /rpc is not NDJSON");
  let firstFrameMs;
  let buffer = "";
  const frames = [];
  const decoder = new TextDecoder();
  for await (const chunk of response.body) {
    firstFrameMs ??= performance.now() - started;
    buffer += decoder.decode(chunk, { stream: true });
    let end;
    while ((end = buffer.indexOf("\n")) >= 0) {
      frames.push(JSON.parse(buffer.slice(0, end)));
      buffer = buffer.slice(end + 1);
    }
  }
  check(buffer === "", "stream ended mid-frame", buffer);
  return { frames, firstFrameMs, totalMs: performance.now() - started };
}

export async function stats(base, agent, token) {
  const response = await fetch(`${base}/agents/${agent}/stats`, { headers: headers(token) });
  check(response.ok, `stats answered ${response.status}`, await response.clone().text());
  return response.json();
}

// --- assertions ------------------------------------------------------------
export const response = (frames, id) => frames.find(frame => frame.type === "response" && frame.id === id);

export function toolResults(frames) {
  return frames
    .filter(frame => frame.type === "tool_execution_end")
    .map(frame => ({ tool: frame.toolName, error: frame.isError, text: (frame.result?.content ?? []).map(part => part.text ?? "").join("") }));
}

export function finalText(frames) {
  const ends = frames.filter(frame => frame.type === "message_end" && frame.message?.role === "assistant");
  return (ends.at(-1)?.message?.content ?? []).filter(part => part.type === "text").map(part => part.text).join("");
}

export function checkRun(frames, id, tools) {
  check(response(frames, id)?.success, `${id} was not accepted`, frames.filter(frame => frame.type === "response"));
  const results = toolResults(frames);
  check(JSON.stringify(results.map(result => result.tool)) === JSON.stringify(tools), `tools ran ${results.map(result => result.tool)}`, results);
  check(results.every(result => !result.error), "a tool failed", results);
  check(results.at(-1).text.includes(CONTENT), "read did not return the written file", results);
  check(finalText(frames) === `read back: ${CONTENT}`, `final answer was ${JSON.stringify(finalText(frames))}`);
  check(frames.at(-1).type === "agent_settled", "the run did not settle last", frames.at(-1));
}

