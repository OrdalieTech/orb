# Orb on Durable Objects

The Worker host runs the full Orb runtime inside a Cloudflare Durable Object. The
same bundle and the same `wrangler.jsonc` also run in a self-hosted
[workerd](https://github.com/cloudflare/workerd), the open-source runtime behind
Workers. It is a runtime, not a thin client: the agent loop, tools, session
journal and provider calls all run in the object.

## What runs where

```
client ──HTTPS/WSS──▶ Worker (dist/worker.mjs default export)
                        │  checks the bearer token, then routes /agents/<name>/…
                        ▼  env.ORB_AGENT.idFromName(<name>)
                      Durable Object OrbAgent (one per agent name)
                        │  first event: open its Orb in the isolate's runtime
                        ▼  (orb.wasm, cmd/orb-worker, instantiated once per isolate)
                      Go runtime: one platforms/worker.Instance per object
                        host.Host{FS, Store, Env}  ◀──▶  ctx.storage (SQLite-backed)
                        agent session + agent/rpc  ──▶  provider APIs via fetch
```

- **Bundle.** `make -f platforms/worker/worker.mk worker-build` writes
  `.tools/worker/dist/worker.mjs` and `.tools/worker/dist/orb.wasm`. `worker.mjs` is
  Go's `wasm_exec.js`, `deploy/helpers.js` and `deploy/worker.mjs`, so the bundle is one
  JavaScript module plus a Wasm module. `no_bundle: true` means Wrangler runs no
  bundler.
- **Objects.** Each agent name gets its own Durable Object, with its own storage,
  session and Go instance. The name must match `[A-Za-z0-9._~-]{1,128}`. Use one
  name per conversation or per long-lived agent instance. `idFromName` is
  deterministic, so the same name always reaches the same object.
- **Lifecycle.** An object opens its Orb on its first event after a start,
  eviction or hibernation. Opening restores the files and documents from storage
  and resumes the current session. There is no in-memory state that storage does
  not also hold. The objects of an isolate share one Go runtime: only the first
  pays for instantiating it and its one-time setup, and each object adds a few
  megabytes rather than a runtime's worth. An object disposes of its Orb once it
  is collected, and a runtime that stops, as a panic does, stops its objects
  until their next event.

## Capability profile

| Port | Worker host |
| --- | --- |
| FS | A `platforms/memory` tree rooted at `/`, with the workspace at `/workspace` and the agent directory at `/agent`. Every mutation is written through to Durable Object storage before it returns (keys `fs/…`, 4 KiB chunks). A restart restores the tree, including modification times. The default budget is 32 MiB, held in memory. |
| Store | `settings.json`, `models.json` and `auth.json` live in storage under `doc/…`, outside the tools' reach. `ORB_SETTINGS` and `ORB_MODELS` supply the defaults until an object writes its own. |
| Sessions | JSONL journals live under `/agent/sessions`. The object resumes its current session. `new_session` and `switch_session` work; `fork` and `clone` return an error. |
| Env | Worker secrets and vars, looked up by their standard names (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, …). There are no credential files and no interactive OAuth logins. |
| Exec | None. `bash` is omitted, and so are process MCP servers and JS extensions. `grep` needs ripgrep through Exec, so it fails if a setting enables it. `read`, `write` and `edit` are active; `ls` and `find` run over the FS port. |
| Net | Outbound HTTP uses the global `fetch` through a transport of the Worker's own (`fetch_js.go` with the shim's fetch helpers), and SSE streams arrive through `ReadableStream`. Every wait on JavaScript, fetches and storage calls alike, settles through one callback per runtime. The object does not listen on any port; the Worker routes requests to it. `nodejs_compat` is not needed. |

The object does not load context files, skills or prompt templates. The resource
loader reads the process file system, and that is not yet served through the FS
port.

## Mitosis peer

Each object is a full Mitosis peer (`platforms/worker/peer`). Its identity (an
Ed25519 key), grants, pairings and operation receipts live in Store documents
under `/mitosis/`, so they survive restarts. The object's own Orb is enrolled
once, under an alias derived from the object name, and is attached as its
`orb.instance/1` service whenever the peer is open.

- **Inbound streams.** `/agents/<name>/mitosis` accepts a WebSocket that carries
  Mitosis's pinned TLS 1.3 stream, as `orb mitosis run --web-listen` does. The
  route needs no `ORB_TOKEN`: a remote Orb never has it, and Mitosis
  authenticates the peer inside the stream. The socket is accepted, not
  hibernatable. The TLS session lives in the Go runtime, which hibernation
  would discard while the socket stayed open, and the open socket keeps the
  object resident. An object without a Mitosis identity answers 404 without
  booting. A browser `Origin` must be listed in `ORB_MITOSIS_ORIGINS`.
- **Outbound calls.** The object cannot dial (tailcat is native-only, and a
  Worker has no listener of its own). It reaches another peer over a channel
  that peer opened: a laptop Orb that joined keeps its channel, and the object's
  agent calls it with `mitosis_call`. That tool is active only when
  `plugins.mitosis-agent-calls` is on in the object's settings. Each call also
  needs the object's own instance grant for the destination, and a grant on
  the destination for the object's instance subject.
- **Plugins.** The object's settings turn on `titles`, `tasks`, `questions` (answered by the app
  or peer driving the object), `memory` (its journal is the document `/agent/memory/memory.jsonl`,
  shared with the object's Mitosis sync group) and `memtree` (experimental, `docs/plugins.md`): with memtree, every prompt starts from a fixed-size view of the whole
  conversation, and its summaries live in the session journal in object storage.
- **Admin.** `POST /agents/<name>/mitosis/admin` with `{"method", "params"}`,
  behind `ORB_TOKEN`, runs the owner methods of `orb mitosis`: `status`,
  `instances`, `peers`, `grants`, `invite`, `approve`, `block`, `grant`,
  `revoke`, `scope`, `group`, `assign`, `takeover`, `trust` (a full controller
  grant) and `remote`. `self` returns the peer ID, instance ID and stream URL.
  `invite` advertises `wss://<host>/agents/<name>/mitosis`, or the same path on
  `ORB_PUBLIC_ORIGIN` when a proxy fronts the Worker. `join`, `publish` and the
  service lifecycle are refused, because the object cannot dial.

Pairing a laptop Orb with an object:

```sh
curl -s -X POST https://<host>/agents/demo/mitosis/admin -H "Authorization: Bearer $ORB_TOKEN" \
  -d '{"method":"invite"}' > invitation.json
orb mitosis pair join < invitation.json          # prints the claim, with the laptop's peer ID
curl -s -X POST https://<host>/agents/demo/mitosis/admin -H "Authorization: Bearer $ORB_TOKEN" \
  -d '{"method":"approve","params":{"invitation_id":"<id>","claimant":"<laptop peer ID>"}}'
orb mitosis trust <object peer ID>               # optional: the object may list and call this laptop
orb mitosis remote <object peer ID> agent.describe < /dev/null
```

## Deploy

Cloudflare, with Wrangler credentials from `npx wrangler login` or
`CLOUDFLARE_API_TOKEN`:

```sh
make -f platforms/worker/worker.mk worker-deploy        # builds .tools/worker, then wrangler deploy
```

The installed `cf` CLI (0.4) delegates `cf dev` and `cf deploy` to Wrangler only
through the experimental `cloudflare.config.ts`. The targets therefore call the
Wrangler version pinned in `deploy/package.json`, which reads `wrangler.jsonc`.

Local servers read `.tools/worker/.dev.vars` (dotenv format, never committed):

```sh
make -f platforms/worker/worker.mk worker-dev           # workerd via wrangler dev, http://127.0.0.1:8787
```

## Secrets and configuration

| Name | Kind | Purpose |
| --- | --- | --- |
| `ORB_TOKEN` | secret, required | Bearer token for every `/agents/…` route, at least 16 characters. Without it the Worker answers 503. |
| provider keys | secret | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` and the other standard names. |
| `ORB_SETTINGS` | var | Default `settings.json` in kernel format. The template ships with `{"defaultProvider":"anthropic","defaultModel":"claude-sonnet-5"}`. |
| `ORB_MODELS` | var | Default `models.json`, for custom providers. |

On Cloudflare, set secrets with `cd .tools/worker && npx wrangler secret put ORB_TOKEN`
and do the same for each provider key. A client can change the model of a
session with the RPC `set_model` command.

## Endpoint contract

Every `/agents/<name>/…` route except the Mitosis stream requires
`Authorization: Bearer <ORB_TOKEN>`. A missing or wrong token gets 401.

| Route | Behavior |
| --- | --- |
| `GET /health` | `200 ok`, with no authentication. |
| `GET /agents/<name>/rpc` + `Upgrade: websocket` | A hibernatable WebSocket. Each client message carries one or more pi RPC command frames, separated by LF. Each server message is one output frame: `response`, an agent event or `extension_ui_request`. The object sends its frames to every socket it has open. |
| `POST /agents/<name>/rpc` | The body is NDJSON command frames. The reply is `200 application/x-ndjson` and streams the object's frames while the request is open. It ends once every command has its `response` and any run a command started has sent `agent_settled`. |
| `GET /agents/<name>/stats` | JSON: `bootId`, `bootMs`, `busy` and `pending` for the object, and `wasmMemoryBytes` and Go memory statistics for the isolate's runtime. |
| `GET /agents/<name>/mitosis` + `Upgrade: websocket` | A Mitosis stream (binary messages, pinned TLS inside). **No bearer token**: Mitosis authenticates the peer. 404 until the object has a Mitosis identity. |
| `POST /agents/<name>/mitosis/admin` | `{"method": "…", "params": {…}}`: an owner method of `orb mitosis` (see [Mitosis peer](#mitosis-peer)). Returns the method's JSON result, or 400 with `{"error"}`. |

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

- **Size.** With the Mitosis peer, `orb.wasm` is 27.9 MB raw and 7.0 MB gzip,
  about 30% under the 10 MB compressed limit of the paid plan. The free plan
  allows 3 MB, so it cannot host Orb.
- **Memory.** Cloudflare gives each isolate 128 MB. The isolate's objects share
  one runtime, whose Wasm linear memory is about 21 MB with one object after a
  tool-using turn and grows by about 1.7 MB per further object, mostly Go heap
  (89 MB for 40 objects under Node). Linear memory only grows, so this figure is
  also the peak.
- **Time.** In local workerd a boot takes 0.3–1.7 s, and the first frame after a
  restart arrives in 0.25–0.75 s. The Durable Object CPU limit is 30 s per event
  by default.
- **Timers.** After each provider request, Go's js/wasm scheduler leaves a
  JavaScript timer pending for up to `httpIdleTimeoutMs` (5 minutes by default).
  A pending timer keeps the object out of hibernation until it fires.
- **Runs.** One run per object at a time. A prompt sent while a run streams needs
  `streamingBehavior`, as in RPC mode.

## Delete

`cd .tools/worker && npx wrangler delete --name orb-agent` removes the Worker. To
erase stored objects first, deploy a migration with
`"deleted_classes": ["OrbAgent"]`.

## Tests and gates

| Command | Covers |
| --- | --- |
| `go test ./platforms/worker/... ./cmd/orb-worker/...` | FS conformance (`envtest`), restart persistence and key hygiene, documents, session resume, `new_session` and `switch_session`. It also runs the real bundle in Node against a Map-backed Durable Object storage, restarts the object and asserts the frames. |
| `GOOS=js GOARCH=wasm go test -exec="$(go env GOROOT)/lib/wasm/go_js_wasm_exec" ./platforms/worker/...` | The same suites through the syscall/js storage mitosis, against an asynchronous fake `ctx.storage`. |
| `make -f platforms/worker/worker.mk worker-e2e-workerd` | workerd (`wrangler dev`) with a local scripted OpenAI-compatible model: write then read over WebSocket, a dev-server restart, then history and file checks over HTTP NDJSON. |
| `make -f platforms/worker/worker.mk worker-e2e-mitosis-workerd` | Mitosis in both directions with this tree's native `orb`, confined to `.tools/worker-mitosis-e2e/laptop` (its own `HOME`, `ORB_STATE_HOME`, `ORB_MITOSIS_HOME` and `ORB_AGENT_DIR`, profile `e2e`). The object invites, the laptop joins over the advertised URL, and the owner approves. The laptop describes the object's agent, inspects and prompts its conversation and reads it back with `orb mitosis remote`. The object's agent then calls the laptop Orb's conversation with `mitosis_call` under its own agent grant. After a restart the laptop reconnects to the same identity. |
| `make -f platforms/worker/worker.mk worker-e2e-mitosis-deployed ORB_WORKER_URL=…` | The same against a deployed `worker-e2e-build` bundle. It redeploys `.tools/worker-e2e` once to restart the object. |
| `worker-e2e-build`, then `worker-e2e-deployed` | A deployed check. `worker-e2e-build` adds `e2e/fake-model.js`, so the scripted model runs inside the Worker and no provider key is needed. `node platforms/worker/e2e/e2e.mjs --configure-deploy .tools/worker-e2e --name orb-do-e2e` writes its `wrangler.json`. Deploy that directory and set `ORB_TOKEN`. Then run `WORKER_E2E_PHASE=write`, redeploy (a new version restarts every object) and run `WORKER_E2E_PHASE=verify`, with `ORB_WORKER_URL` and `ORB_TOKEN` set. |
