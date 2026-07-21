# Upstream alignment — current findings ledger

Last full audit: 2026-07-20; final delta refresh: 2026-07-21. Candidate: release closure descending
from `fbcabf9`; upstream: pi `0.80.10` at `3a40794ea14c6202586cc203d5b928eca9f6b673`.
This file replaces the previous historical backlog on every audit.

## Open should-fix findings

**None.** Mechanical source coverage is 436/436, and every previously confirmed item is fixed or
recorded as a settled divergence.

## Current evidence

| Dimension | Evidence |
|---|---|
| Source mapping | `docs/MIRROR.md` maps all upstream AI, agent, coding-agent, and TUI source files |
| Public API | `ai/model_helpers.go`, `ai/retry.go`, `ai/streaming_json.go`, `ai/images_models.go`, `codingagent/messages.go`, and `codingagent/modes/rpc_client.go` close the final exported surface |
| Test intent | The six numbered regression tails, provider edge cases, keybinding migrations, restored skill-invocation rendering, theme precedence, RPC reentrancy/panic isolation, and image-registry races have Go tests |
| Wire behavior | F1–F12 regenerate byte-clean and the black-box upstream RPC suite passes 28/28 |
| Docs/process | README, SDK/examples, changelog, contribution/security policy, comparison reports, and trim reports cover the newcomer and maintainer paths |
| Release | `make check`, four static GoReleaser targets, checksums, curl install, Homebrew formula configuration, and ldflags identity cover the deterministic release surface |

## Settled divergences

| Surface | Resolution |
|---|---|
| Radius, telemetry, pi.dev services | Removed or neutralized by D2 |
| Bundled llama extension | Excluded by D2 because upstream removed it immediately after the pin |
| `AgentHarness` and `streamProxy` | Dissolved/excluded by D29; reusable primitives and stream injection remain public |
| Bun/Node packaging and extension native addons | Replaced by static Go and the documented JS shim ceiling under D1, D7, and D17 |
| Native terminal helpers | Darwin modifier-key support remains a ledgered gap; Windows console support remains deferred by D8 |
| Update/self-update | Notify-only OrdalieTech release checks under G4 |
| Chat gateway | Explicit D27/D28 addition, isolated from the mirrored core |

## Watch items

- **Unified implementations:** upstream carries duplicate compaction and skill-prompt paths; Go
  maps both sources to one implementation. Re-check both upstream copies on every sync.
- **Absorbed `ModelRuntime`:** its behavior lives in `config.ModelRegistry`, request-auth
  resolution, and `AgentSessionServices`. Reconsider only if upstream adds behavior that cannot map
  cleanly to those seams.
- **Standalone `pi-ai` CLI:** the importable `ai` package is present, but upstream's small provider
  command has no Go command. Promote only if it becomes part of the coding-agent release path or an
  owner requests it.
- **Vulnerability scanning:** module verification and pinned CI are present, but there is no
  scheduled `govulncheck` equivalent to upstream's npm-audit workflow. This is repository hardening,
  not a current parity defect.

## Release condition

Re-run at the final commit. Zero open should-fix findings is required; watches remain non-blocking
unless their stated promotion condition is met.
