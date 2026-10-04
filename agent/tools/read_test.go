package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
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
