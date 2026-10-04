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
