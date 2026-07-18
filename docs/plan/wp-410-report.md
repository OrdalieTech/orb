# WP-410 — TUI core

WP-410 ports the terminal, input, differential-rendering, keybinding, width, focus, cursor-marker, and primitive-component surfaces from the pinned upstream commit `3da591ab74ab9ab407e72ed882600b2c851fae21`. Terminal images and overlays remain in WP-440 and WP-450, so this package contains only the seams those packages need.

The implementation and automated checks are complete, but WP-410 acceptance remains pending until the interactive smoke is run in actual iTerm2, kitty, and gnome-terminal processes. The headless PTY checks below exercise each terminal's environment signature and terminal-code paths; they cannot establish the required real-terminal behavior.

## Acceptance evidence

F12 now generates 14 primitive render cases by invoking upstream's real `Text`, `TruncatedText`, `Spacer`, `Container`, `Box`, and `Loader` classes. `make fixtures-check` regenerates those goldens without a diff, and the Go runner compares the exact line arrays rather than normalized output.

The headless proxy smoke uses a real Linux PTY, raw-mode transitions, input delivery, fragmented Kitty negotiation, SIGWINCH delivery, and panic restoration. The host is headless and has no iTerm2, kitty, or gnome-terminal GUI process, so the three rows below exercise their environment signatures through that PTY and do not satisfy the interactive-smoke acceptance criterion.

| Profile | Environment exercised | Result |
|---|---|---|
| iTerm2 | `TERM=xterm-256color`, `TERM_PROGRAM=iTerm.app`, truecolor | PTY input, raw restore, dimensions, bracketed paste, Kitty query/disable pass |
| kitty | `TERM=xterm-kitty`, `TERM_PROGRAM=kitty`, truecolor | PTY input, raw restore, dimensions, bracketed paste, Kitty query/disable pass |
| gnome-terminal | `TERM=xterm-256color`, `TERM_PROGRAM=gnome-terminal`, truecolor | PTY input, raw restore, dimensions, bracketed paste, Kitty query/disable pass |

The remaining manual check must launch pi-go in each real terminal and verify input, bracketed paste, resize, Kitty negotiation or fallback, clean exit, and terminal restoration without flicker or leaked control sequences.

The 10,000-line replay changes only the tail line. Ten uncached measurements wrote 46 terminal bytes with zero clear-screen sequence or full redraw; latency ranged from 11.9 ms to 21.3 ms with a 14.9 ms median. That proves the renderer repaints the changed line instead of replaying the session, which is the no-flicker condition for this WP; the corpus-wide 16 ms frame budget remains the WP-460 gate.

## Verification

- `go test -race ./...`
- `go vet ./...`
- `golangci-lint run`
- `CGO_ENABLED=0 go build ./...` for linux and darwin on amd64 and arm64
- pinned fixture regeneration followed by a clean recursive diff
- `go test ./tui -run TestTUITenThousandLineReplayStaysDifferential -count=10 -v`
- `go test ./tui -run 'TestProcessTerminalPTY(TerminalProfiles|InputKittyAndResize|RestoresRawModeAfterPanic)' -v`

The Darwin native modifier addon is deliberately absent under ARCHITECTURE §4; Kitty/CSI-u and modifyOtherKeys provide the supported modifier path, while Apple Terminal Shift+Enter normalization remains testable at its pure boundary.
