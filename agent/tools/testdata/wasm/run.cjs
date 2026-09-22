// Leave globalThis.fs unset: Go must run with its browser filesystem stubs.
const fs = require("node:fs");
globalThis.crypto = require("node:crypto").webcrypto;
require(process.argv[2]);
const go = new Go();
go.exit = code => { process.exitCode = code; };
WebAssembly.instantiate(fs.readFileSync(process.argv[3]), go.importObject)
  .then(({instance}) => go.run(instance))
  .catch(error => { console.error(error); process.exitCode = 1; });
