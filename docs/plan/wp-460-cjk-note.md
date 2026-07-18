# Sprint 2 — ICU 78.2 CJK word-boundary parity

This note carries the scratch checkout's broad CJK F12 corpus into the
validated pure-Go implementation on plain `main`; it does not claim the full
Sprint-2 TUI milestone.

Pinned TS pi delegates word movement and deletion to
`Intl.Segmenter(undefined, { granularity: "word" })`. Node 24.15.0 uses ICU
78.2, whose root word rules pass Han, Hiragana, and Katakana ranges to
`CjkBreakEngine`. `internal/cjksegment` reads ICU 78.2's official compiled
`cjdict.dict` as a UCharsTrie and applies the same 20-UTF-16-unit dictionary
limit, cost-255 fallback, Katakana costs, NFKC normalization, and original
boundary mapping. The public helpers preserve JavaScript UTF-16 offsets,
including the pinned quirk at a cursor that splits a surrogate pair.

The embedded dictionary is 2,007,296 bytes and is copied byte-for-byte from
Unicode ICU tag `release-78.2`; its SHA-256 is
`5b96312a434f4ca3df1f5fa906e88d52fe2e28e3b87c68b9e62d0d77e1995edc`.
`internal/cjksegment/data/PROVENANCE.md` records exact source URLs and hashes,
and the unmodified ICU license is stored beside the asset. No Go module was
added.

F12 now contains 266 generated word-navigation cases. It retains main's 42
focused boundary and rule-status witnesses and adds the scratch checkout's
broad every-UTF-16-offset corpus for Chinese ambiguity, Japanese mixed scripts,
Katakana, halfwidth NFKC forms, combining marks, ZWJ and variation selectors,
supplementary Han through Unicode 17 Extension J, and mixed ASCII/number
boundaries. Regeneration exposed one missing Go behavior: when a backward move
starts between the two UTF-16 units of a supplementary code point, TS first
lands at that code point's leading surrogate. The wrapper now matches that
result without changing normal rune-boundary movement.

Current evidence is the deterministic pinned F12 regeneration and the green
`CGO_ENABLED=0 go test ./tui ./conformance/runner ./codingagent/modes -count=1`
integration run. The core overlay, terminal-color, primitive-composite, and
stress evidence is recorded in `tui-gate-report.md`; M3 remains open for the
application-level replay and manual terminal gates.
