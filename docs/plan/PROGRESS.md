# Implementation progress

The active sequence is `SPRINTS.md`; the old work-package numbers are historical spec references
only. Progress is measured by conformance surfaces moving from red to green and by milestone
criteria closing.

## Sprint 0 — Consolidate

Status: **in progress**.

- [x] Record the owner-directed trunk, fixtures-first, core-first plan as D25/D26.
- [ ] Integrate every GitButler lane and side ref into `main`, reconciling overlapping implementations.
- [ ] Verify `CGO_ENABLED=0 go build ./...` and `make test` at every integrated commit.
- [ ] Delete merged side refs and leave the GitButler workspace entirely.
- [ ] Finish on plain `main` with no worktree, lane, or feature branch.

Historical note: `2a8ac08` and `68c3afa` were intermediate snapshots that did not build by
themselves; their corrected descendants are already represented in the consolidated history and
they are not valid integration points.

Expansion work already landed before D26, including Codex/Copilot/xAI and related fixtures, is kept
but will not be extended until Sprint 3.

## Sprint 1 — Core headless correct (M2)

Status: **pending Sprint 0**.

- [ ] Land the RED F7 RPC transcript/upstream-suite adapter and F8 resource goldens first.
- [ ] Turn the upstream RPC suite and F7/F8/F9/F10 green without exclusions lacking a written reason.
- [ ] Keep F2 green for all landed core API shapes and `auth.json` cross-compatible.
- [ ] Port harness `SessionRepo`/`FileSystem`, JSONL/memory repositories, and rehydrate-from-bytes.
- [ ] Make skills, prompt templates, extension seams, and all 13 SDK examples conformant.
- [ ] Build an external `go get` SDK smoke module and wire the nightly live suite.
- [ ] Publish `docs/compare/sprint-1.md`, complete trim pass #2, and check every M2 criterion.

## Sprint 2 — TUI complete (M3)

Status: **pending**.

- [ ] Land the RED F12 component/editor/markdown/composite frame corpus first.
- [ ] Reach byte-reviewed frame parity, complete commands, image/clipboard checks, fuzz, and frame budget.
- [ ] Publish `docs/compare/sprint-2.md`, complete trim pass #3, and check every M3 criterion.

## Sprint 3 — Expansion (M4)

Status: **pending**.

- [ ] Publish `docs/plan/expansion-study.md` for owner review before extending breadth.
- [ ] Land the RED provider, OAuth, MCP, package/trust, and F11 extension-matrix surfaces first.
- [ ] Complete the full-parity defaults unless the owner records an amended decision.
- [ ] Publish `docs/compare/sprint-3.md`, complete trim pass #4, and check every M4 criterion.

## Sprint 4 — Ship (M5)

Status: **pending**.

- [ ] Re-verify M1–M4, execute a green upstream sync, and build/verify all release artifacts.
- [ ] Complete the 72-hour live-suite window, newcomer docs walk-through, final size/LOC/dep audit.
- [ ] Complete trim pass #5 and tag `v0.1.0` only when every M5 criterion is checked.

## Owner-blocked evidence

- Anthropic Pro/Max end-to-end OAuth requires an interactive subscribed account.
- Tier-2/Tier-3 provider live tests require repository/API credentials and CI secrets.
- Off-machine clean macOS/Linux release validation and the 72-hour burn-in require owner-provided
  hosts/remotes; all local and fixture work continues independently.
