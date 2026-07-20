# pi-go

A faithful, slim, pure-Go port of Mario Zechner's MIT-licensed [pi coding agent](https://pi.dev),
built by Ordalie as an SDK-first Go module and a single static CLI binary. Byte-compatible with
upstream pi's session format, wire protocols, config files, and extension examples at the pinned
upstream version in [UPSTREAM.lock](UPSTREAM.lock); every divergence is recorded in
[docs/DECISIONS.md](docs/DECISIONS.md).

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/OrdalieTech/pi-go/main/scripts/install.sh | sh
```

or with Go ≥ 1.26: `go install github.com/OrdalieTech/pi-go/cmd/pi@latest`.

## First session

```sh
export OPENAI_API_KEY=sk-...        # or ANTHROPIC_API_KEY, or run `pi login`
pi                                   # interactive TUI
pi -p "explain this repository"      # headless print mode
```

Sessions are plain JSONL, interchangeable with upstream pi: a session written by pi-go opens in
TS pi and vice versa. `pi --mode rpc` speaks upstream's RPC protocol; upstream's own RPC test
suite runs unmodified against it.

## Embed the SDK

```go
import "github.com/OrdalieTech/pi-go/codingagent"

result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{})
if err != nil { log.Fatal(err) }
defer result.Session.Dispose()
result.Session.Prompt(context.Background(), "list the files here")
```

Thirteen runnable examples live in [codingagent/examples](codingagent/examples), from a minimal
session to custom tools, providers, and session runtimes — `01_minimal` runs offline against the
bundled faux provider.

## Run an upstream extension

pi-go executes upstream's TypeScript extensions unmodified in an embedded JS runtime. Fetch the
pirate example from the pinned upstream revision and load it:

```sh
curl -fsSLO https://raw.githubusercontent.com/earendil-works/pi/3a40794ea14c6202586cc203d5b928eca9f6b673/packages/coding-agent/examples/extensions/pirate.ts
pi --extension ./pirate.ts
```

Run `/pirate` in the TUI to exercise the extension.

61 of upstream's 69 single-file extension examples run as-is (status per example in
[docs/sync/extension-matrix.md](docs/sync/extension-matrix.md)); `.pi/extensions/` in a trusted
project and the global agent directory are discovered like upstream.

## Provenance

Upstream pi is © Mario Zechner, MIT — this port tracks the exact commit in `UPSTREAM.lock` and
regenerates its conformance goldens from upstream source (`make fixtures-check`). pi-go is MIT
too; see [LICENSE](LICENSE), [CONTRIBUTING.md](CONTRIBUTING.md), and [SECURITY.md](SECURITY.md).
