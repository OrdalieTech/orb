# Ecosystem extension compatibility matrix — 2026-07-26 300-package, three-runtime run at `f64f4f7`

This supersedes the 2026-07-25 run at `53222c3`. Same corpus, same harness, same container, same
upstream Pi 0.81.1 reference and same Bun build: the pigo binary is the only input whose hash
changed, so the difference between the two runs is attributable to
[`f64f4f7` *Run TypeScript published inside node_modules*](#what-running-typescript-under-node_modules-changed)
and to nothing else. pigo is measured on **both** JavaScript hosts it can resolve — local Node 24 and
local Bun 1.3.14 — because a single "does it work" number hid the runtime that produced it.

A later, deliberately narrower Node-only sweep at
[`d620c01`](#node-only-regression-sweep-at-d620c01) re-measured all 300 packages after `77c4c33`
deleted the staged-entry mechanism. Read that section before quoting any number here as current.

The follow-up [normal-install live matrix](ecosystem-extension-live.md) installs 30 packages through
Pi itself, opens the resulting state with Pigo, and executes 15 live extension workflows.

## Headline

Over the full 300-package corpus:

| Runtime | load + registration | load-only | **load compatible** | flaky | unsupported | vs `53222c3` |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| `pigo-node` | 210 | 21 | **231 / 300 = 77.0 %** | 2 | 67 | **+20** |
| `pigo-bun` | 202 | 20 | **222 / 300 = 74.0 %** | 2 | 76 | ±0 |

Pinned upstream Pi 0.81.1 fails to load 15 of the 300 packages itself, and three more never reach a
probe at all (see *Install and harness exclusions*). Against the 282 packages upstream actually
supports, parity is **231/282 = 81.9 %** on Node (was 74.8 %) and **222/282 = 78.7 %** on Bun.

Bun is unchanged by construction: `loader.mjs` is installed only when the resolved engine is Node
(`codingagent/extensions/host/manager.go:381-387`) and Bun has no equivalent hook
(`runtime_entry.go:51-57`). Node has now overtaken Bun on the full corpus.

Tier 1 — the top 50 by monthly downloads, and the number that describes what most users install — is
better than the corpus average and is the more meaningful user-facing figure:

| Scope | `pigo-node` | `pigo-bun` |
| --- | ---: | ---: |
| Tier 1, `perf` profile (13 attempts/runtime, from the 300 run) | 44/50 = 88.0 % | 39/50 = 78.0 % |
| Tier 1, `compat` profile (3 attempts/runtime, dedicated A/B run) | 44/50 = 88.0 % | 39/50 = 78.0 % |
| Tier 1, weighted by monthly downloads (`perf` verdicts) | 96.2 % | 81.5 % |
| All 300, weighted by monthly downloads | 89.0 % | 78.5 % |

Tier 1 gains two packages at both attempt budgets. The `compat` row loses two others that the
`53222c3` `compat` run happened to catch on the lucky side of a race — `pi-intercom` and
`pi-cursor-sdk`, neither a verdict on the package and both named below — which is why the two rows
agree at 44/50 here.

## What running TypeScript under `node_modules` changed

Node refuses to strip types from any TypeScript file whose path contains a `node_modules` segment.
`53222c3` routed around it by staging the extension entry into a `node_modules`-free path and
mirroring the manifest's direct dependencies; a dependency published as TypeScript was reached
through npm's own layout and still refused, at any nesting depth. `f64f4f7` supplies already
transpiled source from the loader's `load` hook instead, leaving resolution untouched.

**All 22 packages that failed on Node only with `typescript_unsupported / typescript_in_node_modules`
were re-measured. 20 now pass. Zero packages regressed, on either runtime.**

| Outcome | Count | Packages |
| --- | ---: | --- |
| now `load_register_pass` | 18 | `@juicesharp/rpiv-{ask-user-question,todo,advisor,voice,web-tools,i18n,pi}`, `@mrclrchtr/supi-{ask-user,cache,context,debug,extras,insights,review,settings}`, `@aliou/pi-{guardrails,synthetic}`, `@maestria/pi` |
| now `load_only_pass` | 2 | `@mrclrchtr/supi-bash-timeout`, `@mrclrchtr/supi-prompt-suggestions` |
| still failing, new cause | 2 | `@aliou/pi-neuralwatt`, `@firstpick/pi-extension-todo-progress` |

Two packages that failed on **both** runtimes with the same Node-side class also got past type
stripping and now fail later, so `typescript_unsupported` is gone from the taxonomy entirely except
for one package upstream Pi cannot load either:

| Package | `53222c3` (node) | `f64f4f7` (node) |
| --- | --- | --- |
| `@fiale-plus/pi-rogue` | `typescript_in_node_modules` | `extension_load_error Cannot add property __piRogueBundleRegistered, object is not extensible` |
| `@mrclrchtr/supi-code-intelligence` | `typescript_in_node_modules` | `unsupported_sdk_export vscode-languageserver-protocol export DidChangeWatchedFilesParams` |
| `@cgh567/agent` | `typescript_in_node_modules` | unchanged — upstream Pi fails it too, excluded from parity |

`@cgh567/agent` still reports the refusal, and the message shape says why: it names a bare path
(`for "/packages/node_modules/@cgh567/agent/lib/container.ts"`) rather than a `file://` URL, so the
file is reached through CommonJS `require`, which `--experimental-loader` hooks do not intercept.
The package loads its own extensions through a vendored `jiti`
(`@cgh567/agent/node_modules/@earendil-works/pi-coding-agent/node_modules/jiti/lib/jiti.cjs`,
visible in its diagnostics). Upstream Pi fails this package too, so it is outside the parity
denominator either way.

### The two that did not make it, and exactly why

* **`@aliou/pi-neuralwatt` — `extension_load_error`, JSON import attribute.**
  `src/config/loader.ts:2` and `src/config/migration/02-flat-to-nested-config.ts:5` do
  `import packageJson from "../../package.json"` with no `with { type: "json" }`. Node ESM requires
  the attribute; upstream Pi tolerates the import through its own loader. This is the same class as
  `pi-grok-cli`, and it is the *next* blocker behind the one that was removed, not a new defect.
* **`@firstpick/pi-extension-todo-progress` — `unsupported_sdk_export @firstpick/pi-utils export
  extractChecklist`.** This one is a real limit of the new load path and is worth acting on.
  `loadRefusedTypeScript` returns `format: await typeScriptFormat(url)`
  (`codingagent/extensions/host/loader.mjs`), which mirrors Node's rule that a bare `.ts` follows the
  nearest `package.json` `type` and defaults to `commonjs` — but the source it returns alongside that
  format is still ES module syntax. `@firstpick/pi-utils` publishes `"exports": {".": "./index.ts"}`
  with **no `"type": "module"`**, so Node is handed ESM source declared CommonJS. Reproduced directly
  in the same container against the same install tree:

  ```
  node --experimental-transform-types --experimental-loader loader.mjs \
    -e 'await import("@firstpick/pi-utils")'
  → SyntaxError: Unexpected token 'export'
  ```

  with a `"type": "module"` package (`@juicesharp/rpiv-config`) importing cleanly in the same probe.
  Under a static named import the failure surfaces as the missing-export message the matrix recorded.
  Exposure is limited to *dependencies*: an extension's own entry is still staged out of
  `node_modules` and never takes this path, so of the 31 corpus packages whose manifest entry is `.ts`
  in a package with no `"type": "module"`, 20 pass on Node and none of the 11 failures is this defect.
  One package in the corpus reaches it, through a dependency.

  This measurement is pinned to `f64f4f7`. A later commit on `main`, `77c4c33` *Run extensions on
  whatever Node the machine has*, independently identified the same defect ("read an ESM package
  published without `"type": "module"` … as CommonJS; module syntax is now detected") and deleted the
  staged-entry mechanism outright, which also removes the entry-side exemption described above.
  Neither that commit nor `0686971` is measured here; re-running the corpus at `main` is the way to
  price them.

### Tier 1, dedicated `compat` A/B

The fast A/B was run first, `--tier 1 --profile compat`, same container, same install tree. Node
stays at 44/50 and the composition under it is the interesting part:

| Runtime | Package | `53222c3` | `f64f4f7` | Caused by this commit? |
| --- | --- | --- | --- | --- |
| node | `@juicesharp/rpiv-ask-user-question` | `typescript_in_node_modules` | pass, registers `ask_user_question` | yes |
| node | `@juicesharp/rpiv-todo` | `typescript_in_node_modules` | pass, registers `todo` and `/todos` | yes |
| node + bun | `pi-intercom` | pass | `install_failure ENOTEMPTY rmdir '/work/matrix-6'` | no — harness cleanup race |
| node + bun | `pi-cursor-sdk` | pass | `flaky registration_instability(activeTools:cursor_ask_question)` | no — known registration race, 3 attempts is a coin flip |

Counting only genuine incompatibilities — excluding the harness artifact and the known flake — Node
tier 1 goes 44/50 → 46/50, and the four that remain are the four already on the ledger: `gentle-pi`,
`pi-memory`, `pi-llama-cpp`, `pi-hashline-edit-pro`. Bun's 41 → 39 is the same two packages and
nothing else; no Bun-only verdict changed.

## What the SDK-resolution fix changed (previous step, `53222c3`)

The pre-fix baseline for tier 1 on the same corpus, same profile (`compat`), same container was
`pigo-node 39/50` and `pigo-bun 38/50`, with divergence `none 33, node_only 5, bun_only 6,
both_fail 6`. The post-fix rerun is `pigo-node 44/50` and `pigo-bun 41/50`, divergence
`none 38, node_only 3, bun_only 6, both_fail 3`.

**Zero packages regressed on either runtime.** Per package:

| Runtime | Package | Before | After |
| --- | --- | --- | --- |
| node | `pi-mcp-adapter` | `unsupported_sdk_export @earendil-works/pi-ai export complete` | pass |
| node | `pi-powerline-footer` | `extension_load_error extension host exited: signal: killed` | pass |
| node | `pi-vault-mind` | `registration_mismatch command_registration.description` | pass |
| node | `open-zk-kb` | `flaky extension host exited: signal: terminated` | pass |
| node | `pi-cursor-sdk` | `flaky registration_instability` | pass at 4 attempts, still flaky at 14 |
| node | `pi-llama-cpp` | `unsupported_sdk_export … ApiKeyCredential` | still fails, now `Cannot access 'BaseModel' before initialization` |
| bun | `pi-intercom` | `missing_dependency @mariozechner/pi-tui` | pass |
| bun | `pi-powerline-footer` | `extension_load_error … signal: killed` | pass |
| bun | `pi-vault-mind` | `registration_mismatch command_registration.description` | pass |

Two parts of the fix did **not** carry to Bun, and this is the single most actionable finding:

* **The `@earendil-works/pi-ai` → `/compat` redirect does not fire on Bun.** Node now resolves the
  global API; Bun still resolves the deliberately narrow root entry. `pi-mcp-adapter`,
  `@narumitw/pi-goal`, `pi-dgoal`, `zob-harness` and `@danmademe/pi-provider-litellm` fail on Bun
  only, each naming a different missing export (`complete`, `isRetryableAssistantError`,
  `streamSimple`, `completeSimple`, `streamSimpleOpenAICompletions`).
* **The redirect is not applied to the legacy `@mariozechner/pi-ai` alias on either runtime.**
  `pi-memory` fails on Node with `@mariozechner/pi-ai export complete` and on Bun with
  `@earendil-works/pi-ai export complete`, so it is still `both_fail`.

The Bun `NODE_PATH` aliasing did work for whole-module resolution (`@mariozechner/pi-tui` now
resolves, fixing `pi-intercom`), but `@earendil-works/pi-coding-agent` still fails to resolve for
`gentle-pi`, `@cynos-ai/engineer` and `@heyhuynhgiabuu/pi-pretty` on Bun.

## Node versus Bun divergence

Across all 300 packages: `none 211`, `node_only_failure 11`, `bun_only_failure 20`, `both_fail 58`,
`disagree 0` (was `none 191`, `node_only_failure 31`). Node-only failure is no longer the dominant
divergence.

**Node-only failures (11)**, down from 31, with no single dominant class left:

| Class | Count | Packages |
| --- | ---: | --- |
| `extension_load_error` | 7 | `@bacnh85/pi-munin` (`require is not defined in ES module scope`), `@bacnh85/pi-subagent` (`__dirname is not defined`), `@narumitw/pi-subagents` (`SubagentParams is not defined`), `pi-chrome` (`Cannot use import statement outside a module`), `pi-grok-cli` and `@aliou/pi-neuralwatt` (JSON import attribute), `pi-llama-cpp` (`Cannot access 'BaseModel' before initialization`) |
| `unsupported_sdk_export` | 1 | `@firstpick/pi-extension-todo-progress` (`@firstpick/pi-utils export extractChecklist`) |
| `missing_dependency` | 1 | `pi-smart-router` |
| `native_addon` | 1 | `pi-knowledge` (`better_sqlite3.node`) |
| `registration_mismatch` | 1 | `pi-smart-ralph` (`active_tool_gating`) |

`typescript_unsupported` no longer appears in the Node-only column at all. Two of the eleven —
`pi-grok-cli` and `@aliou/pi-neuralwatt` — are the same JSON-import-attribute defect, now the largest
remaining Node-only cluster.

**Bun-only failures (20)** are module resolution and platform surface, unchanged in count:

| Class | Count | Detail |
| --- | ---: | --- |
| `missing_dependency` | 8 | relative, subpath-imports and asset specifiers Bun's plugin never sees: `../hooks/ponytail-config.js`, `#src/async-cache`, `#src/config/agent-types`, `./package.json`, `../data/patch.json` (×2), `./prebuilds/linux-arm64/tree-sitter.node`, `@mariozechner/pi-agent-core` |
| `unsupported_sdk_export` | 5 | the `/compat` gap described above |
| `missing_node_builtin` | 4 | `node:sqlite` — `pi-autopilot`, `@spences10/pi-{mcp,observability,team-mode}` |
| `extension_load_error` | 1 | `atlas-vision-mcp` |
| `flaky` | 1 | `@xaccefy/pi-lookup` (`write \|1: broken pipe`; `extension_load_error` in the previous run — same teardown race, different side of it) |
| `registration_mismatch` | 1 | `@pi-stef/atlassian` (`tool_definition.parameters`) |

`#src/async-cache` is a package-`imports` specifier and is not aliasable on Bun.

**Both fail (58)**, of which 18 are not pigo's fault (15 upstream Pi load failures plus 3 install or
harness exclusions). The remaining 40 split, by their Node-side class, into `extension_load_error`
(14), `registration_mismatch` (12), `unsupported_sdk_export` (8), `unsupported_pi_api` (3), `flaky`
(2) and `missing_dependency` (1). The same 40 seen from Bun are `registration_mismatch` (12),
`extension_load_error` (11), `unsupported_sdk_export` (9), `missing_dependency` (5),
`unsupported_pi_api` (2) and `flaky` (1) — the two runtimes agree that the package fails but
frequently disagree about why. `typescript_unsupported` has left this column too: `@fiale-plus/pi-rogue`
moved into `extension_load_error` and `@mrclrchtr/supi-code-intelligence` into
`unsupported_sdk_export`.

## Failure taxonomy

Counted over the 282 upstream-supported packages, so upstream's own failures are never charged to
pigo:

| Class | `pigo-node` | (`53222c3`) | `pigo-bun` |
| --- | ---: | ---: | ---: |
| `typescript_unsupported` | **0** | (24) | 0 |
| `extension_load_error` | 21 | (19) | 12 |
| `registration_mismatch` | 13 | (13) | 13 |
| `unsupported_sdk_export` | 9 | (7) | 14 |
| `unsupported_pi_api` | 3 | (3) | 2 |
| `missing_dependency` | 2 | (2) | 13 |
| `missing_node_builtin` | 0 | (0) | 4 |
| `native_addon` | 1 | (1) | 0 |
| `flaky` | 2 | (2) | 2 |
| **total non-passing** | **51** | **(71)** | **60** |

The two classes that grew on Node absorbed packages that used to stop at type stripping:
`extension_load_error` gained `@aliou/pi-neuralwatt` and `@fiale-plus/pi-rogue`,
`unsupported_sdk_export` gained `@firstpick/pi-extension-todo-progress` and
`@mrclrchtr/supi-code-intelligence`.

Every `unsupported_pi_api` failure in the run is the same missing member, `pi.unregisterProvider`,
in `@gotgenes/pi-anthropic-auth`, `pi-omlx-picker` and `@router-for-me/pi-cliproxyapi-provider`.
`registration_mismatch` is dominated by
`builtin_tool_definition_override` (a package that redefines `edit`/`read`/`write`/`grep` and whose
override upstream applies but pigo does not), `tool_definition.promptGuidelines` (pigo builtins carry
no per-definition prompt metadata, `agent/types.go:91-98`), and `active_tool_gating` (a package that
gates which tools are active).

**`environment_constraint` count for this run is zero.** No corpus package was rejected for demanding
a toolchain the harness does not provide; `pi-x-ide` (`engines.node >= 26`) and `@patimweb/pi-email`
both install and pass under Node 24.

### Install and harness exclusions — never counted as incompatibility

Three packages produced no probe at all and are excluded from the parity denominator:

| Package | Cause | Verdict |
| --- | --- | --- |
| `@firstpick/pi-package-webui` | manifest points at `node_modules/@firstpick/pi-extension-bang-command-autocomplete/index.ts` inside its own tree, which npm hoisted away | genuine install/tree-shape failure |
| `@shanepadgett/tau-agent` | manifest declares the glob `extensions/*/index.ts`; the harness stats it literally | harness limitation, not a package or pigo defect |
| `pi-intercom` | `ENOTEMPTY: directory not empty, rmdir '/work/matrix-7'` | **harness cleanup race, false failure** |

`pi-intercom` leaks `npm exec tsx` children that outlive the probe (this run's `resources.leaked`
records five, plus one for `pi-crew`), the per-probe `rm(runRoot)` at
`conformance/extensions/matrix.mjs:722` races them, and the resulting exception is swallowed by the
catch-all at `matrix.mjs:1560-1573` which labels *any* harness-level exception `install_failure`
(`matrix.mjs:1567-1568`). The orphan reap that would prevent it runs only after the record is
appended (`matrix.mjs:1580-1587`). It landed on the losing side of that race in both runs here,
including the dedicated tier-1 run where `53222c3` had caught it passing. Treat `pi-intercom` as a
pass; the race is a harness defect, not a package or pigo verdict.

The 15 packages upstream Pi itself cannot load are `@zhachory1/mewrite-markdown-preview`,
`@oas-framework/pi`, `@gamalan/pi-gateway`, `openlore`, `rolebox`, `@amaster.ai/pi-browser-use`,
`pi-multi-account`, `@nquandt/pi-azure-foundry`, `@ch1nyzzz/pi-evo`, `pi-nocturne-memory`,
`@cgh567/agent`, `@pi-archimedes/image-paste`, `@pi-archimedes/diff`, `@jaggerxtrm/pi-extensions`
and `ultimate-pi`. Their causes are missing sibling packages, absent global CLIs, absent config
files, `bun:sqlite` under Node, native addons and load-time crashes.

## Flaky packages

A flake is a finding, not an average. Named individually, never averaged into a pass:

* **`pi-cursor-sdk` — flaky on `pigo-node` *and* `pigo-bun`, in both the 300 run and the dedicated
  tier-1 run.** Two distinct registration snapshots across attempts;
  `activeTools:cursor_ask_question` appears in some and not others. This is the known
  late-registration race: `codingagent/extensions/host/manager.go:518-528` clones every extension's
  registration state and freezes it into `manager.states`, while the `register_tool` handler at
  `:933-954` appends to the live per-generation map (`:948`) and answers `{"accepted": true}`
  (`:954`). A registration that lands after the freeze is acknowledged but never reaches the snapshot
  the agent reads. It is not Node-specific — Bun flakes on the same package. At 13 attempts it shows
  up reliably; at 3 it is a coin flip, and it came up heads at `53222c3` and tails here.
* **`@lebronj/pi-suite` — flaky on `pigo-node`.** Same shape, `activeTools:autogoal`.
* **`@xaccefy/pi-lookup` — flaky on `pigo-bun` only,** `write |1: broken pipe` on the probe command.
  It was a stable `extension_load_error` (`write |1: file already closed`) at `53222c3`: the same
  teardown race, observed from the other side. Bun-side and therefore untouched by this commit.

`open-zk-kb`, which flaked before `53222c3`, remains stable in every attempt.

## Workflow smoke

Six of seven read-only command handlers pass with byte-identical normalized output in both runtimes,
unchanged. **The one workflow case now passes as well** — `workflowPassCount` went 0 → 1 and
`allWorkflowsComparedPass` is `true`:

* **`piolium-knowledge-base-stage` now passes under Pigo on Node, 6/6 attempts, `parity: true` with
  byte-identical observable output against Pi.** At `53222c3` the probe's direct import of
  `@vigolium/piolium/extensions/piolium/knowledge-base-input.ts` was refused with
  `Stripping types is currently unsupported for files under node_modules`. That is the same
  limitation reached through a direct import rather than manifest staging, which the staging escape
  could not cover and the `load`-hook approach does.
* **`subagents-doctor` still fails in *both* Pi and Pigo.** Both match `Subagents doctor report` and
  both miss the fixture's `agents: total 8 (builtin 8, package 0, user 0, project 0)`. `pi-subagents`
  moved 0.35.1 → 0.36.0 in the recapture and the agent census changed. This is fixture drift in
  `conformance/extensions/smoke-cases.json`, not a pigo defect: the case is recorded `parity: false`
  only because neither runtime produced comparable evidence, not because they differed. It is the
  sole reason `smoke.mjs` still exits `2`.

## Performance

Observer-only baseline startup, 11 samples each, all non-noisy: Pi 368.0 ms, pigo-node 99.5 ms,
pigo-bun 32.7 ms. Pigo's base process is 3.7× cheaper than Pi's on Node and 11.3× cheaper on Bun.
The absolute numbers are well below the `53222c3` run's (697.4 / 187.8 / 60.4 ms) because the host
was less loaded, not because anything got faster; only the ratios within a run mean anything.

Across the 41 tier-1 packages with usable measurements, total startup is at parity — median
`pigo/pi` ratio **1.02**, range 0.274–2.228, pigo faster in 19 of 41. After subtracting each
runtime's own global observer baseline, the median extension-load ratio is **3.26×**, range
0.744–13.126. Pigo's cheaper process start is being spent again in out-of-process extension load.
Subtraction amplifies drift when a package's incremental load is a few milliseconds, so treat the
tail of that range as noise rather than as a workload measurement, and note that this run was
CPU-capped at two cores on a shared machine. Bun and Node startup are not comparable to each other
here; each tier is compared only against the shared Node reference.

## What was tested

The corpus is the 300 most downloaded `https://pi.dev/packages` entries whose published
`pi.extensions` manifest field is a non-empty array, captured 2026-07-25. Downloads are registry
traffic, not unique users. Exact versions and integrity hashes are in
[`conformance/extensions/corpus.json`](../../conformance/extensions/corpus.json); the committed lock
pins the full dependency graph. Tiers are 1 = ranks 1–50, 2 = 51–100, 3 = the rest.

Three runtimes ran interleaved inside one probe per package:

| Tier | Role | Binary | Engine | Verified how |
| --- | --- | --- | --- | --- |
| `pi` | reference | `@earendil-works/pi-coding-agent@0.81.1` | Node 24.18.0 | engine self-report from inside the host process |
| `pigo-node` | candidate | `pigo 0.1.0-dev (upstream pi 0.81.1 @ 20be4b18)` | Node 24.18.0 | idem |
| `pigo-bun` | candidate | same binary | Bun 1.3.14 | private `PATH` with no `node`, plus engine self-report |

Sampling used the harness defaults: tier 1 on the `perf` profile (1 cold + 1 warm-up + 11 measured
samples per runtime = 13 attempts), tiers 2 and 3 on `compat` (1 cold + 2 samples = 3 attempts), 30 s
per-process deadline, 420 s per-package budget. The dedicated tier-1 A/B run forced `compat` for
every tier. A package is `load_register_pass` only when every attempt succeeded and every attempt
produced byte-identical baseline-subtracted registrations.

The pigo binary was built from a detached worktree at `f64f4f7`, not from a working tree:
`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/pigo ./cmd/pigo` with Go 1.26.5.
Every other input hashes identically to the `53222c3` run (see *Reproducibility hashes*), so the two
runs differ in exactly one variable.

Packages were installed once, in a container, by `prepare.mjs` running `npm ci --ignore-scripts`
against the committed lock (3825 packages; every one of the 300 corpus entries verified at its pinned
version). Execution ran in a separate container with `--network none --read-only --cap-drop ALL
--security-opt no-new-privileges --pids-limit 1024 --memory 4g --cpus 2 --init`, tmpfs `/work` and
`/tmp`, the repository bind-mounted read-only, and no host `HOME`, no `~/.pi` and no credential
reachable. Both runners refuse to start unless the network namespace contains only loopback; the
result records `networkNamespaceGuard.isolated: true` with no external interfaces and
`credentialsInherited: false`. No corpus package ran outside a container.

This run completed in one pass — 300/300 packages in 54 minutes, `incomplete: false`, peak harness
RSS 152 MiB, no zombies, no `resources.warnings`, and no `resource_exhaustion` classification. The
only reaped leaks were `pi-intercom` (five children) and `pi-crew` (one). `--resume` was armed but
never needed. `environment_constraint` and `runtime_not_forced` counts are both zero; every attempt's
engine self-report matched its tier.

## Known harness limitations found by this run

`report.mjs` cannot process a three-runtime, 300-package matrix. Four independent blockers, none
fixed here because the harness is out of scope for this measurement:

1. `report.mjs:768` reads `corpusFile.json`, but `loadFile` returns the parsed document under
   `.value` (`report.mjs:141-153`). `EXPECTED_CORPUS_SIZE` is therefore always `0` and every
   invocation fails `corpus is empty or unreadable` unless `--expect-corpus` is passed. This is a
   plain bug.
2. `report.mjs:272-275` requires `matrix.baseline.pigo`. `matrix.mjs` keys `baseline` by runtime id
   (`pi`, `pigo-node`, `pigo-bun`) and provides the `pi`/`pigo` back-compat aliases only on
   per-extension records (`matrix.mjs:1555-1556`), not on the baseline block. This fails with
   `matrix.baseline.pigo must be an object`.
3. `report.mjs:301` requires every `unsupported` package to carry `reason === "pigo_load_failure"`.
   The current taxonomy also emits `registration_mismatch`, `upstream_load_failure` (15) and
   `install_failure` (3).
4. `report.mjs:296` requires `upstreamSupported === true` for every entry and `report.mjs:317`
   requires `summary.counts.flaky === 0`. Both are legitimate outcomes at corpus scale, and the
   README itself states a flake is never averaged into a pass.

Consequently this run publishes the raw matrix and raw smoke rather than a compact report artifact.

## Historical embedded-bridge workflow matrix

The table below retains the 2026-07-22 per-package workflow audit and performance evidence for the
44-package corpus. Its load-status column is the embedded-bridge baseline and its `load blocked`
labels describe a deleted bridge; the classifications above supersede it. It is kept because the
line-grounded workflow verdicts were not rerun, and load parity is not workflow parity.

The final column is `Pigo / Pi` for total process startup followed by observer-baseline-subtracted
extension load. A value below `1×` favors Pigo. `—` means Pigo could not load the package, `n/r`
means the adjusted value was below measurement resolution, and `not executed` means exactly that
rather than a failed workflow.

| Rank | Package (monthly downloads) | Load and registration | Workflow verdict | Primary condition | Executed evidence | Startup / adjusted load |
| ---: | --- | --- | --- | --- | --- | ---: |
| 1 | `@vigolium/piolium@0.0.13` (281,805) | load + registration | likely | none | `/piolium-help` exact; `/piolium-status` exact; knowledge-base workflow exact | 0.276× / 0.189× |
| 2 | `pi-mcp-adapter@2.11.0` (138,045) | load blocked | load blocked | Node streams and stdio transport | not executed | — |
| 3 | `pi-web-access@0.13.0` (132,583) | load + registration | partial | optional crypto cookie import | not executed | 0.831× / 1.205× |
| 4 | `pi-subagents@0.35.1` (117,670) | load + registration | partial | private SDK/TUI and long-lived stdio | `/subagents-doctor` exact | 0.227× / 0.190× |
| 5 | `context-mode@1.0.169` (106,249) | load blocked | load blocked | native SQLite addon | not executed | — |
| 6 | `@tintinweb/pi-subagents@0.14.2` (40,400) | load blocked | load blocked | private coding-agent and TUI SDK | not executed | — |
| 7 | `@remnic/plugin-pi@9.10.0` (35,739) | load + registration | likely | external service prerequisite | not executed | 0.418× / 0.839× |
| 8 | `pi-lens@3.8.71` (31,382) | load blocked | load blocked | native AST-grep addon | not executed | — |
| 9 | `@plannotator/pi-extension@0.24.2` (29,485) | load blocked | load blocked | eagerly resolved optional dependency | not executed | — |
| 10 | `@quintinshaw/pi-dynamic-workflows@3.3.0` (28,294) | load blocked | load blocked | isolated VM and private SDK | not executed | — |
| 11 | `@gotgenes/pi-permission-system@20.10.0` (26,894) | load + registration | partial | settings TUI only | not executed | 0.344× / 0.310× |
| 12 | `pi-simplify@0.2.3` (24,721) | load + registration | likely | none | not executed | 0.426× / 11.480× |
| 13 | `@ff-labs/pi-fff@0.10.1` (22,067) | load + registration | main feature blocked | lazy native FFI backend | `/fff-health` ran; output differs | 0.340× / 0.191× |
| 14 | `@mjasnikovs/pi-task@0.18.49` (21,223) | load blocked | load blocked | streams, networking, and private SDK | not executed | — |
| 15 | `@juicesharp/rpiv-ask-user-question@2.0.0` (18,955) | load blocked | load blocked | top-level-await module loading | not executed | — |
| 16 | `@raindrop-ai/pi-agent@0.1.0` (17,023) | load-only | likely | external service prerequisite | not executed | 0.439× / n/r |
| 17 | `pi-hermes-memory@0.8.2` (16,573) | load blocked | load blocked | native SQLite addon | not executed | — |
| 18 | `@juicesharp/rpiv-todo@2.0.0` (16,474) | load blocked | load blocked | top-level-await module loading | not executed | — |
| 19 | `@hypabolic/pi-hypa@0.1.11` (16,396) | load + registration | likely | none | `/hypa` exact | 0.331× / 0.143× |
| 20 | `@narumitw/pi-goal@0.24.0` (15,934) | load + registration | likely | none | `/goal status` exact | 0.298× / 0.144× |
| 21 | `@ayulab/pi-rewind@0.4.6` (15,556) | load + registration | main feature blocked | session listing and rich TUI | not executed | 0.472× / 3.283× |
| 22 | `gentle-pi@1.2.0` (15,065) | load blocked | load blocked | package resolution and tool factories | not executed | — |
| 23 | `pi-agent-browser-native@0.2.71` (12,576) | load + registration | likely | external executable prerequisite | not executed | 0.664× / 4.277× |
| 24 | `@ollama/pi-web-search@0.0.5` (12,495) | load + registration | likely | external service prerequisite | not executed | 0.415× / 1.614× |
| 25 | `pi-readseek@0.8.0` (12,409) | load + registration | partial | child stdin and grep tool factory | not executed | 0.526× / 1.959× |
| 26 | `pi-deepseek-search@1.0.15` (12,021) | load-only | likely | external credentials prerequisite | not executed | 0.421× / 1.086× |
| 27 | `pi-crew@0.9.46` (11,909) | load blocked | load blocked | top-level await, workers, and process IPC | not executed | — |
| 28 | `pi-landstrip@0.17.31` (11,382) | load blocked | load blocked | real socket server and stream decoding | not executed | — |
| 29 | `pi-fabric@0.22.4` (11,375) | load blocked | load blocked | CJS resolution, private runner, workers, sockets | not executed | — |
| 30 | `@alexanderfortin/pi-deepseek-usage@0.3.12` (11,205) | load-only | likely | external credentials prerequisite | not executed | 0.457× / 1.243× |
| 31 | `pi-prompt-template-model@0.10.0` (10,543) | load + registration | likely | none | not executed | 0.297× / 0.176× |
| 32 | `pi-intercom@0.6.0` (10,341) | load + registration | main feature blocked | real Unix and TCP sockets | not executed | 0.299× / 0.153× |
| 33 | `opencode-codebase-index@0.14.0` (10,061) | load + registration | main feature blocked | native codebase-index addon | not executed | 0.508× / 1.546× |
| 34 | `@pi-stef/atlassian@0.4.1` (9,894) | load + registration | likely | external credentials prerequisite | not executed | 0.420× / 0.463× |
| 35 | `@braintrust/pi-extension@0.10.0` (9,831) | load-only | partial | unproven Web media, stream, and crypto APIs | not executed | 0.926× / 1.925× |
| 36 | `pi-lean-ctx@3.9.12` (9,815) | load blocked | load blocked | MCP streams and tool-definition factories | not executed | — |
| 37 | `@narumitw/pi-lsp@0.25.0` (9,750) | load + registration | main feature blocked | long-lived bidirectional child stdio | `/lsp` status exact; real LSP not started | 0.329× / 0.185× |
| 38 | `pi-shazam@0.30.0` (9,662) | load blocked | load blocked | native tree-sitter addons | not executed | — |
| 39 | `pi-cursor-sdk@0.1.60` (9,575) | load blocked | load blocked | Bun SQLite and SDK packaging | not executed | — |
| 40 | `pi-llama-cpp@0.9.1` (9,549) | load blocked | load blocked | private settings and credential SDK | not executed | — |
| 41 | `pi-vault-mind@0.16.25` (9,356) | load blocked | load blocked | native vector, ML, and image addons | not executed | — |
| 42 | `gentle-engram@0.1.10` (8,665) | load + registration | partial | detached daemon lifecycle | not executed | 0.443× / 0.458× |
| 43 | `pi-hashline-edit-pro@0.16.15` (8,541) | load blocked | load blocked | WASM, asset URLs, tool factory, and TUI | not executed | — |
| 44 | `cc-safety-net@1.0.6` (8,379) | load + registration | likely | none | intentionally excluded from offline command smoke | 0.489× / 9.401× |

The four load-only packages are not weaker load results: Raindrop and DeepSeek Usage are event-only,
DeepSeek Search registers conditionally after credential lookup, and Braintrust initializes tracing
through lifecycle hooks. The matrix can prove stable loading for them but sees no unconditional
tool or command registration to compare.

## Node-only regression sweep at `d620c01`

`77c4c33` *Run extensions on whatever Node the machine has* deletes the staged-entry mechanism and
with it `--preserve-symlinks`. An extension entry under `node_modules` is no longer reached through
`~/.pi/host/entries/<hash>/source/…`; it is loaded in place, through the same load hook as
everything else. That touches the code path of **every** package, so the corpus was re-measured.

This sweep answers one question — *did anything that passed at `f64f4f7` break?* — and is scoped to
that question:

* **`pigo-bun` was not run.** Staging early-returned for every non-Node runtime and
  `--preserve-symlinks` is a Node flag, so `77c4c33` cannot have moved Bun. Bun's last measured
  figure remains the `f64f4f7` 222/300 above.
* **Attempt counts were cut** to `compat` (1 warm-up + 2 samples) for tier 1 and `quick`
  (0 + 1) for tiers 2–3. Timing was not being measured.

> **Profile caveat.** A `quick`-profile count is comparable to a `perf`-profile count for hard
> pass/fail **and for nothing else**. One attempt cannot separate a transient failure from a real
> one, no timing figure from this sweep is publishable, and a package whose registrations differ
> between attempts can be recorded as stable simply because only one attempt ran. Every candidate
> regression below was therefore re-probed at `perf` (2 + 11) before being called one.

| Runtime | load + registration | load-only | **load compatible** | flaky | unsupported | vs `f64f4f7` |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| `pigo-node` | 215 | 21 | **236 / 300 = 78.7 %** | 1 | 63 | **+5** |

Seven packages that failed at `f64f4f7` now pass, and two that passed now fail.

### Seven fixed

| Package | Tier | `f64f4f7` failure | Cause of the fix |
| --- | ---: | --- | --- |
| `@firstpick/pi-extension-todo-progress` | 3 | `unsupported_sdk_export @firstpick/pi-utils export extractChecklist` | the load hook no longer reads an ESM package published without `"type": "module"` as CommonJS — this is the defect `f64f4f7` named as its actionable finding |
| `pi-memory` | 1 | `unsupported_sdk_export @mariozechner/pi-ai export complete` | same format-detection fix |
| `pi-interview` | 2 | `unsupported_sdk_export @mariozechner/pi-ai export complete` | same format-detection fix |
| `pi-smart-router` | 3 | `missing_dependency Cannot find module …/telemetry/telemetry-limits` | staging mirrored only the manifest's direct dependencies, so a deep relative import inside a dependency had nothing to resolve against; loading in place restores npm's own layout |
| `pi-smart-ralph` | 3 | `registration_mismatch activeTools.added missing_in_candidate` | as above |
| `pi-intercom` | 1 | `install_failure ENOTEMPTY rmdir '/work/matrix-7'` | not a verdict — the documented harness `rm(runRoot)` race (`matrix.mjs:722`) landed on the other side this time |
| `@lebronj/pi-suite` | 3 | `flaky`, 2 distinct registration snapshots | not a verdict — the known registration race (`manager.go:518-528`, `:933-954`); it also ran at 1 attempt here, which cannot observe flakiness |

`typescript_unsupported` is still down to the single package upstream Pi cannot load either
(`@cgh567/agent`), unchanged from `f64f4f7`.

### Two regressions, reproduced

Both are **real and deterministic**. Each was re-probed alone at `perf` — 13 attempts per runtime —
against a `d620c01` binary and against an `f64f4f7` binary, in the same container, on the same
installed corpus, with the harness, corpus, observer, lock and upstream Pi byte-identical. The pigo
binary hash is the only input that differs between the two columns.

| Package | Tier | `f64f4f7` binary, 13 attempts | `d620c01` binary, 13 attempts |
| --- | ---: | --- | --- |
| `@narumitw/pi-goal` | 1 | `load_register_pass`, 0 failures | **fails 13/13** — `unsupported_sdk_export @earendil-works/pi-ai export isRetryableAssistantError` |
| `@pi-stef/atlassian` | 1 | `load_register_pass`, 0 failures | **fails 13/13** — `registration_mismatch tool_definition.parameters`, `jira_add_comment: parameters_differs` |

**Both had exactly these two signatures as `bun_only_failure` at `53222c3`, while passing on Node.**
That is the tell, and it names the mechanism: one shared root cause, not two.

Bun resolves the SDK through the real npm layout via `NODE_PATH`. Node used to resolve it through
the staged mirror, which placed the SDK where the entry expected it. With staging deleted, Node
resolves through the real npm layout too — so Node inherited Bun's failures because Node's
resolution now *is* Bun's resolution. This is a change in **which copy of a duplicated dependency
wins**, not a failure to resolve:

* **`@narumitw/pi-goal`.** `isRetryableAssistantError` exists only in `@earendil-works/pi-ai`
  ≥ 0.80.6. The hoisted `node_modules/@earendil-works/pi-ai` in the corpus is **0.80.2** and does
  not export it; the copy Pigo ships under `PIGO_PI_SDK_ROOT` is **0.81.1** and does. `legacySurface`
  in `codingagent/extensions/host/loader.mjs` redirects `@earendil-works/pi-ai` →
  `@earendil-works/pi-ai/compat` *through the caller's own context*, by design, "so a pinned or
  older install still wins". `installedSDK`, the fallback that resolves against `PIGO_PI_SDK_ROOT`,
  is only reached from the `catch` in `resolve`. Resolving 0.80.2 does not throw — it succeeds and
  then lacks the export — so the fallback never runs. Staging used to hide this by making the
  in-context resolution land on the SDK copy.
* **`@pi-stef/atlassian`.** `@pi-stef/atlassian@0.4.1` depends on `@pi-stef/atlassian@^0.3.0`, so
  npm installs **0.3.4 nested inside 0.4.1**. Two copies of the same package with different tool
  schemas; which one the extension's own imports reach changed with the resolution path. The
  surviving difference is one `jira_add_comment` parameter pattern, `^.*$` upstream against
  `^(.*)$` in the candidate. `zod` and `zod-to-json-schema` are single-version in this tree, which
  rules out a converter-version explanation.

Neither is a flake, and neither is on the known non-verdict list.

**`resolveFromSource` is not dead code — `77c4c33` deleted it.** At `f64f4f7` it sat at
`loader.mjs:61` gated on `context.parentURL.includes("/host/entries/")`, called from `resolve` at
`:263`. At `d620c01` the symbol does not appear in the file at all. The remaining SDK path is
`legacySurface` → `nextResolve` → `installedSDK`-on-throw, described above.

### What this does not cover

`pigo-bun` was not re-measured, so no Bun number here is current as of `d620c01`. The smoke and
workflow suites were not re-run. Tier 2 and tier 3 verdicts rest on a single attempt each: a package
recorded as passing there is not evidence of stability, only of one successful load, and the flaky
count of 1 is a floor, not a measurement.

## Caveats

`load_register_pass` is deliberately narrower than end-to-end extension compatibility. No
package-specific tool or command is executed. Flags, shortcuts, renderers, providers, event-handler
behavior, credentials, model requests and external services need separate workflow probes.

The harness expands manifest-declared extension directories before invoking either runtime, so this
result does not test Pi versus Pigo package-directory discovery semantics. Packages share `/work`
and `/tmp` inside one runtime container; the container protects the host, but a hostile package could
contaminate a later package. Use one fresh runtime container per package before treating these
results as adversarial security evidence.

## Reproducibility hashes

Inputs. Every row except the pigo executable is byte-identical to the `53222c3` run, which is what
makes the comparison a controlled A/B rather than two separate measurements:

| Input | SHA-256 | Same as `53222c3`? |
| --- | --- | --- |
| `conformance/extensions/corpus.json` | `b3301f36cc8fb5ba26802ebff712349b9b99f4c3f01a08da1fd08d8c79cc90c6` | yes |
| `conformance/extensions/matrix.mjs` | `4d64e172743b79fd393d15ba3a19652ab788409b23a8b656fbf88a899b3b9075` | yes |
| `conformance/extensions/taxonomy.mjs` | `97c4215cdabc18231f04b56903cac121e29696102cd8cf3df0e883d13504babf` | yes |
| `conformance/extensions/observer.ts` | `de1baf804358c5ceb51a2a71a92bda587b47249429806741cd7c8c4c4e98d9e2` | yes |
| `conformance/extensions/package-lock.json` | `b95e4fc1d684711fddcb9408591197c4db26b9d615fc41af7647f5969a754960` | yes |
| tested upstream Pi executable | `af302f231437eaf6f37691bce4b34234fcb626bcb5eb3910d4fc3f6519bf78ca` | yes |
| tested Bun 1.3.14 executable | `37141662ebed915a2ab89313156e455e2a1374395f5f6760d06407f49406f086` | yes |
| tested Pigo executable (linux/arm64, **`f64f4f7`**, 48,661,574 B) | `6efddd4ef4d1cd0162ab4a04cd49fd0bf306e3a545b00daed5902f416d2ff037` | **no** |
| previous Pigo executable (linux/arm64, `53222c3`) | `e49168f94e7fdcd35c60428d79a0698496858feb0c57505b47520715ed11ee5c` | — |

The `d620c01` sweep reuses every input above unchanged — same `corpus.json`, `matrix.mjs`,
`taxonomy.mjs`, `observer.ts`, `package-lock.json` and upstream Pi hash — and changes only the pigo
executable and the sampling profile. Both pigo binaries used there were built from detached
worktrees, not a working tree:

| Input | SHA-256 | Bytes |
| --- | --- | ---: |
| Pigo executable (linux/arm64, **`d620c01`**) | `4d32f6b3f91b38c4383d60a68d1bcd037707617dc3b66f72dd12060f8ae6d8d2` | 48,828,075 |
| Pigo executable (linux/arm64, `f64f4f7`, rebuilt for the A/B) | `9ed29a838b84c668d3a3d242022ee9f776ce3a7fb55c66b57d143424aff13084` | — |

Artifacts. Large raw artifacts are committed `gzip -9`; the *raw* SHA-256 is the hash of the
uncompressed JSON inside, so it can be checked after `gunzip`. Only the newest full sweep and the
current A/B pair stay in the tree — earlier dumps are dropped once their conclusions are written up
here, and their rows below keep the size and both hashes so a superseded run is still attested:

| Artifact | raw bytes | raw SHA-256 | committed SHA-256 |
| --- | ---: | --- | --- |
| 300-package Node-only sweep, `d620c01` [`…-300-d620c01.json.gz`](../../conformance/extensions/results/pi-0.81.1-pigo-runtimes-300-d620c01.json.gz) | 14,602,607 | `dc91f9e0b145d5a35890f5802a8c49554b4a8d8b3e4e27960b0735b6cf9aabb0` | `9bbdafb514f4bdf2026a29f6ea0d22c01a0bb7736ab088f993f4785450af079e` |
| regression A/B (2 packages, `perf`), `d620c01` binary [`…-regression-ab-d620c01.json.gz`](../../conformance/extensions/results/pi-0.81.1-pigo-runtimes-regression-ab-d620c01.json.gz) | 1,050,292 | `3ca6f9d3833f41b68ea75b1a7867af9cc607d27e2a05057e78f1650dd0a93e29` | `4d3c3b9d4581447290b530031858a33fc23e649d04617014ace33f5ac5b544d0` |
| regression A/B (2 packages, `perf`), `f64f4f7` binary [`…-regression-ab-f64f4f7.json.gz`](../../conformance/extensions/results/pi-0.81.1-pigo-runtimes-regression-ab-f64f4f7.json.gz) | 226,747 | `dc7d71f068516109de5b3fde3a96d0187769818195b782eb7d3df5866f234bc5` | `d0459cace199a0839d41d51e97ba27f4ea348ae863247c16a4e39dfb97a7a1f8` |
| 300-package matrix, `f64f4f7` `…-300-f64f4f7.json.gz` (raw artifact dropped) | 18,818,163 | `764dec1736c7365c5d8db7f58965fa8b6f9830128ca0dc4eca6cdf568ad6ee77` | `7ef07b1e70fc215962c0c7cf7a08ca3b1b88a19fd575ceae95e08db1b94713c4` |
| smoke, `f64f4f7` `…-300-f64f4f7-smoke.json.gz` (raw artifact dropped) | 929,966 | `807611cf1a12b7a3841e5a88d085a1d5b48178fc5dc3ae3ed171187fa7a57339` | `d65699980f2f8486f6f398d00004eafe176aeb4151d9516e21eaa3a506dce243` |
| tier-1 `compat` A/B, `f64f4f7` `…-tier1-compat-f64f4f7.json.gz` (raw artifact dropped) | 2,886,036 | `dcec6b9c5e0f0065d13a969c7b69d157a2856ac68ce1ff309bd052d21cb99581` | `00e5453acdcbd0406a810840df6e096980ccd3180877593438454d2ca6549866` |
| 300-package matrix, `53222c3` `…-300.json.gz` (raw artifact dropped) | 37,227,960 | `6bcf1d54cf55bb35a9ef36f32b020934c832b6f93131c467ca931c8deba1842b` | `dea7d00f08485c1ec8147e7ff7800f2a68191294ffd38ba9deb292ed3eced3d0` |
| smoke, `53222c3` `…-300-smoke.json` (raw artifact dropped) | 870,944 | `876135a7c41a7559d2ef7821449637027914da1b67de54f3bf31ba1b0da9b73e` | uncompressed |
| tier-1 `compat` A/B, `53222c3` `…-tier1-compat.json` (raw artifact dropped) | 2,781,108 | `9891519e06c6bf986d2321495644550c304008081e76d857780255bf7f8f1cb1` | uncompressed |
| superseded 44-package `pi-0.81.1-pigo-host.json` (raw artifact dropped) | 3,839,265 | `d9f1baa88d6cf875973d5842b4d2d8f286060c9ac105c3954ca75c540820ee4e` | uncompressed |

Each aggregate is the raw matrix at the default `--retain-registrations diagnostic`, copied directly
from the isolated run without compaction. It retains every attempt, every failure's registration
snapshot and every diagnostic; the bulk of its bytes is the registration evidence a mismatch verdict
rests on, not timing data. The `f64f4f7` aggregate is about half the size of its predecessor, which
is consistent with 20 fewer failures each dropping a retained per-runtime registration snapshot.
