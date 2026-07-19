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

**3. Binary size needs an owner decision before M5.** The stripped binary is already 102,882 bytes
over the decimal 35 MB cap **before** `cmd/pi` links sobek and esbuild; the bridge will push it
several MB further. Options: (a) raise the M5 cap and record the decision, (b) fund a size
workstream (section-level audit, table compaction, build-tag split), (c) ship the bridge as the
default build and accept the number the measurement gives, recording it honestly. **Recommendation:
(a) + a bounded (b):** cap at 45 MB decimal, one size pass in Sprint 4's trim. The 50 ms cold-start
cap stays.

**4. Providers/MCP/packages need no owner input** — they are done to full-parity defaults already;
only live smoke (CI secrets) and subscribed-account OAuth runs are open, and those are on the
owner-blocked list, not scope decisions.

## Recommended cut (defaults I am proceeding on)

1. WP-541 `ctx.ui` bridge → WP-542 custom components/overlays (G3: sobek marshaling of component
   callbacks) → WP-550 matrix sweep to ≥80% with `docs/sync/extension-matrix.md`, fixtures first.
2. Port openrouter-images.
3. Treat binary size as a tracked constraint with the M5 cap decision above pending owner sign-off;
   everything else in M4 unchanged.

Amend via DECISIONS.md; silence = defaults stand (D25/D26 protocol).
