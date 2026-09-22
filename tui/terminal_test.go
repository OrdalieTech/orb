package tui

import (
	"testing"
	"time"
)

func TestKeyboardProtocolNegotiationParser(t *testing.T) {
	tests := []struct {
		sequence, kind string
		flags          int
		ok             bool
	}{{"\x1b[?7u", "kitty-flags", 7, true}, {"\x1b[?0u", "kitty-flags", 0, true}, {"\x1b[?62;4;52c", "device-attributes", 0, true}, {"\x1b[A", "", 0, false}}
	for _, test := range tests {
		got, ok := ParseKeyboardProtocolNegotiation(test.sequence)
		if ok != test.ok || got.Type != test.kind || got.Flags != test.flags {
			t.Errorf("parse %q = %#v, %v", test.sequence, got, ok)
		}
	}
}

func TestNormalizeAppleTerminalInput(t *testing.T) {
	if got := NormalizeAppleTerminalInput("\r", true, true); got != "\x1b[13;2u" {
		t.Fatalf("shift return = %q", got)
	}
	if got := NormalizeAppleTerminalInput("\r", true, false); got != "\r" {
		t.Fatalf("plain return = %q", got)
	}
	if got := NormalizeAppleTerminalInput("a", true, true); got != "a" {
		t.Fatalf("non-return = %q", got)
	}
}

func TestResolveEscapeTimeout(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want time.Duration
	}{
		{"local", nil, 10 * time.Millisecond},
		{"ssh connection", map[string]string{"SSH_CONNECTION": "10.0.0.1 22 10.0.0.2 22"}, 100 * time.Millisecond},
		{"ssh tty", map[string]string{"SSH_TTY": "/dev/pts/3"}, 100 * time.Millisecond},
		{"override", map[string]string{"PI_TUI_ESC_TIMEOUT": "250"}, 250 * time.Millisecond},
		{"override beats ssh", map[string]string{"PI_TUI_ESC_TIMEOUT": " 40 ", "SSH_TTY": "/dev/pts/3"}, 40 * time.Millisecond},
		{"unparsable override", map[string]string{"PI_TUI_ESC_TIMEOUT": "soon"}, 10 * time.Millisecond},
		{"non-positive override", map[string]string{"PI_TUI_ESC_TIMEOUT": "0"}, 10 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, name := range []string{"PI_TUI_ESC_TIMEOUT", "SSH_CONNECTION", "SSH_TTY"} {
				t.Setenv(name, test.env[name])
			}
			if got := resolveEscapeTimeout(); got != test.want {
				t.Fatalf("escape timeout = %s, want %s", got, test.want)
			}
		})
	}
}
