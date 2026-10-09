package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/internal/truncate"
)

func TestGrepToolTruncatesLongLinesAndReportsDetails(t *testing.T) {
	requireUnixSearchTest(t)
	root := searchTreeRoot(t)
	path := filepath.Join(root, "visible.txt")
	line := strings.Repeat("x", truncate.GrepMaxLineLength+1)
	installFakeManagedTool(t, "rg", rgMatchEvent(t, path, 1, line+"\n"), "", 0, "")
	result, err := NewGrepTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "x", "path": path,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "visible.txt:1: " + strings.Repeat("x", truncate.GrepMaxLineLength) + "... [truncated]\n\n[Some lines truncated to 500 chars. Use read tool to see full lines]"
	if got := toolResultText(t, result); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	details, ok := result.Details.(GrepToolDetails)
	if !ok || !details.LinesTruncated {
		t.Fatalf("details = %#v", result.Details)
	}
}

func TestGrepToolPassesFlagLikePatternAfterDoubleDash(t *testing.T) {
	requireUnixSearchTest(t)
	record := filepath.Join(t.TempDir(), "args")
	installFakeManagedTool(t, "rg", "", "", 1, record)
	root := searchTreeRoot(t)
	result, err := NewGrepTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "--pre=payload", "path": root, "glob": "*.txt", "ignoreCase": true, "literal": true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result); got != "No matches found" {
		t.Fatalf("output = %q", got)
	}
	args := readRecordedArgs(t, record)
	wantTail := []string{"--ignore-case", "--fixed-strings", "--glob", "*.txt", "--", "--pre=payload", root}
	if len(args) < len(wantTail) || !slices.Equal(args[len(args)-len(wantTail):], wantTail) {
		t.Fatalf("args = %#v, want tail %#v", args, wantTail)
	}
}

func TestFindToolMiniTreeOutputAndPathGlobArguments(t *testing.T) {
	requireUnixSearchTest(t)
	root := searchTreeRoot(t)
	record := filepath.Join(t.TempDir(), "args")
	stdout := strings.Join([]string{
		filepath.Join(root, "visible.txt"),
		filepath.Join(root, ".secret", "hidden.txt"),
		filepath.Join(root, "src", "foo", "bar", "example.spec.ts"),
	}, "\n")
	installFakeManagedTool(t, "fd", stdout, "", 0, record)
	result, err := NewFindTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "src/**/*.spec.ts", "path": root,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "visible.txt\n.secret/hidden.txt\nsrc/foo/bar/example.spec.ts"
	if got := toolResultText(t, result); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	args := readRecordedArgs(t, record)
	if slices.Contains(args, "--no-require-git") {
		t.Fatalf("args inside repository unexpectedly contain --no-require-git: %#v", args)
	}
	wantTail := []string{"--max-results", "1000", "--full-path", "--", "**/src/**/*.spec.ts", root}
	if len(args) < len(wantTail) || !slices.Equal(args[len(args)-len(wantTail):], wantTail) {
		t.Fatalf("args = %#v, want tail %#v", args, wantTail)
	}
}

func TestFindToolSurfacesFDErrorAndProtectsFlagPattern(t *testing.T) {
	requireUnixSearchTest(t)
	root := searchTreeRoot(t)
	installFakeManagedTool(t, "fd", "", "error parsing glob: unclosed character class", 1, "")
	_, err := NewFindTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "[", "path": root,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "error parsing glob") {
		t.Fatalf("error = %v", err)
	}

	record := filepath.Join(t.TempDir(), "args")
	installFakeManagedTool(t, "fd", "", "", 0, record)
	result, err := NewFindTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "--help", "path": root,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result); got != "No files found matching pattern" {
		t.Fatalf("output = %q", got)
	}
	args := readRecordedArgs(t, record)
	if index := slices.Index(args, "--"); index < 0 || index+1 >= len(args) || args[index+1] != "--help" {
		t.Fatalf("args do not protect flag-like pattern: %#v", args)
	}
}

func TestFindToolCustomGlobAbortWinsWithoutWaiting(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	operations := &blockingFindOperations{started: started, release: release}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewFindTool(hostPath("remote"), &FindToolOptions{Operations: operations}).Execute(ctx, "call", map[string]any{"pattern": "*"}, nil)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errOperationAborted) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("find waited for custom Glob after abort")
	}
	close(release)
}

type blockingFindOperations struct {
	started chan struct{}
	release chan struct{}
}

func (*blockingFindOperations) Exists(context.Context, string) (bool, error) { return true, nil }

func (operations *blockingFindOperations) Glob(context.Context, string, string, FindGlobOptions) ([]string, error) {
	close(operations.started)
	<-operations.release
	return []string{"late"}, nil
}

func searchTreeRoot(t *testing.T) string {
	t.Helper()
	source, err := filepath.Abs(filepath.Join("testdata", "search", "tree"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "tree")
	if err := os.CopyFS(root, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || entry.Name() != "gitignore.rules" {
			return walkErr
		}
		rules, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(filepath.Join(filepath.Dir(path), ".gitignore"), rules, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func rgMatchEvent(t *testing.T, path string, lineNumber int, text string) string {
	t.Helper()
	event := map[string]any{
		"type": "match",
		"data": map[string]any{
			"path":        map[string]any{"text": path},
			"line_number": lineNumber,
			"lines":       map[string]any{"text": text},
		},
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func installFakeManagedTool(t *testing.T, name, stdout, stderr string, exitCode int, recordPath string) {
	t.Helper()
	agentDir := t.TempDir()
	binDir := filepath.Join(agentDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	if recordPath != "" {
		fmt.Fprintf(&script, "printf '%%s\\n' \"$@\" > %s\n", shellSingleQuote(recordPath))
	}
	if stdout != "" {
		script.WriteString("cat <<'ORB_STDOUT'\n")
		script.WriteString(stdout)
		script.WriteString("\nORB_STDOUT\n")
	}
	if stderr != "" {
		script.WriteString("cat >&2 <<'ORB_STDERR'\n")
		script.WriteString(stderr)
		script.WriteString("\nORB_STDERR\n")
	}
	fmt.Fprintf(&script, "exit %d\n", exitCode)
	writeSearchExecutable(t, filepath.Join(binDir, name), script.String())
	t.Setenv("ORB_AGENT_DIR", agentDir)
	t.Setenv("ORB_OFFLINE", "1")
}

func writeSearchExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func readRecordedArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func requireUnixSearchTest(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake rg and fd are POSIX shell scripts")
	}
}
