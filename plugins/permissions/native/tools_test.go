package native

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/sandbox"
)

func TestNativeFileTools(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	workspace := filepath.Join(root, "workspace")
	for _, dir := range []string{scratch, workspace} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", scratch)
	t.Setenv("TMP", scratch)
	t.Setenv("TEMP", scratch)
	for _, mode := range []sandbox.Mode{sandbox.ModeReadOnly, sandbox.ModeWorkspaceWrite} {
		t.Run(string(mode), func(t *testing.T) {
			opts := ToolOptions(mode, workspace, "")
			for _, tc := range []struct {
				path    string
				allowed bool
			}{{filepath.Join(root, "outside"), false}, {filepath.Join(workspace, "nested/file"), mode == sandbox.ModeWorkspaceWrite}, {filepath.Join(scratch, "file"), true}} {
				_, err := tools.NewWriteTool(workspace, opts.Write).Execute(t.Context(), "write", map[string]any{"path": tc.path, "content": "before"}, nil)
				if (err == nil) != tc.allowed {
					t.Fatalf("write %s: %v", tc.path, err)
				}
			}
			path := filepath.Join(workspace, "existing")
			if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := tools.NewEditTool(workspace, opts.Edit).Execute(t.Context(), "edit", map[string]any{"path": path, "edits": []any{map[string]any{"oldText": "before", "newText": "after"}}}, nil)
			if (err == nil) != (mode == sandbox.ModeWorkspaceWrite) {
				t.Fatalf("edit: %v", err)
			}
		})
	}
}
