# Orb

A faithful, slim, pure-Go port of Mario Zechner's MIT-licensed [pi coding agent](https://pi.dev),
built by Ordalie as an SDK-first Go module and a single static CLI binary. Byte-compatible with
upstream pi's session format, wire protocols, config files, and extension examples at the pinned
upstream version in [UPSTREAM.lock](UPSTREAM.lock); every divergence is recorded in
[docs/DECISIONS.md](docs/DECISIONS.md). The `orb` binary deliberately coexists with upstream's
`pi`.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/OrdalieTech/orb/main/scripts/install.sh | sh
```

This installs `orb` to `~/.local/bin` after verifying the release checksum. Override the directory
with `ORB_INSTALL_DIR`; alternatively, with Go ≥ 1.26.5:

```sh
go install github.com/OrdalieTech/orb/cmd/orb@latest
```

## Update

Run `orb update` to install the latest verified release. Homebrew, Nix, and Snap installs stay
package-manager-owned. Use `--extensions`, `--models`, or `--all` for the other update targets.

## First session

```sh
export OPENAI_API_KEY=sk-...          # or ANTHROPIC_API_KEY, or run `orb login`
orb                                  # interactive TUI
orb -p "explain this repository"    # headless print mode
```

Sessions are plain JSONL, interchangeable with upstream pi: a session written by Orb opens in
TS pi and vice versa. `orb --mode rpc` speaks upstream's RPC protocol; upstream's own RPC test
suite runs unmodified against it.

## Embed the SDK

```go
import "github.com/OrdalieTech/orb/codingagent"

result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{})
if err != nil { log.Fatal(err) }
defer result.Session.Dispose()
result.Session.Prompt(context.Background(), "list the files here")
```

Thirteen runnable examples live in [codingagent/examples](codingagent/examples), from a minimal
session to custom tools, providers, and session runtimes — `01_minimal` runs offline against the
bundled faux provider.

## Run an upstream extension

Orb executes many upstream TypeScript extensions unmodified through a local Node.js or Bun host. Fetch the
pirate example from the pinned upstream revision and load it:

```sh
curl -fsSLO https://raw.githubusercontent.com/earendil-works/pi/845d6ff1f6643aba440341cce877ce1c43ebbc39/packages/coding-agent/examples/extensions/pirate.ts
orb --extension ./pirate.ts
```

Run `/pirate` in the TUI to exercise the extension.

61 of upstream's 69 single-file examples run as-is. In a locked snapshot of the 44 most-downloaded
valid Pi packages, 43 load and 39 are exact-compatible — 35 with load-and-registration parity plus
four event-driven packages with load-only parity — 88.6% by package count and 96.3% weighted by
monthly downloads. In a follow-up run where real Pi installed 30 popular packages into isolated
projects, Orb loaded 29 and 15 completed live tool or hook workflows.
See the [ecosystem matrix](docs/sync/ecosystem-extension-matrix.md), the
[live matrix](docs/sync/ecosystem-extension-live.md), and the [bridge guide](docs/sync/node-shims.md) for
the exact package-by-package result and remaining runtime ceilings.
`.pi/extensions/` in a trusted project and the global agent directory are discovered like upstream.

## Provenance

Upstream pi is © Mario Zechner, MIT — this port tracks the exact commit in `UPSTREAM.lock` and
regenerates its conformance goldens from upstream source (`make fixtures-check`). Orb is MIT
too; see [LICENSE](LICENSE), [CONTRIBUTING.md](CONTRIBUTING.md), and [SECURITY.md](SECURITY.md).

Every GitHub release includes a checksummed `orb_<version>_source.tar.gz`. To verify that source
independently, download it with `checksums.txt`, run `sha256sum -c checksums.txt`, extract it, and
run `CGO_ENABLED=0 go build -buildvcs=false ./cmd/orb`; release CI performs the same rebuild before
publishing. The flag is required because a source archive intentionally contains no `.git` metadata.
