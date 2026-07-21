# Node shim coverage — jsbridge

Shims implemented in `codingagent/extensions/jsbridge/shims.go`, backed by Go host functions.
Upstream extensions run in Sobek (pure-Go JS engine); these shims replace Node.js built-in modules.

## Loading and packages

Load a file or package directory with `pigo --extension PATH`. pigo also discovers `.ts`/`.js`
files and package `index.ts`/`index.js` entries under the global agent `extensions/` directory and,
after trust approval, `.pi/extensions/`. Settings and `pigo install` may resolve additional local,
`npm:`, or `git:` package entries; a package's `package.json` may list entries in `pi.extensions`.

Embedded esbuild bundles TypeScript, local imports, and pure-JS dependencies from `node_modules`.
Node built-ins and the upstream pi packages stay external and resolve to the Go shims below, so no
Node runtime is required. Native `.node` addons and imported `.wasm` files are rejected clearly;
`worker_threads` and raw `net`/`tls`/`dgram` sockets are not exposed. The
[extension matrix](extension-matrix.md) is the authoritative list of supported pi and pi-tui
exports.

## Module coverage

| Module | Functions | Status |
|---|---|---|
| `fs` (sync) | `existsSync`, `readFileSync`, `writeFileSync`, `appendFileSync`, `readdirSync` (withFileTypes+Dirent), `statSync`, `lstatSync`, `mkdirSync`, `unlinkSync`, `rmdirSync`, `copyFileSync`, `renameSync`, `createReadStream`, `watch` (100ms poll), `constants` | implemented |
| `fs/promises` | `readFile`, `writeFile`, `stat`, `mkdir`, `access`, `appendFile`, `mkdtemp` (resolves relative prefixes against extension cwd), `unlink`, `rm`, `readdir`, `cp` | implemented |
| `path` | `join`, `dirname`, `basename`, `extname`, `resolve` (uses extension cwd), `relative`, `isAbsolute`, `normalize`, `parse`, `format`, `sep`, `delimiter`, `posix` — POSIX semantics validated against 119-entry Node v24 differential corpus | implemented |
| `os` | `homedir`, `tmpdir`, `hostname`, `platform()` (function), `arch()` (Node names: x64/ia32/arm/arm64), `type`, `EOL`, `cpus` | implemented |
| `process` (global) | `cwd` (extension cwd), `env`, `platform`, `arch` (Node names), `pid`, `argv`, `execPath`, `exit` (throws), `kill` (no-op), `stdout.write`, `stderr.write`, `version`, `versions` | implemented |
| `url` | `fileURLToPath`, `pathToFileURL`, `URL` (minimal constructor) | implemented |
| `util` | `promisify`, `inspect` (JSON-based), `format` (%s/%d/%j) | implemented |
| `crypto` | `randomUUID`, `randomBytes`, `createHash`, `createHmac` | implemented |
| `http` / `https` | buffered request/get clients; HTTP createServer/listen/close/address | implemented subset |
| `module` | `createRequire` backed by the bridge module resolver | implemented subset |
| `readline` | `createInterface`, line events, async iteration, question/close | implemented subset |
| `child_process` | `execSync`, `exec` (deferred callback via event loop), `execFile` (deferred callback via event loop), `spawnSync`, `spawn` (deferred execution via event loop; stdout/stderr/close/exit); options: cwd, env | implemented |
| `fetch` (global) | `fetch(url, opts)` or `fetch(Request)`→ Promise\<Response\>; resolves on headers (true streaming); body read incrementally via `getReader().read()` off VM goroutine with per-chunk 50MB enforcement; `text()`, `json()`, `arrayBuffer()` drain the stream; `bodyUsed` single-consumption semantics; reader `cancel()`/VM `Close()` clean up without leak or deadlock; Headers input: object/pairs/Headers-like | implemented |
| `Headers` (global) | Constructor: `new Headers(init?)` — init is object/pairs; methods: get (combines duplicates), has (present-empty-aware), set, append, delete, forEach, entries (deterministic sorted order) | implemented |
| `Request` (global) | Constructor: `new Request(url, opts?)` — opts: method, headers, body; usable with `fetch(req)` | implemented |
| `Response` (global) | Constructor: `new Response(body?, opts?)` — opts: status, headers; methods: text(), json(), arrayBuffer(), body.getReader(); bodyUsed single-consumption semantics | implemented |
| `Buffer` (global) | `from`, `alloc`, `concat`, `byteLength`, `isBuffer`; instances: `length`, `toString`, `slice` (negative/end bounds) | implemented |
| `console` (global) | `log`, `info`, `warn`, `error`, `debug`, `trace` | implemented |
| encoding globals | `atob`, `btoa`, `TextDecoder`, `structuredClone` | implemented subset |
| `setTimeout`/`clearTimeout` | deferred via VM event loop; honors delay; returns cancellable handle | implemented |
| `setInterval`/`clearInterval` | repeating via VM event loop; cancellable; stopped on VM close | implemented |
| `events` | `EventEmitter` (on/once/off/removeListener/removeAllListeners/addListener/emit/listenerCount) | implemented |

## VM event loop

Timer goroutines, fs.watch pollers, fetch I/O, and child_process exec/spawn goroutines post
callbacks onto a single buffered channel (`callbacks chan func(*sobek.Runtime)`, cap 64). The
VM owner goroutine selects on this channel alongside the external request channel.
`awaitPromise` pumps the callback queue while a JS promise is pending, enabling
`await fetch()`, `await new Promise(resolve => setTimeout(resolve, N))`, and deferred
child_process callbacks to work inside async factory functions and registered callbacks.

`Close()`/reload cancels the root context (which is the parent of all timer, watcher,
fetch, and child_process contexts), closes the stop channel, and waits for all goroutines
via `sync.WaitGroup` before the run loop exits.

## Known gaps

| Gap | Impact | Upgrade path |
|---|---|---|
| `fs.watch` uses 100ms polling, not inotify/kqueue | Higher latency and CPU than native watchers; sufficient for file-trigger use | Implement platform-native watcher goroutine behind the same API |
| `child_process.spawn` collects all output then delivers at once | Stream-based spawn consumers get all data at once, not incrementally | Async pipe bridging to VM callback queue |

`process.exit` deliberately throws instead of terminating the host process.

## Pinned example behavioral status

| Example | Node-dependent behavior tested | WP-520 integration seam |
|---|---|---|
| **file-trigger** | fs.watch detects file changes, callback fires on VM goroutine, reads + clears trigger file | `pi.sendMessage` (delivers content to agent conversation) |
| **protected-paths** | loads successfully; tool_call handler registered | `eventPayload`/`decodeEventResult` for ToolCallEvent must marshal/decode JS↔Go types (WP-520 canonical binding) |
| **git-checkpoint** | loads successfully; event handlers registered for tool_result, turn_start, session_before_fork, agent_end | `pi.exec` (runs git stash), `ctx.sessionManager.getLeafEntry()`, `ctx.ui.select` |
| **dirty-repo-guard** | loads successfully; event handlers registered for session_before_switch, session_before_fork | `pi.exec` (runs git status), `ctx.ui.select` |
| **claude-rules** | fs.existsSync, fs.readdirSync(withFileTypes), path.join scan .claude/rules/; before_agent_start appends rules to system prompt | fully functional — no WP-520 dependency |
