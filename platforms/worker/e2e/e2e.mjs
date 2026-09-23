// End-to-end check of the Orb Worker in a real runtime: workerd (wrangler dev),
// Celld (celld dev) or a deployed Worker. A scripted model makes the agent
// write then read a file over the WebSocket transport; after a restart the
// HTTP NDJSON transport must show the same session history and file.
//
//   node e2e.mjs --runtime workerd --dir .tools/worker
//   node e2e.mjs --runtime celld --dir .tools/worker --celld .tools/bin/celld
//   ORB_TOKEN=... node e2e.mjs --runtime remote --url https://orb-do-e2e.<account>.workers.dev --phase write|verify
//   node e2e.mjs --configure-deploy .tools/worker-e2e --name orb-do-e2e   (writes its wrangler.json)
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { once } from "node:events";
import { readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import http from "node:http";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { gzipSync } from "node:zlib";

const here = dirname(fileURLToPath(import.meta.url));
const { values: args } = parseArgs({
  options: {
    runtime: { type: "string" },
    dir: { type: "string" },
    celld: { type: "string", default: "celld" },
    url: { type: "string" },
    phase: { type: "string", default: "all" },
    agent: { type: "string" },
    "configure-deploy": { type: "string" },
    name: { type: "string", default: "orb-do-e2e" },
  },
});

if (args.dir) args.dir = resolve(args.dir);
if (args.celld.includes("/")) args.celld = resolve(args.celld);

const PATH = "notes/hello.txt";
const CONTENT = "persisted across restarts";
const MODELS = baseUrl => JSON.stringify({
  providers: {
    fake: {
      baseUrl, api: "openai-completions", apiKey: "FAKE_MODEL_KEY",
      models: [{ id: "scripted", name: "Scripted", contextWindow: 128000, maxTokens: 4096, input: ["text"] }],
    },
  },
});
const SETTINGS = JSON.stringify({ defaultProvider: "fake", defaultModel: "scripted" });

function fail(message) {
  throw new Error(message);
}

function check(condition, message, detail) {
  if (!condition) fail(`${message}${detail === undefined ? "" : `\n${typeof detail === "string" ? detail : JSON.stringify(detail, null, 2)}`}`);
}

if (args["configure-deploy"]) {
  // The deployed check runs the model inside the Worker (fake-model.js), so
  // the vars carry no endpoint or key of any real provider.
  const dir = args["configure-deploy"];
  const config = {
    name: args.name, main: "dist/worker.mjs", no_bundle: true, compatibility_date: "2026-09-01",
    durable_objects: { bindings: [{ name: "ORB_AGENT", class_name: "OrbAgent" }] },
    migrations: [{ tag: "v1", new_sqlite_classes: ["OrbAgent"] }],
    vars: { ORB_SETTINGS: SETTINGS, ORB_MODELS: MODELS("https://fake-model.invalid/v1"), FAKE_MODEL_KEY: "not-a-secret" },
  };
  rmSync(join(dir, "wrangler.jsonc"), { force: true });
  writeFileSync(join(dir, "wrangler.json"), `${JSON.stringify(config, null, 2)}\n`);
  console.log(`wrote ${join(dir, "wrangler.json")} for ${args.name}`);
  process.exit(0);
}

// --- scripted model over local HTTP ---------------------------------------
await import(join(here, "fake-model.js"));
let modelRequests = 0;
async function startModel() {
  const server = http.createServer(async (req, res) => {
    modelRequests++;
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
async function freePort() {
  const server = http.createServer().listen(0, "127.0.0.1");
  await once(server, "listening");
  const { port } = server.address();
  server.close();
  return port;
}

function launch(runtime, port, fresh) {
  const dir = args.dir;
  const command =
    runtime === "celld"
      ? [args.celld, ["dev", dir, "--port", String(port), "--no-watch", ...(fresh ? ["--clean"] : [])]]
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

async function stop(child) {
  if (child.exitCode !== null) return;
  const exited = once(child, "exit");
  process.kill(-child.pid, "SIGINT");
  const timer = setTimeout(() => process.kill(-child.pid, "SIGKILL"), 10000);
  await exited;
  clearTimeout(timer);
}

async function waitHealthy(base, child) {
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
function headers(token) {
  return { Authorization: `Bearer ${token}` };
}

// overSocket sends one command and collects frames until the run settles.
async function overSocket(base, agent, token, command) {
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
async function overHTTP(base, agent, token, commands) {
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

async function stats(base, agent, token) {
  const response = await fetch(`${base}/agents/${agent}/stats`, { headers: headers(token) });
  check(response.ok, `stats answered ${response.status}`, await response.clone().text());
  return response.json();
}

// --- assertions ------------------------------------------------------------
const response = (frames, id) => frames.find(frame => frame.type === "response" && frame.id === id);

function toolResults(frames) {
  return frames
    .filter(frame => frame.type === "tool_execution_end")
    .map(frame => ({ tool: frame.toolName, error: frame.isError, text: (frame.result?.content ?? []).map(part => part.text ?? "").join("") }));
}

function finalText(frames) {
  const ends = frames.filter(frame => frame.type === "message_end" && frame.message?.role === "assistant");
  return (ends.at(-1)?.message?.content ?? []).filter(part => part.type === "text").map(part => part.text).join("");
}

function checkRun(frames, id, tools) {
  check(response(frames, id)?.success, `${id} was not accepted`, frames.filter(frame => frame.type === "response"));
  const results = toolResults(frames);
  check(JSON.stringify(results.map(result => result.tool)) === JSON.stringify(tools), `tools ran ${results.map(result => result.tool)}`, results);
  check(results.every(result => !result.error), "a tool failed", results);
  check(results.at(-1).text.includes(CONTENT), "read did not return the written file", results);
  check(finalText(frames) === `read back: ${CONTENT}`, `final answer was ${JSON.stringify(finalText(frames))}`);
  check(frames.at(-1).type === "agent_settled", "the run did not settle last", frames.at(-1));
}

async function phaseWrite(base, agent, token) {
  const run = await overSocket(base, agent, token, { id: "p1", type: "prompt", message: `orb-e2e write ${PATH} ${CONTENT}` });
  checkRun(run.frames, "p1", ["write", "read"]);
  const state = await overHTTP(base, agent, token, [{ id: "s1", type: "get_state" }]);
  const data = response(state.frames, "s1")?.data;
  check(data?.sessionFile?.startsWith("/agent/sessions/"), "get_state has no persisted session file", state.frames);
  check(data.model?.provider === "fake" && data.model?.id === "scripted", "the scripted model is not active", data);
  return { sessionFile: data.sessionFile, messageCount: data.messageCount, webSocketRun: run };
}

async function phaseVerify(base, agent, token, before) {
  const history = await overHTTP(base, agent, token, [{ id: "m1", type: "get_messages" }, { id: "s1", type: "get_state" }]);
  const messages = response(history.frames, "m1")?.data?.messages ?? [];
  const state = response(history.frames, "s1")?.data;
  check(messages.some(message => message.role === "user" && JSON.stringify(message.content).includes(`orb-e2e write ${PATH}`)), "the first prompt is missing after restart", messages);
  check(messages.some(message => message.role === "assistant" && JSON.stringify(message.content).includes(`read back: ${CONTENT}`)), "the first answer is missing after restart", messages);
  if (before) {
    check(state?.sessionFile === before.sessionFile, "a different session was resumed", { before, state });
    check(state.messageCount === before.messageCount, "the message count changed across the restart", { before, state });
  }
  const run = await overHTTP(base, agent, token, [{ id: "p2", type: "prompt", message: `orb-e2e read ${PATH}` }]);
  checkRun(run.frames, "p2", ["read"]);
  return { coldHistory: history, httpRun: run, messages: messages.length };
}

// --- main ------------------------------------------------------------------
const report = { runtime: args.runtime };
const agent = args.agent ?? `e2e-${Date.now().toString(36)}`;
if (args.runtime === "remote") {
  const token = process.env.ORB_TOKEN ?? fail("set ORB_TOKEN");
  const base = (args.url ?? fail("pass --url")).replace(/\/$/, "");
  const statePath = join(here, "..", "..", "..", ".tools", "worker-e2e-remote.json");
  check(args.phase === "write" || args.phase === "verify", "a remote check runs --phase write, then a redeploy, then --phase verify");
  await waitHealthy(base);
  if (args.phase === "write") {
    const written = await phaseWrite(base, agent, token);
    report.write = { firstFrameMs: Math.round(written.webSocketRun.firstFrameMs), runMs: Math.round(written.webSocketRun.totalMs) };
    report.statsAfterWrite = await stats(base, agent, token);
    writeFileSync(statePath, JSON.stringify({ agent, sessionFile: written.sessionFile, messageCount: written.messageCount, bootId: report.statsAfterWrite.bootId }));
  }
  if (args.phase === "verify") {
    const before = JSON.parse(readFileSync(statePath, "utf8"));
    const verified = await phaseVerify(base, before.agent, token, before);
    report.verify = { firstFrameMs: Math.round(verified.coldHistory.firstFrameMs), runMs: Math.round(verified.httpRun.totalMs), messages: verified.messages };
    report.statsAfterVerify = await stats(base, before.agent, token);
    check(report.statsAfterVerify.bootId !== before.bootId, "the object was not restarted between the phases");
  }
} else {
  check(args.runtime === "workerd" || args.runtime === "celld", "--runtime must be workerd, celld or remote");
  check(args.dir, "pass --dir");
  const wasm = readFileSync(join(args.dir, "dist", "orb.wasm"));
  report.wasm = { rawBytes: statSync(join(args.dir, "dist", "orb.wasm")).size, gzipBytes: gzipSync(wasm, { level: 9 }).length };
  const model = await startModel();
  const token = randomBytes(24).toString("hex");
  writeFileSync(join(args.dir, ".dev.vars"), [
    `ORB_TOKEN=${token}`, "FAKE_MODEL_KEY=fake-key",
    `ORB_MODELS='${MODELS(`http://127.0.0.1:${model.address().port}/v1`)}'`, `ORB_SETTINGS='${SETTINGS}'`, "",
  ].join("\n"));
  rmSync(join(args.dir, ".e2e-state"), { recursive: true, force: true });
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  let child;
  try {
    let started = performance.now();
    child = launch(args.runtime, port, true);
    await waitHealthy(base, child);
    report.startupMs = Math.round(performance.now() - started);
    const unauthorized = await fetch(`${base}/agents/${agent}/rpc`, { method: "POST", body: "{}" });
    check(unauthorized.status === 401, `an unauthenticated request got ${unauthorized.status}`);

    const written = await phaseWrite(base, agent, token);
    check(modelRequests === 3, `the model was called ${modelRequests} times, want 3`);
    report.coldFirstFrameMs = Math.round(written.webSocketRun.firstFrameMs);
    report.writeRunMs = Math.round(written.webSocketRun.totalMs);
    report.statsAfterWrite = await stats(base, agent, token);

    await stop(child);
    started = performance.now();
    child = launch(args.runtime, port, false);
    await waitHealthy(base, child);
    report.restartMs = Math.round(performance.now() - started);
    const verified = await phaseVerify(base, agent, token, written);
    check(modelRequests === 5, `the model was called ${modelRequests} times in total, want 5`);
    report.afterRestartFirstFrameMs = Math.round(verified.coldHistory.firstFrameMs);
    report.readRunMs = Math.round(verified.httpRun.totalMs);
    report.messagesAfterRestart = verified.messages;
    report.statsAfterRestart = await stats(base, agent, token);
    check(report.statsAfterRestart.bootId !== report.statsAfterWrite.bootId, "the object was not restarted");
  } catch (error) {
    if (child) console.error(child.log.slice(-6000));
    throw error;
  } finally {
    if (child) await stop(child);
    model.close();
    rmSync(join(args.dir, ".dev.vars"), { force: true });
  }
}
console.log(JSON.stringify(report, null, 2));
console.log(`worker e2e (${args.runtime}): PASS`);
