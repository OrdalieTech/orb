package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
)

func TestWriteToolCreatesParentsAndWritesContent(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir, nil)
	result, err := tool.Execute(context.Background(), "call", WriteToolInput{
		Path: "nested/dir/file.txt", Content: "hello\n",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result); got != "Successfully wrote 6 bytes to nested/dir/file.txt" {
		t.Fatalf("result = %q", got)
	}
	content, err := os.ReadFile(filepath.Join(dir, "nested", "dir", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello\n" {
		t.Fatalf("content = %q", content)
	}
}

func TestWriteToolReportsJavaScriptStringLength(t *testing.T) {
	result, err := NewWriteTool(t.TempDir(), nil).Execute(context.Background(), "call", map[string]any{
		"path": "emoji.txt", "content": "😀x",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result); got != "Successfully wrote 3 bytes to emoji.txt" {
		t.Fatalf("result = %q", got)
	}
}

func TestWriteToolEncodesWTF8SurrogatesLikeNode(t *testing.T) {
	dir := t.TempDir()
	content := string([]byte{0xed, 0xa0, 0xbd, 0xed, 0xb8, 0x80}) + string([]byte{0xed, 0xa0, 0x80})
	result, err := NewWriteTool(dir, nil).Execute(context.Background(), "call", map[string]any{
		"path": "surrogate.txt", "content": content,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result); got != "Successfully wrote 3 bytes to surrogate.txt" {
		t.Fatalf("result = %q", got)
	}
	written, err := os.ReadFile(filepath.Join(dir, "surrogate.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(written), "😀�"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestWriteToolSchemaBytesMatchUpstreamTypeBox(t *testing.T) {
	want := `{"type":"object","required":["path","content"],"properties":{"path":{"type":"string","description":"Path to the file to write (relative or absolute)"},"content":{"type":"string","description":"Content to write to the file"}}}`
	if got := string(NewWriteTool(t.TempDir(), nil).Spec().Parameters); got != want {
		t.Fatalf("schema = %s, want %s", got, want)
	}
}

func TestWriteToolMkdirExistingFileUsesNodeEEXIST(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "parent")
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewWriteTool(dir, nil).Execute(context.Background(), "call", map[string]any{
		"path": "parent/child.txt", "content": "x",
	}, nil)
	want := "EEXIST: file already exists, mkdir '" + parent + "'"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestWriteToolMkdirIntermediateFileKeepsRequestedNodePath(t *testing.T) {
	dir := t.TempDir()
	intermediate := filepath.Join(dir, "file")
	if err := os.WriteFile(intermediate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewWriteTool(dir, nil).Execute(context.Background(), "call", map[string]any{
		"path": "file/child/output.txt", "content": "x",
	}, nil)
	wantPath := filepath.Join(intermediate, "child")
	want := "ENOTDIR: not a directory, mkdir '" + wantPath + "'"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestWriteToolMkdirDanglingSymlinkMatchesNodeENOENT(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "missing"), link); err != nil {
		t.Fatal(err)
	}
	_, err := NewWriteTool(dir, nil).Execute(context.Background(), "call", map[string]any{
		"path": "link/output.txt", "content": "x",
	}, nil)
	want := "ENOENT: no such file or directory, mkdir '" + link + "'"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestWriteToolMkdirBelowDanglingSymlinkMatchesNodeENOTDIR(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "missing"), link); err != nil {
		t.Fatal(err)
	}
	_, err := NewWriteTool(dir, nil).Execute(context.Background(), "call", map[string]any{
		"path": "link/child/output.txt", "content": "x",
	}, nil)
	want := "ENOTDIR: not a directory, mkdir '" + link + "'"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestNodeFilesystemErrorUsesUnderlyingReadWriteOperation(t *testing.T) {
	writeError := asNodeFilesystemError("open", "/dev/full", &os.PathError{Op: "write", Path: "/dev/full", Err: syscall.ENOSPC})
	if got, want := writeError.Error(), "ENOSPC: no space left on device, write"; got != want {
		t.Fatalf("write error = %q, want %q", got, want)
	}
	readError := asNodeFilesystemError("open", "/proc/self/mem", &os.PathError{Op: "read", Path: "/proc/self/mem", Err: syscall.EIO})
	if got, want := readError.Error(), "EIO: i/o error, read"; got != want {
		t.Fatalf("read error = %q, want %q", got, want)
	}
}

func TestNodeFilesystemErrorAtKeepsRequestedNodeOperationAndPath(t *testing.T) {
	underlying := &os.PathError{Op: "lstat", Path: "/intermediate", Err: syscall.EACCES}
	for _, test := range []struct {
		operation string
		path      string
	}{
		{operation: "realpath", path: "/requested/file"},
		{operation: "scandir", path: "/requested/dir"},
		{operation: "mkdir", path: "/requested/parent"},
	} {
		got := asNodeFilesystemErrorAt(test.operation, test.path, underlying)
		want := "EACCES: permission denied, " + test.operation + " '" + test.path + "'"
		if got == nil || got.Error() != want {
			t.Fatalf("%s error = %v, want %q", test.operation, got, want)
		}
	}
}

func TestWriteToolKeepsQueueLockedUntilAbortedWriteSettles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abort-write.txt")
	operations := &orderedWriteOperations{
		firstStarted:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	tool := NewWriteTool(dir, &WriteToolOptions{Operations: operations})
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, "first", map[string]any{"path": path, "content": "first\n"}, nil)
		firstDone <- err
	}()
	<-operations.firstStarted
	cancel()

	key, err := mutationQueueKey(path)
	if err != nil {
		t.Fatal(err)
	}
	mutationQueues.Lock()
	firstEntry := mutationQueues.byPath[key]
	mutationQueues.Unlock()
	secondDone := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), "second", map[string]any{"path": path, "content": "second\n"}, nil)
		secondDone <- err
	}()
	waitForQueueSuccessor(t, key, firstEntry)
	select {
	case <-operations.secondStarted:
		t.Fatal("second write started before aborted write settled")
	default:
	}

	close(operations.releaseFirst)
	if err := <-firstDone; !errors.Is(err, errOperationAborted) {
		t.Fatalf("first error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second error = %v", err)
	}
	if !operations.didFirstSettle() {
		t.Fatal("second write ran before first operation settled")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "second\n" {
		t.Fatalf("content = %q", content)
	}
}

func TestWriteToolParallelReservationsPreserveSourceOrder(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir, nil)
	preparer, ok := tool.(agent.ParallelExecutionPreparer)
	if !ok {
		t.Fatal("write tool does not reserve parallel execution")
	}
	firstContext, releaseFirst, err := preparer.PrepareParallelExecution(context.Background(), map[string]any{"path": "ordered.txt", "content": "first"})
	if err != nil {
		t.Fatal(err)
	}
	secondContext, releaseSecond, err := preparer.PrepareParallelExecution(context.Background(), map[string]any{"path": "ordered.txt", "content": "second"})
	if err != nil {
		releaseFirst()
		t.Fatal(err)
	}
	firstReservation := firstContext.Value(mutationReservationContextKey{}).(*mutationReservation)
	secondReservation := secondContext.Value(mutationReservationContextKey{}).(*mutationReservation)
	if secondReservation.previous != firstReservation.current {
		releaseFirst()
		releaseSecond()
		t.Fatal("same-file reservations were not chained in source order")
	}

	secondDone := make(chan error, 1)
	go func() {
		defer releaseSecond()
		_, executeErr := tool.Execute(secondContext, "second", map[string]any{"path": "ordered.txt", "content": "second"}, nil)
		secondDone <- executeErr
	}()
	firstDone := make(chan error, 1)
	go func() {
		defer releaseFirst()
		_, executeErr := tool.Execute(firstContext, "first", map[string]any{"path": "ordered.txt", "content": "first"}, nil)
		firstDone <- executeErr
	}()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(dir, "ordered.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(written), "second"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestAgentParallelWritesLeaveLaterSourceCallContent(t *testing.T) {
	dir := t.TempDir()
	responses := []*ai.AssistantMessage{
		{
			Content: ai.AssistantContent{
				&ai.ToolCall{ID: "first", Name: "write", Arguments: map[string]any{"path": "ordered.txt", "content": "first"}},
				&ai.ToolCall{ID: "second", Name: "write", Arguments: map[string]any{"path": "ordered.txt", "content": "second"}},
			},
			API: "test", Provider: "test", Model: "test-model", Usage: ai.Usage{Cost: ai.Cost{}}, StopReason: ai.StopReasonToolUse,
		},
		{API: "test", Provider: "test", Model: "test-model", Usage: ai.Usage{Cost: ai.Cost{}}, StopReason: ai.StopReasonStop},
	}
	stream := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		message := responses[0]
		responses = responses[1:]
		return func(yield func(ai.AssistantMessageEvent, error) bool) {
			yield(ai.DoneEvent{Reason: message.StopReason, Message: message}, nil)
		}, nil
	}
	_, err := agent.RunLoop(context.Background(), agent.AgentMessages{&ai.UserMessage{Content: ai.NewUserText("write twice")}}, agent.AgentContext{
		Tools: []agent.AgentTool{NewWriteTool(dir, nil)},
	}, agent.AgentLoopConfig{
		Model:         &ai.Model{ID: "test-model", API: "test", Provider: "test"},
		ToolExecution: agent.ToolExecutionParallel,
	}, nil, stream)
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(dir, "ordered.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(written), "second"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

type orderedWriteOperations struct {
	firstStarted  chan struct{}
	releaseFirst  chan struct{}
	secondStarted chan struct{}
	firstSettled  bool
	mutex         sync.Mutex
}

func (operations *orderedWriteOperations) didFirstSettle() bool {
	operations.mutex.Lock()
	defer operations.mutex.Unlock()
	return operations.firstSettled
}

func (*orderedWriteOperations) MkdirAll(context.Context, string) error { return nil }

func (operations *orderedWriteOperations) WriteFile(_ context.Context, path, content string) error {
	if content == "first\n" {
		close(operations.firstStarted)
		<-operations.releaseFirst
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
		operations.mutex.Lock()
		operations.firstSettled = true
		operations.mutex.Unlock()
		return nil
	}
	if content == "second\n" {
		operations.mutex.Lock()
		settled := operations.firstSettled
		operations.mutex.Unlock()
		if !settled {
			return errors.New("first write has not settled")
		}
		close(operations.secondStarted)
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func waitForQueueSuccessor(t *testing.T, key string, previous *mutationQueueEntry) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mutationQueues.Lock()
		current := mutationQueues.byPath[key]
		mutationQueues.Unlock()
		if current != nil && current != previous {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for queue successor for %s", key)
		}
		runtime.Gosched()
	}
}

func TestWriteToolRejectsInvalidContent(t *testing.T) {
	_, err := NewWriteTool(t.TempDir(), nil).Execute(context.Background(), "call", map[string]any{
		"path": "file.txt", "content": 42,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "content must be a string") {
		t.Fatalf("error = %v", err)
	}
}
