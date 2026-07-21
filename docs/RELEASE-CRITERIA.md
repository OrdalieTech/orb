# Success criteria — milestones M1–M5

For implementing agents: these criteria are binding. A milestone WP is **done when every box below
checks, and not before** — keep working until they pass or a criterion is genuinely impossible, in
which case stop and surface it; never silently weaken a criterion, skip a fixture, or hand-edit a
golden to get green. (Deferred *decision* gates G1–G4 live in DECISIONS.md — different thing.)

## Standing criteria (every merge, no exceptions)

- [x] AGENTS.md definition of done: `CGO_ENABLED=0` cross-build (linux+darwin × amd64+arm64),
      `go vet` + golangci-lint clean, `go test -race ./...` green including fixtures, MIRROR.md
      updated. Per D7, CGo is permitted only inside development race-test binaries.
- [x] No dependency outside ARCHITECTURE §8's table.
- [x] No fixture golden weakened, regenerated-away, or hand-edited to pass.
- [x] No single-implementation interface unless it's an upstream seam (Operations, env, credential store).

## M1 — Skeleton (closes Phase 1; verified by WP-180)

- [x] `pigo -p "<task>"` completes a real OpenAI round-trip with tool calls on a sample repo.
- [x] Session written in `-p` mode opens in TS pi; a TS-pi session resumes with `pigo -c`. (F6 cross-read)
- [x] Fixture families F1, F2(openai), F3, F4, F5, F6, F9 green; `make fixtures` regeneration is clean.
- [x] Cold start < 50 ms (hyperfine, warm cache); binary < 25 MB.
- [x] Dogfood: ≥ 1 real pigo WP executed using pigo itself; transcript committed.
- [x] Trim pass #1 done (checklist below), report committed.

## M2 — Headless parity (closes Sprint 1)

- [x] F2 green for the landed API shapes (openai-responses/completions, anthropic, google, vertex,
      mistral, azure, bedrock, pi-messages). Codex shape, compat family, and further providers are
      Sprint-3 expansion (D26).
- [ ] OAuth: Anthropic Pro/Max verified end-to-end once, documented; `auth.json` cross-compat
      fixture green. (ChatGPT/Codex, Copilot, xAI OAuth: Sprint-3 expansion.)
- [x] Harness `SessionRepo`/`FileSystem` parity landed (upstream harness types, jsonl-repo,
      memory-repo, rehydrate-from-bytes) and wired into SessionRuntime.
- [x] Upstream's RPC test suite passes against `pigo --mode rpc`; every exclusion listed with a
      reason; F7 transcript fixtures green.
- [x] F8, F9, F10 green — compaction picks the same boundaries as upstream on the fixture corpus.
- [x] SDK: all 13 ported examples run on faux; an external `go get` smoke module builds.
- [ ] Nightly live suite (Tier 3 below) wired into CI and running.
- [x] Trim pass #2 done.

## M3 — TUI parity (closes Sprint 2)

- [x] F12 green: components, editor wide-char cases, markdown corpus, full-screen composites.
- [x] Side-by-side replay vs TS pi: frame diffs reviewed; every deviation fixed or added to the
      divergence ledger (ledger delta listed in the milestone report — zero for Sprint 2).
- [x] All built-in interactive commands functional; ctx.ui extension demos behave per upstream docs.
- [x] Render < 16 ms/frame on the replay corpus; resize/paste fuzz clean under `-race`.
- [ ] Images verified on kitty + iTerm2; `/copy` works on darwin and linux. (Encodings, capability
      profiles, and clipboard command paths byte-tested; real terminal/desktop smoke owner-blocked.)
- [x] Trim pass #3 done.

## M4 — Expansion: providers, MCP, packages, extension bridge (closes Sprint 3)

- [x] Expansion study (`docs/plan/expansion-study.md`) committed and surfaced to the owner; scope
      below stands as full-parity default unless the owner amends DECISIONS.
- [ ] Every upstream provider except Radius resolves in `--list-models`; F2 green for the codex
      shape; ChatGPT/Codex, Copilot, xAI OAuth flows verified end-to-end once, documented.
      (Providers and codex shape green; OAuth flows code-complete with tests — live end-to-end
      runs owner-blocked on subscribed accounts.)
- [x] MCP: go-sdk example server round-trips (list/execute/stream); zero MCP work when unconfigured.
- [x] pi packages (npm:/git:) install/update/list + project trust work as upstream.
- [x] F11 matrix published; ≥ 80% of upstream single-file examples run **unmodified** with their
      documented behavior; every "unsupported" maps to a ledger line or a written WP proposal.
      (61/69 = 88%; per-row missing-surface notes in docs/sync/extension-matrix.md.)
- [x] hello, todo, pirate, permission-gate, status-line, modal-editor run unmodified end-to-end.
- [x] Node-shim coverage table committed; VM bridge calls < 8 ms on the corpus (p90 137 µs,
      test-guarded); `/reload` works; TS errors map to source lines.
- [x] Trim pass #4 done.

## M5 — v0.1.0 release (closes Sprint 4)

- [ ] All M1–M4 criteria re-verified at the release commit, fixtures regenerated at current
      UPSTREAM.lock. (The deterministic surfaces are green, but the subscribed OAuth, hosted
      nightly, and real-terminal checks in M2–M4 remain owner-blocked.)
- [x] One full sync cycle executed against a fresher upstream commit (≤ 30 days old) with a green
      lock bump — proves the sync machinery, not just the snapshot. (`3a40794e`, 2026-07-20: 116
      changed paths classified, zero unmapped paths, fixtures regenerated, full race suite green.)
- [ ] goreleaser artifacts for all 4 targets; install script verified on clean linux + macOS VMs.
      (All four current snapshot artifacts built and checksum-verified; the install script was
      exercised as an unprivileged user in a network-disabled clean Linux container with both
      checksum-tool paths. A real macOS VM remains owner-blocked.)
- [x] Cold start < 50 ms; every bridged release binary ≤ 55 MB decimal; numbers recorded in release
      notes. (Owner-amended 2026-07-20 without changing D17. The Go 1.26.5 candidate measures
      42.1 ± 0.9 ms on one CPU; the largest current artifact is darwin/amd64 at 52,240,976 B.)
- [ ] Nightly live suite ≥ 90% pass over the trailing 72 hours. (Owner-blocked: CI secrets and
      authorized hosted runs.)
- [ ] Docs newcomer path (install → first session → embed SDK → run an upstream extension) verified
      by following the docs literally; README credit/provenance; divergence ledger current.
      (SDK-embed and extension steps verified offline; install step needs a published release.)
- [ ] Upstream alignment audit re-run at the release commit with zero open should-fix findings
      (docs/compare/upstream-alignment.md); release notes extracted from CHANGELOG.md; version
      injected via ldflags and printed with the upstream pin. (The current candidate is zero-open,
      maps 436/436 files, has extractable `0.1.0` notes, and prints both identities; repeat the
      delta audit at the eventual tag commit after the owner-gated runs.)
- [x] Final trim pass #5; LOC report: mirrored packages ≤ 1.3× upstream TS src LOC or justified
      per-package in the report; dep audit clean. (Current candidate: 1.124x, 19 reviewed clone
      groups, modules verified and tidy; repeat if production code changes before the tag.)

## Live-test policy

- **Tier 1 — every merge:** no network. Fixtures and unit tests only.
- **Tier 2 — provider WPs:** opt-in (`PIGO_LIVE_TESTS=1`): one real streamed tool-call round-trip
  per provider, run before merging that provider's WP and on demand.
- **Tier 3 — nightly (from M2):** CI workflow, secrets from repo settings, cheap models, spend cap.
  Corpus: 3 scripted tasks (multi-turn read+edit+bash; parallel tool calls; compaction-length
  session) × {OpenAI, Anthropic}. Failures file work items and never block merges; only the M5
  72-hour window blocks a release. Never record live outputs into fixtures.

## Trim pass (closes every sprint)

Slimness is a standing product goal: the fat accumulates as work lands, so it is burned off at
every sprint close. The deliverable is a **shrink diff** plus `docs/trim/M<n>.md` reporting:

1. **Dead code** — staticcheck/unused + deadcode findings deleted (not suppressed).
2. **Dependency audit** — `go mod graph` vs ARCHITECTURE §8; deps no longer pulling their weight
   removed; new transitive bloat flagged.
3. **Duplication sweep** — near-duplicate helpers merged.
4. **Abstraction audit** — interfaces/indirection not on the upstream-seam list inlined.
5. **LOC report** — per mirrored package vs upstream TS src (budget ≤ 1.3×); overshoot shrunk or
   justified in the report.
6. **Size/speed trend** — binary size and cold start recorded; > 10% regression investigated.
7. **Milestone verification** — every criterion of the current milestone checked and reported.
8. **Upstream alignment** — the six-dimension alignment audit (docs/compare/upstream-alignment.md)
   re-run; new should-fix findings become work items before the sprint closes.

Iron rule: **a trim never changes behavior** — all fixtures stay green; a trim that breaks one is
reverted, not adapted around.
