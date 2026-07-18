# Success criteria — milestones M1–M5

For implementing agents: these criteria are binding. A milestone WP is **done when every box below
checks, and not before** — keep working until they pass or a criterion is genuinely impossible, in
which case stop and surface it; never silently weaken a criterion, skip a fixture, or hand-edit a
golden to get green. (Deferred *decision* gates G1–G4 live in DECISIONS.md — different thing.)

## Standing criteria (every merge, no exceptions)

- [x] AGENTS.md definition of done: `CGO_ENABLED=0` cross-build (linux+darwin × amd64+arm64),
      `go vet` + golangci-lint clean, `go test -race ./...` green including fixtures, MIRROR.md updated.
- [x] No dependency outside ARCHITECTURE §8's table.
- [x] No fixture golden weakened, regenerated-away, or hand-edited to pass.
- [x] No single-implementation interface unless it's an upstream seam (Operations, env, credential store).

## M1 — Skeleton (closes Phase 1; verified by WP-180)

- [x] `pi -p "<task>"` completes a real OpenAI round-trip with tool calls on a sample repo.
- [x] Session written in `-p` mode opens in TS pi; a TS-pi session resumes with `pi -c`. (F6 cross-read)
- [x] Fixture families F1, F2(openai), F3, F4, F5, F6, F9 green; `make fixtures` regeneration is clean.
- [x] Cold start < 50 ms (hyperfine, warm cache); binary < 25 MB.
- [x] Dogfood: ≥ 1 real pi-go WP executed using pi-go itself; transcript committed.
- [x] Trim pass #1 done (checklist below), report committed.

## M2 — Headless parity (closes Phases 2+3; verified by WP-390)

- [ ] Every upstream provider except Radius resolves in `--list-models`; F2 green for all 9 API
      shapes (openai-responses/completions, anthropic, google, vertex-or-deferred-per-G2, mistral,
      azure, bedrock, codex, pi-messages).
- [ ] OAuth: Anthropic Pro/Max, ChatGPT/Codex, Copilot flows each verified end-to-end once,
      documented; `auth.json` cross-compat fixture green.
- [ ] Upstream's RPC test suite passes against `pi-go --mode rpc`; every exclusion listed with a
      reason; F7 transcript fixtures green.
- [ ] F8, F9, F10 green — compaction picks the same boundaries as upstream on the fixture corpus.
- [ ] MCP: go-sdk example server round-trips (list/execute/stream); zero MCP work when unconfigured.
- [ ] SDK: all 13 ported examples run on faux; an external `go get` smoke module builds.
- [ ] Nightly live suite (Tier 3 below) wired into CI and running.
- [ ] Trim pass #2 done.

## M3 — TUI parity (closes Phase 4; verified by WP-470)

- [ ] F12 green: components, editor wide-char cases, markdown corpus, full-screen composites.
- [ ] Side-by-side replay vs TS pi: frame diffs reviewed; every deviation fixed or added to the
      divergence ledger (ledger delta listed in the milestone report).
- [ ] All built-in interactive commands functional; ctx.ui extension demos behave per upstream docs.
- [ ] Render < 16 ms/frame on the replay corpus; resize/paste fuzz clean under `-race`.
- [ ] Images verified on kitty + iTerm2; `/copy` works on darwin and linux.
- [ ] Trim pass #3 done.

## M4 — Extension bridge (closes Phase 5; verified by WP-560)

- [ ] F11 matrix published; ≥ 80% of upstream single-file examples run **unmodified** with their
      documented behavior; every "unsupported" maps to a ledger line or a written WP proposal.
- [ ] hello, todo, pirate, permission-gate, status-line, modal-editor run unmodified end-to-end.
- [ ] Node-shim coverage table committed; VM bridge calls < 8 ms on the corpus; `/reload` works;
      TS errors map to source lines.
- [ ] Trim pass #4 done.

## M5 — v1.0 release (closes Phase 6; verified by WP-650 + WP-661)

- [ ] All M1–M4 criteria re-verified at the release commit, fixtures regenerated at current UPSTREAM.lock.
- [ ] One full sync cycle executed against a fresher upstream commit (≤ 30 days old) with a green
      lock bump — proves the sync machinery, not just the snapshot.
- [ ] goreleaser artifacts for all 4 targets; install script verified on clean linux + macOS VMs.
- [ ] Cold start < 50 ms; binary ≤ 35 MB (with bridge); numbers recorded in release notes.
- [ ] Nightly live suite ≥ 90% pass over the trailing 7 days.
- [ ] Docs newcomer path (install → first session → embed SDK → run an upstream extension) verified
      by following the docs literally; README credit/provenance; divergence ledger current.
- [ ] Final trim pass #5; LOC report: mirrored packages ≤ 1.3× upstream TS src LOC or justified
      per-package in the report; dep audit clean.

## Live-test policy

- **Tier 1 — every merge:** no network. Fixtures and unit tests only.
- **Tier 2 — provider WPs:** opt-in (`PI_GO_LIVE_TESTS=1`): one real streamed tool-call round-trip
  per provider, run before merging that provider's WP and on demand.
- **Tier 3 — nightly (from M2):** CI workflow, secrets from repo settings, cheap models, spend cap.
  Corpus: 3 scripted tasks (multi-turn read+edit+bash; parallel tool calls; compaction-length
  session) × {OpenAI, Anthropic}. Failures file work items and never block merges; only the M5
  7-day window blocks a release. Never record live outputs into fixtures.

## Trim pass (the recurring slimming WP: WP-180/390/470/560/650)

Slimness is a standing product goal: the fat accumulates WP by WP, so it is burned off at every
milestone. A trim pass is a dedicated WP whose deliverable is a **shrink diff** plus
`docs/trim/M<n>.md` reporting:

1. **Dead code** — staticcheck/unused + deadcode findings deleted (not suppressed).
2. **Dependency audit** — `go mod graph` vs ARCHITECTURE §8; deps no longer pulling their weight
   removed; new transitive bloat flagged.
3. **Duplication sweep** — near-duplicate helpers merged.
4. **Abstraction audit** — interfaces/indirection not on the upstream-seam list inlined.
5. **LOC report** — per mirrored package vs upstream TS src (budget ≤ 1.3×); overshoot shrunk or
   justified in the report.
6. **Size/speed trend** — binary size and cold start recorded; > 10% regression investigated.
7. **Milestone verification** — every criterion of the current milestone checked and reported.

Iron rule: **a trim never changes behavior** — all fixtures stay green; a trim that breaks one is
reverted, not adapted around.
