# Live Pi extension compatibility — 2026-07-23

Real Pi 0.81.1 installed 30 popular packages into three isolated projects. Pigo loaded 29; the one
remaining incompatibility, `pi-llama-cpp`, produced a warning and left the session usable. Fifteen
packages then completed real tool or hook workflows driven by `openai/gpt-4.1-mini` through an
audited, rate-limited relay.

`live` means the model called the extension and its result was checked. `registered` means Pigo and
Pi exposed the same resource names in the tested profile, but the main workflow was not executed.
`load-only` means the event extension started without exposing a command, skill, or tool.

| Rank | Package | Result | Evidence |
| ---: | --- | --- | --- |
| 1 | `@vigolium/piolium@0.0.13` | registered | command/resource parity |
| 2 | `pi-mcp-adapter@2.11.0` | live | model discovered and called `compat_echo`; `MCP_PROOF:LIVE_MCP_7` |
| 3 | `pi-web-access@0.13.0` | registered | three tools exposed; network workflow not claimed |
| 4 | `pi-subagents@0.35.1` | registered | command/tool parity; alternate subagent package was exercised |
| 5 | `context-mode@1.0.169` | live | lifecycle hooks persisted session state in its SQLite store |
| 6 | `@tintinweb/pi-subagents@0.14.2` | live | child Pigo read `service.ts`; `SUBAGENT_PROOF:activeUserIds,summarizeUsers` |
| 8 | `pi-lens@3.8.71` | live | `module_report` found five Go symbols; `read_symbol` returned `Transfer` |
| 10 | `@quintinshaw/pi-dynamic-workflows@3.3.0` | live | foreground child read `service.ts`; `WORKFLOW_PROOF:summarizeUsers` |
| 11 | `@gotgenes/pi-permission-system@20.10.0` | live | blocked a headless MCP call through its real preflight hook |
| 12 | `pi-simplify@0.2.3` | registered | command/resource parity |
| 14 | `@mjasnikovs/pi-task@0.18.49` | registered | tool/resource parity; worker workflow not claimed |
| 15 | `@juicesharp/rpiv-ask-user-question@2.0.0` | registered | interactive tool exposed; dialog not automated |
| 17 | `pi-hermes-memory@0.8.2` | live | project memory added, then found from a fresh process |
| 18 | `@juicesharp/rpiv-todo@2.0.0` | live | task created, completed, and recovered with `--continue` |
| 20 | `@narumitw/pi-goal@0.24.0` | registered | command/tool parity |
| 23 | `pi-agent-browser-native@0.2.71` | registered | tool parity; external browser workflow not claimed |
| 25 | `pi-readseek@0.8.0` | live | `readSeek_def` found `Balance` in `main.go` |
| 27 | `pi-crew@0.9.46` | live | crew child read project settings; `CREW_PROOF:pi-crew` |
| 29 | `pi-fabric@0.22.4` | live | QuickJS type-check and execution returned `proof: 42` |
| 31 | `pi-prompt-template-model@0.10.0` | registered | prompt/resource parity |
| 32 | `pi-intercom@0.6.0` | registered | tool/resource parity; multi-process socket workflow not claimed |
| 33 | `opencode-codebase-index@0.14.0` | live | indexed three chunks; semantic search found both target functions |
| 37 | `@narumitw/pi-lsp@0.25.0` | live | mounted `gopls` diagnosed the intentional type error |
| 38 | `pi-shazam@0.30.0` | live | graph built over ten symbols with Go/TypeScript detection |
| 39 | `pi-cursor-sdk@0.1.60` | registered | resource parity |
| 40 | `pi-llama-cpp@0.9.1` | incompatible | expects private SDK export `ApiKeyCredential`; warning is isolated |
| 41 | `pi-vault-mind@0.16.25` | live | `vault_write` and `vault_read` round-tripped `cobalt-orchid` |
| 42 | `gentle-engram@0.1.10` | load-only | stable event-extension startup |
| 43 | `pi-hashline-edit-pro@0.16.15` | registered | resource parity |
| 44 | `cc-safety-net@1.0.6` | registered | command/resource parity |

The dense global profile exposed 67 extension commands, seven prompts, and 35 skills in both
runtimes; its test project added one prompt and one skill. Split profiles were required for two
upstream package collisions: Lens and LSP both register `lsp_diagnostics`, while Pi Crew and Tintin
Subagents both register `Agent`. Pi rejects those combinations too.

Pi's own install of `gentle-pi@1.2.0` failed in the package postinstall tar extraction, so it is an
installer baseline failure rather than a Pigo result.

## Dense-profile startup

The benchmark measured process spawn through `get_commands` after one warm-up and three alternating
runs with the same 17-extension profile:

| Runtime | Mean | Range |
| --- | ---: | ---: |
| Pi 0.81.1 | 14.586 s ± 0.212 | 14.344–14.733 s |
| Pigo candidate | 13.033 s ± 0.379 | 12.658–13.416 s |

Pigo used 89.3% of Pi's startup time; Pi took about 1.12× as long. Three samples support this
specific dense-startup comparison, not a general throughput guarantee.

## Test boundary

The model relay accepted only OpenAI-compatible chat-completion requests, rewrote every request to
one fixed model, capped output at 1,200 tokens, logged no payloads, and stopped after 50 requests.
Test containers had no direct internet route. MCP, embeddings, and LSP used deterministic local
fixtures; the OpenRouter credential was never passed to extension processes.

The machine-readable summary is
[`conformance/extensions/results/pi-0.81.1-pigo-live.json`](../../conformance/extensions/results/pi-0.81.1-pigo-live.json).
