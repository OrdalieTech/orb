// Browser Mitosis end-to-end check: the Worker runtime as a browser tab runs it (peer.wasm with
// platforms/browser/web/peer.js; here under Node, with a Map in place of IndexedDB) pairs with a
// native `orb` over WebSocket as a full peer, joins its sync group, and so receives the owner's
// credentials and memory. Reopened on the same storage, it keeps its identity, its data, and its
// place in the group. The native side is a throwaway profile under --laptop, never ~/.orb.
//
//   node browser.mjs --wasm .tools/worker-mitosis-e2e/peer.wasm --wasm-exec "$(go env GOROOT)/lib/wasm/wasm_exec.js" \
//     --orb .tools/worker-mitosis-e2e/orb --laptop .tools/worker-mitosis-e2e/laptop
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import { MODELS, check, freePort } from "./harness.mjs";

const { values: args } = parseArgs({
  options: { wasm: { type: "string" }, "wasm-exec": { type: "string" }, orb: { type: "string" }, laptop: { type: "string" }, profile: { type: "string", default: "e2e-browser" } },
});
for (const key of ["wasm", "wasm-exec", "orb", "laptop"]) {
  check(args[key], `pass --${key}`);
  args[key] = resolve(args[key]);
}
check(!args.laptop.startsWith(resolve(process.env.HOME ?? "/nonexistent", ".orb")), "--laptop must not be the real ~/.orb");
check(/^[a-z0-9_-]+$/.test(args.profile) && args.profile !== "personal", "use a throwaway --profile, never personal");
await import(pathToFileURL(args["wasm-exec"]));
await import("../deploy/helpers.js");
const { openPeer } = await import("../../browser/web/peer.js");

const report = {};
const OWNER_KEY = "owner-key";
const OWNER_MEMORY = "the owner prefers terse answers";

// --- the owner: a native Orb with Pi files, confined to --laptop --------------
const env = {
  PATH: process.env.PATH, HOME: join(args.laptop, "home"), ORB_STATE_HOME: join(args.laptop, "state"),
  ORB_MITOSIS_HOME: join(args.laptop, "mitosis"), ORB_AGENT_DIR: join(args.laptop, "agent"), NO_COLOR: "1", TMPDIR: process.env.TMPDIR,
};
async function orb(argv, input) {
  const child = spawn(args.orb, ["--pi-files", "mitosis", "--profile", args.profile, ...argv], { env, stdio: ["pipe", "pipe", "pipe"] });
  let stdout = "", stderr = "";
  child.stdout.on("data", chunk => (stdout += chunk));
  child.stderr.on("data", chunk => (stderr += chunk));
  child.stdin.end(input === undefined ? "" : JSON.stringify(input));
  const [code] = await once(child, "exit");
  check(code === 0, `orb mitosis ${argv.join(" ")} failed (${code})`, stderr || stdout);
  return stdout.trim() ? JSON.parse(stdout) : {};
}
async function until(what, probe, timeout = 90000) {
  for (const deadline = Date.now() + timeout; ; ) {
    const value = await probe().catch(() => undefined);
    if (value) return value;
    check(Date.now() < deadline, `timed out waiting for ${what}`);
    await new Promise(done => setTimeout(done, 300));
  }
}

// --- the browser tab --------------------------------------------------------
const storage = (() => {
  const map = new Map();
  return {
    map,
    async get(keys) {
      return new Map(keys.filter(key => map.has(key)).map(key => [key, map.get(key)]));
    },
    async list({ prefix }) {
      return new Map([...map].filter(([key]) => key.startsWith(prefix)));
    },
    async put(entries) {
      for (const [key, value] of Object.entries(entries)) map.set(key, value.slice());
    },
    async delete(keys) {
      for (const key of keys) map.delete(key);
    },
  };
})();
// A document the tab keeps, from its chunks (platforms/worker kv.go).
function stored(name) {
  const prefix = `doc/c${name}#`;
  const chunks = [...storage.map].filter(([key]) => key.startsWith(prefix)).sort(([a], [b]) => Number(a.slice(prefix.length)) - Number(b.slice(prefix.length)));
  return Buffer.concat(chunks.map(([, value]) => value)).toString();
}
const decoder = new TextDecoder();
// The tab's catalog names no key of its own: it can use the fake provider only once a credential arrives.
const KEYLESS = (() => {
  const models = JSON.parse(MODELS("http://127.0.0.1:9/v1"));
  delete models.providers.fake.apiKey;
  return JSON.stringify(models);
})();
async function tab() {
  const frames = [];
  const peer = await openPeer({
    wasm: readFileSync(args.wasm), storage, name: "browser",
    env: { ORB_MODELS: KEYLESS, ORB_SETTINGS: JSON.stringify({ defaultProvider: "fake", defaultModel: "scripted", plugins: { memory: true } }) },
    emit: (array, length) => frames.push(JSON.parse(decoder.decode(array.slice(0, length)))),
  });
  let next = 0;
  peer.rpc = async (type, params = {}) => {
    const id = `c${++next}`;
    check(!peer.command(JSON.stringify({ id, type, ...params })), `${type} refused`);
    return until(type, async () => frames.find(frame => frame.type === "response" && frame.id === id), 20000);
  };
  peer.admin = async (method, params = {}) => JSON.parse(await peer.mitosisAdmin(method, JSON.stringify(params), ""));
  peer.models = async () => (await peer.rpc("get_available_models")).data.models.map(model => `${model.provider}/${model.id}`);
  return peer;
}

// --- main ------------------------------------------------------------------
rmSync(args.laptop, { recursive: true, force: true });
for (const dir of ["home", "agent/memory"]) mkdirSync(join(args.laptop, dir), { recursive: true, mode: 0o700 });
writeFileSync(join(args.laptop, "agent", "models.json"), MODELS("http://127.0.0.1:9/v1"));
writeFileSync(join(args.laptop, "agent", "auth.json"), JSON.stringify({ fake: { type: "api_key", key: OWNER_KEY } }), { mode: 0o600 });
writeFileSync(join(args.laptop, "agent", "memory", "memory.jsonl"), `${JSON.stringify({ item: { id: "owner-1", time: new Date().toISOString(), content: OWNER_MEMORY } })}\n`);
const port = await freePort();
const url = `ws://127.0.0.1:${port}/mitosis`;
const daemon = spawn(args.orb, ["--pi-files", "mitosis", "--profile", args.profile, "run", "--web-listen", `127.0.0.1:${port}`, "--web-url", url], { env, stdio: ["ignore", "ignore", "pipe"] });
let daemonLog = "";
daemon.stderr.on("data", chunk => (daemonLog += chunk));
let peer;
try {
  const owner = (await until("the owner's Mitosis", () => orb(["status"]))).peer_id;
  peer = await tab();
  const self = await peer.admin("self");
  check(!(await peer.models()).includes("fake/scripted"), "the tab had the owner's provider before joining");

  // Pairing: the owner invites, the tab joins over WebSocket, the owner approves and each trusts the other.
  const invitation = await orb(["pair", "invite"]);
  check(invitation.locator === url, "the invitation does not advertise the WebSocket URL", invitation);
  const claimed = await peer.admin("join", invitation);
  check(claimed.claimant === self.peer_id, "the claim names another peer", claimed);
  await orb(["pair", "approve", invitation.invitation_id, self.peer_id]);
  await peer.admin("trust", { peer_id: owner });

  // The owner admits the tab to its sync group; the tab follows it.
  await orb(["sync", "add", self.peer_id]);
  const joined = await peer.admin("sync", { join: owner });
  check(joined.owner === owner, "the tab joined another group", joined);
  const began = Date.now();
  await until("the owner's provider in the tab", async () => (await peer.models()).includes("fake/scripted"));
  await until("the owner's memory in the tab", async () => stored("/agent/memory/memory.jsonl").includes(OWNER_MEMORY));
  check(JSON.parse(stored("/agent/auth.json")).fake?.key === OWNER_KEY, "the tab holds another credential");
  report.joined = { owner, tab: self.peer_id, receivedMs: Date.now() - began };

  // A reopened tab is the same peer, with what it received, and reconnects to the owner by itself.
  peer.dispose();
  await until("the closed tab's channel to end", async () => (await orb(["status"])).peer_states[self.peer_id] === "disconnected");
  peer = await tab();
  const again = await peer.admin("self");
  check(again.peer_id === self.peer_id, "the reopened tab has another identity", again);
  check((await peer.models()).includes("fake/scripted"), "the reopened tab lost the owner's provider");
  await until("the tab's channel back to the owner", async () => (await orb(["status"])).peer_states[self.peer_id] === "connected");
  const members = (await orb(["sync"])).members;
  check(members.includes(self.peer_id), "the tab left the owner's group", members);
  report.reopened = { identity: again.peer_id === self.peer_id, members: members.length };
} catch (error) {
  console.error(`owner mitosis log:\n${daemonLog.slice(-6000)}`);
  throw error;
} finally {
  peer?.dispose();
  daemon.kill("SIGTERM");
  if (daemon.exitCode === null) await once(daemon, "exit");
}
console.log(JSON.stringify(report, null, 2));
console.log("browser mitosis e2e: PASS");
process.exit(0);
