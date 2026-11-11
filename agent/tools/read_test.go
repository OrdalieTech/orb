package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/truncate"
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

func TestReadToolReturnsImageAttachmentForVisionAndTextOnlyModels(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tiny.png"), mustBase64(t, tinyPNG), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(dir, nil)
	result, err := tool.Execute(context.Background(), "call", map[string]any{"path": "tiny.png"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 2 {
		t.Fatalf("vision content = %#v", result.Content)
	}
	imageBlock, ok := result.Content[1].(*ai.ImageContent)
	if !ok || imageBlock.Data != tinyPNG || imageBlock.MimeType != "image/png" {
		t.Fatalf("image block = %#v", result.Content[1])
	}
	ctx := engine.WithToolExecutionModel(context.Background(), &ai.Model{Input: ai.InputModalities{ai.InputText}})
	result, err = tool.Execute(ctx, "call", map[string]any{"path": "tiny.png"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	textBlock, textOK := result.Content[0].(*ai.TextContent)
	if len(result.Content) != 2 || !textOK || !strings.Contains(textBlock.Text, "Current model does not support images") {
		t.Fatalf("text-only content = %#v", result.Content)
	}
	imageBlock, ok = result.Content[1].(*ai.ImageContent)
	if !ok || imageBlock.Data != tinyPNG || imageBlock.MimeType != "image/png" {
		t.Fatalf("text-only image block = %#v", result.Content[1])
	}
}

func TestReadToolConvertsBMPWithAutoResizeDisabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tiny.bmp"), tinyBMP(), 0o600); err != nil {
		t.Fatal(err)
	}
	autoResize := false
	result, err := NewReadTool(dir, &ReadToolOptions{AutoResizeImages: &autoResize}).Execute(context.Background(), "call", map[string]any{"path": "tiny.bmp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 2 {
		t.Fatalf("bmp content = %#v", result.Content)
	}
	textBlock, textOK := result.Content[0].(*ai.TextContent)
	if !textOK || !strings.Contains(textBlock.Text, "converted from image/bmp to image/png") {
		t.Fatalf("bmp content = %#v", result.Content)
	}
	if imageBlock, ok := result.Content[1].(*ai.ImageContent); !ok || imageBlock.MimeType != "image/png" {
		t.Fatalf("bmp attachment = %#v", result.Content[1])
	}
}

// Streaming stores only the lines truncateHead can show; the result must match truncating the
// whole selection, and operations without OpenFile must read the same (#9654).
func TestReadToolStreamingMatchesWholeSelection(t *testing.T) {
	dir := t.TempDir()
	numbered := func(count, width int) string {
		lines := make([]string, count)
		for index := range lines {
			lines[index] = fmt.Sprintf("%0*d", width, index+1)
		}
		return strings.Join(lines, "\n")
	}
	files := map[string]string{
		"lines.txt":      numbered(2001, 4) + "\n",
		"exact.txt":      numbered(2000, 4) + "\n",
		"bytes.txt":      numbered(600, 99),
		"long-first.txt": strings.Repeat("\U0001F600", truncate.DefaultMaxBytes) + "\nshort",
		"endings.txt":    "a\r\nb\rc\n\n",
	}
	ranges := [][2]any{{nil, nil}, {2.0, nil}, {2.5, 3.0}, {1.0, 0.0}, {nil, -1.0}, {1999.0, 5.0}, {9999.0, nil}}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		allLines := strings.Split(content, "\n")
		for _, bounds := range ranges {
			args := map[string]any{"path": name}
			start, end := 0, len(allLines)
			if bounds[0] != nil {
				args["offset"] = bounds[0]
				start = jsSliceIndex(bounds[0].(float64)-1, len(allLines))
			}
			if bounds[1] != nil {
				args["limit"] = bounds[1]
				end = jsSliceIndex(math.Min(float64(start)+bounds[1].(float64), float64(len(allLines))), len(allLines))
			}
			streamed, streamErr := NewReadTool(dir, nil).Execute(context.Background(), "call", args, nil)
			buffered, bufferErr := NewReadTool(dir, &ReadToolOptions{Operations: wholeFileReadOperations{}}).Execute(context.Background(), "call", args, nil)
			if fmt.Sprint(streamErr) != fmt.Sprint(bufferErr) || !reflect.DeepEqual(streamed, buffered) {
				t.Fatalf("%s %v: streamed %#v, %v; whole file %#v, %v", name, bounds, streamed, streamErr, buffered, bufferErr)
			}
			if streamErr != nil {
				continue
			}
			want := truncate.TruncateHead(strings.Join(allLines[start:max(start, end)], "\n"))
			details, _ := streamed.Details.(ReadToolDetails)
			if want.Truncated && (details.Truncation == nil || !reflect.DeepEqual(*details.Truncation, want)) {
				t.Fatalf("%s %v: truncation %#v, want %#v", name, bounds, details.Truncation, want)
			}
			if text := toolResultText(t, streamed); !want.FirstLineExceedsLimit && !strings.HasPrefix(text, want.Content) {
				t.Fatalf("%s %v: output %q, want prefix %q", name, bounds, text, want.Content)
			}
		}
	}
}

type wholeFileReadOperations struct{}

func (wholeFileReadOperations) Access(context.Context, string) error { return nil }
func (wholeFileReadOperations) ReadFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
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

func toolResultText(t *testing.T, result engine.AgentToolResult) string {
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
