# Sprint 3 comparison — expansion

Status: **GREEN for deterministic Sprint 3 parity**. The expansion ring is complete on full-parity
defaults: every upstream provider except ledgered Radius, the codex shape, all four OAuth flows in
code, MCP, packages and trust, and the JS extension bridge running 61 of 69 upstream single-file
examples unmodified (88%, against the 80% criterion) with the bridge wired into the real product.
Live OAuth end-to-end runs and provider live smoke remain owner-blocked evidence.

## Revisions and method

- pi-go base: `7b86eda` (Sprint 2 closure); candidate: the Sprint 3 closure containing this report.
- upstream: pi `0.80.10`, `3da591ab74ab9ab407e72ed882600b2c851fae21`.
- Bridge conformance executes the verbatim upstream example corpus (copied byte-identical by the
  extractor) inside the sobek VM against scripted UI seams; provider shapes are byte-compared by
  the F2 golden families; `make fixtures-check` regenerates the tree byte-clean.

## Expansion surface comparison

| Surface | Result and evidence |
|---|---|
| Compat provider family | **GREEN.** 35 of 36 upstream providers registered in upstream order (Radius ledgered); 11 custom-code providers; `--list-models` resolves the full catalog (WP250 goldens). |
| Codex shape | **GREEN.** F2 codex request/stream/websocket goldens; per-platform user-agent. |
| OAuth (Anthropic, ChatGPT/Codex, Copilot, xAI) | **Code GREEN, live OWNER-BLOCKED.** All four flows ported with tests, CLI login wired, `auth.json` cross-compat green; subscribed-account end-to-end runs need owner credentials. |
| openrouter-images | **GREEN.** Last unported API shape landed: request-byte goldens, modality handling, data-URL decoding, dispatch entry point. |
| MCP | **GREEN.** stdio + Streamable HTTP round-trips, trust gating, `/mcp` commands. (Upstream has no MCP source — this is the D18 addition; no upstream divergence possible.) |
| Packages + trust | **GREEN.** npm:/git: install/update/list/remove, project trust, WP360 fixtures. |
| JS bridge — matrix | **GREEN.** 61/69 examples run unmodified (`docs/sync/extension-matrix.md`); the 8 unsupported each name their missing surface (JS-exported tool factories, embeddable interactive tui classes) and none is among the named criteria set. |
| JS bridge — named six | **GREEN.** hello, todo, pirate, permission-gate, status-line, modal-editor end-to-end. |
| JS bridge — mechanics | **GREEN.** Node-shim table (`docs/sync/node-shims.md`), TS errors mapped to source lines, `/reload` replaces per-path VMs, bridge call latency p90 137 µs against the 8 ms budget (guarded by test). |
| Product wiring | **GREEN.** Settings/project paths plus `--extension`/`-e` load through the bridge into the shared registry; real-binary smoke shows a TypeScript extension loading before the credential error. |
| SDK alignment closures | **GREEN.** Tool bundles, public ai model helpers (duplicates deleted), typed `settings.httpProxy` with environment precedence. |

## Deviations and ledger delta

**Ledger delta for Sprint 3: one entry** — the bundled llama.cpp extension is recorded as excluded
(shipped at the pin, deleted upstream immediately after; porting would be dead-on-arrival). The
eight unsupported matrix examples are documented per-row in the matrix, not ledgered: they are
bridge-surface roadmap, not behavioral divergence. Gate G3 is resolved as bridge-now in
DECISIONS.md. No wire-format deviation was found; F2/F11 fixtures stayed byte-clean throughout.

## Verification

```text
make check                                       # build, vet, golangci-lint 0 issues, full race suite
make fixtures-check                              # byte-clean regeneration at 3da591ab
make upstream-rpc-tests                          # 27/27
go test ./codingagent/extensions/jsbridge/ -run TestBridgeCallBudget -v
real-binary smoke: pi --extension probe.ts -p    # extension loads, then expected credential error
```

Trim pass #4 and the size/cold-start trend are reported in `docs/trim/M4.md` — note the binary and
cold-start consequences of linking the bridge are material and carried there as release risks.

## M4 disposition

| Criterion | Status |
|---|---|
| Expansion study committed and surfaced | **GREEN** — owner decisions listed in PROGRESS.md |
| Every provider except Radius resolves; codex shape green; OAuth verified once | **Code GREEN; live runs OWNER-BLOCKED** (subscribed accounts) |
| MCP round-trips; zero work unconfigured | **GREEN** |
| Packages install/update/list + trust | **GREEN** |
| F11 matrix ≥80% unmodified, published | **GREEN** — 61/69 (88%) |
| Named six end-to-end | **GREEN** |
| Node-shim table; <8 ms bridge calls; /reload; TS error mapping | **GREEN** — p90 137 µs |
| Trim pass #4 | `docs/trim/M4.md` |
