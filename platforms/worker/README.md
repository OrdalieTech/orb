# Orb on Durable Objects and Celld

The Worker host runs the full Orb runtime inside a Cloudflare Durable Object. The
same bundle and the same `wrangler.jsonc` also run on a self-hosted
[Celld](https://github.com/denoland/celld) cell. It is a runtime, not a thin
client: the agent loop, tools, session journal and provider calls all run in the
object.

## What runs where

```
client ──HTTPS/WSS──▶ Worker (dist/worker.mjs default export)
                        │  checks the bearer token, then routes /agents/<name>/…
                        ▼  env.ORB_AGENT.idFromName(<name>)
                      Durable Object OrbAgent (one per agent name)
                        │  first event: instantiate orb.wasm (cmd/orb-worker)
                        ▼
                      Go runtime: platforms/worker.Instance
                        host.Host{FS, Store, Env}  ◀──▶  ctx.storage (SQLite-backed)
                        agent session + agent/rpc  ──▶  provider APIs via fetch
```

- **Bundle.** `make -f platforms/worker/worker.mk worker-build` writes
  `.tools/worker/dist/worker.mjs` and `.tools/worker/dist/orb.wasm`. `worker.mjs` is
  Go's `wasm_exec.js` followed by `deploy/worker.mjs`, so the bundle is one
  JavaScript module plus a Wasm module. `no_bundle: true` means neither Wrangler
  nor Celld runs a bundler, and Celld does not need esbuild.
- **Objects.** Each agent name gets its own Durable Object, with its own storage,
  session and Go instance. The name must match `[A-Za-z0-9._~-]{1,128}`. Use one
  name per conversation or per long-lived agent instance. `idFromName` is
  deterministic, so the same name always reaches the same object. On Celld, the
  id also depends on the Worker name: renaming the Worker there moves every name
  to a new, empty object.
- **Lifecycle.** An object boots its Go runtime on its first event after a start,
  eviction or hibernation. Boot restores the files and documents from storage and
  resumes the current session. There is no in-memory state that storage does not
  also hold.

## Capability profile

| Port | Worker host |
| --- | --- |
| FS | A `platforms/memory` tree rooted at `/`, with the workspace at `/workspace` and the agent directory at `/agent`. Every mutation is written through to Durable Object storage before it returns (keys `fs/…`, 64 KiB chunks). A restart restores the tree, including modification times. The default budget is 32 MiB, held in memory. |
| Store | `settings.json`, `models.json` and `auth.json` live in storage under `doc/…`, outside the tools' reach. `ORB_SETTINGS` and `ORB_MODELS` supply the defaults until an object writes its own. |
| Sessions | JSONL journals live under `/agent/sessions`. The object resumes its current session. `new_session` and `switch_session` work; `fork` and `clone` return an error. |
| Env | Worker secrets and vars, looked up by their standard names (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, …). There are no credential files and no interactive OAuth logins. |
| Exec | None. `bash` is omitted, and so are process MCP servers and JS extensions. `grep` needs ripgrep through Exec, so it fails if a setting enables it. `read`, `write` and `edit` are active; `ls` and `find` run over the FS port. |
| Net | Outbound HTTP uses the global `fetch` through Go's js/wasm transport, and SSE streams arrive through `ReadableStream`. The object does not listen on any port; the Worker routes requests to it. `nodejs_compat` is not needed. |

The object does not load context files, skills or prompt templates. The resource
loader reads the process file system, and that is not yet served through the FS
port. The `/agents/<name>/bridge` route is reserved for the Bridge peer endpoint.

## Deploy

Cloudflare, with Wrangler credentials from `npx wrangler login` or
`CLOUDFLARE_API_TOKEN`:

```sh
make -f platforms/worker/worker.mk worker-deploy        # builds .tools/worker, then wrangler deploy
```

Celld, with a fleet bucket:

```sh
make -f platforms/worker/worker.mk worker-build && .tools/bin/celld deploy .tools/worker --bucket s3://my-cells-bucket
```

The installed `cf` CLI (0.4) delegates `cf dev` and `cf deploy` to Wrangler only
through the experimental `cloudflare.config.ts`, which Celld cannot read. The
targets therefore call the Wrangler version pinned in `deploy/package.json`,
which shares `wrangler.jsonc` with Celld.

Local servers read `.tools/worker/.dev.vars` (dotenv format, never committed):

```sh
make -f platforms/worker/worker.mk worker-dev           # workerd via wrangler dev, http://127.0.0.1:8787
make -f platforms/worker/worker.mk worker-celld-dev     # Celld, http://127.0.0.1:9876 (installs celld into .tools)
```

## Secrets and configuration

| Name | Kind | Purpose |
| --- | --- | --- |
| `ORB_TOKEN` | secret, required | Bearer token for every `/agents/…` route, at least 16 characters. Without it the Worker answers 503. |
| provider keys | secret | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` and the other standard names. |
| `ORB_SETTINGS` | var | Default `settings.json` in kernel format. The template ships with `{"defaultProvider":"anthropic","defaultModel":"claude-sonnet-5"}`. |
| `ORB_MODELS` | var | Default `models.json`, for custom providers. |

On Cloudflare, set secrets with `cd .tools/worker && npx wrangler secret put ORB_TOKEN`
and do the same for each provider key. Celld has no secret store: `celld dev`
reads `.dev.vars`, and a fleet takes string `vars` from the deployed config.
Keep that config out of git. A client can change the model of a session with the
RPC `set_model` command.

## Endpoint contract

Every `/agents/<name>/…` route requires `Authorization: Bearer <ORB_TOKEN>`. A
missing or wrong token gets 401.

| Route | Behavior |
| --- | --- |
| `GET /health` | `200 ok`, with no authentication. |
| `GET /agents/<name>/rpc` + `Upgrade: websocket` | A hibernatable WebSocket. Each client message carries one or more pi RPC command frames, separated by LF. Each server message is one output frame: `response`, an agent event or `extension_ui_request`. The object sends its frames to every socket it has open. |
| `POST /agents/<name>/rpc` | The body is NDJSON command frames. The reply is `200 application/x-ndjson` and streams the object's frames while the request is open. It ends once every command has its `response` and any run a command started has sent `agent_settled`. |
| `GET /agents/<name>/stats` | JSON: `bootId`, `bootMs`, `wasmMemoryBytes`, Go memory statistics, `busy` and `pending`. |
| `/agents/<name>/bridge` | `501`, reserved for the Bridge peer endpoint. |

Frames are the kernel RPC frames of `orb --mode rpc` (`agent/rpc`), byte for
byte. A transport failure, such as a runtime that failed to boot, arrives as
`{"type":"worker_error","error":"…"}`. That frame is specific to this host and
is not part of the kernel.

```sh
curl -N https://orb-agent.<account>.workers.dev/agents/demo/rpc \
  -H "Authorization: Bearer $ORB_TOKEN" --data-binary @- <<'EOF'
{"id":"1","type":"prompt","message":"Create notes/todo.md with three items"}
EOF
```

## Limits

- **Size.** `orb.wasm` is 53.6 MB raw and 9.66 MB gzip. Wrangler reports a total
  upload of 9.61 MB gzip, which is under the 10 MB compressed limit of the paid
  plan. The free plan allows 3 MB, so it cannot host Orb.
- **Memory.** Cloudflare gives each isolate 128 MB. After a tool-using turn, one
  object uses 36–38 MB of Wasm linear memory, of which 3–6 MB is Go heap. Linear
  memory only grows, so this figure is also the peak. Several objects can share
  an isolate, and each runs its own Go instance.
- **Time.** In local workerd a boot takes 0.3–1.7 s, and the first frame after a
  restart arrives in 0.25–0.75 s. Celld's first activation of a cell also
  compiles the module, which takes about 2.8 s in its isolate log. The Durable
  Object CPU limit is 30 s per event by default.
- **Timers.** After each provider request, Go's js/wasm scheduler leaves a
  JavaScript timer pending for up to `httpIdleTimeoutMs` (5 minutes by default).
  A pending timer keeps the object out of hibernation until it fires.
- **Runs.** One run per object at a time. A prompt sent while a run streams needs
  `streamingBehavior`, as in RPC mode.
- **Celld.** `performance.now()` stays fixed while JavaScript runs, so `bootMs`
  reads 0. Celld's `process` global is partial, and the shim fills in the members
  Go needs.

## Delete

- **Cloudflare.** `cd .tools/worker && npx wrangler delete --name orb-agent`
  removes the Worker. To erase stored objects first, deploy a migration with
  `"deleted_classes": ["OrbAgent"]`.
- **Celld.** Locally, `celld dev --clean` discards `.tools/worker/.celld/dev`. For
  a fleet, remove the deployment's prefix from the bucket.

## Tests and gates

| Command | Covers |
| --- | --- |
| `go test ./platforms/worker/... ./cmd/orb-worker/...` | FS conformance (`envtest`), restart persistence and key hygiene, documents, session resume, `new_session` and `switch_session`. It also runs the real bundle in Node against a Map-backed Durable Object storage, restarts the object and asserts the frames. |
| `GOOS=js GOARCH=wasm go test -exec="$(go env GOROOT)/lib/wasm/go_js_wasm_exec" ./platforms/worker/...` | The same suites through the syscall/js storage bridge, against an asynchronous fake `ctx.storage`. |
| `make -f platforms/worker/worker.mk worker-e2e-workerd` | workerd (`wrangler dev`) with a local scripted OpenAI-compatible model: write then read over WebSocket, a dev-server restart, then history and file checks over HTTP NDJSON. |
| `make -f platforms/worker/worker.mk worker-e2e-celld` | The same scenario under `celld dev`, including a node restart. |
| `worker-e2e-build`, then `worker-e2e-deployed` | A deployed check. `worker-e2e-build` adds `e2e/fake-model.js`, so the scripted model runs inside the Worker and no provider key is needed. `node platforms/worker/e2e/e2e.mjs --configure-deploy .tools/worker-e2e --name orb-do-e2e` writes its `wrangler.json`. Deploy that directory and set `ORB_TOKEN`. Then run `WORKER_E2E_PHASE=write`, redeploy (a new version restarts every object) and run `WORKER_E2E_PHASE=verify`, with `ORB_WORKER_URL` and `ORB_TOKEN` set. |
