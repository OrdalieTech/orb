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
// its end. The entry points are the Worker's: command(frame), stats(), dispose(), and
// mitosisAdmin(method, paramsJSON, "") for the owner's calls (self, join, trust, sync, remote, …).
export async function openPeer({ wasm, storage, name = "browser", env = {}, emit = () => {}, exit = () => {} }) {
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
  return orb.open({ storage, env, name, emit, exit, dial: true });
}
