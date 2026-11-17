// Orb on Cloudflare Durable Objects and self-hosted workerd: one Durable Object
// per agent name, each with its own Orb over the object's storage, all of an
// isolate's objects in one Go runtime (orb.wasm, cmd/orb-worker). `make
// worker-build` prepends Go's wasm_exec.js (which defines globalThis.Go) and
// helpers.js (globalThis.orbHelpers) to this module, so the bundle is one
// JavaScript module plus orb.wasm and needs no bundler.
import orbModule from "./orb.wasm";

const encoder = new TextEncoder();
const decoder = new TextDecoder();
const AGENT_ROUTE = /^\/agents\/([A-Za-z0-9._~-]{1,128})\/(rpc|stats|mitosis|mitosis\/admin)$/;
// Present once the object has a Mitosis identity (platforms/worker/peer.StateKey).
const MITOSIS_STATE_KEY = "doc/m/mitosis/state.json";

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname === "/health") return new Response("ok\n");
    const route = AGENT_ROUTE.exec(url.pathname);
    if (!route) return problem(404, "Routes: /agents/<name>/rpc, /agents/<name>/stats, /agents/<name>/mitosis, /agents/<name>/mitosis/admin, /health");
    // Mitosis streams authenticate peers with pinned TLS inside the stream; a
    // remote Orb has no ORB_TOKEN. Everything else needs the bearer token.
    if (route[2] !== "mitosis") {
      const denied = authorize(request, env);
      if (denied) return denied;
    } else if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket") {
      return problem(426, "/agents/<name>/mitosis carries Mitosis streams over WebSocket");
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

// The isolate's Go runtime, which its objects share, and the exit callbacks
// of the objects open in it.
let runtime;
const running = new Set();

// orbRuntime instantiates orb.wasm once per isolate. A runtime that stops,
// as a panic does, stops every object in it, and the next event boots another.
function orbRuntime() {
  runtime ??= (async () => {
    const go = new Go();
    let exitCode = 1;
    const warn = go.exit;
    go.exit = code => {
      exitCode = code;
      warn(code);
    };
    const ready = new Promise((resolve, reject) => {
      globalThis.__orbWorkerBoot = { helpers: orbHelpers, resolve, reject: message => reject(new Error(message)) };
    });
    go.argv = ["orb-worker", "__orbWorkerBoot"];
    go.env = {};
    const instance = await WebAssembly.instantiate(orbModule, go.importObject);
    const stopped = go.run(instance).then(() => {
      runtime = undefined;
      for (const exit of running) exit(exitCode);
      running.clear();
      throw new Error("orb-worker exited");
    });
    stopped.catch(() => {});
    const api = await Promise.race([ready, stopped]);
    return { api, memoryBytes: () => instance.exports.mem.buffer.byteLength };
  })().catch(error => {
    runtime = undefined;
    throw error;
  });
  return runtime;
}

// start opens one object's Orb in the isolate's runtime.
async function start(storage, env, name, emit, exited) {
  const began = Date.now();
  const { api: orb, memoryBytes } = await orbRuntime();
  const exit = code => {
    running.delete(exit);
    exited(code);
  };
  running.add(exit);
  try {
    const api = await orb.open({ storage, env, name, emit, exit });
    return { api, memoryBytes, bootMs: Date.now() - began, bootId: crypto.randomUUID() };
  } catch (error) {
    running.delete(exit);
    throw error;
  }
}

// The runtime outlives the objects in it, so an object reaches its Orb's
// callbacks only weakly and disposes of its Orb once it is collected.
const collected = new FinalizationRegistry(dispose => {
  try {
    dispose();
  } catch {}
});

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
    const self = new WeakRef(this);
    this.orb ??= start(this.ctx.storage, this.env, this.ctx.id?.name ?? "",
      (array, length) => self.deref()?.frame(array.slice(0, length)), code => self.deref()?.exited(code)).then(orb => {
      collected.register(this, () => orb.api.dispose(), orb);
      return orb;
    }, error => {
      this.orb = undefined;
      throw error;
    });
    return this.orb;
  }

  exited(code) {
    this.orb?.then(orb => collected.unregister(orb), () => {});
    this.orb = undefined;
    this.pending = 0;
    this.busy = false;
    this.settle(new Error(`orb-worker stopped with exit code ${code}`));
  }

  // frame takes one encoded frame, LF included. Frames name their type first
  // (after a response's id), and responses their command and success next:
  // reading them from the head spares decoding and parsing frames that carry
  // a whole transcript, which streams pass on as bytes.
  frame(bytes) {
    const head = /^\{(?:"id":"(?:[^"\\]|\\.)*",)?"type":"([a-z_]+)"(?:,"command":"([^"\\]*)","success":(true|false))?/.exec(decoder.decode(bytes.subarray(0, 512)));
    let frame = head ? { type: head[1], command: head[2], success: head[3] === "true" } : {};
    let text;
    if (!head) {
      text = decoder.decode(bytes.subarray(0, -1));
      try {
        frame = JSON.parse(text);
      } catch {}
    }
    if (frame.type === "response") {
      this.pending = Math.max(0, this.pending - 1);
      if (frame.success && frame.command === "prompt") this.busy = true;
    } else if (frame.type === "agent_start") {
      this.busy = true;
    } else if (frame.type === "agent_settled") {
      this.busy = false;
    }
    const sockets = this.ctx.getWebSockets();
    if (sockets.length > 0) text ??= decoder.decode(bytes.subarray(0, -1));
    for (const socket of sockets) {
      try {
        socket.send(text);
      } catch {}
    }
    for (const stream of this.streams) stream(bytes);
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
    if (action === "mitosis") return this.mitosisStream(request);
    if (action === "mitosis/admin") return this.mitosisAdmin(request, url, name);
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
    const sink = bytes => output.enqueue(bytes);
    this.streams.add(sink);
    this.dispatch(commands)
      .catch(error => sink(encoder.encode(`${JSON.stringify({ type: "worker_error", error: error.message })}\n`)))
      .finally(() => {
        this.streams.delete(sink);
        output.close();
      });
    return new Response(body, { headers: { "Content-Type": "application/x-ndjson", "Cache-Control": "no-store" } });
  }

  // mitosisStream hands a WebSocket to the Go Mitosis peer. It is accepted, not
  // hibernatable: the TLS session inside it lives in the Go runtime, and the
  // open socket keeps the object resident. Unknown objects are not booted.
  async mitosisStream(request) {
    const origin = request.headers.get("Origin");
    if (origin && !(this.env.ORB_MITOSIS_ORIGINS ?? "").split(",").map(value => value.trim()).includes(origin)) {
      return problem(403, "Origin not allowed; list it in ORB_MITOSIS_ORIGINS");
    }
    if ((await this.ctx.storage.get(MITOSIS_STATE_KEY)) === undefined) {
      return problem(404, "This agent has no Mitosis identity yet; create an invitation through /mitosis/admin");
    }
    let api;
    try {
      ({ api } = await this.boot());
    } catch (error) {
      return problem(500, `Orb failed to start: ${error.message}`);
    }
    const [client, server] = Object.values(new WebSocketPair());
    server.accept();
    api.mitosisAccept(server);
    return new Response(null, { status: 101, webSocket: client });
  }

  // mitosisAdmin runs one owner method of `orb mitosis` ({"method", "params"}).
  // Invitations advertise this object's stream URL, from ORB_PUBLIC_ORIGIN
  // when a proxy fronts the Worker.
  async mitosisAdmin(request, url, name) {
    if (request.method !== "POST") return problem(405, 'POST {"method": "...", "params": {...}}', { Allow: "POST" });
    let body;
    try {
      body = await request.json();
    } catch {}
    if (typeof body?.method !== "string") return problem(400, 'POST {"method": "...", "params": {...}}');
    const origin = (this.env.ORB_PUBLIC_ORIGIN ?? url.origin).replace(/^http/, "ws").replace(/\/$/, "");
    try {
      const { api } = await this.boot();
      const result = await api.mitosisAdmin(body.method, JSON.stringify(body.params ?? {}), `${origin}/agents/${name}/mitosis`);
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
