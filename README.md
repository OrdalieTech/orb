# Orb

A faithful, slim, pure-Go port of Mario Zechner's MIT-licensed [pi coding agent](https://pi.dev),
built by Ordalie as an SDK-first Go module and a single static CLI binary. Native CLI state lives
in SQLite, with Pi JSONL import/export and file-backed SDK defaults. Wire protocols, compatibility
formats and extension behavior follow the pinned version in [UPSTREAM.lock](UPSTREAM.lock);
every divergence is recorded in
[docs/DECISIONS.md](docs/DECISIONS.md). The `orb` binary deliberately coexists with upstream's
`pi`.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/OrdalieTech/orb/main/scripts/install.sh | sh
```

This installs `orb` to `~/.local/bin` after verifying the release checksum. Override the directory
with `ORB_INSTALL_DIR`; alternatively, with Go ≥ 1.27.1:

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

Sessions and global state live in `~/.orb/state/orb.db` by default. On first launch, Orb migrates
legacy state and preserves the original files; close older Orb and Bridge processes first. Use
`orb storage import <session.jsonl>` and `orb storage export <session-id> <output.jsonl>` to exchange
sessions with Pi. Native sessions have IDs rather than live JSONL paths. SDK defaults remain
file-backed, and `orb --pi-files ...` provides explicit compatibility in a separate state root.
See the [storage guide](docs/sdk.md#session-management) and [0.8.0 upgrade notes](CHANGELOG.md#080---2026-09-21).

`orb --mode rpc` retains the upstream RPC protocol; upstream's unmodified suite also passes
through the explicit Pi-file entry point.

## Connect devices with Bridge

Open **Bridge** from Settings or **Ctrl+P**, enable it, and choose **Add device**. Pair with an
invitation or existing SSH access; SSH setup installs or updates remote Orb when needed, then
conversation traffic uses Bridge over Tailcat. From the shell: `orb bridge connect-ssh user@host`.
Pairing grants mutual conversation access. Bridge runs in the background until explicitly stopped;
turning it off retains saved pairings. Recent foreign previews are read-only offline.

The current release controls attached conversations. Managed remote hosting and a unified
multi-Bridge Sessions page remain planned; see the [release notes](CHANGELOG.md#080---2026-09-21).

## Embed the SDK

```go
import "github.com/OrdalieTech/orb/agent"

result, err := agent.NewAgentSession(agent.AgentSessionOptions{})
if err != nil { log.Fatal(err) }
defer result.Session.Dispose()
result.Session.Prompt(context.Background(), "list the files here")
```

Thirteen runnable examples live in [agent/examples](agent/examples), from a minimal
session to custom tools, providers, and session runtimes — `01_minimal` runs offline against the
bundled faux provider.

## Run an upstream extension

Orb executes many upstream TypeScript extensions unmodified through a local Node.js or Bun host. Fetch the
pirate example from the pinned upstream revision and load it:

```sh
curl -fsSLO https://raw.githubusercontent.com/earendil-works/pi/ecac0a9c4edad3dac5d9f8b40e0c7db7a56471fc/packages/coding-agent/examples/extensions/pirate.ts
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

## Plugins, permissions, and MCP

Orb's bundled plugins (tasks, websearch, subagents, permissions, memory) are off by default and
configured through Settings or the CLI — including external CLIs as sub-agents, a fail-closed bash
filesystem sandbox, and MCP servers. `/plugins`, `/permissions`, and `/mcp` open configuration
windows in the TUI; `orb plugins …` and `orb mcp …` configure everything from the shell without a
session. See [docs/plugins.md](docs/plugins.md) for the full reference.

## Provenance

Upstream pi is © Mario Zechner, MIT — this port tracks the exact commit in `UPSTREAM.lock` and
regenerates its conformance goldens from upstream source (`make fixtures-check`). Orb is MIT
too; see [LICENSE](LICENSE), [CONTRIBUTING.md](CONTRIBUTING.md), and [SECURITY.md](SECURITY.md).

Every GitHub release includes a checksummed `orb_<version>_source.tar.gz`. To verify that source
independently, download it with `checksums.txt`, run `sha256sum -c checksums.txt`, extract it, and
run `CGO_ENABLED=0 go build -buildvcs=false ./cmd/orb`; release CI performs the same rebuild before
publishing. The flag is required because a source archive intentionally contains no `.git` metadata.
