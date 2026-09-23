package config

import (
	"context"

	"github.com/OrdalieTech/orb/agent/tools"
)

// Upstream runs "!command" values through the configured bash on win32 and
// falls back to execSync's cmd.exe only when no bash can be spawned.
func shellCommandOutput(ctx context.Context, command string) (string, bool) {
	return tools.ShellCommandOutput(ctx, command)
}
