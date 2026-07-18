# WP-420 — Editor, Input, autocomplete, lists: completion report

WP-420's automated surface is complete. The port now reproduces Node v24.15.0's ICU 78.2 dictionary-based Chinese and Japanese word boundaries in pure Go, so the four previously red F12 deletion and navigation assertions pass unchanged alongside twelve focused dictionary-rule regressions and the broad every-UTF-16-offset Sprint-2 corpus. No compatibility path for legacy ICU data was added.

Upstream was read at the pinned commit `3da591ab74ab9ab407e72ed882600b2c851fae21` (v0.80.10), including `packages/tui/src/components/{editor,input,select-list,settings-list}.ts`, `autocomplete.ts`, `fuzzy.ts`, `kill-ring.ts`, `undo-stack.ts`, `word-navigation.ts`, the relevant `utils.ts` segmenting and column-slicing code, and their upstream tests.

## Implemented scope

- `Editor` covers multiline editing, visual wrapping and sticky-column movement, history and draft restore, fish-style undo coalescing, kill-ring accumulation/yank-pop, word movement and deletion, bracketed-paste collapse and deterministic expansion, character jump, scrolling, and the upstream newline key variants.
- `Input` covers single-line editing, horizontal scrolling, grapheme movement and deletion, undo, kill-ring behavior, and bracketed-paste normalization.
- The autocomplete layer covers stacked slash-command, command-argument, `@` fuzzy-file, and path completion providers. Trigger, gate, and apply callbacks run outside the editor lock so synchronous re-entry is safe; debounce, cancellation, stale-result checks, forced completion, and auto-apply follow upstream.
- `SelectList` and `SettingsList` cover navigation, scroll windows, filtering, value cycling, submenu callbacks, layout, and upstream empty-filter quirks.
- Shared ports include fuzzy scoring over JavaScript UTF-16 units, JavaScript-style expanding lowercase, locale collation through the already-approved `x/text`, exact pinned Unicode property membership, JavaScript whitespace including FEFF, word wrapping, ANSI column slicing, ICU CJK dictionary segmentation, kill-ring state, and undo state.

No Go module dependency was added. Public cursor and word-navigation indices use upstream UTF-16 units; internal rune indices are only a storage detail. This is exercised by the astral vertical-snap fixture, so a single astral code point no longer hides an upstream column difference.

## ICU 78.2 CJK segmentation

`internal/cjksegment` embeds the official 2,007,296-byte `cjdict.dict` from Unicode ICU tag `release-78.2`, commit `f1b3db8ecd39d5b3a6eff4d5641b176c7f914dfb`. The asset SHA-256 is `5b96312a434f4ca3df1f5fa906e88d52fe2e28e3b87c68b9e62d0d77e1995edc`; its exact source path, generator, and redistribution notices are recorded beside it in `data/PROVENANCE.md` and the unmodified ICU license.

The implementation is the narrow read-only subset needed by this asset: the big-endian ICU `Dict` v1 container, UCharsTrie 1.0 traversal, NFKC normalization with original-boundary mapping, and ICU's strict-cost dynamic program. It preserves the 20-UTF-16-unit dictionary limit, fallback cost 255, Katakana cost table and run limit, tie behavior, supplementary code points, compatibility ideographs, the exact Unicode 17 Han/Hiragana/Katakana set used by Node, and ICU's contextual rule status across dictionary breaks, Katakana chains, and trailing Extend/Format characters. The data remains embedded bytes rather than an expanded heap map.

## F12 conformance

`conformance/extract/f12-components.ts` drives the pinned TypeScript implementations and is called by the existing F12 generator. It produces 348 generated cases: 25 editor sessions, 10 input sessions, 6 select-list sessions, 4 settings-list sessions, 15 word-wrap cases, 17 fuzzy matches, 5 fuzzy filters, and 266 word-navigation cases. The manifest records every upstream source, and the Go replay lives in `conformance/runner/f12_components_test.go`.

The corpus includes wide Korean, Japanese, Chinese, and fullwidth Input renders; emoji and CJK wrapping; astral vertical cursor snapping; UTF-16 fuzzy offsets; expanding lowercase; autocomplete sessions; paste-marker atomicity; exact dictionary-CJK deletion and navigation; standalone CJK non-word status; contextual status inherited by dictionary breaks; halfwidth and combining Extend behavior; and U+309B/U+309C word joining. `make fixtures-check` is green, proving the checked-in goldens regenerate byte-for-byte from the pinned upstream. The four formerly red observations and twelve added edge cases are normal green assertions, not a divergence list.

## Keybinding verification

| Behavior | Binding coverage | Evidence |
|---|---|---|
| Submit and newline | Enter submits; kitty/xterm Shift+Enter, Ctrl+J, Alt+Enter, and the backslash+Enter workaround insert a newline | `TestEditorNewlineKeyMatrix`, `TestEditorBackslashEnter`, `TestEditorPasteMarkerExpansion` |
| Word editing | Ctrl+W and Alt+Backspace delete backward; Alt+D and Alt+Delete delete forward; Ctrl+Left/Right move by word | `TestEditorDeleteWordMatrix`, `TestEditorKillRingAltD`, `TestEditorAltDeleteWordForward`, `TestEditorWordNavigationKeys` |
| Line and page movement | Ctrl+A/E move to line bounds; arrows preserve a sticky visual column; PageUp/PageDown move by the upstream page amount | `TestEditorUnicodeEditing`, `TestEditorStickyColumn`, `TestEditorPageMovement` |
| Kill ring and undo | Ctrl+K/U/W and Alt+D accumulate kills; Ctrl+Y/Alt+Y yank and cycle; Ctrl+- undoes with upstream coalescing | `TestEditorKillRing`, `TestEditorKillRingAccumulation`, `TestEditorKillRingCycling`, `TestEditorUndo` |
| Character jump and history | Ctrl+] and Ctrl+Alt+] jump forward/backward; Up/Down navigate history and restore the draft | `TestEditorCharacterJump`, `TestEditorHistoryNavigation`, `TestEditorHistoryDraftRestore`, `TestEditorHistoryRules` |
| Autocomplete | Tab forces or accepts completion; typed trigger characters debounce; stale requests cancel; callbacks may re-enter | `TestEditorAutocomplete*` and the F12 autocomplete editor sessions |
| Grapheme and paste behavior | Arrows, Backspace, and Delete stay on grapheme boundaries; large bracketed pastes are atomic and expand in insertion order | `TestEditorUnicodeEditing`, `TestEditorPasteMarkers`, `TestEditorPasteExpansionUsesInsertionOrder`, `TestInputGraphemeMovement` |

## Verification result

The closing CJK implementation is green under the product's pure-Go constraint:

- `CGO_ENABLED=0 go test ./internal/cjksegment ./tui`
- `CGO_ENABLED=0 go test ./conformance/runner -run 'TestF12(InputSessions|WordNavigation)$' -count=10`
- `CGO_ENABLED=0 go vet ./internal/cjksegment ./tui`
- exact dictionary size/hash parsing, Unicode-set hash, representative Chinese/Japanese/Katakana/NFKC/supplementary cases, all four formerly red F12 assertions, and twelve added TS-derived boundary and status cases

The stock Go race detector cannot run with `CGO_ENABLED=0` (`go: -race requires cgo`), so this closing check makes no race claim. The previously pending WP-410 interactive smoke on iTerm2, kitty, and gnome-terminal is unchanged and is not claimed by this report.
