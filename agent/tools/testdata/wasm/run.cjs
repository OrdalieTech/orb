// Leave globalThis.fs unset: Go must run with its browser filesystem stubs.
const fs = require("node:fs");
globalThis.crypto = require("node:crypto").webcrypto;
// Node omits the POSIX identity calls on win32, and wasm_exec.js supplies its
// fallbacks only when process itself is absent: Go's os/user would call them.
for (const name of ["getuid", "getgid", "geteuid", "getegid"]) process[name] ??= () => -1;
process.getgroups ??= () => { throw Object.assign(new Error("not implemented"), { code: "ENOSYS" }); };
require(process.argv[2]);
const go = new Go();
go.exit = code => { process.exitCode = code; };
WebAssembly.instantiate(fs.readFileSync(process.argv[3]), go.importObject)
  .then(({instance}) => go.run(instance))
  .catch(error => { console.error(error); process.exitCode = 1; });
