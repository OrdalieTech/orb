package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLsToolListsDotfilesAndDirectoriesInOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"z", "B", "a", ".hidden-file"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".hidden-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := NewLsTool(dir, nil).Execute(context.Background(), "call", map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := ".hidden-dir/\n.hidden-file\na\nB\nfolder/\nz"
	if got := toolResultText(t, result); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if result.Details != nil {
		t.Fatalf("Details = %#v, want nil", result.Details)
	}
}

func TestLsToolHandlesEmptyMissingAndFilePaths(t *testing.T) {
	dir := t.TempDir()
	tool := NewLsTool(dir, nil)
	result, err := tool.Execute(context.Background(), "empty", LsToolInput{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result); got != "(empty directory)" {
		t.Fatalf("empty output = %q", got)
	}
	_, err = tool.Execute(context.Background(), "missing", map[string]any{"path": "missing"}, nil)
	if err == nil || err.Error() != "Path not found: "+filepath.Join(dir, "missing") {
		t.Fatalf("missing error = %v", err)
	}
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(context.Background(), "file", map[string]any{"path": file}, nil)
	if err == nil || err.Error() != "Not a directory: "+file {
		t.Fatalf("file error = %v", err)
	}
}

func TestLsToolOrdersAndFiltersEntries(t *testing.T) {
	for _, test := range []struct {
		name     string
		locale   string   // LC_ALL override; empty keeps the ambient process locale
		entries  []string // ReadDir enumeration order
		statable []string // entries whose Stat succeeds
		want     string
	}{
		{"UsesJavaScriptFullLowercaseMapping", "", []string{"İ", "i"}, []string{"İ", "i"}, "i\nİ"},
		{"StableSortPreservesEnumerationOrderForEqualFoldedNames", "", []string{"a", "A"}, []string{"a", "A"}, "a\nA"},
		{"UsesProcessDefaultLocale", "sv_SE.UTF-8", []string{"z", "ä", "å", "ö", "a"}, []string{"z", "ä", "å", "ö", "a"}, "a\nz\nå\nä\nö"},
		{"SkipsEntriesItCannotStat", "", []string{"bad", "good"}, []string{"good"}, "good"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.locale != "" {
				t.Setenv("LC_ALL", test.locale)
			}
			stats := map[string]LsPathStat{hostPath("remote"): {Directory: true}}
			for _, name := range test.statable {
				stats[hostPath("remote", name)] = LsPathStat{}
			}
			operations := &fakeLsOperations{exists: true, stats: stats, entries: test.entries}
			result, err := NewLsTool(hostPath(), &LsToolOptions{Operations: operations}).Execute(context.Background(), "call", map[string]any{"path": hostPath("remote")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := toolResultText(t, result); got != test.want {
				t.Fatalf("output = %q, want %q", got, test.want)
			}
		})
	}
}

type fakeLsOperations struct {
	exists  bool
	stats   map[string]LsPathStat
	entries []string
	readErr error
}

func (operations *fakeLsOperations) Exists(context.Context, string) (bool, error) {
	return operations.exists, nil
}

func (operations *fakeLsOperations) Stat(_ context.Context, path string) (LsPathStat, error) {
	stat, ok := operations.stats[path]
	if !ok {
		return LsPathStat{}, os.ErrNotExist
	}
	return stat, nil
}

func (operations *fakeLsOperations) ReadDir(context.Context, string) ([]string, error) {
	return append([]string(nil), operations.entries...), operations.readErr
}

func TestLsToolAbortReturnsWhileUpstreamStyleWorkContinues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	operations := &continuingLsOperations{cancel: cancel, done: done}
	_, err := NewLsTool(hostPath(), &LsToolOptions{Operations: operations}).Execute(ctx, "call", map[string]any{"path": hostPath("remote")}, nil)
	if !errors.Is(err, errOperationAborted) {
		t.Fatalf("error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ls worker stopped scheduling operations after abort")
	}
}

type continuingLsOperations struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (operations *continuingLsOperations) Exists(context.Context, string) (bool, error) {
	operations.cancel()
	return true, nil
}

func (operations *continuingLsOperations) Stat(_ context.Context, path string) (LsPathStat, error) {
	if path == hostPath("remote", "second") {
		close(operations.done)
	}
	return LsPathStat{Directory: path == hostPath("remote")}, nil
}

func (*continuingLsOperations) ReadDir(context.Context, string) ([]string, error) {
	return []string{"first", "second"}, nil
}

// hostPath joins elements under an absolute root of the running platform, for
// fake operations that never touch the disk.
func hostPath(elements ...string) string {
	root := "/"
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	return filepath.Join(append([]string{root}, elements...)...)
}
