# Phase 4 — TUI

Lane C: faithful pi-tui port (D15). Spec: `packages/tui/docs/tui.md` (942 lines) + `packages/tui/src/`.
Component contract `Render(width) []string` is sacred — extensions and F12 goldens depend on it.

## WP-410 — tui core

**Upstream refs:** `packages/tui/src/{tui,terminal,keybindings}.ts`; CURSOR_MARKER convention.

**Scope:** terminal abstraction on x/term (raw mode, resize, restore-on-panic), differential
line-based renderer (only changed lines repaint; scrollback-friendly), Component/Focusable
interfaces, focus management, hardware-cursor/IME positioning via zero-width cursor marker,
configurable keybindings engine (+ `keybindings.json`), kitty keyboard protocol (incl. key-release)
with graceful fallback, Box/Container/Text/TruncatedText/Spacer/Loader primitives.

**Acceptance:** F12 goldens for primitives; interactive smoke on iTerm2/kitty/gnome-terminal
documented; no flicker on a 10k-line scripted session replay (perf note in PR).

## WP-420 — Editor, Input, autocomplete, lists

**Upstream refs:** `packages/tui/src/{editor,input,autocomplete,fuzzy,select-list,settings-list}.ts`.

**Scope:** multi-line Editor (undo stack, kill-ring, word navigation, paste collapse, East-Asian
width via uniseg), Input, autocomplete engine + fuzzy matcher + provider stacking, SelectList,
SettingsList.

**Acceptance:** F12 goldens incl. wide-char/emoji editing cases extracted from upstream tui tests;
editor keybinding behavior matrix verified against upstream docs.

## WP-430 — Markdown, highlighting, themes

**Upstream refs:** `packages/tui/src/components/markdown.ts` (marked-based), theme system across tui +
coding-agent (`docs/themes.md`, settings `theme`, resources_discover theme paths).

**Scope:** goldmark-AST → ANSI renderer matching upstream's markdown presentation (headings,
lists, tables, code blocks with `markdown.codeBlockIndent`, links), chroma-based syntax
highlighting mapped to theme palette, theme loading/switching (built-ins + user themes + packages),
`/theme` data plumbing.

**Acceptance:** F12 markdown goldens over upstream's markdown test corpus; two upstream themes
render with matching palettes (spot-check screenshots in PR).

## WP-440 — Terminal images + read-tool images + clipboard

**Upstream refs:** `packages/tui/src/terminal-image.ts` (kitty + iTerm2, no sixel),
`packages/coding-agent/src/core/tools/read.ts` image path + `image-resize-worker.ts`,
settings `terminal.*`, `images.*`; optional clipboard dep.

**Scope:** kitty/iTerm2 image emission + capability detection + `terminal.showImages`/width cells;
read tool image support (jpg/png/gif/webp/bmp decode via stdlib+x/image, resize ≤2000×2000, EXIF
orientation, and upstream's quirk of retaining a successful image block beside the non-vision
"omitted" note); clipboard via OSC52 + pbcopy/xclip/wl-copy fallback (`/copy`).

**Acceptance:** image fixtures decode/resize byte-stable (golden dimensions/orientation matrix);
manual kitty + iTerm2 verification documented; `/copy` works on darwin + linux (X11/Wayland).

## WP-450 — TUI app assembly

**Upstream refs:** `packages/coding-agent/src/` TUI application layer (chat rendering, tool
renderCall/renderResult views, dialogs, status/widget/footer/header zones, working indicator),
interactive slash commands list in `docs/usage.md`, `docs/settings.md` UI keys.

**Scope:** the interactive app: chat view over session tree (streaming message rendering, thinking
block hide/show, tool call/result renderers incl. per-tool custom renderers + diff views), editor
integration (slash/path autocomplete, `@file` attach, `!` user-bash, double-escape action), dialog
system (select/confirm/input/editor with timeout+signal), ctx.ui backing implementation for
extensions (status/widget/footer/header/title, notify, working indicator, editor access,
autocomplete providers), built-in interactive commands (`/login /logout /model /scoped-models
/settings /resume /new /name /session /tree /trust /fork /clone /compact /copy /export /import
/reload /hotkeys /changelog /quit`, `/share`→local export), changelog display, quiet startup,
`terminal.clearOnShrink`, `hideThinkingBlock`, `showCacheMissNotices` and remaining UI settings.

**Acceptance:** side-by-side protocol: scripted faux session replayed in TS pi and pi-go, per-frame
diff reviewed and deviations either fixed or ledgered; all listed commands functional; extension
ctx.ui demos (status-line, widget-placement ports) behave per upstream docs.

## WP-460 — TUI parity pass + goldens

**Scope:** F12 expansion to full-screen composites (chat + editor + status under multiple widths),
fuzz pass (resize storms, paste floods, giant lines), perf budget check (render < 16 ms/frame on
the replay corpus), remaining `docs/tui.md` conformance sweep, ledger update for any accepted gaps
(e.g. darwin modifier addon).

**Acceptance:** F12 suite green and adopted into `make fixtures`; fuzz run clean under `-race`;
gate report `docs/plan/tui-gate-report.md`.
