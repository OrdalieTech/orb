package modes

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// externalEditorResult mirrors upstream ExternalEditorResult: complete carries
// the edited content, anything else keeps the original text.
type externalEditorResult struct {
	complete bool
	content  string
}

// editInExternalEditor mirrors modes/interactive/external-editor.ts: the
// prompt is written to prompt.md inside a private mkdtemp directory (instead
// of scanning the shared temp dir), a non-zero editor exit keeps the original
// content, and a single trailing newline is stripped from the edited result.
func editInExternalEditor(command, content string) externalEditorResult {
	directory, err := os.MkdirTemp("", "pi-editor-")
	if err != nil {
		return externalEditorResult{}
	}
	// Cleanup is best effort.
	defer func() { _ = os.RemoveAll(directory) }()
	filePath := filepath.Join(directory, "prompt.md")
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		return externalEditorResult{}
	}
	_, _ = fmt.Fprintf(os.Stdout, "Launching external editor: %s\npigo will resume when the editor exits.\n", command)
	// Split by space to support editor arguments (e.g., "code --wait").
	parts := strings.Split(command, " ")
	var process *exec.Cmd
	if runtime.GOOS == "windows" {
		process = exec.Command("cmd", "/C", command+` "`+filePath+`"`)
	} else {
		process = exec.Command(parts[0], append(parts[1:], filePath)...)
	}
	process.Stdin, process.Stdout, process.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := process.Run(); err != nil {
		return externalEditorResult{}
	}
	edited, err := os.ReadFile(filePath)
	if err != nil {
		return externalEditorResult{}
	}
	return externalEditorResult{complete: true, content: strings.TrimSuffix(string(edited), "\n")}
}
