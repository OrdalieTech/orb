package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/truncate"
)

func TestGrepToolMiniTreeContextLimitAndOutputShape(t *testing.T) {
	requireUnixSearchTest(t)
	root := searchTreeRoot(t)
	path := filepath.Join(root, "context.txt")
	events := strings.Join([]string{
		rgMatchEvent(t, path, 2, "match one\n"),
		rgMatchEvent(t, path, 5, "match two\n"),
	}, "\n")
	installFakeManagedTool(t, "rg", events, "", 0, "")
	result, err := NewGrepTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "match", "path": path, "limit": 1, "context": 1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "context.txt-1- before\ncontext.txt:2: match one\ncontext.txt-3- after\n\n[1 matches limit reached. Use limit=2 for more, or refine pattern]"
	if got := toolResultText(t, result); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	details, ok := result.Details.(GrepToolDetails)
	if !ok || details.MatchLimitReached == nil || *details.MatchLimitReached != 1 {
		t.Fatalf("details = %#v", result.Details)
	}
}

func TestSearchToolsLiveMiniTree(t *testing.T) {
	if os.Getenv("PI_GO_LIVE_TESTS") != "1" {
		t.Skip("set PI_GO_LIVE_TESTS=1 to download rg and fd")
	}
	requireUnixSearchTest(t)
	agentDir := t.TempDir()
	binDir := filepath.Join(agentDir, "bin")
	manager := &toolManager{
		binDir: binDir, goos: runtime.GOOS, goarch: runtime.GOARCH,
		apiBaseURL: "https://api.github.com", client: http.DefaultClient,
	}
	for _, managed := range []managedTool{managedRG, managedFD} {
		if _, err := manager.downloadTool(context.Background(), managed); err != nil {
			t.Fatalf("download %s: %v", managed, err)
		}
	}
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	t.Setenv("PI_OFFLINE", "1")
	root := searchTreeRoot(t)
	grepResult, err := NewGrepTool(root, nil).Execute(context.Background(), "grep", map[string]any{
		"pattern": "match", "path": filepath.Join(root, "context.txt"), "context": 1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	grepWant := "context.txt-1- before\ncontext.txt:2: match one\ncontext.txt-3- after\ncontext.txt-4- middle\ncontext.txt:5: match two\ncontext.txt-6- after two"
	if got := toolResultText(t, grepResult); got != grepWant {
		t.Fatalf("live grep output = %q, want %q", got, grepWant)
	}

	findResult, err := NewFindTool(root, nil).Execute(context.Background(), "find", map[string]any{
		"pattern": "**/*.txt", "path": root,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := strings.Split(toolResultText(t, findResult), "\n")
	for _, wanted := range []string{".secret/hidden.txt", "a/deep/kept.txt", "a/kept.txt", "b/ignored.txt", "b/kept.txt", "context.txt", "kept.txt", "root.txt", "visible.txt"} {
		if !slices.Contains(paths, wanted) {
			t.Fatalf("live find output %q lacks %q", paths, wanted)
		}
	}
	for _, ignored := range []string{"ignored.txt", "a/ignored.txt", "a/deep/ignored.txt", "a/deep/secret.txt"} {
		if slices.Contains(paths, ignored) {
			t.Fatalf("live find output %q includes ignored %q", paths, ignored)
		}
	}
}

func TestGrepToolPreservesFractionalContextIndexQuirk(t *testing.T) {
	requireUnixSearchTest(t)
	root := searchTreeRoot(t)
	path := filepath.Join(root, "context.txt")
	installFakeManagedTool(t, "rg", rgMatchEvent(t, path, 2, "match one\n"), "", 0, "")
	result, err := NewGrepTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "match", "path": path, "context": 0.5,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "context.txt-1.5- \ncontext.txt-2.5- "
	if got := toolResultText(t, result); got != want {
		t.Fatalf("output = %q, want upstream fractional-index quirk %q", got, want)
	}
}

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

func TestGrepToolMissingPathAndSchemaMatchUpstream(t *testing.T) {
	requireUnixSearchTest(t)
	installFakeManagedTool(t, "rg", "", "", 1, "")
	root := searchTreeRoot(t)
	missing := filepath.Join(root, "missing")
	_, err := NewGrepTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "x", "path": missing,
	}, nil)
	if err == nil || err.Error() != "Path not found: "+missing {
		t.Fatalf("error = %v", err)
	}
	wantSchema := `{"type":"object","required":["pattern"],"properties":{"pattern":{"type":"string","description":"Search pattern (regex or literal string)"},"path":{"type":"string","description":"Directory or file to search (default: current directory)"},"glob":{"type":"string","description":"Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'"},"ignoreCase":{"type":"boolean","description":"Case-insensitive search (default: false)"},"literal":{"type":"boolean","description":"Treat pattern as literal string instead of regex (default: false)"},"context":{"type":"number","description":"Number of lines to show before and after each match (default: 0)"},"limit":{"type":"number","description":"Maximum number of matches to return (default: 100)"}}}`
	if got := string(NewGrepTool(root, nil).Spec().Parameters); got != wantSchema {
		t.Fatalf("schema = %s, want %s", got, wantSchema)
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

func TestFindToolUsesNoRequireGitOutsideRepository(t *testing.T) {
	requireUnixSearchTest(t)
	root := "/usr/share"
	path := filepath.Join(root, "file.txt")
	record := filepath.Join(t.TempDir(), "args")
	installFakeManagedTool(t, "fd", path, "", 0, record)
	_, err := NewFindTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "*.txt", "path": root,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if args := readRecordedArgs(t, record); !slices.Contains(args, "--no-require-git") {
		t.Fatalf("args outside repository lack --no-require-git: %#v", args)
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

func TestFindToolCustomOperationsPreserveOutputAndLimitShape(t *testing.T) {
	root := "/remote"
	operations := &recordingFindOperations{
		exists:  true,
		results: []string{"/remote/a.txt", "/remote/nested/b.txt"},
	}
	result, err := NewFindTool(root, &FindToolOptions{Operations: operations}).Execute(context.Background(), "call", map[string]any{
		"pattern": "**/*.txt", "limit": 2,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolResultText(t, result), "a.txt\nnested/b.txt\n\n[2 results limit reached]"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if operations.pattern != "**/*.txt" || operations.cwd != root || operations.options.Limit != 2 {
		t.Fatalf("operation call = pattern %q cwd %q options %#v", operations.pattern, operations.cwd, operations.options)
	}
	if want := []string{"**/node_modules/**", "**/.git/**"}; !slices.Equal(operations.options.Ignore, want) {
		t.Fatalf("ignore = %#v, want %#v", operations.options.Ignore, want)
	}
	details, ok := result.Details.(FindToolDetails)
	if !ok || details.ResultLimitReached == nil || *details.ResultLimitReached != 2 {
		t.Fatalf("details = %#v", result.Details)
	}
}

func TestFindToolCustomGlobAbortWinsWithoutWaiting(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	operations := &blockingFindOperations{started: started, release: release}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewFindTool("/remote", &FindToolOptions{Operations: operations}).Execute(ctx, "call", map[string]any{"pattern": "*"}, nil)
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

func TestFindToolCustomMissingPathAndSchema(t *testing.T) {
	operations := &recordingFindOperations{}
	_, err := NewFindTool("/remote", &FindToolOptions{Operations: operations}).Execute(context.Background(), "call", map[string]any{"pattern": "*"}, nil)
	if err == nil || err.Error() != "Path not found: /remote" {
		t.Fatalf("error = %v", err)
	}
	want := `{"type":"object","required":["pattern"],"properties":{"pattern":{"type":"string","description":"Glob pattern to match files, e.g. '*.ts', '**/*.json', or 'src/**/*.spec.ts'"},"path":{"type":"string","description":"Directory to search in (default: current directory)"},"limit":{"type":"number","description":"Maximum number of results (default: 1000)"}}}`
	if got := string(NewFindTool("/remote", nil).Spec().Parameters); got != want {
		t.Fatalf("schema = %s, want %s", got, want)
	}
}

type recordingFindOperations struct {
	exists  bool
	results []string
	pattern string
	cwd     string
	options FindGlobOptions
}

func (operations *recordingFindOperations) Exists(context.Context, string) (bool, error) {
	return operations.exists, nil
}

func (operations *recordingFindOperations) Glob(_ context.Context, pattern, cwd string, options FindGlobOptions) ([]string, error) {
	operations.pattern = pattern
	operations.cwd = cwd
	operations.options = options
	return operations.results, nil
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
		script.WriteString("cat <<'PI_GO_STDOUT'\n")
		script.WriteString(stdout)
		script.WriteString("\nPI_GO_STDOUT\n")
	}
	if stderr != "" {
		script.WriteString("cat >&2 <<'PI_GO_STDERR'\n")
		script.WriteString(stderr)
		script.WriteString("\nPI_GO_STDERR\n")
	}
	fmt.Fprintf(&script, "exit %d\n", exitCode)
	writeSearchExecutable(t, filepath.Join(binDir, name), script.String())
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	t.Setenv("PI_OFFLINE", "1")
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
		t.Skip("WP-670 ports Windows process execution")
	}
}

func TestSearchToolDetailsMarshalInUpstreamFieldOrder(t *testing.T) {
	limit := 2.0
	grepJSON, err := ai.Marshal(GrepToolDetails{MatchLimitReached: &limit, LinesTruncated: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(grepJSON), `{"matchLimitReached":2,"linesTruncated":true}`; got != want {
		t.Fatalf("grep details = %s, want %s", got, want)
	}
	findJSON, err := ai.Marshal(FindToolDetails{ResultLimitReached: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(findJSON), `{"resultLimitReached":2}`; got != want {
		t.Fatalf("find details = %s, want %s", got, want)
	}
}
