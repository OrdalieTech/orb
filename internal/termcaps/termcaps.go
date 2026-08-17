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

type TerminalCapabilities struct {
	Images     ImageProtocol
	TrueColor  bool
	Hyperlinks bool
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
	termProgram := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	terminalEmulator := strings.ToLower(os.Getenv("TERMINAL_EMULATOR"))
	term := strings.ToLower(os.Getenv("TERM"))
	colorTerm := strings.ToLower(os.Getenv("COLORTERM"))
	trueColorHint := colorTerm == "truecolor" || colorTerm == "24bit"
	if os.Getenv("TMUX") != "" || strings.HasPrefix(term, "tmux") {
		forwarded := false
		if tmuxForwardsHyperlink != nil {
			forwarded = tmuxForwardsHyperlink()
		}
		return TerminalCapabilities{TrueColor: trueColorHint, Hyperlinks: forwarded}
	}
	if strings.HasPrefix(term, "screen") {
		return TerminalCapabilities{TrueColor: trueColorHint}
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || termProgram == "kitty" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if termProgram == "ghostty" || strings.Contains(term, "ghostty") || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if os.Getenv("WEZTERM_PANE") != "" || termProgram == "wezterm" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if termProgram == "warpterminal" || os.Getenv("WARP_SESSION_ID") != "" || os.Getenv("WARP_TERMINAL_SESSION_UUID") != "" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if os.Getenv("ITERM_SESSION_ID") != "" || termProgram == "iterm.app" {
		return TerminalCapabilities{Images: ImageProtocolITerm2, TrueColor: true, Hyperlinks: true}
	}
	if os.Getenv("WT_SESSION") != "" || termProgram == "vscode" || termProgram == "alacritty" {
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
