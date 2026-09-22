// Package native attaches filesystem containment to local tool operations.
// Browser/VFS hosts supply their own operations instead of importing this adapter.
package native

import (
	"os"

	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/sandbox"
)

// ToolOptions applies one native boundary to bash, edit and write. Linux hosts
// must handle Orb's __sandbox launcher before starting the application.
func ToolOptions(mode sandbox.Mode, cwd, shell string) *tools.ToolsOptions {
	if mode == "" || mode == sandbox.ModeDangerFullAccess {
		return nil
	}
	roots := []string{os.TempDir()}
	if mode == sandbox.ModeWorkspaceWrite {
		roots = append([]string{cwd}, roots...)
	}
	files := sandbox.Files{WritableRoots: roots}
	return &tools.ToolsOptions{
		Bash: &tools.BashToolOptions{ShellPath: shell, SpawnHook: func(spawn tools.BashSpawnContext) tools.BashSpawnContext {
			spawn.Command, spawn.Env = sandbox.Wrap(mode, cwd, shell, spawn.Command, spawn.Env)
			return spawn
		}},
		Edit:  &tools.EditToolOptions{Operations: files},
		Write: &tools.WriteToolOptions{Operations: files},
	}
}
