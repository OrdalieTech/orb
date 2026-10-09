package tui

import (
	"testing"
	"time"
)

func TestTerminalControlsUseOrbEnvironmentOnly(t *testing.T) {
	t.Setenv("PI_TUI_ESC_TIMEOUT", "999")
	t.Setenv("PI_CLEAR_ON_SHRINK", "1")
	t.Setenv("PI_HARDWARE_CURSOR", "1")
	for _, name := range []string{"ORB_TUI_ESC_TIMEOUT", "ORB_CLEAR_ON_SHRINK", "ORB_HARDWARE_CURSOR", "SSH_CONNECTION", "SSH_TTY"} {
		t.Setenv(name, "")
	}
	if got := resolveEscapeTimeout(); got != defaultEscapeTimeout {
		t.Fatalf("Pi environment changed escape timeout: %v", got)
	}
	ui := NewTUI(nil)
	if ui.ClearOnShrink() || ui.ShowHardwareCursor() {
		t.Fatal("Pi environment changed Orb terminal controls")
	}
	t.Setenv("ORB_TUI_ESC_TIMEOUT", "250")
	t.Setenv("ORB_CLEAR_ON_SHRINK", "1")
	t.Setenv("ORB_HARDWARE_CURSOR", "1")
	if got := resolveEscapeTimeout(); got != 250*time.Millisecond {
		t.Fatalf("Orb escape timeout = %v", got)
	}
	ui = NewTUI(nil)
	if !ui.ClearOnShrink() || !ui.ShowHardwareCursor() {
		t.Fatal("Orb terminal controls not applied")
	}
}
