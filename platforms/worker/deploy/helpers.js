// The Worker runtime's JavaScript side (cmd/orb-worker), wherever it runs: a Durable Object
// (deploy/worker.mjs) or a browser tab (platforms/browser/web/peer.js). A classic script, so the
// Worker bundle can prepend it as it does wasm_exec.js.
//
// Go waits on JavaScript through these helpers (platforms/worker): each takes
// the wait's id and the runtime's one settle callback, and calls it once with
// the id and the results. The http transport (fetch_js.go) crosses as little
// as it can: headers travel as "name\nvalue" lines, a response settles once,
// and each read brings one chunk, null at the end or an error message.
{
  const message = error => (error instanceof Error ? String(error.message) : String(error));
  globalThis.orbHelpers = {
    // await settles with the promise's value, or undefined and the reason.
    await(promise, id, settle) {
      promise.then(value => settle(id, value), error => settle(id, undefined, message(error)));
    },
    fetch(url, method, headers, body, id, settle) {
      const controller = new AbortController();
      const lines = headers === "" ? [] : headers.split("\n");
      const pairs = [];
      for (let index = 0; index + 1 < lines.length; index += 2) pairs.push([lines[index], lines[index + 1]]);
      fetch(url, { method, headers: pairs, body, signal: controller.signal }).then(
        response => settle(id, response.status, [...response.headers].flat().join("\n"), response.redirected ? response.url : "", response.body?.getReader() ?? null),
        error => settle(id, 0, `net/http: fetch() failed: ${error}${error?.cause === undefined ? "" : `: ${error.cause}`}`),
      );
      return controller;
    },
    // pause settles on the event loop's next turn, so what waits there (a stop) runs.
    pause(id, settle) {
      setTimeout(() => settle(id), 0);
    },
    read(reader, id, settle) {
      reader.read().then(
        ({ value }) => settle(id, value ?? null),
        error => settle(id, String(error?.message ?? error)),
      );
    },
    // A body Go stopped reading: one that has ended needs nothing, and
    // cancelling it would abort a finished request.
    release(reader) {
      reader.read().then(({ done }) => done || reader.cancel(), () => {});
    },
  };
}
