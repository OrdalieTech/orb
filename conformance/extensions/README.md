# Ecosystem extension matrix

This harness measures whether the most-downloaded public Pi extension packages load and expose the same observable tool and command registrations in pinned upstream Pi 0.81.1 and Pigo. It never sends a model request: RPC starts with a dummy API key, `get_commands` proves session startup, and `observer.ts` emits canonical JSON for `pi.getActiveTools()`, full tool definitions from `pi.getAllTools()`, and `pi.getCommands()`.

The separate [live matrix](../../docs/sync/ecosystem-extension-live.md) installs packages through
Pi and exercises representative model-driven workflows; do not infer workflow support from this
offline harness alone.

Registration comparison subtracts each runtime's own observer-only baseline, then compares stable active-tool names, canonical tool descriptions/parameter schemas/prompt guidelines, and command names/descriptions. Runtime-specific source paths and metadata are intentionally excluded. A package-specific command or tool is never executed, so `load_register_pass` is deliberately narrower than end-to-end extension compatibility. Flags, shortcuts, renderers, providers, event-handler behavior, credentials, model requests, and external services need separate workflow probes.

Package install is deliberately separate from package execution. The install container uses the committed `package-lock.json` through `npm ci --ignore-scripts`. The runtime container has no network, no host credentials, a read-only root filesystem, dropped Linux capabilities, a PID and memory limit, and disposable tmpfs state. Both runners fail unless the network namespace contains only loopback interfaces, and the load result hashes the exact harness, taxonomy, corpus, observer, lock, and binaries it used. **Do not run the corpus directly on a developer host.**

## Runtime tiers

pigo runs JavaScript extensions through a local Node or Bun host, so a single "does it work"
answer hides the runtime that produced it. Every package is therefore measured on each tier
independently:

| Tier | Role | Binary | JS engine |
| --- | --- | --- | --- |
| `pi` | reference | upstream `@earendil-works/pi-coding-agent` | Node 24 |
| `pigo-node` | candidate | pigo | Node 24 |
| `pigo-bun` | candidate | pigo | Bun 1.3.14 |

The reference always stays on Node because Node ≥22.6 is upstream Pi's own documented requirement;
running upstream on Bun would compare two unknowns. Each candidate is compared against the same
Node reference, so a Node-only regression and a Bun-only regression are separately visible in
`crossRuntime.divergence` (`none`, `node_only_failure`, `bun_only_failure`, `both_fail`,
`disagree`).

### How Bun is forced, and how you know it worked

pigo's `DiscoverRuntime` (`codingagent/extensions/host/runtime.go:32`) resolves `node` from `PATH`
first and only falls back to `bun` (`runtime.go:46`). There is no environment override in product
source, and this harness deliberately does not add one. Forcing therefore happens entirely in the
harness:

1. Each probe gets a private `PATH`. For `pigo-bun` it is `<run>/enginebin:<packages>/node_modules/.bin`,
   where `enginebin` contains a single symlink to the Bun binary.
2. Before spawning, the harness resolves `node` against that exact `PATH`. If anything answers, the
   attempt fails immediately with `runtime_not_forced` instead of silently measuring Node again.
3. `observer.ts` reports the engine that actually evaluated it (`globalThis.Bun` / `process.versions`)
   from inside the extension host process. Every attempt is checked against the tier's expected
   engine; a mismatch fails the attempt and is classified `runtime_not_forced`.

The engine self-report is recorded per runtime as `hostIdentity` and `engineVerified`, and the
engine binary is hashed into `inputs.engines`. The host identity is excluded from the registration
comparison, so it cannot influence a pass or fail verdict.

## Build the runner image

The image pairs Node 24 with the pinned official Bun build. It contains no corpus packages, no
credentials and no repository code; the repository is bind-mounted read-only at run time.

```sh
docker build -f conformance/extensions/Dockerfile \
  -t pigo-extension-matrix:24-bun1.3.14 conformance/extensions
```

## Run the matrix

From the repository root:

```sh
matrix_root="$(mktemp -d /tmp/pigo-extension-matrix.XXXXXX)"
mkdir -p "$matrix_root/packages" "$matrix_root/results" "$matrix_root/bin"
CGO_ENABLED=0 go build -o "$matrix_root/bin/pigo" ./cmd/pigo

docker run --rm \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --pids-limit 1024 \
  --memory 4g \
  --cpus 2 \
  -e HOME=/tmp/matrix-home \
  -e npm_config_cache=/tmp/npm-cache \
  --tmpfs /tmp:rw,nosuid,nodev,size=1g,mode=1777 \
  -v "$PWD:/repo:ro" \
  -v "$matrix_root/packages:/packages:rw" \
  -w /repo \
  pigo-extension-matrix:24-bun1.3.14 \
  node /repo/conformance/extensions/prepare.mjs --output /packages

docker run --rm --init \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --pids-limit 1024 \
  --memory 4g \
  --cpus 2 \
  --tmpfs /work:rw,noexec,nosuid,nodev,size=1g,mode=1777 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=256m,mode=1777 \
  -v "$PWD:/repo:ro" \
  -v "$matrix_root/packages:/packages:ro" \
  -v "$matrix_root/bin:/opt/pigo:ro" \
  -v "$matrix_root/results:/results:rw" \
  -w /work \
  pigo-extension-matrix:24-bun1.3.14 \
  node /repo/conformance/extensions/matrix.mjs \
    --packages /packages \
    --pigo /opt/pigo/pigo \
    --runtimes pi,pigo-node,pigo-bun \
    --output /results/matrix.json
```

The install container keeps network access, because it must reach the registry; it still drops
capabilities and never sees a credential. The runtime container is the one that must stay offline.

### Container limits, and why they were raised

The published 44-package result used `--pids-limit 256`, `--memory 2g` and no init process. At
corpus sizes past ~100 packages that configuration produces `EAGAIN … new Worker` failures and
SIGABRT crashes that look like package incompatibilities but are actually harness resource
exhaustion. Two causes, both fixed here rather than masked:

* **PID exhaustion from leaked children.** Packages spawn helpers that survive the probe. The
  harness now takes a `/proc` census between packages and SIGKILLs processes reparented to PID 1,
  recording them in `resources.leaked` so a leaky package is named.
* **Zombies when the harness is PID 1.** Without an init process, Node as PID 1 never reaps
  reparented children, and each one holds a PID slot against the cgroup limit. `--init` is now
  required; the run warns in `resources.warnings` if it detects zombies while running as PID 1.

`--pids-limit 1024` and `--memory 4g` are the documented floor for a 200+ package corpus. A run
that still hits resource exhaustion classifies the affected package `flaky` with reason
`resource_exhaustion` and exits `2`: that result is explicitly *not* a verdict on the package, and
the run should be repeated with more headroom.

### Selecting a subset

```sh
node conformance/extensions/matrix.mjs ... --tier 1              # only the top-50 tier
node conformance/extensions/matrix.mjs ... --package pi-mcp-adapter,pi-lens
node conformance/extensions/matrix.mjs ... --only 1,pi-mcp-adapter   # names or ranks (legacy)
```

`tier` comes from the corpus entry when present and is otherwise derived from `rank`
(1 = ranks 1–50, 2 = 51–100, 3 = the rest), so the same harness runs against a 44-entry corpus and
a 200+ entry corpus without an edit here. Any filtered run is marked incomplete and produces no
all-corpus percentage.

### Sampling profiles

Sampling cost is the dominant term at 200+ packages: every sample is a full process spawn per
runtime tier. Profiles trade timing fidelity for wall clock:

| Profile | Warm-ups | Samples | Use |
| --- | --- | --- | --- |
| `perf` | 2 | 11 | publishable startup timing; the historical default |
| `compat` | 1 | 2 | load/registration parity and flake detection |
| `quick` | 0 | 1 | smoke only; cannot detect flakiness |

By default tier 1 runs `perf` and tiers 2–3 run `compat`; override with `--profile <name>`,
`--tier-profile 2=quick`, or the explicit `--warmups`/`--samples`. Performance ratios are only
meaningful for `perf`-profile packages. `method.profileByTier` records what a run actually used.

### Partial progress is durable

With `--output <file>` the harness writes three artifacts:

* `<file>.jsonl` — one line per package, appended the moment that package finishes, with the full
  raw registrations and diagnostics. A run killed at package 150 leaves 149 complete records.
* `<file>.progress.json` — small, rewritten atomically after every package: counts, elapsed time,
  resident memory, last package and status.
* `<file>` — the aggregate, written at the end, or after `SIGINT`/`SIGTERM` with `incomplete: true`.

`--resume` reuses `<file>.jsonl` records when the harness, taxonomy, corpus, observer, lock and
binary hashes all still match, and re-runs only what is missing; a mismatched fingerprint starts a
fresh stream instead of silently mixing two builds. `--finalize` rebuilds an aggregate from a
stream without probing. A torn last line from a `SIGKILL` is skipped, not fatal.

To keep the aggregate small at 200+ packages, `--retain-registrations diagnostic` (the default)
drops two redundant things from packages that *passed*: the full per-runtime registration snapshot,
which is the baseline plus the delta that is kept next to it, and the per-candidate delta, which a
pass defines to be identical to the reference delta already stored in `registrationDeltas`. Nothing
is dropped from a failure, mismatch or flake, and the `.jsonl` stream always has the untruncated
record. `--retain-registrations all` keeps everything everywhere. Measured on the 44-package
three-runtime run: 3.5 MB aggregate and 5.2 MB stream, so a 220-package corpus lands near 17 MB and
26 MB respectively.

## Probes and workflow smoke tests

Run the inspected read-only command and Piolium workflow probes in the same hardened container:

```sh
docker run --rm --init \
  --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges --pids-limit 1024 --memory 4g --cpus 2 \
  --tmpfs /work:rw,noexec,nosuid,nodev,size=512m,mode=1777 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=256m,mode=1777 \
  -v "$PWD:/repo:ro" \
  -v "$matrix_root/packages:/packages:ro" \
  -v "$matrix_root/bin:/opt/pigo:ro" \
  -v "$matrix_root/results:/results:rw" \
  -w /work pigo-extension-matrix:24-bun1.3.14 \
  node /repo/conformance/extensions/smoke.mjs \
    --packages /packages --pigo /opt/pigo/pigo \
    --output /results/smoke.json
```

`smoke.mjs` exits non-zero when an observed Pi/Pigo difference is retained in the report. Generate
the deterministic compact artifact after both raw runs:

```sh
node conformance/extensions/report.mjs \
  --matrix "$matrix_root/results/matrix.json" \
  --smoke "$matrix_root/results/smoke.json" \
  --output "$matrix_root/results/report.json"
```

`report.mjs` publishes the compact Node-tier artifact and validates that shape; it reads the
back-compatible `pi`/`pigo` keys, which always mirror the reference and the primary candidate
(`pigo-node` when present). Its expected corpus size is derived from `corpus.json` itself, so a
recapture does not invalidate the report; pass `--expect-corpus <n>` to pin it for a release.

`smoke-cases.json` and `workflow-audit.json` are keyed by **package name only**. Gallery rank is a
capture-time position that moves whenever download figures are refreshed (`@ayulab/pi-rewind` moved
21 → 59 and `pi-shazam` 38 → 56 in one recapture), so nothing in the harness pins it; the rank
published beside a result is looked up from the corpus at run time. The audit may cover a subset of
a larger corpus, and every audited package must still exist in the corpus and have been measured.

## Harness self-test

`scale-check.mjs` proves the harness itself scales, using stub extensions this repository writes.
It never touches a corpus package: it generates its own package tree, points both the reference
and the candidate at the same pigo binary, and drives the real `matrix.mjs`. It must run in the
same hardened container, because `matrix.mjs` refuses to run outside a network-isolated namespace.

```sh
docker run --rm --init \
  --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges --pids-limit 1024 --memory 4g --cpus 2 \
  --tmpfs /work:rw,noexec,nosuid,nodev,size=1g,mode=1777 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777 \
  -v "$PWD:/repo:ro" -v "$matrix_root/bin:/opt/pigo:ro" -w /work \
  pigo-extension-matrix:24-bun1.3.14 \
  node /repo/conformance/extensions/scale-check.mjs \
    --pigo /opt/pigo/pigo --root /work/scale --count 220 --kill-at 150
```

It asserts that per-package cost does not grow with corpus position, that resident memory stays
bounded, that a stub which wedges its extension host is stopped by the per-package budget instead
of stopping the run, that a leaked grandchild is reaped, and that a `SIGKILL` at package 150 leaves
149 durable records which `--resume` continues from.

## Interpret the result

Every extension has one mutually exclusive status plus a more specific `reason`:

- `load_register_pass`: every cold, warm-up, and measured probe succeeded, registrations were stable, and both runtimes produced the same non-empty baseline-subtracted tool or command changes.
- `load_only_pass`: the same stable probes succeeded but produced no observed tool or command changes. This includes conditional or event-only packages whose useful behavior was not exercised.
- `flaky`: a cold, warm-up, or measured attempt disagreed with another attempt, or registrations varied between attempts.
- `unsupported`: every attempt failed under upstream Pi or Pigo, or stable registrations differed. The `reason` distinguishes those cases.
- `infra_error`: the observer-only baseline was not stable in every runtime, so every package conclusion and aggregate percentage is invalid.

A flake is never averaged into a pass. A runtime is `stable` only when *every* attempt succeeded
and every attempt produced byte-identical registrations; one failed attempt out of thirteen makes
the package `flaky`, and `flaky` is counted separately from `loadCompatible`. Packages that flake
only under resource pressure are reported as `flaky` with reason `resource_exhaustion` and force a
non-zero exit, because that is a statement about the run, not about the package.

The top-level `status`/`reason` describe the primary candidate (`pigo-node`). `candidates` carries
the same classification for every tier that ran, and `summary.byRuntime` reports per-tier counts.

### Failure taxonomy

Every non-passing package carries a `failure` object naming the specific missing capability, so it
can be acted on without re-reading stderr. `summary.failureTaxonomy` aggregates them by class and
capability across the run, and `summary.byRuntime[*].failureClasses` splits them per runtime tier.

| Class | Meaning | Example capability |
| --- | --- | --- |
| `install_failure` | the package never materialized in the prepared tree | `installed_package(name@version)` |
| `escaping_entrypoint` | `pi.extensions` points outside the package root | `manifest_entrypoint(avtc-pi)` |
| `invalid_entrypoint` | `pi.extensions` holds something that is not a path | `manifest_entrypoint(name)` |
| `environment_constraint` | the package demands a toolchain the harness does not provide | `engines.node >=26`, `package_manager(pnpm)` |
| `native_addon` | needs a compiled `.node` addon or node-gyp build | `better_sqlite3.node` |
| `typescript_unsupported` | TypeScript under `node_modules` the runtime refused to evaluate | `typescript_evaluation(.ts)` |
| `missing_node_builtin` | a `node:*` builtin or its shim is absent | `node:sqlite` |
| `missing_dependency` | a plain specifier did not resolve | `some-package` |
| `unsupported_sdk_export` | the pi SDK shim lacks a named export the package imports | `@earendil-works/pi-ai export ApiKeyCredential` |
| `unsupported_pi_api` | `pi.<member>` is absent or not callable | `pi.registerRenderer` |
| `unsupported_syntax` | the runtime could not parse the source | `parse(Unexpected token)` |
| `extension_load_error` | host-reported, package-specific load failure | the reported message |
| `registration_mismatch` | loaded, but the observable registration differs | `builtin_tool_definition_override(edit,read,write)` |
| `timeout` | exceeded the per-process deadline or per-package budget | `load_within_deadline` |
| `crash` | exited or aborted before the probe completed | `process_survives_load` |
| `resource_exhaustion` | the *harness* ran out of PIDs, memory or threads | `host_process_or_memory_headroom` |
| `runtime_not_forced` | the probe did not run on the tier's engine | `js_engine(bun)` |
| `flaky` | attempts disagreed with each other | underlying class in `flakeClass` |
| `unknown` | unclassified; raw diagnostics retained | — |

A package whose manifest resolves to no usable entrypoint is recorded and skipped without probing,
never thrown: several real gallery packages declare sibling paths such as `../avtc-pi-alpha/index.ts`,
and one of them must not be able to abort a 300-package run before the first probe. When only some
entries escape, the survivors are still probed and the rejected ones are listed in
`extension.entrypointProblems`. `environment_constraint` exists so that a package demanding Node ≥26
or pnpm (`@patimweb/pi-email`, `pi-x-ide`, `@braintrust/pi-extension`) is reported as a harness
environment limit rather than counted as a pigo incompatibility.

`registration_mismatch` is explained further in `failure.findings`: the surface (`activeTools`,
`allTools`, `commands`, `rpcCommands`), the side (`added`/`removed`), the names involved, and for a
same-name difference the exact field (`description`, `parameters`, `promptGuidelines`) with the
reference and candidate values. A name that upstream both adds and removes while the candidate
leaves it untouched is reported as `builtin_tool_definition_override`, which is a different defect
from failing to register a tool at all. `evidence` is a bounded excerpt; the untruncated stderr
stays in `<output>.jsonl`.

## Performance and other caveats

The summary reports results over the whole corpus and separately reports Pigo parity over packages that loaded stably in pinned upstream Pi. A filtered run is marked incomplete and does not produce an all-corpus percentage. Cold, warm-up, and sample failures and registration variation are retained in each runtime summary.

Startup measures process spawn through the `get_commands` response. The report includes median, p90, median absolute deviation, raw startup comparison, and observer-baseline-subtracted load. Ratios are omitted and labeled `noisy` when either MAD exceeds ten percent of its median, or `below_resolution` when either baseline-subtracted value is non-positive. `observerRPC` times only the common observer command round trip; it is not package-specific extension performance. The single global baseline does not remove machine drift, so small load deltas should not be treated as a benchmark guarantee. Bun and Node startup are not comparable to each other as a runtime benchmark here: each tier is compared only against the shared Node reference.

The harness expands manifest-declared extension directories before invoking both runtimes. This keeps the JavaScript runtime comparison deterministic but does not test Pi versus Pigo package/directory discovery semantics. Packages also share `/work` and `/tmp` inside one runtime container; the container protects the host, but a hostile package could contaminate a later package. Use one fresh runtime container per package before treating results as adversarial security evidence.

The download counts in `corpus.json` are a dated registry-traffic popularity proxy, not unique active users. The corpus includes the top gallery packages whose `pi.extensions` manifest field is a non-empty array; string-valued malformed manifests are excluded because upstream iterates them as characters and resolves no extension. Exact top-level versions and integrity hashes are checked against the corpus, while the committed lock pins the complete installed dependency graph.
