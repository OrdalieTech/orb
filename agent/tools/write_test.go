package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
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
	if got := toolResultText(t, result); got != "Successfully wrote to nested/dir/file.txt" {
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

func TestWriteToolMkdir(t *testing.T) {
	for _, test := range []struct {
		name         string
		danglingLink bool // the blocker is a symlink to a missing target instead of a file
		blocker      string
		path         string
		want         string
		mkdirPath    string
	}{
		{"ExistingFileUsesNodeEEXIST", false, "parent", "parent/child.txt", "EEXIST: file already exists", "parent"},
		{"IntermediateFileKeepsRequestedNodePath", false, "file", "file/child/output.txt", "ENOTDIR: not a directory", "file/child"},
		{"DanglingSymlinkMatchesNodeENOENT", true, "link", "link/output.txt", "ENOENT: no such file or directory", "link"},
		{"BelowDanglingSymlinkMatchesNodeENOTDIR", true, "link", "link/child/output.txt", "ENOTDIR: not a directory", "link"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			blocker := filepath.Join(dir, test.blocker)
			var err error
			if test.danglingLink {
				err = os.Symlink(filepath.Join(dir, "missing"), blocker)
			} else {
				err = os.WriteFile(blocker, nil, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = NewWriteTool(dir, nil).Execute(context.Background(), "call", map[string]any{
				"path": test.path, "content": "x",
			}, nil)
			want := test.want + ", mkdir '" + filepath.Join(dir, filepath.FromSlash(test.mkdirPath)) + "'"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
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
	preparer, ok := tool.(engine.ParallelExecutionPreparer)
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
	_, err := engine.RunLoop(context.Background(), engine.AgentMessages{&ai.UserMessage{Content: ai.NewUserText("write twice")}}, engine.AgentContext{
		Tools: []engine.AgentTool{NewWriteTool(dir, nil)},
	}, engine.AgentLoopConfig{
		Model:         &ai.Model{ID: "test-model", API: "test", Provider: "test"},
		ToolExecution: engine.ToolExecutionParallel,
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
