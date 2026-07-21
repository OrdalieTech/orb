# pigo

A faithful, slim, pure-Go port of Mario Zechner's MIT-licensed [pi coding agent](https://pi.dev),
built by Ordalie as an SDK-first Go module and a single static CLI binary. Byte-compatible with
upstream pi's session format, wire protocols, config files, and extension examples at the pinned
upstream version in [UPSTREAM.lock](UPSTREAM.lock); every divergence is recorded in
[docs/DECISIONS.md](docs/DECISIONS.md). The `pigo` binary deliberately coexists with upstream's
`pi`; this module is unrelated to the older `github.com/dimetron/pi-go` project.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/OrdalieTech/pigo/main/scripts/install.sh | sh
```

This installs `pigo` to `~/.local/bin` after verifying the release checksum. Override the directory
with `PIGO_INSTALL_DIR`; alternatively, with Go ≥ 1.26.5:

```sh
go install github.com/OrdalieTech/pigo/cmd/pigo@latest
```

## Update

Run `pigo update`. It never replaces its running binary; it prints the exact installer or Go command
for your installation, while `pigo update --extensions` updates installed pi packages.

## First session

```sh
export OPENAI_API_KEY=sk-...          # or ANTHROPIC_API_KEY, or run `pigo login`
pigo                                  # interactive TUI
pigo -p "explain this repository"    # headless print mode
```

Sessions are plain JSONL, interchangeable with upstream pi: a session written by pigo opens in
TS pi and vice versa. `pigo --mode rpc` speaks upstream's RPC protocol; upstream's own RPC test
suite runs unmodified against it.

## Embed the SDK

```go
import "github.com/OrdalieTech/pigo/codingagent"

result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{})
if err != nil { log.Fatal(err) }
defer result.Session.Dispose()
result.Session.Prompt(context.Background(), "list the files here")
```

Thirteen runnable examples live in [codingagent/examples](codingagent/examples), from a minimal
session to custom tools, providers, and session runtimes — `01_minimal` runs offline against the
bundled faux provider.

## Run an upstream extension

pigo executes upstream's TypeScript extensions unmodified in an embedded JS runtime. Fetch the
pirate example from the pinned upstream revision and load it:

```sh
curl -fsSLO https://raw.githubusercontent.com/earendil-works/pi/9c480b6ad2c7419875a7a850fb4ad5f9232313b8/packages/coding-agent/examples/extensions/pirate.ts
pigo --extension ./pirate.ts
```

Run `/pirate` in the TUI to exercise the extension.

61 of upstream's 69 single-file extension examples run as-is (status per example in
[docs/sync/extension-matrix.md](docs/sync/extension-matrix.md)); the
[bridge guide](docs/sync/node-shims.md) documents loading, package support, and runtime ceilings.
`.pi/extensions/` in a trusted project and the global agent directory are discovered like upstream.

## Provenance

Upstream pi is © Mario Zechner, MIT — this port tracks the exact commit in `UPSTREAM.lock` and
regenerates its conformance goldens from upstream source (`make fixtures-check`). pigo is MIT
too; see [LICENSE](LICENSE), [CONTRIBUTING.md](CONTRIBUTING.md), and [SECURITY.md](SECURITY.md).
