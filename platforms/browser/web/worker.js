// This worker owns the Wasm lifetime; it has no DOM or application logic.
importScripts("wasm_exec.js");

self.onmessage = ({ data }) => {
  try {
    const error = self.orbDispatch(JSON.stringify(data));
    if (error) self.postMessage(JSON.stringify({ type: data.id ? "bridge.result" : "error", id: data.id, error }));
  } catch (error) {
    self.postMessage(JSON.stringify({ type: data.id ? "bridge.result" : "error", id: data.id, error: error.message }));
  }
};

async function boot() {
  const go = new Go();
  const response = await fetch("orb.wasm");
  if (!response.ok) throw new Error(`Wasm download failed: ${response.status}`);
  const { instance } = await WebAssembly.instantiate(await response.arrayBuffer(), go.importObject);
  await go.run(instance);
  throw new Error("Orb's Wasm runtime exited");
}

boot().catch(error => self.postMessage(JSON.stringify({ type: "fatal", error: error.message })));
