package tui

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const kittyKeyboardQuery = "\x1b[>7u\x1b[?u\x1b[c"

// resolveEscapeTimeout answers how long a lone ESC waits before it dispatches
// as the Escape key. SSH splits legacy Alt+key input wider than the local
// wait, turning Alt+Enter into an interrupt; PI_TUI_ESC_TIMEOUT overrides in
// milliseconds for transports slower still.
func resolveEscapeTimeout() time.Duration {
	if milliseconds, err := strconv.Atoi(strings.TrimSpace(os.Getenv("PI_TUI_ESC_TIMEOUT"))); err == nil && milliseconds > 0 {
		return time.Duration(milliseconds) * time.Millisecond
	}
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return 100 * time.Millisecond
	}
	return defaultEscapeTimeout
}

type KeyboardProtocolNegotiation struct {
	Type  string
	Flags int
}

func ParseKeyboardProtocolNegotiation(sequence string) (KeyboardProtocolNegotiation, bool) {
	if len(sequence) >= 5 && sequence[:3] == "\x1b[?" && sequence[len(sequence)-1] == 'u' {
		flags, err := strconv.Atoi(sequence[3 : len(sequence)-1])
		if err == nil {
			return KeyboardProtocolNegotiation{Type: "kitty-flags", Flags: flags}, true
		}
	}
	if len(sequence) >= 4 && sequence[:3] == "\x1b[?" && sequence[len(sequence)-1] == 'c' {
		for _, character := range sequence[3 : len(sequence)-1] {
			if (character < '0' || character > '9') && character != ';' {
				return KeyboardProtocolNegotiation{}, false
			}
		}
		return KeyboardProtocolNegotiation{Type: "device-attributes"}, true
	}
	return KeyboardProtocolNegotiation{}, false
}

func isKeyboardProtocolNegotiationPrefix(sequence string) bool {
	if sequence == "\x1b[" {
		return true
	}
	if !strings.HasPrefix(sequence, "\x1b[?") {
		return false
	}
	for _, character := range sequence[3:] {
		if (character < '0' || character > '9') && character != ';' {
			return false
		}
	}
	return true
}

func NormalizeAppleTerminalInput(data string, appleTerminal, shiftPressed bool) string {
	if appleTerminal && shiftPressed && data == "\r" {
		return "\x1b[13;2u"
	}
	return data
}

// Terminal is the renderer's minimal terminal contract.
type Terminal interface {
	Start(onInput func(string), onResize func()) error
	Stop() error
	DrainInput(maxDuration, idleDuration time.Duration)
	Write(string)
	Columns() int
	Rows() int
	KittyProtocolActive() bool
	MoveBy(lines int)
	HideCursor()
	ShowCursor()
	ClearLine()
	ClearFromCursor()
	ClearScreen()
	SetTitle(string)
	SetProgress(bool)
}
