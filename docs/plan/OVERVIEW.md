# Implementation plan — overview

Full-parity port of pi v0.80.10 (see `docs/DECISIONS.md`, `docs/ARCHITECTURE.md`). Work is cut into
work packages (WPs), each sized for one coding-agent session, each landing with its conformance
fixtures. Detailed specs live in `phase-N-*.md` files; this file is the map.

## Phases

```
Phase 0  Bootstrap            repo, CI, conformance harness skeleton
Phase 1  Walking skeleton     OpenAI + loop + core tools + print mode  →  USABLE AGENT (gate WP-170)
Phase 2  Provider breadth     Anthropic, Gemini, Mistral/Azure, Bedrock, OAuth, catalog, compat family
Phase 3  Harness & headless   compaction, session tree, json/rpc modes, skills, ext API, MCP, packages, SDK
Phase 4  TUI                  renderer, editor, markdown, images, app assembly, parity pass
Phase 5  JS extension bridge  sobek+esbuild runtime, API bindings, node shims, UI bridge, example matrix
Phase 6  Ops & release        sync tooling, docs, release pipeline; Windows wave (future)
```

Dependency structure — after the WP-170 gate, three lanes run in parallel:

```
0 ── 1(gate WP-170) ──┬── Lane A: 2 (providers)
                      ├── Lane B: 3 (harness/headless)
                      └── Lane C: 4 (TUI)          [WP-450 needs B: 320/341/351]
                             5 (bridge) needs B(350/351) for 510–530, C(450) for 541–542
                             6 closes; 670 (Windows) unscheduled future
```

## Execution protocol

1. Pick the lowest-numbered WP whose dependencies are merged (or the one assigned to you).
2. Follow `AGENTS.md` (workflow, hard rules, definition of done).
3. A WP is *merged* only with fixtures green and acceptance checks verified in the PR body.
4. Anything discovered out-of-scope becomes a note in the PR body → owner turns it into a WP or a
   DECISIONS change; never silently expand scope.
5. Milestones M1–M5 (`docs/RELEASE-CRITERIA.md`) close each phase; the trim/verify WPs
   (180/390/470/560/650) check every milestone criterion and run the trim-pass checklist — a
   milestone is not done until all its boxes check.

## Work-package index

| WP | Title | Depends |
|---|---|---|
| **Phase 0** | [phase-0-bootstrap.md](phase-0-bootstrap.md) | |
| 001 | Repo bootstrap: go.mod, CI, Makefile, LICENSE, lint | — |
| 002 | Conformance harness + upstream materialization | 001 |
| **Phase 1** | [phase-1-skeleton.md](phase-1-skeleton.md) | |
| 110 | ai: unified types, streaming events, Schema (+gate G1), partialjson | 002 |
| 120 | ai/api: openai-responses + openai-completions via openai-go | 110 |
| 130 | agent: loop, Agent, hooks, events, faux provider | 110 |
| 140 | tools wave 1: read(text)/write/edit/ls, mutation queue, truncate | 130 |
| 150 | tools wave 2: bash, grep, find, binary manager | 130 |
| 160 | codingagent skeleton: settings, sessions(JSONL v3), system prompt, print mode | 130,140 |
| 170 | **Skeleton gate**: end-to-end usable agent, dogfood check | 120,140,150,160 |
| 180 | **M1 trim + verify** (RELEASE-CRITERIA) | 170 |
| **Phase 2** | [phase-2-providers.md](phase-2-providers.md) | |
| 210 | Anthropic messages + prompt caching | 170 |
| 211 | Auth storage + Anthropic OAuth (Pro/Max) + headless login | 210 |
| 221 | Gemini (+Vertex) — gate G2 | 170 |
| 231 | Mistral conversations + Azure responses | 170 |
| 232 | Bedrock converse-stream | 170 |
| 241 | Codex responses + ChatGPT OAuth; Copilot device-code; xAI | 211 |
| 250 | Model catalog: models.dev generate + refresh; models.json; model patterns | 170 |
| 260 | pi-messages wire shape (generic SSE gateway client) | 110 |
| 270 | Compat-family enablement (~20 providers via data + flags) | 250 |
| **Phase 3** | [phase-3-harness.md](phase-3-harness.md) | |
| 310 | Compaction, branch summaries, auto-retry, queue events | 170 |
| 320 | Session tree ops: fork/resume/tree/continue; export HTML/MD | 170 |
| 330 | JSON mode (AgentSessionEvent stream) | 310 |
| 331 | RPC mode (bidirectional JSONL protocol) | 330 |
| 340 | Skills, prompt templates, slash-command resolution | 160 |
| 350 | Go-native ExtensionAPI: types, registry, runner dispatch | 170 |
| 351 | Extension wire-through: hooks in loop/tools/providers/input, built-in override | 350 |
| 352 | Bundled MCP extension (official go-sdk) | 351 |
| 360 | pi packages (npm:/git:) + project trust | 340,350 |
| 370 | SDK surface: AgentSession, channel adapter, 13 examples, sdk docs | 310,320,351 |
| 390 | **M2 trim + verify** (+ nightly live-suite CI wiring) | all of Phase 2+3 |
| **Phase 4** | [phase-4-tui.md](phase-4-tui.md) | |
| 410 | tui core: terminal, differential renderer, Component/Focusable, keybindings | 170 |
| 420 | Editor, Input, autocomplete+fuzzy, Select/Settings lists | 410 |
| 430 | Markdown renderer, syntax highlighting, themes | 410 |
| 440 | Terminal images (kitty/iTerm2), image read support, clipboard | 410 |
| 450 | TUI app assembly: chat view, dialogs, built-in slash commands, status zones | 420,430,320,340,351 |
| 460 | TUI parity pass + render goldens (F12) | 450,440 |
| 470 | **M3 trim + verify** | 460 |
| **Phase 5** | [phase-5-jsbridge.md](phase-5-jsbridge.md) | |
| 510 | Bridge runtime: sobek + esbuild pipeline, discovery, /reload | 350 |
| 520 | JS ExtensionAPI bindings wave 1 (events, registrations, messaging) | 510,351 |
| 530 | Node shims: fs/path/os/process/url/util, fetch, exec bridge | 510 |
| 541 | ctx.ui bridge: dialogs, notify, status/widget/footer, autocomplete | 520,450 |
| 542 | Custom components, editor replacement, overlays — gate G3 | 541 |
| 550 | Example-extension matrix runner (F11) + fix wave | 520,530,541 |
| 560 | **M4 trim + verify** | 550,542 |
| **Phase 6** | [phase-6-ops.md](phase-6-ops.md) | |
| 610 | Sync tooling: delta mapping, fixture regen driver, report generator | 002 (improves), 170 |
| 620 | Docs: README (credit/provenance), SDK guide, extension-author guide | 370,550 |
| 650 | **M5 trim + verify** (final slimming, LOC/dep audit, criteria re-check) | 610,620 |
| 661 | Release pipeline: goreleaser, install script, version check — gate G4 | 650 |
| 670 | Windows wave (future, unscheduled): shell/console/paths/CI | 661 |

Trim/verify WPs (180/390/470/560/650) have no phase-file spec on purpose: their spec IS the
trim-pass checklist plus the current milestone's criteria in `docs/RELEASE-CRITERIA.md`.
