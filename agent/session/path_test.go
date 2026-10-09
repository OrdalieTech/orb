package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultSessionDirectoryIsIndependentOfPi(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ORB_AGENT_DIR", "")
	piDir := filepath.Join(home, ".pi", "agent")
	t.Setenv("PI_CODING_AGENT_DIR", piDir)
	got, err := DefaultSessionDir(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, filepath.Join(home, ".orb", "agent", "sessions")+string(filepath.Separator)) {
		t.Fatalf("session directory = %q", got)
	}
	if _, err := os.Stat(piDir); !os.IsNotExist(err) {
		t.Fatalf("created Pi directory: %v", err)
	}
	explicit := filepath.Join(home, "explicit")
	t.Setenv("ORB_AGENT_DIR", explicit)
	if got, err = DefaultSessionDirPath(cwd, ""); err != nil || !strings.HasPrefix(got, explicit+string(filepath.Separator)) {
		t.Fatalf("explicit Orb directory = %q, %v", got, err)
	}
}
