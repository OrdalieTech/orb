// The browser as a full Mitosis peer: the Worker runtime (cmd/orb-worker, built as peer.wasm)
// running in this tab over IndexedDB, reaching its peers itself over WebSocket. It joins an
// invitation and its owner's sync group like any device, so its Orb gets the owner's providers
// and memory. Go's wasm_exec.js (globalThis.Go) and the Worker's helpers.js
// (globalThis.orbHelpers) must be loaded first.

// indexedStorage is the subset of the Durable Object storage API the runtime persists through
// (platforms/worker KV), over one IndexedDB database. Writes issued in one turn commit in one
// transaction, as a Durable Object coalesces them.
export function indexedStorage(name) {
  const done = request =>
    new Promise((resolve, reject) => {
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
  const opening = indexedDB.open(name, 1);
  opening.onupgradeneeded = () => opening.result.createObjectStore("kv");
  const database = done(opening);
  // Requests are issued in the turn their transaction is created in, which keeps it active.
  const read = async () => (await database).transaction("kv", "readonly").objectStore("kv");
  let batch;
  const write = change => {
    if (!batch) {
      const current = (batch = { changes: [] });
      current.done = database.then(
        db =>
          new Promise((resolve, reject) => {
            batch = undefined;
            const transaction = db.transaction("kv", "readwrite");
            const kv = transaction.objectStore("kv");
            for (const apply of current.changes) apply(kv);
            transaction.oncomplete = () => resolve();
            transaction.onerror = transaction.onabort = () => reject(transaction.error);
          }),
      );
    }
    batch.changes.push(change);
    return batch.done;
  };
  return {
    async get(keys) {
      const kv = await read();
      const values = await Promise.all(keys.map(key => done(kv.get(key))));
      return new Map(keys.map((key, index) => [key, values[index]]).filter(([, value]) => value !== undefined));
    },
    async list({ prefix }) {
      const kv = await read();
      const range = IDBKeyRange.bound(prefix, `${prefix}￿`);
      const [keys, values] = await Promise.all([done(kv.getAllKeys(range)), done(kv.getAll(range))]);
      return new Map(keys.map((key, index) => [key, values[index]]));
    },
    put(entries) {
      return write(kv => {
        for (const [key, value] of Object.entries(entries)) kv.put(value, key);
      });
    },
    delete(keys) {
      return write(kv => {
        for (const key of keys) kv.delete(key);
      });
    },
  };
}

// openPeer boots peer.wasm (its bytes) and opens this tab's Orb over storage: env holds
// ORB_SETTINGS and ORB_MODELS defaults, emit(array, length) receives its RPC frames and exit(code)
// its end; run(code), when given, is code mode: its agent's js tool, resolving to the run as JSON.
// The entry points are the Worker's: command(frame), stats(), dispose(), plugins([name, on]), and
// mitosisAdmin(method, paramsJSON, "") for the owner's calls (self, join, trust, sync, remote, …).
export async function openPeer({ wasm, storage, name = "browser", env = {}, emit = () => {}, exit = () => {}, run }) {
  const go = new Go();
  const ready = new Promise((resolve, reject) => {
    globalThis.__orbPeerBoot = { helpers: globalThis.orbHelpers, resolve, reject: message => reject(new Error(message)) };
  });
  go.argv = ["orb-worker", "__orbPeerBoot"];
  go.env = {};
  let code = 1;
  go.exit = value => (code = value);
  const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
  const stopped = go.run(instance).then(() => {
    exit(code);
    throw new Error("peer.wasm exited");
  });
  stopped.catch(() => {});
  const orb = await Promise.race([ready, stopped]);
  return orb.open({ storage, env, name, emit, exit, run, dial: true });
}

// spawnPeer opens the tab's Orb, as openPeer does, in a Web Worker running this file, so what Orb
// computes (a long reply, a big tool result) never holds up the page. Its entry points resolve
// with what openPeer's return; emit(frame) gets each RPC frame as text, the frames going over
// at most every 16 ms, so a fast stream crosses in a few messages rather than thousands; run(code, names, call),
// code mode, runs in the page, where its sandboxed frame lives, and its calls come back here.
export function spawnPeer({ wasm, storage, env = {}, emit = () => {}, exit = () => {}, run }) {
  const worker = new Worker(import.meta.url, { type: "module" }), calls = new Map();
  let next = 0;
  const call = (method, ...args) => new Promise((resolve, reject) => {
    calls.set(++next, { resolve, reject });
    worker.postMessage({ id: next, method, args });
  });
  worker.onmessage = ({ data }) => {
    if ("frames" in data) return data.frames.forEach(emit);
    if ("exit" in data) return exit(data.exit);
    if ("run" in data) {
      const nested = (name, args) => call("nested", data.run, name, args);
      return run(data.code, data.names, nested).then(result => worker.postMessage({ ran: data.run, result }));
    }
    const { resolve, reject } = calls.get(data.id);
    calls.delete(data.id);
    "error" in data ? reject(new Error(data.error)) : resolve(data.value);
  };
  worker.onerror = () => exit(1);
  worker.postMessage({ boot: { wasm, storage, env, code: !!run } }, [wasm]);
  return new Proxy({}, { get: (_, method) => (...args) => call(method, ...args) });
}

// In the worker: boot peer.wasm, then answer the page's calls with the peer's entry points.
if (typeof WorkerGlobalScope !== "undefined" && self instanceof WorkerGlobalScope) {
  const runs = new Map(), decoder = new TextDecoder(), frames = [];
  let booted, nextRun = 0, flushed = 0, waiting = false;
  const flush = () => {
    waiting = false;
    flushed = performance.now();
    if (frames.length) postMessage({ frames: frames.splice(0) });
  };
  self.onmessage = async ({ data }) => {
    if (data.boot) {
      const { wasm, storage, env, code } = data.boot;
      booted = import("./wasm_exec.js").then(() => import("./helpers.js")).then(() => openPeer({
        wasm, storage: indexedStorage(storage), env,
        emit: (array, length) => {
          frames.push(decoder.decode(array.subarray(0, length)));
          if (performance.now() - flushed > 16) flush();
          else if (!waiting) waiting = !!setTimeout(flush, 16);
        },
        exit: code => { flush(); postMessage({ exit: code }); },
        run: code && ((source, names, call) => new Promise(resolve => {
          runs.set(++nextRun, { resolve, call });
          postMessage({ run: nextRun, code: source, names });
        })),
      }));
      return;
    }
    if (data.ran) {
      runs.get(data.ran).resolve(data.result);
      return runs.delete(data.ran);
    }
    try {
      const peer = await booted, [id, ...args] = data.method === "nested" ? data.args : [0, ...data.args];
      postMessage({ id: data.id, value: await (data.method === "nested" ? runs.get(id).call(...args) : peer[data.method](...args)) });
    } catch (error) {
      postMessage({ id: data.id, error: String(error?.message ?? error) });
    }
  };
}
