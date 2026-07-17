package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/truncate"
)

func TestReadToolReadsTextAndPagesByLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	lines := make([]string, 100)
	for index := range lines {
		lines[index] = fmt.Sprintf("Line %d", index+1)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(dir, nil)

	result, err := tool.Execute(context.Background(), "call", ReadToolInput{Path: "@test.txt", Offset: floatPointer(41), Limit: floatPointer(20)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	output := toolResultText(t, result)
	if strings.Contains(output, "Line 40\n") || !strings.Contains(output, "Line 41") || !strings.Contains(output, "Line 60") || strings.Contains(output, "Line 61\n") {
		t.Fatalf("unexpected page output:\n%s", output)
	}
	if !strings.HasSuffix(output, "[40 more lines in file. Use offset=61 to continue.]") {
		t.Fatalf("missing continuation notice:\n%s", output)
	}
	if result.Details != nil {
		t.Fatalf("Details = %#v, want nil", result.Details)
	}
}

func TestReadToolTruncatesAtLineLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	lines := make([]string, 2500)
	for index := range lines {
		lines[index] = fmt.Sprintf("Line %d", index+1)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewReadTool(dir, nil).Execute(context.Background(), "call", map[string]any{"path": "large.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	output := toolResultText(t, result)
	if !strings.Contains(output, "Line 2000") || strings.Contains(output, "Line 2001") {
		t.Fatalf("wrong truncated content tail:\n%s", output[len(output)-min(500, len(output)):])
	}
	if !strings.HasSuffix(output, "[Showing lines 1-2000 of 2500. Use offset=2001 to continue.]") {
		t.Fatalf("wrong truncation notice: %q", output[len(output)-100:])
	}
	details, ok := result.Details.(ReadToolDetails)
	if !ok || details.Truncation == nil || !details.Truncation.Truncated || details.Truncation.TruncatedBy == nil || *details.Truncation.TruncatedBy != truncate.ReasonLines {
		t.Fatalf("Details = %#v", result.Details)
	}
}

func TestReadToolReportsOversizedFirstLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wide.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", truncate.DefaultMaxBytes+1)+"\nnext"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewReadTool(dir, nil).Execute(context.Background(), "call", map[string]any{"path": "wide.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "[Line 1 is 50.0KB, exceeds 50.0KB limit. Use bash: sed -n '1p' wide.txt | head -c 51200]"
	if got := toolResultText(t, result); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestReadToolPreservesFractionalOversizedLineTypeError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wide.txt"), []byte(strings.Repeat("x", truncate.DefaultMaxBytes+1)+"\nnext"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewReadTool(dir, nil).Execute(context.Background(), "call", map[string]any{"path": "wide.txt", "offset": 1.5}, nil)
	want := `The "string" argument must be of type string or an instance of Buffer or ArrayBuffer. Received undefined`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestReadToolRejectsOffsetBeyondEnd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "short.txt"), []byte("one\ntwo\nthree"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewReadTool(dir, nil).Execute(context.Background(), "call", map[string]any{"path": "short.txt", "offset": float64(100)}, nil)
	if err == nil || err.Error() != "Offset 100 is beyond end of file (3 lines total)" {
		t.Fatalf("error = %v", err)
	}
}

func TestReadToolPreservesJavaScriptFractionalNumberSemantics(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fractional.txt"), []byte("a\nb\nc\nd"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewReadTool(dir, nil).Execute(context.Background(), "call", map[string]any{
		"path": "fractional.txt", "offset": 0.5, "limit": 0.5,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolResultText(t, result), "\n\n[3.5 more lines in file. Use offset=1.5 to continue.]"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestReadToolDecodesInvalidUTF8LikeNode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "invalid.txt"), []byte{0xff, 0xff, '\n', 0xe2, 0x82}, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewReadTool(dir, nil).Execute(context.Background(), "call", map[string]any{"path": "invalid.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolResultText(t, result), "��\n�"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestReadToolSchemaBytesMatchUpstreamTypeBox(t *testing.T) {
	want := `{"type":"object","required":["path"],"properties":{"path":{"type":"string","description":"Path to the file to read (relative or absolute)"},"offset":{"type":"number","description":"Line number to start reading from (1-indexed)"},"limit":{"type":"number","description":"Maximum number of lines to read"}}}`
	if got := string(NewReadTool(t.TempDir(), nil).Spec().Parameters); got != want {
		t.Fatalf("schema = %s, want %s", got, want)
	}
}

func TestReadToolMissingErrorUsesNodeFilesystemShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.txt")
	_, err := NewReadTool(dir, nil).Execute(context.Background(), "call", map[string]any{"path": "missing.txt"}, nil)
	want := "ENOENT: no such file or directory, access '" + path + "'"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestReadToolRejectsNullPathLikeNode(t *testing.T) {
	dir := t.TempDir()
	_, err := NewReadTool(dir, nil).Execute(context.Background(), "call", map[string]any{"path": "a\x00b"}, nil)
	wantPath := filepath.Join(dir, "a\x00b")
	want := "The argument 'path' must be a string, Uint8Array, or URL without null bytes. Received " + nodeInspectString(wantPath)
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestReadToolAbortDoesNotWaitForReadOperation(t *testing.T) {
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	readFinished := make(chan struct{})
	operations := &blockingReadOperations{readStarted: readStarted, releaseRead: releaseRead, readFinished: readFinished}
	tool := NewReadTool(t.TempDir(), &ReadToolOptions{Operations: operations})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, "call", map[string]any{"path": "remote.txt"}, nil)
		done <- err
	}()
	<-readStarted
	cancel()
	if err := <-done; !errors.Is(err, errOperationAborted) {
		t.Fatalf("error = %v", err)
	}
	close(releaseRead)
	<-readFinished
}

func TestReadToolAbortWinsWhenOperationCancelsBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tool := NewReadTool(t.TempDir(), &ReadToolOptions{Operations: cancelingReadOperations{cancel: cancel}})
	_, err := tool.Execute(ctx, "call", map[string]any{"path": "remote.txt"}, nil)
	if !errors.Is(err, errOperationAborted) {
		t.Fatalf("error = %v", err)
	}
}

type cancelingReadOperations struct{ cancel context.CancelFunc }

func (cancelingReadOperations) Access(context.Context, string) error { return nil }
func (operations cancelingReadOperations) ReadFile(context.Context, string) ([]byte, error) {
	operations.cancel()
	return []byte("late"), nil
}

type blockingReadOperations struct {
	readStarted  chan struct{}
	releaseRead  chan struct{}
	readFinished chan struct{}
}

func (*blockingReadOperations) Access(context.Context, string) error { return nil }

func (operations *blockingReadOperations) ReadFile(context.Context, string) ([]byte, error) {
	close(operations.readStarted)
	<-operations.releaseRead
	close(operations.readFinished)
	return []byte("late"), nil
}

func toolResultText(t *testing.T, result agent.AgentToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("content length = %d, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(*ai.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *ai.TextContent", result.Content[0])
	}
	return text.Text
}

func floatPointer(value float64) *float64 { return &value }
