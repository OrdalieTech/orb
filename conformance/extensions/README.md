# Ecosystem extension matrix

This harness measures whether the most-downloaded public Pi extension packages load and expose the same observable tool and command registrations in pinned upstream Pi 0.81.1 and Pigo. It never sends a model request: RPC starts with a dummy API key, `get_commands` proves session startup, and `observer.ts` emits canonical JSON for `pi.getActiveTools()`, full tool definitions from `pi.getAllTools()`, and `pi.getCommands()`.

The separate [live matrix](../../docs/sync/ecosystem-extension-live.md) installs packages through
Pi and exercises representative model-driven workflows; do not infer workflow support from this
offline harness alone.

Registration comparison subtracts each runtime's own observer-only baseline, then compares stable active-tool names, canonical tool descriptions/parameter schemas/prompt guidelines, and command names/descriptions. Runtime-specific source paths and metadata are intentionally excluded. A package-specific command or tool is never executed, so `load_register_pass` is deliberately narrower than end-to-end extension compatibility. Flags, shortcuts, renderers, providers, event-handler behavior, credentials, model requests, and external services need separate workflow probes.

Package install is deliberately separate from package execution. The install container uses the committed `package-lock.json` through `npm ci --ignore-scripts`. The runtime container has no network, no host credentials, a read-only root filesystem, dropped Linux capabilities, a PID and memory limit, and disposable tmpfs state. Both runners fail unless the network namespace contains only loopback interfaces, and the load result hashes the exact harness, corpus, observer, lock, and binaries it used. Do not run the corpus directly on a developer host.

## Run the matrix

Node 24 is both the upstream Pi requirement and the only harness runtime dependency. From the repository root:

```sh
matrix_root="$(mktemp -d /tmp/pigo-extension-matrix.XXXXXX)"
mkdir -p "$matrix_root/packages" "$matrix_root/results" "$matrix_root/bin"
CGO_ENABLED=0 go build -o "$matrix_root/bin/pigo" ./cmd/pigo

docker run --rm \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --pids-limit 256 \
  --memory 2g \
  --cpus 2 \
  -e HOME=/tmp/matrix-home \
  -e npm_config_cache=/tmp/npm-cache \
  --tmpfs /tmp:rw,nosuid,nodev,size=512m,mode=1777 \
  -v "$PWD:/repo:ro" \
  -v "$matrix_root/packages:/packages:rw" \
  -w /repo \
  node:24-bookworm-slim \
  node /repo/conformance/extensions/prepare.mjs --output /packages

docker run --rm \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --pids-limit 256 \
  --memory 2g \
  --cpus 2 \
  --tmpfs /work:rw,noexec,nosuid,nodev,size=512m,mode=1777 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777 \
  -v "$PWD:/repo:ro" \
  -v "$matrix_root/packages:/packages:ro" \
  -v "$matrix_root/bin:/opt/pigo:ro" \
  -w /work \
  node:24-bookworm-slim \
  node /repo/conformance/extensions/matrix.mjs \
    --packages /packages \
    --pigo /opt/pigo/pigo \
    --observer /repo/conformance/extensions/observer.ts \
  > "$matrix_root/results/matrix.json"
```

Use `--only 1,pi-mcp-adapter` for a focused run, or override `--warmups`, `--samples`, and `--timeout-ms`. Defaults are two warm-ups and eleven measured samples per runtime. Pi and Pigo runs alternate first position on each sample and never run concurrently, which limits scheduler bias without letting the two runtimes contend for CPU.

Run the inspected read-only command and Piolium workflow probes in the same hardened container,
adding a writable results mount:

```sh
docker run --rm \
  --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges --pids-limit 256 --memory 2g --cpus 2 \
  --tmpfs /work:rw,noexec,nosuid,nodev,size=512m,mode=1777 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=256m,mode=1777 \
  -v "$PWD:/repo:ro" \
  -v "$matrix_root/packages:/packages:ro" \
  -v "$matrix_root/bin:/opt/pigo:ro" \
  -v "$matrix_root/results:/results:rw" \
  -w /work node:24-bookworm-slim \
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

## Interpret the result

Every extension has one mutually exclusive status plus a more specific `reason`:

- `load_register_pass`: every cold, warm-up, and measured probe succeeded, registrations were stable, and both runtimes produced the same non-empty baseline-subtracted tool or command changes.
- `load_only_pass`: the same stable probes succeeded but produced no observed tool or command changes. This includes conditional or event-only packages whose useful behavior was not exercised.
- `flaky`: a cold, warm-up, or measured attempt disagreed with another attempt, or registrations varied between attempts.
- `unsupported`: every attempt failed under upstream Pi or Pigo, or stable registrations differed. The `reason` distinguishes those cases.
- `infra_error`: the observer-only baseline was not stable in both runtimes, so every package conclusion and aggregate percentage is invalid.

The summary reports results over all 44 corpus packages and separately reports Pigo parity over packages that loaded stably in pinned upstream Pi. A focused `--only` run is marked incomplete and does not produce an all-corpus percentage. Cold, warm-up, and sample failures and registration variation are retained in each runtime summary.

Startup measures process spawn through the `get_commands` response. The report includes median, p90, median absolute deviation, raw startup comparison, and observer-baseline-subtracted load. Ratios are omitted and labeled `noisy` when either MAD exceeds ten percent of its median, or `below_resolution` when either baseline-subtracted value is non-positive. `observerRPC` times only the common observer command round trip; it is not package-specific extension performance. The single global baseline does not remove machine drift, so small load deltas should not be treated as a benchmark guarantee.

The harness expands manifest-declared extension directories before invoking both runtimes. This keeps the JavaScript runtime comparison deterministic but does not test Pi versus Pigo package/directory discovery semantics. Packages also share `/work` and `/tmp` inside one runtime container; the container protects the host, but a hostile package could contaminate a later package. Use one fresh runtime container per package before treating results as adversarial security evidence.

The download counts in `corpus.json` are a dated registry-traffic popularity proxy, not unique active users. The corpus includes the top gallery packages whose `pi.extensions` manifest field is a non-empty array; string-valued malformed manifests are excluded because upstream iterates them as characters and resolves no extension. Exact top-level versions and integrity hashes are checked against the corpus, while the committed lock pins the complete installed dependency graph.
