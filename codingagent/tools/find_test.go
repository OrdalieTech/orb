package tools

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFindMiniTreeOutsideGitUsesHierarchicalIgnoreMode(t *testing.T) {
	requireUnixSearchTest(t)
	root := filepath.Join(string(filepath.Separator), "usr", "share", "pigo-search-fixture")
	wantedPaths := []string{
		"a/deep/kept.txt",
		"a/kept.txt",
		"b/ignored.txt",
		"b/kept.txt",
		"root.txt",
	}
	absolute := make([]string, len(wantedPaths))
	for index, path := range wantedPaths {
		absolute[index] = filepath.Join(root, filepath.FromSlash(path))
	}
	record := filepath.Join(t.TempDir(), "args")
	installFakeManagedTool(t, "fd", strings.Join(absolute, "\n"), "", 0, record)

	result, err := NewFindTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "**/*.txt",
		"path":    root,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolResultText(t, result), strings.Join(wantedPaths, "\n"); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	args := readRecordedArgs(t, record)
	if !slices.Contains(args, "--no-require-git") {
		t.Fatalf("outside-git args lack --no-require-git: %#v", args)
	}
	if slices.Contains(args, "--ignore-file") {
		t.Fatalf("find must leave nested .gitignore scoping to fd: %#v", args)
	}
}

func TestFindAcceptsNonzeroExitWhenFDProducedOutput(t *testing.T) {
	requireUnixSearchTest(t)
	root := searchTreeRoot(t)
	paths := []string{
		filepath.Join(root, "visible.txt"),
		filepath.Join(root, ".secret", "hidden.txt"),
	}
	installFakeManagedTool(t, "fd", strings.Join(paths, "\n"), "warning from fd", 1, "")

	result, err := NewFindTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "*.txt",
		"path":    root,
		"limit":   2,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "visible.txt\n.secret/hidden.txt\n\n[2 results limit reached. Use limit=4 for more, or refine pattern]"
	if got := toolResultText(t, result); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	details, ok := result.Details.(FindToolDetails)
	if !ok || details.ResultLimitReached == nil || *details.ResultLimitReached != 2 {
		t.Fatalf("details = %#v", result.Details)
	}
}

func TestFindTerminalResultsAndFallbackErrorMatchUpstream(t *testing.T) {
	requireUnixSearchTest(t)
	t.Run("empty success", func(t *testing.T) {
		root := searchTreeRoot(t)
		installFakeManagedTool(t, "fd", "", "", 0, "")
		result, err := NewFindTool(root, nil).Execute(context.Background(), "call", map[string]any{
			"pattern": "absent",
			"path":    root,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := toolResultText(t, result); got != "No files found matching pattern" || result.Details != nil {
			t.Fatalf("result = %q, details %#v", got, result.Details)
		}
	})

	t.Run("exit code fallback", func(t *testing.T) {
		root := searchTreeRoot(t)
		installFakeManagedTool(t, "fd", "", "", 7, "")
		_, err := NewFindTool(root, nil).Execute(context.Background(), "call", map[string]any{
			"pattern": "[",
			"path":    root,
		}, nil)
		if err == nil || err.Error() != "fd exited with code 7" {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unavailable offline", func(t *testing.T) {
		t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
		t.Setenv("PI_OFFLINE", "yes")
		t.Setenv("PATH", "")
		_, err := NewFindTool(t.TempDir(), nil).Execute(context.Background(), "call", map[string]any{"pattern": "*"}, nil)
		want := "fd is not available and could not be downloaded"
		if err == nil || err.Error() != want {
			t.Fatalf("error = %v, want %q", err, want)
		}
	})
}
