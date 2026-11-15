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

	// Exactly limit results report no limit; limits below 1 mean 1 (#7121).
	for _, test := range []struct {
		limit float64
		want  string
	}{
		{2, "visible.txt\n.secret/hidden.txt"},
		{1.9, "visible.txt\n\n[1 results limit reached. Use limit=2 for more, or refine pattern]"},
		{0, "visible.txt\n\n[1 results limit reached. Use limit=2 for more, or refine pattern]"},
	} {
		result, err := NewFindTool(root, nil).Execute(context.Background(), "call", map[string]any{
			"pattern": "*.txt", "path": root, "limit": test.limit,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := toolResultText(t, result); got != test.want {
			t.Fatalf("limit %v: output = %q, want %q", test.limit, got, test.want)
		}
		details, _ := result.Details.(FindToolDetails)
		if limited := details.ResultLimitReached != nil; limited != (test.limit < 2) || limited && *details.ResultLimitReached != 1 {
			t.Fatalf("limit %v: details = %#v", test.limit, result.Details)
		}
	}
}

func TestFindCustomGlobGetsOneResultPastTheLimit(t *testing.T) {
	operations := &recordingFindOperations{results: []string{"a.txt", "b.txt", "c.txt"}}
	tool := NewFindTool(hostPath("remote"), &FindToolOptions{Operations: operations})
	for limit, want := range map[float64]string{3: "a.txt\nb.txt\nc.txt", 2: "a.txt\nb.txt\n\n[2 results limit reached]"} {
		result, err := tool.Execute(context.Background(), "call", map[string]any{"pattern": "*.txt", "limit": limit}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := toolResultText(t, result); got != want || operations.limit != limit+1 {
			t.Fatalf("limit %v: output = %q, requested %v", limit, got, operations.limit)
		}
	}
}

type recordingFindOperations struct {
	results []string
	limit   float64
}

func (*recordingFindOperations) Exists(context.Context, string) (bool, error) { return true, nil }

func (operations *recordingFindOperations) Glob(_ context.Context, _, _ string, options FindGlobOptions) ([]string, error) {
	operations.limit = options.Limit
	return operations.results[:min(len(operations.results), int(options.Limit))], nil
}

// Windows path patterns are matched in-process because fd's --full-path globs differ by
// version there (#9262).
func TestFindPathMatcherMatchesLikeFD(t *testing.T) {
	for _, test := range []struct {
		pattern, path string
		want          bool
	}{
		{"**/src/**/*.ts", "C:/repo/src/a.ts", true},
		{"**/src/**/*.ts", "C:/repo/src/deep/b.ts", true},
		{"**/src/*.ts", "C:/repo/src/deep/b.ts", false},
		{"**/src/*.ts", "C:/repo/src/.hidden.ts", true},
		{"**/src/**", "C:/repo/src", false},
		{"**/src/**", "C:/repo/src/a.ts", true},
		{"**/src/*.ts", "C:/Repo/SRC/A.TS", true},
		{"**/Src/*.ts", "C:/repo/src/a.ts", false},
		{"**/+(a|b).ts", "C:/repo/a.ts", false},
	} {
		if got := findPathMatcher(test.pattern)(test.path); got != test.want {
			t.Errorf("findPathMatcher(%q)(%q) = %v, want %v", test.pattern, test.path, got, test.want)
		}
	}
}
