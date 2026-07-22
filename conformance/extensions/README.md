# Ecosystem extension matrix

This harness measures whether the most-downloaded public Pi extension packages load and register the same surfaces in pinned upstream Pi 0.81.0 and Pigo. It never sends a model request: RPC starts with a dummy API key, `get_commands` proves session startup, and `observer.ts` emits canonical JSON for `pi.getActiveTools()`, `pi.getAllTools()`, and `pi.getCommands()`.

Registration comparison subtracts each runtime's own observer-only baseline, then compares stable active-tool names, all-tool names, and command names/descriptions. Runtime-specific source paths and metadata are intentionally excluded. This establishes install/load/register compatibility and a shared command-execution path; it does not claim that every package-specific command or external service workflow has run.

Package install is deliberately separate from package execution. The install container has network access but disables npm lifecycle scripts. The runtime container has no network, no host credentials, a read-only root filesystem, dropped Linux capabilities, a PID and memory limit, and only disposable tmpfs state plus a writable results directory. Do not run the corpus directly on a developer host.

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
  -v "$matrix_root/results:/results:rw" \
  -w /work \
  node:24-bookworm-slim \
  node /repo/conformance/extensions/matrix.mjs \
    --packages /packages \
    --pigo /opt/pigo/pigo \
    --observer /repo/conformance/extensions/observer.ts \
    --output /results/matrix.json
```

Use `--only 1,pi-mcp-adapter` for a focused run, or override `--warmups`, `--samples`, and `--timeout-ms`. Defaults are two warm-ups and eleven measured samples per runtime. Pi and Pigo runs alternate first position on each sample and never run concurrently, which limits scheduler bias without letting the two runtimes contend for CPU.

## Interpret the result

Every extension has one mutually exclusive status:

- `pass`: both runtimes loaded the package and produced byte-equivalent baseline-subtracted registrations.
- `pi_baseline_failure`: pinned upstream Pi could not load or probe the package, so it is excluded from Pigo's compatibility denominator.
- `pigo_load_failure`: Pi passed but Pigo failed to load or complete the same probe.
- `registration_mismatch`: both probes ran, but tools or commands differed.
- `infrastructure_failure`: the observer-only baseline failed, so package conclusions are invalid.

Startup measures process spawn through the `get_commands` response. Command timing measures observer invocation through both its deterministic notification and the RPC response. The report includes median, p90, median absolute deviation, raw Pigo/Pi ratios, and observer-baseline-subtracted extension load. A result is marked noisy when MAD exceeds ten percent of its median; baseline-subtracted ratios are omitted when the Pi denominator is zero or negative.

The download counts in `corpus.json` are a registry-traffic popularity proxy, not unique active users. Exact versions, npm integrity hashes, and manifest-declared `pi.extensions` entrypoints make the tested corpus reproducible even as package tags move.
