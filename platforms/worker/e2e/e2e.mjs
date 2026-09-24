// End-to-end check of the Orb Worker in a real runtime: workerd (wrangler dev),
// Celld (celld dev) or a deployed Worker. A scripted model makes the agent
// write then read a file over the WebSocket transport; after a restart the
// HTTP NDJSON transport must show the same session history and file.
//
//   node e2e.mjs --runtime workerd --dir .tools/worker
//   node e2e.mjs --runtime celld --dir .tools/worker --celld .tools/bin/celld
//   ORB_TOKEN=... node e2e.mjs --runtime remote --url https://orb-do-e2e.<account>.workers.dev --phase write|verify
//   node e2e.mjs --configure-deploy .tools/worker-e2e --name orb-do-e2e   (writes its wrangler.json)
import { randomBytes } from "node:crypto";
import { readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import {
  CONTENT, MODELS, PATH, SETTINGS, check, checkRun, fail, freePort, launch, modelStats, overHTTP, overSocket, response, startModel, stats,
  stop, waitHealthy,
} from "./harness.mjs";
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
    child = launch(args.runtime, port, true, args);
    await waitHealthy(base, child);
    report.startupMs = Math.round(performance.now() - started);
    const unauthorized = await fetch(`${base}/agents/${agent}/rpc`, { method: "POST", body: "{}" });
    check(unauthorized.status === 401, `an unauthenticated request got ${unauthorized.status}`);

    const written = await phaseWrite(base, agent, token);
    check(modelStats.requests === 3, `the model was called ${modelStats.requests} times, want 3`);
    report.coldFirstFrameMs = Math.round(written.webSocketRun.firstFrameMs);
    report.writeRunMs = Math.round(written.webSocketRun.totalMs);
    report.statsAfterWrite = await stats(base, agent, token);

    await stop(child);
    started = performance.now();
    child = launch(args.runtime, port, false, args);
    await waitHealthy(base, child);
    report.restartMs = Math.round(performance.now() - started);
    const verified = await phaseVerify(base, agent, token, written);
    check(modelStats.requests === 5, `the model was called ${modelStats.requests} times in total, want 5`);
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
