# WP-440 — terminal images, read-tool images, and clipboard

WP-440 ports the pinned upstream Kitty and iTerm2 graphics protocols, image component, read-tool image pipeline, terminal/image settings reads, and clipboard fallback behavior. The Sprint 0 foundation and its automated conformance surfaces are complete. WP-440 acceptance remains open until WP-450 wires the terminal-setting consumers and `/copy`, followed by real Kitty, iTerm2, Darwin, X11, and Wayland smoke.

## Automated foundation evidence

F12 invokes upstream's real terminal-image and Image APIs for exact Kitty chunk framing, iTerm2 parameters, cell-size calculations, and component line arrays. The Go renderer queries `CSI 16 t`, consumes cell-size replies, reserves image rows before placement, leaves graphics sequences untouched by SGR resets and width checks, and deletes replaced Kitty IDs. `make fixtures-check` regenerates F12, WP440, and WP440Read fixtures from commit `3da591ab74ab9ab407e72ed882600b2c851fae21` without a diff.

The WP440 fixtures cover PNG, JPEG, and WebP resizing; GIF pass-through; a patterned Lanczos3 resize; a 64x64 size-pressure case that reaches 11x11 through repeated 0.75 reductions; the rounded-zero thin-image failure; EXIF orientations 1–8; and BMP-to-PNG with auto-resize on and off. Go compares MIME, dimensions, resize flags, and output format for the four format cases and repeats those four Go resizes for determinism. It compares every decoded RGBA byte for the patterned case, checks distinct orientation pixel signatures, and checks BMP hints and PNG magic. The GIF fixture proves upstream pass-through bytes; Go unit tests separately cover small PNG pass-through and BMP detection and conversion.

A dedicated pinned-upstream WP440Read fixture proves that both vision and non-vision models receive text plus `ImageContent`; for a non-vision model, upstream also appends the contradictory note claiming the image was omitted. The model is read from per-invocation tool context rather than captured when the tool is built. `images.autoResize` is wired into CLI and SDK read-tool construction. Existing dynamic `images.blockImages` projection tests prove mid-session setting changes take effect and session history is not mutated.

Injected clipboard tests cover `pbcopy` on Darwin, `termux-clipboard-set` on Termux, `wl-copy` on Wayland, `xclip` then `xsel` on X11, remote native-copy-plus-OSC52, and OSC52 fallback with the upstream 100,000-character base64 cap. D7 and the owner-confirmed clipboard standing assumption exclude the optional native addon. The plan names `/copy` in WP-440 acceptance and again in WP-450's command assembly; because no interactive application exists before WP-450, this package lands the clipboard service and records its command binding as the dependency gap rather than inventing a non-upstream CLI surface.

The settings manager exposes the upstream defaults and merged reads for `terminal.showImages`, `terminal.imageWidthCells`, `images.autoResize`, and `images.blockImages`. The image component accepts a width option, but WP-450 must wire `terminal.showImages` and the resolved width into the tool-result view and settings UI.

## Manual verification boundary

The host is Linux 6.8 x86_64 under SSH with `TERM=dumb`, `XDG_SESSION_TYPE=tty`, no `DISPLAY` or `WAYLAND_DISPLAY`, no Kitty or iTerm2 session, and no `pbcopy`, `wl-copy`, `xclip`, or `xsel` executable. Automated proxy checks prove emitted bytes and platform selection but cannot prove that a GUI terminal displays pixels or that another desktop application receives clipboard ownership.

Terminal smoke still needs real Kitty and iTerm2 sessions to render landscape and portrait images, resize, repaint, replace, remove, and exit without leaked graphics. `/copy` smoke first waits for WP-450's command binding, then needs Darwin, Linux Wayland, and Linux X11 desktops to paste multiline Unicode text into a separate application.

## Verification

- `make fixtures-check`
- `go test -race ./...`
- `go vet ./...` and golangci-lint
- `CGO_ENABLED=0 go build ./...` for Linux and Darwin on amd64 and arm64
- focused terminal image, image-processing, read-tool, settings, and clipboard suites

No dependency was added outside ARCHITECTURE section 8. `golang.org/x/image` supplies the approved pure-Go BMP and WebP decoders; the Lanczos3 sampler is internal, and the Node Photon package is pinned dev-only fixture tooling and absent from release binaries.
