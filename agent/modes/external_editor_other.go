//go:build !windows

package modes

import (
	"os/exec"
	"strings"
)

// externalEditorProcess splits the command on spaces to support editor
// arguments (e.g. "code --wait") and appends the prompt file, like upstream's
// spawn without a shell.
func externalEditorProcess(command, filePath string) *exec.Cmd {
	parts := strings.Split(command, " ")
	return exec.Command(parts[0], append(parts[1:], filePath)...)
}
