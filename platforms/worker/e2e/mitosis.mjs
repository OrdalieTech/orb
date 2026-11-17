// Mitosis end-to-end check: a native `orb` on this machine, with a throwaway
// Mitosis profile and state under --laptop (never ~/.orb), pairs with a Worker
// object over its ws(s):// stream URL. Then:
//   (a) laptop → object: list the object's instance, prompt it, read its
//       conversation back, all through `orb mitosis remote`;
//   (b) object → laptop: the object's agent calls the laptop's Orb with
//       mitosis_call under the object's own instance grant.
// Locally the object restarts and the laptop reconnects to the same identity.
//
//   node mitosis.mjs --runtime workerd --dir .tools/worker --orb .tools/worker-mitosis-e2e/orb --laptop .tools/worker-mitosis-e2e/laptop
//   node mitosis.mjs --runtime remote --url https://orb-do-e2e.<account>.workers.dev --token-file .tools/worker-e2e/.orb-token --orb … --laptop … [--redeploy .tools/worker-e2e]
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { once } from "node:events";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { parseArgs } from "node:util";
import { MODELS, check, fail, finalText, freePort, launch, modelStats, overHTTP, response, startModel, stats, stop, toolResults, waitHealthy } from "./harness.mjs";

const { values: args } = parseArgs({
  options: {
    runtime: { type: "string" }, dir: { type: "string" },
    url: { type: "string" }, "token-file": { type: "string" }, orb: { type: "string" }, laptop: { type: "string" },
    agent: { type: "string" }, profile: { type: "string", default: "e2e" }, redeploy: { type: "string" },
  },
});
for (const key of ["dir", "orb", "laptop", "token-file", "redeploy"]) if (args[key]) args[key] = resolve(args[key]);
check(args.orb && args.laptop, "pass --orb and --laptop");
check(!args.laptop.startsWith(resolve(process.env.HOME ?? "/nonexistent", ".orb")), "--laptop must not be the real ~/.orb");
check(/^[a-z0-9_-]+$/.test(args.profile) && args.profile !== "personal", "use a throwaway --profile, never personal");

const report = { runtime: args.runtime };
const agent = args.agent ?? `mitosis-${Date.now().toString(36)}`;
const PROFILE = args.profile;

// --- laptop: a native Orb confined to --laptop -----------------------------
const laptopEnv = {
  PATH: process.env.PATH, HOME: join(args.laptop, "home"), ORB_STATE_HOME: join(args.laptop, "state"),
  ORB_MITOSIS_HOME: join(args.laptop, "mitosis"), ORB_AGENT_DIR: join(args.laptop, "agent"),
  ORB_OFFLINE: "1", FAKE_MODEL_KEY: "fake-key", NO_COLOR: "1", TMPDIR: process.env.TMPDIR,
};

async function orb(argv, input) {
  const child = spawn(args.orb, argv, { cwd: join(args.laptop, "workspace"), env: laptopEnv, stdio: ["pipe", "pipe", "pipe"] });
  let stdout = "", stderr = "";
  child.stdout.on("data", chunk => (stdout += chunk));
  child.stderr.on("data", chunk => (stderr += chunk));
  child.stdin.on("error", () => {});
  child.stdin.end(input === undefined ? "" : typeof input === "string" ? input : JSON.stringify(input));
  const [code] = await once(child, "exit");
  check(code === 0, `orb ${argv.join(" ")} failed (${code})`, stderr || stdout);
  return stdout.trim();
}
const mitosisCLI = async (argv, input) => JSON.parse(await orb(["mitosis", "--profile", PROFILE, ...argv], input));

async function laptopInstance() {
  const child = spawn(args.orb, ["--mode", "rpc", "--mitosis", PROFILE, "--instance", "laptop"], {
    cwd: join(args.laptop, "workspace"), env: laptopEnv, stdio: ["pipe", "pipe", "pipe"],
  });
  child.frames = [];
  child.log = "";
  let buffer = "";
  child.stdout.on("data", chunk => {
    buffer += chunk;
    let end;
    while ((end = buffer.indexOf("\n")) >= 0) {
      try {
        child.frames.push(JSON.parse(buffer.slice(0, end)));
      } catch {}
      buffer = buffer.slice(end + 1);
    }
  });
  child.stderr.on("data", chunk => (child.log += chunk));
  return child;
}

async function until(what, probe, timeout = 60000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const value = await probe().catch(() => undefined);
    if (value) return value;
    check(Date.now() < deadline, `timed out waiting for ${what}`);
    await new Promise(done => setTimeout(done, 500));
  }
}

// --- the object's owner surface ----------------------------------------------
async function objectAdmin(base, token, method, params = {}) {
  const reply = await fetch(`${base}/agents/${agent}/mitosis/admin`, {
    method: "POST", headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }, body: JSON.stringify({ method, params }),
  });
  const text = await reply.text();
  check(reply.ok, `mitosis admin ${method} answered ${reply.status}`, text);
  return JSON.parse(text);
}

const FILE = "notes/mitosis.txt";
const WRITTEN = "written through mitosis";
const CALLED = "hello from the durable object";

async function scenario(base, token, restart) {
  const laptopStatus = await mitosisCLI(["status"]);
  const laptopPeer = laptopStatus.peer_id;
  const laptopID = await until("the laptop instance", async () =>
    (await mitosisCLI(["instances"])).instances.find(instance => instance.alias === "laptop" && instance.available)?.instance_id);

  // Pairing: the object invites, the laptop joins over the advertised URL, the owner approves.
  const invitation = await objectAdmin(base, token, "invite");
  check(invitation.locator === `${base.replace(/^http/, "ws")}/agents/${agent}/mitosis`, "the invitation does not advertise the object's stream URL", invitation);
  const claimed = await mitosisCLI(["pair", "join"], invitation);
  check(claimed.claimant === laptopPeer, "the claim names another peer", claimed);
  await objectAdmin(base, token, "approve", { invitation_id: invitation.invitation_id, claimant: laptopPeer });
  const self = await objectAdmin(base, token, "self");
  check(self.peer_id === invitation.peer_id, "the object's identity changed during pairing", self);
  await orb(["mitosis", "--profile", PROFILE, "trust", self.peer_id]);
  // Each side grants the object's own instance subject: the laptop lets it
  // inspect and prompt the laptop Orb, the object lets its agent call there.
  const subject = { peer_id: self.peer_id, subject: { kind: "instance", instance_id: self.instance_id } };
  const permissions = ["agent.inspect", "agent.prompt"];
  await mitosisCLI(["grant"], { principal: subject, instances: [laptopID], permissions });
  await objectAdmin(base, token, "grant", { principal: subject, destination: laptopPeer, instances: [laptopID], permissions });
  report.pairing = { object: self.peer_id, instance: self.instance_id, laptop: laptopPeer, laptopInstance: laptopID, locator: invitation.locator };

  // (a) laptop → object.
  const remote = (method, params) => mitosisCLI(["remote", self.peer_id, method], params);
  const catalog = await remote("instances.list", {});
  check(catalog.items.some(item => item.instance_id === self.instance_id && item.available), "the object's instance is not listed", catalog);
  const call = { instance_id: self.instance_id, service: "orb.instance/1" };
  const described = await remote("instances.call", { ...call, method: "inspect", args: {} });
  const operation = randomBytes(16).toString("base64url");
  const receipt = await remote("instances.call", {
    ...call, method: "prompt", session_id: described.target.session_id, operation_id: operation,
    expected: { registration_generation: described.registration_generation, session_revision: described.target.session_revision },
    args: { text: `orb-e2e write ${FILE} ${WRITTEN}` },
  });
  check(receipt.status === "accepted", "the prompt was not accepted", receipt);
  const done = await until("the object's prompt", async () => {
    const current = await remote("operations.get", { instance_id: self.instance_id, operation_id: operation });
    check(current.status !== "failed", "the object's prompt failed", current);
    return current.status === "succeeded" && current;
  });
  const snapshot = await remote("events.subscribe", { instance_id: self.instance_id });
  const transcript = JSON.stringify(snapshot.messages);
  check(transcript.includes(`orb-e2e write ${FILE}`) && transcript.includes(`read back: ${WRITTEN}`), "the conversation read back lacks the turn", snapshot.messages);
  report.laptopToObject = { listed: catalog.items.length, operation: done.status, messagesReadBack: snapshot.messages.length };

  // (b) object → laptop: the object's agent runs mitosis_call twice.
  const laptopFrames = laptop.frames.length;
  const run = await overHTTP(base, agent, token, [{ id: "b1", type: "prompt", message: `orb-e2e mitosis ${laptopPeer} ${laptopID} ${CALLED}` }]);
  check(response(run.frames, "b1")?.success, "the mitosis prompt was refused", run.frames.filter(frame => frame.type === "response"));
  const calls = toolResults(run.frames);
  check(calls.length === 2 && calls.every(result => result.tool === "mitosis_call" && !result.error), "mitosis_call failed", calls);
  check(finalText(run.frames) === "mitosis prompt: accepted", `the object's agent answered ${JSON.stringify(finalText(run.frames))}`);
  const echoed = await until("the laptop turn", async () => {
    const frames = laptop.frames.slice(laptopFrames);
    const user = frames.find(frame => frame.type === "message_end" && frame.message?.role === "user" && JSON.stringify(frame.message.content).includes(CALLED));
    const reply = frames.find(frame => frame.type === "message_end" && frame.message?.role === "assistant");
    return user && reply && reply.message.content.map(part => part.text ?? "").join("");
  });
  report.objectToLaptop = { toolCalls: calls.length, laptopReceived: CALLED, laptopAnswered: echoed };

  if (restart) {
    // The object restarts; the laptop reconnects to the same pinned identity.
    await restart();
    const again = await until("the restarted object", async () => (await remote("instances.list", {})).items.find(item => item.instance_id === self.instance_id));
    const after = await objectAdmin(base, token, "self");
    check(after.peer_id === self.peer_id && again.registration_generation !== catalog.items[0].registration_generation, "identity or registration after restart", { after, again });
    report.afterRestart = { peer: after.peer_id === self.peer_id, generation: again.registration_generation };
  }
  report.objectStats = await stats(base, agent, token);
}

// --- main ------------------------------------------------------------------
rmSync(args.laptop, { recursive: true, force: true });
for (const dir of ["home", "agent", "workspace"]) mkdirSync(join(args.laptop, dir), { recursive: true, mode: 0o700 });
const modelServer = await startModel();
writeFileSync(join(args.laptop, "agent", "models.json"), MODELS(`http://127.0.0.1:${modelServer.address().port}/v1`));
writeFileSync(join(args.laptop, "agent", "settings.json"), JSON.stringify({ defaultProvider: "fake", defaultModel: "scripted" }));
let laptop, server;
try {
  await orb(["mitosis", "--profile", PROFILE, "start"]);
  laptop = await laptopInstance();
  if (args.runtime === "remote") {
    const base = (args.url ?? fail("pass --url")).replace(/\/$/, "");
    const token = process.env.ORB_TOKEN ?? readFileSync(args["token-file"] ?? fail("pass --token-file or ORB_TOKEN"), "utf8").trim();
    await waitHealthy(base);
    // --redeploy names the deployed bundle directory: a new version restarts
    // every object, so the laptop must reconnect to the persisted identity.
    await scenario(base, token, args.redeploy && (async () => {
      const child = spawn(join(args.redeploy, "node_modules", ".bin", "wrangler"), ["deploy", "--var", `ORB_E2E_RESTART:${Date.now()}`], { cwd: args.redeploy, stdio: "ignore" });
      const [code] = await once(child, "exit");
      check(code === 0, "the redeploy failed");
    }));
  } else {
    check(args.runtime === "workerd", "--runtime must be workerd or remote");
    const token = randomBytes(24).toString("hex");
    const settings = JSON.stringify({ defaultProvider: "fake", defaultModel: "scripted", plugins: { "mitosis-agent-calls": true } });
    writeFileSync(join(args.dir, ".dev.vars"), [
      `ORB_TOKEN=${token}`, "FAKE_MODEL_KEY=fake-key",
      `ORB_MODELS='${MODELS(`http://127.0.0.1:${modelServer.address().port}/v1`)}'`, `ORB_SETTINGS='${settings}'`, "",
    ].join("\n"));
    rmSync(join(args.dir, ".e2e-state"), { recursive: true, force: true });
    const port = await freePort();
    const base = `http://127.0.0.1:${port}`;
    server = launch(port, args.dir);
    await waitHealthy(base, server);
    await scenario(base, token, async () => {
      await stop(server);
      // The dev server can hold the port briefly while it closes open streams.
      await until("the port to close", async () => !(await fetch(`${base}/health`).then(() => true, () => false)));
      server = launch(port, args.dir);
      await waitHealthy(base, server);
    });
  }
} catch (error) {
  if (laptop) console.error(`laptop orb stderr:\n${laptop.log.slice(-4000)}`);
  if (server) console.error(`dev server:\n${server.log.slice(-6000)}`);
  try {
    console.error(`laptop mitosis log:\n${readFileSync(join(args.laptop, "mitosis", PROFILE, "service.log"), "utf8").slice(-4000)}`);
  } catch {}
  throw error;
} finally {
  laptop?.kill("SIGTERM");
  await orb(["mitosis", "--profile", PROFILE, "stop"]).catch(() => {});
  // The next run starts an Orb on the same agent directory, which refuses while this one still runs.
  if (laptop && laptop.exitCode === null) await once(laptop, "exit");
  if (server) await stop(server);
  if (args.dir) rmSync(join(args.dir, ".dev.vars"), { force: true });
  modelServer.close();
}
report.modelRequests = modelStats.requests;
console.log(JSON.stringify(report, null, 2));
console.log(`worker mitosis e2e (${args.runtime}): PASS`);
