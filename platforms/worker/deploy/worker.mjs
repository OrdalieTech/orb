// Orb on Cloudflare Durable Objects and self-hosted Celld: one Durable Object
// per agent name, each running its own Go runtime (orb.wasm, cmd/orb-worker)
// over the object's storage. `make worker-build` prepends Go's wasm_exec.js
// (which defines globalThis.Go) to this module, so the bundle is one
// JavaScript module plus orb.wasm and needs no bundler.
import orbModule from "./orb.wasm";

// Go's syscall/js reaches these process members. wasm_exec.js installs them
// only when there is no global process; Celld defines a partial one, so fill
// in what it lacks with wasm_exec.js's own stubs.
if (globalThis.process) {
  const enosys = () => Object.assign(new Error("not implemented"), { code: "ENOSYS" });
  const stubs = {
    getuid: () => -1, getgid: () => -1, geteuid: () => -1, getegid: () => -1,
    getgroups() { throw enosys(); }, umask() { throw enosys(); }, cwd() { throw enosys(); }, chdir() { throw enosys(); },
  };
  for (const [name, stub] of Object.entries(stubs)) if (typeof process[name] !== "function") process[name] = stub;
  for (const name of ["pid", "ppid"]) if (typeof process[name] !== "number") process[name] = -1;
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();
const AGENT_ROUTE = /^\/agents\/([A-Za-z0-9._~-]{1,128})\/(rpc|stats|bridge|bridge\/admin)$/;
// Present once the object has a Bridge identity (platforms/worker/peer.StateKey).
const BRIDGE_STATE_KEY = "doc/m/bridge/state.json";

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname === "/health") return new Response("ok\n");
    const route = AGENT_ROUTE.exec(url.pathname);
    if (!route) return problem(404, "Routes: /agents/<name>/rpc, /agents/<name>/stats, /agents/<name>/bridge, /agents/<name>/bridge/admin, /health");
    // Bridge streams authenticate peers with pinned TLS inside the stream; a
    // remote Orb has no ORB_TOKEN. Everything else needs the bearer token.
    if (route[2] !== "bridge") {
      const denied = authorize(request, env);
      if (denied) return denied;
    } else if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket") {
      return problem(426, "/agents/<name>/bridge carries Bridge streams over WebSocket");
    }
    return env.ORB_AGENT.get(env.ORB_AGENT.idFromName(route[1])).fetch(request);
  },
};

// Every agent route requires the ORB_TOKEN secret as a bearer token; without
// the secret the Worker serves nothing.
function authorize(request, env) {
  const token = env.ORB_TOKEN;
  if (typeof token !== "string" || token.length < 16) {
    return problem(503, "Set the ORB_TOKEN secret (at least 16 characters) to enable this Worker");
  }
  const given = encoder.encode(request.headers.get("Authorization") ?? "");
  const expected = encoder.encode(`Bearer ${token}`);
  let difference = given.length ^ expected.length;
  for (let index = 0; index < expected.length; index++) difference |= (given[index] ?? 0) ^ expected[index];
  if (difference !== 0) return problem(401, "Missing or wrong bearer token", { "WWW-Authenticate": "Bearer" });
  return undefined;
}

function problem(status, error, headers = {}) {
  return Response.json({ error }, { status, headers });
}

let bootSlots = 0;

// start instantiates one Go runtime for one object. Go reads its boot slot
// synchronously at startup, so the slot is free again by the time the next
// object in this isolate boots.
async function start(storage, env, name, emit, exited) {
  const began = Date.now();
  const go = new Go();
  const slot = `__orbWorkerBoot${++bootSlots}`;
  const ready = new Promise((resolve, reject) => {
    globalThis[slot] = { storage, env, name, emit, resolve, reject: message => reject(new Error(message)), exit: exited };
  });
  go.argv = ["orb-worker", slot];
  go.env = {};
  const instance = await WebAssembly.instantiate(orbModule, go.importObject);
  const stopped = go.run(instance).then(() => Promise.reject(new Error("orb-worker exited while booting")));
  stopped.catch(() => {});
  const api = await Promise.race([ready, stopped]);
  return { api, memoryBytes: () => instance.exports.mem.buffer.byteLength, bootMs: Date.now() - began, bootId: crypto.randomUUID() };
}

export class OrbAgent {
  constructor(ctx, env) {
    this.ctx = ctx;
    this.env = env;
    this.orb = undefined;
    // Commands awaiting their response frame, and whether a run is active:
    // an event handler stays open until both settle, so the object is not
    // evicted mid-turn and an HTTP stream knows when to end.
    this.pending = 0;
    this.busy = false;
    this.waiters = [];
    this.streams = new Set();
  }

  boot() {
    this.orb ??= start(this.ctx.storage, this.env, this.ctx.id?.name ?? "", frame => this.frame(frame), code => this.exited(code)).catch(error => {
      this.orb = undefined;
      throw error;
    });
    return this.orb;
  }

  exited(code) {
    this.orb = undefined;
    this.pending = 0;
    this.busy = false;
    this.settle(new Error(`orb-worker stopped with exit code ${code}`));
  }

  frame(text) {
    let frame = {};
    try {
      frame = JSON.parse(text);
    } catch {}
    if (frame.type === "response") {
      this.pending = Math.max(0, this.pending - 1);
      if (frame.success && frame.command === "prompt") this.busy = true;
    } else if (frame.type === "agent_start") {
      this.busy = true;
    } else if (frame.type === "agent_settled") {
      this.busy = false;
    }
    for (const socket of this.ctx.getWebSockets()) {
      try {
        socket.send(text);
      } catch {}
    }
    for (const stream of this.streams) stream(text);
    if (this.pending === 0 && !this.busy) this.settle();
  }

  settle(error) {
    const waiters = this.waiters;
    this.waiters = [];
    for (const waiter of waiters) error ? waiter.reject(error) : waiter.resolve();
  }

  idle() {
    if (this.pending === 0 && !this.busy) return Promise.resolve();
    return new Promise((resolve, reject) => this.waiters.push({ resolve, reject }));
  }

  // dispatch sends command frames (one per line) to the runtime and resolves
  // once each has its response and any run it started has settled.
  async dispatch(text) {
    const { api } = await this.boot();
    for (const line of text.split("\n")) {
      if (line.trim() === "") continue;
      let type;
      try {
        type = JSON.parse(line).type;
      } catch {}
      const answered = type !== "extension_ui_response";
      if (answered) this.pending++;
      const refused = api.command(line);
      if (refused) {
        if (answered) this.pending--;
        throw new Error(refused);
      }
    }
    await this.idle();
  }

  async fetch(request) {
    const url = new URL(request.url);
    const [, name, action] = AGENT_ROUTE.exec(url.pathname);
    if (action === "stats") return this.stats();
    if (action === "bridge") return this.bridgeStream(request);
    if (action === "bridge/admin") return this.bridgeAdmin(request, url, name);
    if (request.headers.get("Upgrade")?.toLowerCase() === "websocket") {
      const [client, server] = Object.values(new WebSocketPair());
      this.ctx.acceptWebSocket(server);
      this.boot().catch(error => server.close(1011, String(error.message).slice(0, 120)));
      return new Response(null, { status: 101, webSocket: client });
    }
    if (request.method !== "POST") {
      return problem(405, "Send command frames as NDJSON with POST, or upgrade to a WebSocket", { Allow: "POST" });
    }
    const commands = await request.text();
    try {
      await this.boot();
    } catch (error) {
      return problem(500, `Orb failed to start: ${error.message}`);
    }
    let output;
    const body = new ReadableStream({
      start: controller => {
        output = controller;
      },
    });
    const sink = text => output.enqueue(encoder.encode(`${text}\n`));
    this.streams.add(sink);
    this.dispatch(commands)
      .catch(error => sink(JSON.stringify({ type: "worker_error", error: error.message })))
      .finally(() => {
        this.streams.delete(sink);
        output.close();
      });
    return new Response(body, { headers: { "Content-Type": "application/x-ndjson", "Cache-Control": "no-store" } });
  }

  // bridgeStream hands a WebSocket to the Go Bridge peer. It is accepted, not
  // hibernatable: the TLS session inside it lives in the Go runtime, and the
  // open socket keeps the object resident. Unknown objects are not booted.
  async bridgeStream(request) {
    const origin = request.headers.get("Origin");
    if (origin && !(this.env.ORB_BRIDGE_ORIGINS ?? "").split(",").map(value => value.trim()).includes(origin)) {
      return problem(403, "Origin not allowed; list it in ORB_BRIDGE_ORIGINS");
    }
    if ((await this.ctx.storage.get(BRIDGE_STATE_KEY)) === undefined) {
      return problem(404, "This agent has no Bridge identity yet; create an invitation through /bridge/admin");
    }
    let api;
    try {
      ({ api } = await this.boot());
    } catch (error) {
      return problem(500, `Orb failed to start: ${error.message}`);
    }
    const [client, server] = Object.values(new WebSocketPair());
    server.accept();
    api.bridgeAccept(server);
    return new Response(null, { status: 101, webSocket: client });
  }

  // bridgeAdmin runs one owner method of `orb bridge` ({"method", "params"}).
  // Invitations advertise this object's stream URL, from ORB_PUBLIC_ORIGIN
  // when a proxy fronts the Worker.
  async bridgeAdmin(request, url, name) {
    if (request.method !== "POST") return problem(405, 'POST {"method": "...", "params": {...}}', { Allow: "POST" });
    let body;
    try {
      body = await request.json();
    } catch {}
    if (typeof body?.method !== "string") return problem(400, 'POST {"method": "...", "params": {...}}');
    const origin = (this.env.ORB_PUBLIC_ORIGIN ?? url.origin).replace(/^http/, "ws").replace(/\/$/, "");
    try {
      const { api } = await this.boot();
      const result = await api.bridgeAdmin(body.method, JSON.stringify(body.params ?? {}), `${origin}/agents/${name}/bridge`);
      return new Response(result, { headers: { "Content-Type": "application/json" } });
    } catch (error) {
      return problem(400, error.message);
    }
  }

  async stats() {
    const { api, memoryBytes, bootMs, bootId } = await this.boot();
    return Response.json({ bootId, bootMs, wasmMemoryBytes: memoryBytes(), go: api.stats(), busy: this.busy, pending: this.pending });
  }

  async webSocketMessage(socket, message) {
    try {
      await this.dispatch(typeof message === "string" ? message : decoder.decode(message));
    } catch (error) {
      socket.send(JSON.stringify({ type: "worker_error", error: error.message }));
    }
  }

  webSocketClose(socket, code, reason) {
    try {
      socket.close(code, reason);
    } catch {}
  }
}
