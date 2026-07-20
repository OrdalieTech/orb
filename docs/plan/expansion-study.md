# Expansion study — Sprint 3 decision memo (owner review)

Status: **surfaced for owner review; proceeding on full-parity defaults meanwhile** (SPRINTS.md
Sprint 3 rule: bridge work continues during review; an owner amendment lands in DECISIONS.md).

## Where expansion actually stands

Audit of the integrated-but-frozen expansion ring (Sprint 0 consolidation, D26 freeze) found it
already ~90% built and green:

| Area | State |
|---|---|
| Compat provider family | **Done.** 35 of 36 upstream providers registered in upstream order (Radius ledgered out); 11 custom-code providers; all 11 chat API shapes including codex (requests/streams/websocket F2 goldens green); `--list-models` resolves the full catalog. |
| OAuth | **Done in code.** Anthropic PKCE, ChatGPT/Codex, Copilot device-code, xAI — all four flows ported with tests, CLI wired, auth.json cross-compat green. Only live subscribed-account runs remain (owner-blocked). |
| MCP | **Done.** stdio + Streamable HTTP, settings surface, trust gating, `/mcp` commands, lifecycle green. (Note: upstream pi has no MCP — this is the D18 addition, so there is no upstream to diverge from.) |
| Packages + trust | **Done.** npm:/git: install/update/list/remove, project trust, WP360 fixtures green. |
| JS bridge core | **Done.** sobek VM per extension, esbuild + TS source maps, full non-UI `pi.*` API, node shims incl. fetch/child_process (`docs/sync/node-shims.md`), `/reload`. 15 non-UI upstream examples run unmodified. |

## What genuinely remains, and the decisions in it

**1. The `ctx.ui` bridge is the whole game (WP-541 + WP-542/G3).** The in-VM `ui` object exposes
only `notify`. 53 of upstream's 69 single-file extension examples call `ctx.ui` — dialogs, editor
access, widgets/status/footer/header, custom components and overlays. The ≥80% F11 matrix target is
arithmetically unreachable without it (~22% runnable today). The Go-side seams all exist (the
native extension UI host landed in Sprints 1–2 and is byte-tested); the work is marshaling the JS
surface onto them. **Recommendation: build it in full — it is the only path to the M4 criterion
and there is no meaningful partial cut** (a dialogs-only cut would strand widget/custom examples
and still leave the matrix short of 80%).

**2. openrouter-images (image *generation* client).** Typed but not ported. Small, self-contained,
upstream-spec'd. **Recommendation: port it** (full-parity default); ledger it instead only if you
want zero image-generation surface.

**3. Binary size decision — resolved by the owner on 2026-07-20.** Measured after the bridge wiring
landed: the stripped binary with sobek and esbuild linked is **51,376,290 bytes (49.0 MiB)** against
the former 35 MB M5 cap (it was 35.1 MB before the bridge — the bridge itself costs ~16.3 MB,
dominated by the embedded esbuild toolchain and the sobek runtime). Options: (a) raise the M5 cap
and record the decision, (b) fund a size workstream (build-tag split shipping a bridge-less
variant, esbuild-external mode, section audit), (c) accept the measured number. **Recommendation:
cap at 55 MB decimal + one bounded size pass in Sprint 4's trim examining an optional bridge-less
build target.** The owner adopted that recommendation; D17 and the 50 ms cold-start cap stay
unchanged.

**4. Providers/MCP/packages need no owner input** — they are done to full-parity defaults already;
only live smoke (CI secrets) and subscribed-account OAuth runs are open, and those are on the
owner-blocked list, not scope decisions.

## Recommended cut (defaults I am proceeding on)

1. WP-541 `ctx.ui` bridge → WP-542 custom components/overlays (G3: sobek marshaling of component
   callbacks) → WP-550 matrix sweep to ≥80% with `docs/sync/extension-matrix.md`, fixtures first.
2. Port openrouter-images.
3. Treat binary size as a tracked constraint under the owner-approved 55 MB decimal M5 cap;
   everything else in M4 remains unchanged.

The owner confirmed these defaults on 2026-07-20; the resulting release rule is recorded in
DECISIONS.md and RELEASE-CRITERIA.md.
