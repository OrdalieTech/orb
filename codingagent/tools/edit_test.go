package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
)

type editOperationsFunc struct {
	access    func(context.Context, string) error
	readFile  func(context.Context, string) ([]byte, error)
	writeFile func(context.Context, string, string) error
}

func (operations editOperationsFunc) Access(ctx context.Context, path string) error {
	return operations.access(ctx, path)
}

func (operations editOperationsFunc) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return operations.readFile(ctx, path)
}

func (operations editOperationsFunc) WriteFile(ctx context.Context, path, content string) error {
	return operations.writeFile(ctx, path, content)
}

func TestEditPrepareArgumentsMatchesLegacyCompatibility(t *testing.T) {
	prepare := NewEditTool(t.TempDir(), nil).Spec().PrepareArguments
	valid := map[string]any{"path": "file.txt", "edits": []any{map[string]any{"oldText": "a", "newText": "b"}}}
	prepared, err := prepare(valid)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(prepared).Pointer() != reflect.ValueOf(valid).Pointer() {
		t.Fatal("valid input was copied")
	}

	legacy := map[string]any{
		"path": "file.txt", "edits": []any{map[string]any{"oldText": "a", "newText": "b"}},
		"oldText": "c", "newText": "d",
	}
	prepared, err = prepare(legacy)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"path": "file.txt",
		"edits": []any{
			map[string]any{"oldText": "a", "newText": "b"},
			map[string]any{"oldText": "c", "newText": "d"},
		},
	}
	if !reflect.DeepEqual(prepared, want) {
		t.Fatalf("legacy prepare = %#v, want %#v", prepared, want)
	}

	stringified := map[string]any{"path": "file.txt", "edits": `[{"oldText":"a","newText":"b"}]`}
	prepared, err = prepare(stringified)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(prepared).Pointer() != reflect.ValueOf(stringified).Pointer() {
		t.Fatal("stringified edits did not retain upstream object identity")
	}
	if _, ok := stringified["edits"].([]any); !ok {
		t.Fatalf("stringified edits were not parsed: %#v", stringified)
	}

	invalid := map[string]any{"path": "file.txt", "edits": "not json"}
	prepared, err = prepare(invalid)
	if err != nil || prepared.(map[string]any)["edits"] != "not json" {
		t.Fatalf("invalid stringified edits = %#v, %v", prepared, err)
	}

	surrogate := map[string]any{"path": "file.txt", "edits": `[{"oldText":"\ud800","newText":"x"}]`}
	prepared, err = prepare(surrogate)
	if err != nil {
		t.Fatal(err)
	}
	parsedSurrogate := prepared.(map[string]any)["edits"].([]any)[0].(map[string]any)["oldText"].(string)
	if got, want := []byte(parsedSurrogate), []byte{0xed, 0xa0, 0x80}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stringified lone surrogate = % x, want % x", got, want)
	}
}

func TestEditStringifiedLoneSurrogateDoesNotMatchReplacementCharacter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.txt")
	if err := os.WriteFile(path, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	params := map[string]any{"path": "invalid.txt", "edits": `[{"oldText":"\ud800","newText":"x"}]`}
	params = prepareEditArguments(params).(map[string]any)
	_, err := NewEditTool(dir, nil).Execute(context.Background(), "call", params, nil)
	if err == nil || !strings.Contains(err.Error(), "Could not find the exact text") {
		t.Fatalf("error = %v", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(content, []byte{0xff}) {
		t.Fatalf("file changed to % x", content)
	}
}

func TestEditToolPreservesBOMAndCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	if err := os.WriteFile(path, []byte("\ufefffirst\r\nsecond\r\nthird\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewEditTool(dir, nil).Execute(context.Background(), "call-1", map[string]any{
		"path":  "edit.txt",
		"edits": []any{map[string]any{"oldText": "second\n", "newText": "REPLACED\n"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "\ufefffirst\r\nREPLACED\r\nthird\r\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	details, ok := result.Details.(EditToolDetails)
	if !ok || !strings.Contains(details.Diff, "REPLACED") || !strings.Contains(details.Patch, "@@") || details.FirstChangedLine == nil || *details.FirstChangedLine != 2 {
		t.Fatalf("details = %#v", result.Details)
	}
}

func TestEditToolParallelDisjointEditsSerialize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parallel.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool(dir, nil)
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for _, edit := range []Edit{{OldText: "alpha", NewText: "ALPHA"}, {OldText: "beta", NewText: "BETA"}} {
		edit := edit
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := tool.Execute(context.Background(), "call", EditToolInput{Path: "parallel.txt", Edits: []Edit{edit}}, nil)
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "ALPHA\nBETA\ngamma\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestEditToolHoldsMutationQueueUntilAbortedWriteSettles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abort.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstWriteStarted := make(chan struct{})
	finishFirstWrite := make(chan struct{})
	secondWriteStarted := make(chan struct{})
	var onceFirst sync.Once
	var onceSecond sync.Once
	operations := editOperationsFunc{
		access:   func(context.Context, string) error { return nil },
		readFile: func(_ context.Context, path string) ([]byte, error) { return os.ReadFile(path) },
		writeFile: func(_ context.Context, path, content string) error {
			if content == "ALPHA\nbeta\n" {
				onceFirst.Do(func() { close(firstWriteStarted) })
				<-finishFirstWrite
			}
			if strings.Contains(content, "BETA") {
				onceSecond.Do(func() { close(secondWriteStarted) })
			}
			return os.WriteFile(path, []byte(content), 0o600)
		},
	}
	tool := NewEditTool(dir, &EditToolOptions{Operations: operations})
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, "call-1", EditToolInput{Path: "abort.txt", Edits: []Edit{{OldText: "alpha", NewText: "ALPHA"}}}, nil)
		firstDone <- err
	}()
	<-firstWriteStarted
	cancel()
	secondDone := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), "call-2", EditToolInput{Path: "abort.txt", Edits: []Edit{{OldText: "beta", NewText: "BETA"}}}, nil)
		secondDone <- err
	}()
	select {
	case <-secondWriteStarted:
		t.Fatal("second edit entered write before the aborted write settled")
	case <-time.After(20 * time.Millisecond):
	}
	close(finishFirstWrite)
	if err := <-firstDone; !errors.Is(err, errOperationAborted) {
		t.Fatalf("first edit error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "ALPHA\nBETA\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestEditToolAccessErrorsMatchUpstream(t *testing.T) {
	missing := NewEditTool(t.TempDir(), nil)
	_, err := missing.Execute(context.Background(), "call", EditToolInput{
		Path: "missing.txt", Edits: []Edit{{OldText: "a", NewText: "b"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "Error code: ENOENT") {
		t.Fatalf("missing error = %v", err)
	}

	operations := editOperationsFunc{
		access:    func(context.Context, string) error { return errors.New("disk offline") },
		readFile:  func(context.Context, string) ([]byte, error) { return []byte("hello\n"), nil },
		writeFile: func(context.Context, string, string) error { return nil },
	}
	broken := NewEditTool(t.TempDir(), &EditToolOptions{Operations: operations})
	_, err = broken.Execute(context.Background(), "call", EditToolInput{
		Path: "broken.txt", Edits: []Edit{{OldText: "hello", NewText: "world"}},
	}, nil)
	if err == nil || err.Error() != "Could not edit file: broken.txt. Error: disk offline." {
		t.Fatalf("unknown access error = %v", err)
	}
}

func TestEditToolRejectsEmptyEdits(t *testing.T) {
	_, err := NewEditTool(t.TempDir(), nil).Execute(context.Background(), "call", EditToolInput{Path: "file.txt"}, nil)
	if err == nil || err.Error() != "Edit tool input is invalid. edits must contain at least one replacement." {
		t.Fatalf("error = %v", err)
	}
}

func TestEditToolPreservesAccessErrorCodes(t *testing.T) {
	operations := editOperationsFunc{
		access:    func(context.Context, string) error { return codedTestError{code: "ELOOP"} },
		readFile:  func(context.Context, string) ([]byte, error) { return nil, nil },
		writeFile: func(context.Context, string, string) error { return nil },
	}
	_, err := NewEditTool(t.TempDir(), &EditToolOptions{Operations: operations}).Execute(context.Background(), "call", EditToolInput{
		Path: "loop.txt", Edits: []Edit{{OldText: "a", NewText: "b"}},
	}, nil)
	if err == nil || err.Error() != "Could not edit file: loop.txt. Error code: ELOOP." {
		t.Fatalf("error = %v", err)
	}
}

type codedTestError struct{ code string }

func (err codedTestError) Error() string { return err.code }
func (err codedTestError) Code() string  { return err.code }

func TestComputeEditsDiffNeedsReadAccessOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "read-only.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	diff, err := ComputeEditsDiff("read-only.txt", []Edit{{OldText: "before", NewText: "after"}}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Diff, "after") {
		t.Fatalf("diff = %#v", diff)
	}
	_, err = ComputeEditsDiff("missing.txt", []Edit{{OldText: "before", NewText: "after"}}, dir)
	if err == nil || !strings.Contains(err.Error(), "Error code: ENOENT") {
		t.Fatalf("missing error = %v", err)
	}
	nullPath := "bad\x00.txt"
	_, err = ComputeEditsDiff(nullPath, []Edit{{OldText: "before", NewText: "after"}}, dir)
	if want := "Could not edit file: " + nullPath + ". Error code: ERR_INVALID_ARG_VALUE."; err == nil || err.Error() != want {
		t.Fatalf("null path error = %v, want %q", err, want)
	}
}

func TestEditToolSchemaBytesMatchUpstreamTypeBox(t *testing.T) {
	want := `{"type":"object","required":["path","edits"],"properties":{"path":{"type":"string","description":"Path to the file to edit (relative or absolute)"},"edits":{"type":"array","items":{"type":"object","required":["oldText","newText"],"properties":{"oldText":{"type":"string","description":"Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},"newText":{"type":"string","description":"Replacement text for this targeted edit."}}},"description":"One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead."}}}`
	if got := string(NewEditTool(t.TempDir(), nil).Spec().Parameters); got != want {
		t.Fatalf("schema = %s, want %s", got, want)
	}
}

func TestEditToolDirectoryPassesAccessAndFailsAtRead(t *testing.T) {
	dir := t.TempDir()
	_, err := NewEditTool(dir, nil).Execute(context.Background(), "call", EditToolInput{
		Path: ".", Edits: []Edit{{OldText: "a", NewText: "b"}},
	}, nil)
	if err == nil || err.Error() != "EISDIR: illegal operation on a directory, read" {
		t.Fatalf("error = %v", err)
	}
}

var _ agent.AgentTool = (*editTool)(nil)
