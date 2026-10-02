package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

// Nested tool calls are the calls a tool makes while it runs
// (ToolContext.ExecuteTool), as orchestrating tools do. The agent loop does not
// know about them: the session runs each through engine.RunToolCall with its
// hooks, emits tool_execution_* events with ParentToolCallID, and records the
// calls and their usage on the model-issued call's tool result message.

// Limits of the record on a tool result: arguments over the per-call or total
// size are omitted, calls beyond the count are dropped, and the record is then
// marked incomplete.
const (
	nestedMaxCalls                = 256
	nestedMaxArgumentBytesPerCall = 8 << 10
	nestedMaxArgumentBytesTotal   = 32 << 10
	nestedMaxErrorChars           = 500
)

// nestedRecorder collects the nested calls of one model-issued call, those of
// nested tools included.
type nestedRecorder struct {
	mu            sync.Mutex
	calls         []*ai.NestedToolCallRecord
	started       map[*ai.NestedToolCallRecord]time.Time
	complete      bool
	argumentBytes int
	usage         *ai.Usage
}

func (recorder *nestedRecorder) start(call *ai.ToolCall, now time.Time) *ai.NestedToolCallRecord {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.calls) >= nestedMaxCalls {
		recorder.complete = false
		return nil
	}
	record := &ai.NestedToolCallRecord{ID: call.ID, Name: call.Name, Status: "unfinished"}
	encoded, _ := ai.Marshal(call.Arguments)
	if len(encoded) > nestedMaxArgumentBytesPerCall || recorder.argumentBytes+len(encoded) > nestedMaxArgumentBytesTotal {
		record.ArgumentsBytes = len(encoded)
		recorder.complete = false
	} else {
		record.Arguments = encoded
		recorder.argumentBytes += len(encoded)
	}
	recorder.calls = append(recorder.calls, record)
	recorder.started[record] = now
	return record
}

func (recorder *nestedRecorder) finish(record *ai.NestedToolCallRecord, outcome engine.AgentToolCallOutcome, now time.Time) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	// Nested results are not persisted, so their usage counts only here.
	if usage := outcome.Result.Usage; usage != nil {
		recorder.usage = combinedUsage(recorder.usage, usage)
	}
	if record == nil {
		return
	}
	record.Status = "ok"
	if outcome.IsError {
		record.Status = "error"
		var text []string
		for _, block := range outcome.Result.Content {
			if value, ok := block.(*ai.TextContent); ok {
				text = append(text, value.Text)
			}
		}
		record.Error = truncateRunes(strings.Join(text, "\n"), nestedMaxErrorChars)
	}
	duration := now.Sub(recorder.started[record]).Milliseconds()
	record.DurationMS = &duration
	delete(recorder.started, record)
}

func (recorder *nestedRecorder) snapshot() (*ai.NestedToolCalls, *ai.Usage) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.calls) == 0 && recorder.complete {
		return nil, recorder.usage
	}
	calls := &ai.NestedToolCalls{Calls: make([]ai.NestedToolCallRecord, len(recorder.calls)), Complete: recorder.complete}
	for index, call := range recorder.calls {
		calls.Calls[index] = *call
		calls.Complete = calls.Complete && call.Status != "unfinished"
	}
	return calls, recorder.usage
}

type nestedScope struct {
	recorder *nestedRecorder
	next     int
	// holdsQueue is set inside a call holding the exclusive queue, so its own
	// nested calls do not wait on it.
	holdsQueue bool
}

// nestedToolCalls runs and records a session's nested calls.
type nestedToolCalls struct {
	mu     sync.Mutex
	scopes map[string]*nestedScope
	// queue serializes nested calls that must not run concurrently.
	queue chan struct{}
}

type parentToolCallKey struct{}

// parentToolCall is the calling tool's id when hooks run for a nested call.
func parentToolCall(ctx context.Context) string {
	parent, _ := ctx.Value(parentToolCallKey{}).(string)
	return parent
}

// executeNestedTool runs name on behalf of the tool call parentID. The call gets
// the id "<parentID>/<n>"; tool failures come back as error outcomes.
func (runtime *SessionRuntime) executeNestedTool(ctx context.Context, parentID, name string, args any, onUpdate engine.AgentToolUpdateCallback) engine.AgentToolCallOutcome {
	nested := runtime.nested
	nested.mu.Lock()
	scope := nested.scopes[parentID]
	if scope == nil {
		scope = &nestedScope{recorder: &nestedRecorder{started: map[*ai.NestedToolCallRecord]time.Time{}, complete: true}, next: 1}
		nested.scopes[parentID] = scope
	}
	arguments, _ := args.(map[string]any)
	if arguments == nil {
		arguments = map[string]any{}
	}
	call := &ai.ToolCall{ID: fmt.Sprintf("%s/%d", parentID, scope.next), Name: name, Arguments: arguments}
	scope.next++
	nested.mu.Unlock()

	record := scope.recorder.start(call, time.Now())
	runtime.emitNestedEvent(ctx, engine.ToolExecutionStartEvent{ToolCallID: call.ID, ToolName: name, Args: arguments, ParentToolCallID: parentID})
	tools, model, assistant, agentContext, sequential := runtime.nestedCallInputs()
	exclusive := !scope.holdsQueue && (sequential || slicesContainsSequential(tools, name))
	if exclusive {
		select {
		case nested.queue <- struct{}{}:
		case <-ctx.Done():
			outcome := engine.AgentToolCallOutcome{ToolCall: call, Result: engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "Operation aborted"}}}, IsError: true}
			scope.recorder.finish(record, outcome, time.Now())
			return outcome
		}
	}
	nested.mu.Lock()
	nested.scopes[call.ID] = &nestedScope{recorder: scope.recorder, next: 1, holdsQueue: scope.holdsQueue || exclusive}
	nested.mu.Unlock()
	var outcome engine.AgentToolCallOutcome
	if assistant == nil {
		outcome = engine.AgentToolCallOutcome{ToolCall: call, Result: engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "No assistant message issued this call"}}}, IsError: true}
	} else {
		hookContext := context.WithValue(ctx, parentToolCallKey{}, parentID)
		outcome = engine.RunToolCall(hookContext, call, engine.RunToolCallOptions{
			Tools: tools, AssistantMessage: assistant, Context: agentContext, Model: model,
			BeforeToolCall: runtime.beforeExtensionToolCall, AfterToolCall: runtime.afterExtensionToolCall,
			OnUpdate: func(partial engine.AgentToolResult) {
				if onUpdate != nil {
					onUpdate(partial)
				}
				runtime.emitNestedEvent(ctx, engine.ToolExecutionUpdateEvent{ToolCallID: call.ID, ToolName: name, Args: arguments, PartialResult: partial, ParentToolCallID: parentID})
			},
		})
	}
	nested.mu.Lock()
	delete(nested.scopes, call.ID)
	nested.mu.Unlock()
	if exclusive {
		<-nested.queue
	}
	scope.recorder.finish(record, outcome, time.Now())
	runtime.emitNestedEvent(ctx, engine.ToolExecutionEndEvent{ToolCallID: call.ID, ToolName: name, Result: outcome.Result, IsError: outcome.IsError, ParentToolCallID: parentID})
	return outcome
}

func slicesContainsSequential(tools []engine.AgentTool, name string) bool {
	for _, tool := range tools {
		if spec := tool.Spec(); spec.Name == name {
			return spec.ExecutionMode == engine.ToolExecutionSequential
		}
	}
	return false
}

func (runtime *SessionRuntime) emitNestedEvent(ctx context.Context, event engine.AgentEvent) {
	runtime.emit(runtime.extensionLifecycleEvent(ctx, event))
}

// nestedCallInputs are the callable tools and the agent state a nested call runs against.
func (runtime *SessionRuntime) nestedCallInputs() ([]engine.AgentTool, *ai.Model, *ai.AssistantMessage, engine.AgentContext, bool) {
	snapshot := runtime.agent.State()
	var assistant *ai.AssistantMessage
	for index := len(snapshot.Messages) - 1; index >= 0 && assistant == nil; index-- {
		assistant = asAssistant(snapshot.Messages[index])
	}
	agentContext := engine.AgentContext{SystemPrompt: snapshot.SystemPrompt, Messages: snapshot.Messages, Tools: snapshot.Tools}
	sequential := runtime.agent.ToolExecution() == engine.ToolExecutionSequential
	state := runtime.extensionState
	if state == nil {
		return snapshot.Tools, snapshot.Model, assistant, agentContext, sequential
	}
	active := map[string]struct{}{}
	for _, tool := range snapshot.Tools {
		active[tool.Spec().Name] = struct{}{}
	}
	state.mu.Lock()
	callable := state.callableTools(active)
	state.mu.Unlock()
	return callable, snapshot.Model, assistant, agentContext, sequential
}

// recordNestedCalls puts the nested calls of a model-issued call, and their
// usage, on its tool result message as it starts.
func (runtime *SessionRuntime) recordNestedCalls(message *ai.ToolResultMessage) {
	nested := runtime.nested
	nested.mu.Lock()
	scope := nested.scopes[message.ToolCallID]
	delete(nested.scopes, message.ToolCallID)
	nested.mu.Unlock()
	if scope == nil {
		return
	}
	calls, usage := scope.recorder.snapshot()
	message.NestedCalls = calls
	if usage != nil {
		message.Usage = combinedUsage(message.Usage, usage)
	}
}

func combinedUsage(left, right *ai.Usage) *ai.Usage {
	if left == nil {
		copy := *right
		return &copy
	}
	sum := *left
	sum.Input += right.Input
	sum.Output += right.Output
	sum.CacheRead += right.CacheRead
	sum.CacheWrite += right.CacheWrite
	sum.TotalTokens += right.TotalTokens
	sum.Cost.Input += right.Cost.Input
	sum.Cost.Output += right.Cost.Output
	sum.Cost.CacheRead += right.Cost.CacheRead
	sum.Cost.CacheWrite += right.Cost.CacheWrite
	sum.Cost.Total += right.Cost.Total
	return &sum
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// nestedLoadout lists the tools ExecuteTool can call.
func (runtime *SessionRuntime) nestedLoadout() []extensions.LoadoutTool {
	tools, _, _, _, _ := runtime.nestedCallInputs()
	listed := make([]extensions.LoadoutTool, 0, len(tools))
	for _, tool := range tools {
		spec := tool.Spec()
		listed = append(listed, extensions.LoadoutTool{Name: spec.Name, Label: spec.Label, Description: spec.Description, Parameters: spec.Parameters})
	}
	return listed
}
