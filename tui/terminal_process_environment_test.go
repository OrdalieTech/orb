//go:build unix || windows

package tui

import (
	"path/filepath"
	"testing"
)

func TestTerminalWriteLogUsesOrbEnvironmentOnly(t *testing.T) {
	t.Setenv("PI_TUI_WRITE_LOG", filepath.Join(t.TempDir(), "pi.log"))
	t.Setenv("ORB_TUI_WRITE_LOG", "")
	if got := NewProcessTerminalFiles(nil, nil).writeLogPath; got != "" {
		t.Fatalf("Pi environment redirected Orb terminal log: %q", got)
	}
	path := filepath.Join(t.TempDir(), "orb.log")
	t.Setenv("ORB_TUI_WRITE_LOG", path)
	if got := NewProcessTerminalFiles(nil, nil).writeLogPath; got != path {
		t.Fatalf("Orb terminal log path = %q, want %q", got, path)
	}
}
