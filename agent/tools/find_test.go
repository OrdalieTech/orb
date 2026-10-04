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
	root := filepath.Join(string(filepath.Separator), "usr", "share", "orb-search-fixture")
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
