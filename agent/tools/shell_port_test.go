//go:build !wasm && !windows

package tools

import (
	"context"
	"testing"

	"github.com/OrdalieTech/orb/engine/harness"
)

// The Exec port must render every outcome exactly like the local backend.
func TestShellBashOperationsMatchLocalBackend(t *testing.T) {
	root := t.TempDir()
	port := NewBashTool(root, &BashToolOptions{Operations: ShellBashOperations(&harness.NodeExecutionEnv{CWD: root})})
	local := NewBashTool(root, nil)
	for _, params := range []map[string]any{
		{"command": "echo out; sleep 0.1; echo err >&2"},
		{"command": "printf partial; exit 3"},
		{"command": "sleep 5", "timeout": 0.2},
	} {
		want, wantErr := local.Execute(context.Background(), "call", params, nil)
		got, gotErr := port.Execute(context.Background(), "call", params, nil)
		if summarizeToolRun(root, got, gotErr) != summarizeToolRun(root, want, wantErr) {
			t.Errorf("%v:\nport  %s\nlocal %s", params, summarizeToolRun(root, got, gotErr), summarizeToolRun(root, want, wantErr))
		}
	}
}
