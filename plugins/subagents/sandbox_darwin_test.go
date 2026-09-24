//go:build darwin

package subagents

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/orb/platforms/native/sandbox"
)

func TestLiveExternalChildContainment(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	mustOK(os.Mkdir(scratch, 0700))
	t.Setenv("TMPDIR", scratch)
	marker := filepath.Join(root, "outside")
	_, err := runExternalChild(t.Context(), root, "probe", "printf blocked > '"+marker+"'", "", sandbox.ModeReadOnly)
	if err == nil {
		t.Fatal("external child escaped read-only sandbox")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("external child wrote outside: %v", err)
	}
}
