// Package termcaps detects terminal capabilities (image protocol, true
// color, OSC 8 hyperlinks) and provides the pure hyperlink escape helper.
// It is a leaf so non-presentation layers (tool renderers) can ask "does
// this terminal support hyperlinks?" without linking the TUI; the tui
// package re-exports this surface unchanged.
package termcaps

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type ImageProtocol string

const (
	ImageProtocolKitty  ImageProtocol = "kitty"
	ImageProtocolITerm2 ImageProtocol = "iterm2"
)

// NotifyProtocol is how a terminal reaches a user who is looking elsewhere.
type NotifyProtocol string

const (
	NotifyBell   NotifyProtocol = ""
	NotifyNone   NotifyProtocol = "none"
	NotifyOSC9   NotifyProtocol = "osc9"
	NotifyOSC99  NotifyProtocol = "osc99"
	NotifyOSC777 NotifyProtocol = "osc777"
)

type TerminalCapabilities struct {
	Images     ImageProtocol
	TrueColor  bool
	Hyperlinks bool
	Notify     NotifyProtocol
}

// ponytail: the detection cache is process-global on purpose — a process has
// one controlling terminal; per-instance capabilities would be fiction.
var state = struct {
	sync.RWMutex
	capabilities *TerminalCapabilities
}{}

// ProbeTmuxHyperlinks asks the ambient tmux client whether it forwards OSC 8.
func ProbeTmuxHyperlinks() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{client_termfeatures}").Output()
	if err != nil {
		return false
	}
	for feature := range strings.SplitSeq(string(output), ",") {
		if strings.TrimSpace(feature) == "hyperlinks" {
			return true
		}
	}
	return false
}

// Detect derives capabilities from the terminal environment. Behavior is a
// faithful port of upstream terminal-image.ts detection order.
func Detect(tmuxForwardsHyperlink func() bool) TerminalCapabilities {
	env := os.Getenv
	termProgram := strings.ToLower(env("TERM_PROGRAM"))
	terminalEmulator := strings.ToLower(env("TERMINAL_EMULATOR"))
	term := strings.ToLower(env("TERM"))
	colorTerm := strings.ToLower(env("COLORTERM"))
	trueColorHint := colorTerm == "truecolor" || colorTerm == "24bit"
	if env("TMUX") != "" || strings.HasPrefix(term, "tmux") {
		forwarded := false
		if tmuxForwardsHyperlink != nil {
			forwarded = tmuxForwardsHyperlink()
		}
		return TerminalCapabilities{TrueColor: trueColorHint, Hyperlinks: forwarded}
	}
	if strings.HasPrefix(term, "screen") {
		return TerminalCapabilities{TrueColor: trueColorHint}
	}
	// Herdr forwards OSC 8 hyperlinks and draws Kitty graphics (0.9.2 and later). It
	// runs inside another terminal whose variables (GHOSTTY_RESOURCES_DIR,
	// KITTY_WINDOW_ID) leak into its panes, so it is checked first (upstream 1.2.0).
	// Herdr turns the state Orb reports to it into its own notifications.
	if termProgram == "herdr" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: trueColorHint, Hyperlinks: true, Notify: NotifyNone}
	}
	// Rex implements enough of the Kitty graphics protocol for Orb (pi 1.4.0).
	if termProgram == "rex" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if env("KITTY_WINDOW_ID") != "" || termProgram == "kitty" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true, Notify: NotifyOSC99}
	}
	if termProgram == "ghostty" || strings.Contains(term, "ghostty") || env("GHOSTTY_RESOURCES_DIR") != "" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true, Notify: NotifyOSC777}
	}
	if env("WEZTERM_PANE") != "" || termProgram == "wezterm" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true, Notify: NotifyOSC777}
	}
	if termProgram == "warpterminal" || env("WARP_SESSION_ID") != "" || env("WARP_TERMINAL_SESSION_UUID") != "" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if env("ITERM_SESSION_ID") != "" || termProgram == "iterm.app" {
		return TerminalCapabilities{Images: ImageProtocolITerm2, TrueColor: true, Hyperlinks: true, Notify: NotifyOSC9}
	}
	// Alacritty sets no TERM_PROGRAM: TERM is alacritty when its terminfo entry is installed
	// (xterm-256color otherwise), and ALACRITTY_WINDOW_ID is set on Unix and macOS.
	alacritty := termProgram == "alacritty" || strings.HasPrefix(term, "alacritty") || env("ALACRITTY_WINDOW_ID") != ""
	if env("WT_SESSION") != "" || termProgram == "vscode" || alacritty {
		return TerminalCapabilities{TrueColor: true, Hyperlinks: true}
	}
	if terminalEmulator == "jetbrains-jediterm" {
		return TerminalCapabilities{TrueColor: true}
	}
	return TerminalCapabilities{TrueColor: trueColorHint}
}

// Get returns the cached capabilities, detecting them once on first use.
func Get() TerminalCapabilities {
	state.RLock()
	if state.capabilities != nil {
		capabilities := *state.capabilities
		state.RUnlock()
		return capabilities
	}
	state.RUnlock()
	capabilities := Detect(ProbeTmuxHyperlinks)
	state.Lock()
	if state.capabilities == nil {
		state.capabilities = &capabilities
	} else {
		capabilities = *state.capabilities
	}
	state.Unlock()
	return capabilities
}

// Set pins the capabilities (tests and explicit configuration).
func Set(capabilities TerminalCapabilities) {
	state.Lock()
	state.capabilities = &capabilities
	state.Unlock()
}

// ResetCache clears the pinned or detected value so the next Get re-detects.
func ResetCache() {
	state.Lock()
	state.capabilities = nil
	state.Unlock()
}

// Hyperlink wraps text in an OSC 8 hyperlink escape sequence. In terminals
// without hyperlink support the escape codes are ignored and only the plain
// text is displayed.
func Hyperlink(text, url string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}
