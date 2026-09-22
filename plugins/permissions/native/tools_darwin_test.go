//go:build darwin

package native

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/sandbox"
)

func TestLiveBashContainment(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	workspace := filepath.Join(root, "workspace")
	for _, dir := range []string{scratch, workspace} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", scratch)
	for _, mode := range []sandbox.Mode{sandbox.ModeReadOnly, sandbox.ModeWorkspaceWrite} {
		opts := ToolOptions(mode, workspace, "/bin/sh")
		tool := tools.NewBashTool(workspace, opts.Bash)
		for _, tc := range []struct {
			name, path string
			allowed    bool
		}{{"outside", filepath.Join(root, "outside"), false}, {"workspace", filepath.Join(workspace, "file"), mode == sandbox.ModeWorkspaceWrite}, {"scratch", filepath.Join(scratch, "file"), true}} {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				_, err := tool.Execute(t.Context(), "live", map[string]any{"command": "printf probe > '" + tc.path + "'"}, nil)
				if (err == nil) != tc.allowed {
					t.Fatalf("command: %v", err)
				}
				_, statErr := os.Stat(tc.path)
				if (statErr == nil) != tc.allowed {
					t.Fatalf("filesystem effect: %v", statErr)
				}
				if tc.allowed {
					if err := os.Remove(tc.path); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
