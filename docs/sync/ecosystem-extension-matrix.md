# Ecosystem extension compatibility matrix — 2026-07-22

Pigo has exact load-and-registration parity for 24 of the 44 most-downloaded valid Pi extension
packages in this snapshot: 20 register the same observable commands and tools as Pi, and four are
stable load-only or event-driven packages. That is 54.5% by package count and 60.4% when weighted by
the snapshot's monthly npm downloads.

Load parity is not workflow parity. A separate line-grounded audit classifies 13 packages as likely
compatible, six as useful but partial, five as loading successfully while their defining feature is
blocked, and 20 as blocked during load. Only one real package workflow and seven read-only command
handlers were safe enough to execute in the offline comparison, so `likely_compatible` remains a
testable hypothesis rather than an end-to-end guarantee.

## What was tested

The corpus is the top 44 entries from `https://pi.dev/packages?sort=downloads` whose published
`pi.extensions` field is a non-empty array, ordered by the displayed monthly npm download count on
2026-07-22. Downloads are registry traffic, not unique users. Malformed string-valued extension
manifests are excluded because pinned upstream Pi iterates them as characters and resolves no
extension. Exact top-level versions and integrity hashes are in
[`conformance/extensions/corpus.json`](../../conformance/extensions/corpus.json), and the committed
lock pins the full dependency graph.

The load matrix compared upstream Pi 0.81.1 with `pigo 0.1.2 (upstream pi 0.81.1 @ 20be4b18)`
under Node 24.18.0. Each runtime received one cold run, two warm-ups, and eleven measured samples in
alternating order with a 30-second timeout. Every successful status below means all attempts were
stable. The observer subtracts each runtime's own baseline and compares active tools, full tool
names/descriptions/parameter schemas/prompt guidelines, and command names/descriptions. It expands
manifest directories itself, so this result does not test package-directory discovery semantics.

The static workflow verdict comes from
[`conformance/extensions/workflow-audit.json`](../../conformance/extensions/workflow-audit.json).
Every entrypoint and the code behind the package's primary workflow was inspected with package-local
file-and-line evidence. It does not claim execution:

- `likely` means no remaining Pigo-specific blocker was found, subject to the stated real-world
  smoke and any credentials, service, or executable prerequisite.
- `partial` means a useful subset appears viable but a named workflow remains blocked or unproven.
- `main feature blocked` means the package loads and registers, but its defining operation reaches
  an unsupported surface.
- `load blocked` means at least one declared entrypoint cannot start in Pigo.

The offline smoke ran one warm-up and five measured samples per runtime with no inherited
credentials, a dummy API key, a dead local proxy, cancelled dialogs, and a guard that failed any
attempt starting model activity. Its network-namespace check found no external interfaces. Command
smokes prove only their read-only handler output; they do not prove tools, renderers, providers,
event hooks, external services, or model-driven workflows.

## Complete 44-package matrix

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

## Workflow evidence

All seven read-only command handlers completed successfully in both runtimes. Six produced
identical normalized observable output:

- Piolium `/piolium-help` and `/piolium-status` matched exactly, including startup UI and dialog or
  notification output.
- Subagents `/subagents-doctor`, Hypa `/hypa`, Goal `/goal status`, and LSP `/lsp` matched exactly.
  The LSP command only inspected configuration and missing executables; it did not start a language
  server, so rank 37 remains correctly classified as main-feature-blocked.

FFF was the sole handler difference. It reported a healthy initialized `0.10.1` finder under Pi,
while Pigo reported `FFF not initialized` after `dynamic modules not enabled in the host program`.
That is a native-backend compatibility finding, not normalization noise.

The Piolium `piolium-knowledge-base-stage` probe exercised a real workflow rather than registration.
A disposable wrapper imported Piolium's resolver, stager, and staged loader, then resolved two
nested UTF-8 fixtures, performed bounded `FileHandle` reads and fatal `TextDecoder` decoding,
hashed and atomically staged them, and loaded the result back. Pi and Pigo produced the same ordered
two-file, 140-byte payload and the same aggregate SHA-256
`56f24ae0be6d046a0b2bfad76d12a656e9274c9319777bd590993aa7a2f912eb` in all five measured
samples. No model or external network was used.

`cc-safety-net` was inspected but not executed because its command deliberately calls
`pi.sendUserMessage` with a generated workflow prompt, which would start model activity and violate
the offline smoke boundary.

## Performance

The observer-only median startup was 747.068 ms for Pi and 288.411 ms for Pigo, a Pigo/Pi ratio of
0.386. Across all 24 load-compatible packages, total startup favored Pigo in every case; the median
ratio was 0.421 and the range was 0.227–0.926. This measures process spawn through `get_commands`,
so Pigo's lower base process cost is part of the result.

After subtracting each runtime's single global observer baseline, the median extension-load ratio
was 0.839 across 23 comparable packages. Pigo was lower for 12 and higher for 11, with a range of
0.143–11.480; Raindrop was below resolution because its Pi median was 3.560 ms faster than the
global baseline. The wide range is why the table must not be read as workload throughput:
subtraction amplifies machine drift when a package's apparent incremental load is only a few
milliseconds. Pi Simplify's 11.480×, for example, divides 31.306 ms by a 2.727 ms Pi delta rather
than measuring an extension operation.

The observer RPC is common harness work rather than package work and was noisy for 19 of 24
load-compatible packages. Six of seven command-handler latency ratios and the Piolium workflow
ratio were also suppressed because median absolute deviation exceeded 10% of the median. The sole
stable command ratio favored Pigo—0.542 for `/subagents-doctor`—but five samples are not a
performance guarantee. The evidence supports a startup-cost comparison; it does not yet support a
claim about model turns, native tools, network operations, or long-running extension throughput.

## Common blocker families and what would fix them

The families overlap because several packages hit more than one independent blocker.

1. **Private Pi SDK and TUI surfaces affect 12 packages** (ranks 4, 6, 10, 11, 14, 21, 22, 25,
   29, 36, 40, and 43). The recurring gaps are native tool factories and definitions,
   `SettingsManager` and credential reads, session listing, and keyboard-driven TUI components.
   The useful fix is a native-backed, behavior-tested facade for each real surface. Placeholder
   objects would merely convert load failures into later workflow failures.
2. **Streams, sockets, and process lifecycle affect 11 packages** (ranks 2, 4, 14, 25, 27, 28,
   29, 32, 36, 37, and 42). MCP, LSP, brokers, sandboxes, and several subagent workflows need
   writable child stdin, incremental stdout/stderr events, backpressure, cancellation, detached
   lifecycle, or real Unix/TCP sockets. This needs a coherent process and networking design; small
   method-name shims cannot provide the semantics safely.
3. **Native Node, Bun, or WASM backends affect nine packages** (ranks 5, 8, 13, 17, 33, 38, 39,
   41, and 43). These include better-sqlite3, AST-grep, FFF, tree-sitter, LanceDB, ONNX, sharp,
   Cursor's Bun store, and xxhash WASM. Generic `.node` loading conflicts with Pigo's pure-Go,
   `CGO_ENABLED=0` product contract. Prefer upstream-supported executables, services, WASM where
   the host can preserve module assets, or explicit Go adapters; do not pretend native bindings
   loaded.
4. **Module-loader and bundler semantics affect eight packages** (ranks 9, 15, 18, 22, 27, 29,
   39, and 43). The shared gaps are top-level await in a synchronous CommonJS host, eager resolution
   of optional dynamic imports, executable relative CommonJS requires, and per-module
   `import.meta.url` assets. A true asynchronous ESM path plus lazy package resolution and
   asset-preserving bundling would directly unlock ranks 15 and 18 and expose the next honest
   blocker in several others.
5. **Selective Web and crypto APIs limit two otherwise useful packages**. Rank 3's ordinary web
   search/fetch path appears viable, but Chrome-cookie import needs PBKDF2 and AES decipher APIs.
   Rank 35's plain JSON tracing may work, while attachment and streaming paths can reach `Blob`,
   `FormData`, `ReadableStream`, and `crypto.subtle`. Add standards-faithful APIs only after a
   focused smoke proves that a popular workflow reaches them.

The highest-leverage compatibility work is faithful SDK/tool/settings facades followed by real
streamed child-process I/O. Async ESM and bundler fixes would then remove several early load
barriers. Native-addon packages need alternate backends rather than generic Sobek shims, so they
should remain explicitly unsupported until such a backend exists.

## Reproducibility hashes

These hashes identify the exact inputs and raw outputs used for this report:

| Artifact | SHA-256 |
| --- | --- |
| `conformance/extensions/corpus.json` | `632bd7c8361fe33feec4155035eed879349b57c399684beb0a118f8ef99e8c33` |
| `conformance/extensions/workflow-audit.json` | `28502fdfa428703a993db85e56456923db74a481efe1d9dd5f9a19dc0215b51e` |
| `conformance/extensions/package-lock.json` | `05bdb25ad57e4fba4f15f4c9a8305663e592e6954f0a79e930866c3a827e2b6c` |
| `conformance/extensions/matrix.mjs` | `704b9e9e6842d0e55cac21da493b112b1345a070bcf165f1afa8defba5d1f458` |
| `conformance/extensions/observer.ts` | `f7a686a0ddfe60f015a310c94b748243568ec7667875607ed4965d3f6f5b62b3` |
| `conformance/extensions/smoke.mjs` | `92623dcf16f5360d2cefe44ac39ee7aefd0078b88e99d872248964c18ff20042` |
| `conformance/extensions/smoke-cases.json` | `a424e2cfc745e9e9f2d1146ca6ec4dfe379619957dfb49d081706f7750146aa5` |
| raw `matrix-final.json` | `c2b9226b83c6bea78817e64cea2563826e7daf6e145bbc4e6a6c6f5e493b554f` |
| raw `smoke-final.json` | `fe5336aa077801dbaa574acd0dc29c10785d729b2cb00eb9677184f4c5c8c946` |
| compact [`pi-0.81.1-pigo-v0.1.2.json`](../../conformance/extensions/results/pi-0.81.1-pigo-v0.1.2.json) | `db741d55c76c31f25384af923b99e34c49ead208be97bca1f482db626efcd9eb` |
| tested upstream Pi executable | `af302f231437eaf6f37691bce4b34234fcb626bcb5eb3910d4fc3f6519bf78ca` |
| tested Pigo executable | `a616d8486ce6047976b7688d9b641b5ff51d9ef781351b6f8433a340faaa375e` |

The raw result files were generated at
`/tmp/pigo-extension-matrix-v0811.954GWz/results/{matrix-final.json,smoke-final.json}`. The matrix
artifact is 2.5 MiB because it retains every attempt and canonical registration snapshot; the smoke
artifact is 938,604 bytes because it retains every normalized event and workflow payload. The
tracked compact artifact contains all verdicts, remediation, summary statistics, and provenance;
the raw hashes preserve the complete attempt-level audit identity.
