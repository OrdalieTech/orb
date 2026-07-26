package modes

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeExternalEditor mirrors upstream test/fixtures/fake-external-editor.mjs:
// it captures the prompt file path, its content, and the directory listing,
// then rewrites (or fails) according to the flag.
func fakeExternalEditor(t *testing.T, capturePath, flag string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-editor.sh")
	content := `#!/bin/sh
capture="$1"
file=""
for arg in "$@"; do file="$arg"; done
dir=$(dirname "$file")
{
  echo "file:$file"
  echo "content:$(cat "$file")"
  echo "entries:$(ls "$dir" | tr '\n' ' ')"
} > "$capture"
case "$*" in *--fail*) exit 1 ;; esac
case "$*" in *--empty*) printf '' > "$file" ;; *) printf 'edited\n' > "$file" ;; esac
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	command := "/bin/sh " + script + " " + capturePath
	if flag != "" {
		command += " " + flag
	}
	return command
}

func captureLine(t *testing.T, capturePath, prefix string) string {
	t.Helper()
	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("capture %q has no %q line", string(data), prefix)
	return ""
}

// Upstream 75e6123a: the prompt is edited inside a private mkdtemp directory
// (pi-editor-*/prompt.md) instead of a file scanned out of the shared temp
// dir, and the directory is removed afterwards.
func TestEditInExternalEditorUsesPrivateTempDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires /bin/sh")
	}
	capturePath := filepath.Join(t.TempDir(), "capture.txt")

	result := editInExternalEditor(fakeExternalEditor(t, capturePath, ""), "original")
	if !result.complete || result.content != "edited" {
		t.Fatalf("result = %#v, want complete %q", result, "edited")
	}
	promptPath := captureLine(t, capturePath, "file:")
	directory := filepath.Dir(promptPath)
	if filepath.Base(promptPath) != "prompt.md" {
		t.Fatalf("prompt file = %q, want prompt.md", promptPath)
	}
	if !strings.HasPrefix(filepath.Base(directory), "pi-editor-") {
		t.Fatalf("temp directory = %q, want pi-editor-* prefix", directory)
	}
	if got := captureLine(t, capturePath, "content:"); got != "original" {
		t.Fatalf("editor saw content %q, want %q", got, "original")
	}
	if got := strings.TrimSpace(captureLine(t, capturePath, "entries:")); got != "prompt.md" {
		t.Fatalf("temp directory entries = %q, want only prompt.md", got)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("temp directory %q was not removed: %v", directory, err)
	}
}

func TestEditInExternalEditorKeepsOriginalOnFailureAndReturnsEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires /bin/sh")
	}
	capturePath := filepath.Join(t.TempDir(), "capture.txt")

	result := editInExternalEditor(fakeExternalEditor(t, capturePath, "--fail"), "original")
	if result.complete || result.content != "" {
		t.Fatalf("failed result = %#v, want incomplete", result)
	}
	if directory := filepath.Dir(captureLine(t, capturePath, "file:")); dirExists(directory) {
		t.Fatalf("temp directory %q was not removed after failure", directory)
	}

	result = editInExternalEditor(fakeExternalEditor(t, capturePath, "--empty"), "original")
	if !result.complete || result.content != "" {
		t.Fatalf("empty result = %#v, want complete empty content", result)
	}
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
