package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
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

func TestLsToolDecodesInvalidFilenameBytesLikeNode(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("this filesystem cannot create invalid UTF-8 filenames")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	invalidName := string([]byte{'b', 0xff})
	if err := os.WriteFile(filepath.Join(dir, invalidName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewLsTool(dir, nil).Execute(context.Background(), "call", map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolResultText(t, result), "good"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
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

func TestLsToolEntryLimitHasActionableNotice(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := NewLsTool(dir, nil).Execute(context.Background(), "call", map[string]any{"limit": float64(2)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "a\nb\n\n[2 entries limit reached. Use limit=4 for more]"
	if got := toolResultText(t, result); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	details, ok := result.Details.(LsToolDetails)
	if !ok || details.EntryLimitReached == nil || *details.EntryLimitReached != 2 {
		t.Fatalf("Details = %#v", result.Details)
	}
}

func TestLsToolPreservesLocaleAndFractionalLimitSemantics(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a", "_", "-", "0", "z"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := NewLsTool(dir, nil).Execute(context.Background(), "call", map[string]any{"limit": 1.5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "_\n-\n\n[1.5 entries limit reached. Use limit=3 for more]"
	if got := toolResultText(t, result); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	details, ok := result.Details.(LsToolDetails)
	if !ok || details.EntryLimitReached == nil || *details.EntryLimitReached != 1.5 {
		t.Fatalf("Details = %#v", result.Details)
	}
}

func TestLsToolUsesJavaScriptFullLowercaseMapping(t *testing.T) {
	operations := &fakeLsOperations{
		exists: true,
		stats: map[string]LsPathStat{
			"/remote":   {Directory: true},
			"/remote/İ": {},
			"/remote/i": {},
		},
		entries: []string{"İ", "i"},
	}
	result, err := NewLsTool("/", &LsToolOptions{Operations: operations}).Execute(context.Background(), "call", map[string]any{"path": "/remote"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolResultText(t, result), "i\nİ"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestLsToolStableSortPreservesEnumerationOrderForEqualFoldedNames(t *testing.T) {
	operations := &fakeLsOperations{
		exists: true,
		stats: map[string]LsPathStat{
			"/remote":   {Directory: true},
			"/remote/a": {},
			"/remote/A": {},
		},
		entries: []string{"a", "A"},
	}
	result, err := NewLsTool("/", &LsToolOptions{Operations: operations}).Execute(context.Background(), "call", map[string]any{"path": "/remote"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolResultText(t, result), "a\nA"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestLsToolUsesProcessDefaultLocale(t *testing.T) {
	t.Setenv("LC_ALL", "sv_SE.UTF-8")
	operations := &fakeLsOperations{
		exists: true,
		stats: map[string]LsPathStat{
			"/remote":   {Directory: true},
			"/remote/z": {}, "/remote/ä": {}, "/remote/å": {}, "/remote/ö": {}, "/remote/a": {},
		},
		entries: []string{"z", "ä", "å", "ö", "a"},
	}
	result, err := NewLsTool("/", &LsToolOptions{Operations: operations}).Execute(context.Background(), "call", map[string]any{"path": "/remote"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolResultText(t, result), "a\nz\nå\nä\nö"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestLsToolIgnoresLCOnlyLocaleLikeNodeIntl(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("LC_COLLATE", "sv_SE.UTF-8")
	if got := defaultCollationLanguage().String(); got != "en-US" {
		t.Fatalf("default locale = %q, want en-US", got)
	}
}

func TestLsToolUsesLCMessagesBeforeLangLikeNodeIntl(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "sv_SE.UTF-8")
	t.Setenv("LANG", "en_US.UTF-8")
	if got := defaultCollationLanguage().String(); got != "sv-SE" {
		t.Fatalf("default locale = %q, want sv-SE", got)
	}
}

func TestLsToolDetailsMatchUpstreamOrderAndSafeIntegerLimit(t *testing.T) {
	long := strings.Repeat("x", 30_000)
	operations := limitLsOperations{entries: []string{long + "a", long + "b", long + "c"}}
	result, err := NewLsTool("/", &LsToolOptions{Operations: operations}).Execute(context.Background(), "call", map[string]any{
		"path": "/remote", "limit": 2,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	details, ok := result.Details.(LsToolDetails)
	if !ok || details.Truncation == nil || details.Truncation.MaxLines != 9007199254740991 {
		t.Fatalf("details = %#v", result.Details)
	}
	encoded, err := ai.Marshal(result.Details)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(encoded), `{"entryLimitReached":2,"truncation":{`) {
		t.Fatalf("details member order = %s", encoded)
	}
}

type limitLsOperations struct{ entries []string }

func (limitLsOperations) Exists(context.Context, string) (bool, error) { return true, nil }
func (limitLsOperations) Stat(_ context.Context, path string) (LsPathStat, error) {
	return LsPathStat{Directory: path == "/remote"}, nil
}
func (operations limitLsOperations) ReadDir(context.Context, string) ([]string, error) {
	return append([]string(nil), operations.entries...), nil
}

func TestLsToolSkipsEntriesItCannotStat(t *testing.T) {
	operations := &fakeLsOperations{
		exists: true,
		stats: map[string]LsPathStat{
			"/remote":      {Directory: true},
			"/remote/good": {},
		},
		entries: []string{"bad", "good"},
	}
	result, err := NewLsTool("/", &LsToolOptions{Operations: operations}).Execute(context.Background(), "call", map[string]any{"path": "/remote"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result); got != "good" {
		t.Fatalf("output = %q", got)
	}
}

func TestLsToolWrapsReadDirError(t *testing.T) {
	operations := &fakeLsOperations{
		exists:  true,
		stats:   map[string]LsPathStat{"/remote": {Directory: true}},
		readErr: errors.New("disk offline"),
	}
	_, err := NewLsTool("/", &LsToolOptions{Operations: operations}).Execute(context.Background(), "call", map[string]any{"path": "/remote"}, nil)
	if err == nil || err.Error() != "Cannot read directory: disk offline" {
		t.Fatalf("error = %v", err)
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

func TestLsToolDescriptionAndSchema(t *testing.T) {
	spec := NewLsTool(t.TempDir(), nil).Spec()
	if spec.Name != "ls" || spec.Label != "ls" || !strings.Contains(spec.Description, "500 entries") {
		t.Fatalf("Spec = %#v", spec)
	}
}

func TestLsToolAbortReturnsWhileUpstreamStyleWorkContinues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	operations := &continuingLsOperations{cancel: cancel, done: done}
	_, err := NewLsTool("/", &LsToolOptions{Operations: operations}).Execute(ctx, "call", map[string]any{"path": "/remote"}, nil)
	if !errors.Is(err, errOperationAborted) {
		t.Fatalf("error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ls worker stopped scheduling operations after abort")
	}
}

func TestLsToolPreAbortedCallWinsOverPathResolutionError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewLsTool(t.TempDir(), nil).Execute(ctx, "call", map[string]any{"path": "file:///%E0%A4%A"}, nil)
	if !errors.Is(err, errOperationAborted) {
		t.Fatalf("error = %v", err)
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
	if path == "/remote/second" {
		close(operations.done)
	}
	return LsPathStat{Directory: path == "/remote"}, nil
}

func (*continuingLsOperations) ReadDir(context.Context, string) ([]string, error) {
	return []string{"first", "second"}, nil
}
