package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
)

func TestAgentCoalescesMissingInitialModelToUnknownSentinel(t *testing.T) {
	created := NewAgent(WithInitialState(AgentState{}))
	model := created.State().Model
	if model == nil || model.Provider != "unknown" || model.ID != "unknown" || model.API != "unknown" {
		t.Fatalf("default model = %#v", model)
	}
}

func TestAgentStatePreservesRawCustomMessageType(t *testing.T) {
	original := json.RawMessage(`{"role":"custom","content":"original"}`)
	created := NewAgent(WithInitialState(AgentState{Model: loopModel(), Messages: AgentMessages{original}}))
	original[0] = 'x'
	state := created.State()
	preserved, ok := state.Messages[0].(json.RawMessage)
	if !ok || string(preserved) != `{"role":"custom","content":"original"}` {
		t.Fatalf("preserved = %T %q", state.Messages[0], preserved)
	}
	preserved[0] = 'y'
	again := created.State().Messages[0].(json.RawMessage)
	if string(again) != `{"role":"custom","content":"original"}` {
		t.Fatalf("state raw message was aliased: %q", again)
	}
}

func TestAgentAwaitsOrderedSubscribersBeforeIdle(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "done"})}}
	agent := NewAgent(
		WithInitialState(AgentState{Model: loopModel()}),
		WithStreamFn(responses.stream),
		WithClock(func() int64 { return 77 }),
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	var orderMu sync.Mutex
	var order []int
	agent.Subscribe(func(_ context.Context, event AgentEvent) error {
		if _, ok := event.(AgentEndEvent); ok {
			orderMu.Lock()
			order = append(order, 1)
			orderMu.Unlock()
			close(entered)
			<-release
		}
		return nil
	})
	agent.Subscribe(func(_ context.Context, event AgentEvent) error {
		if _, ok := event.(AgentEndEvent); ok {
			orderMu.Lock()
			order = append(order, 2)
			orderMu.Unlock()
		}
		return nil
	})

	promptDone := make(chan error, 1)
	go func() { promptDone <- agent.Prompt(context.Background(), "hello") }()
	<-entered
	if !agent.State().IsStreaming {
		t.Fatal("agent became idle before agent_end subscribers settled")
	}
	waitContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := agent.WaitForIdle(waitContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForIdle while listener blocked = %v", err)
	}
	close(release)
	if err := <-promptDone; err != nil {
		t.Fatal(err)
	}
	if err := agent.WaitForIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := joinInts(order); got != "1,2" {
		t.Fatalf("subscriber order = %s", got)
	}
	state := agent.State()
	if state.IsStreaming || len(state.Messages) != 2 {
		t.Fatalf("settled state = %#v", state)
	}
	user := state.Messages[0].(*ai.UserMessage)
	if user.Timestamp != 77 || user.Content.Text != nil || len(user.Content.Blocks) != 1 {
		t.Fatalf("normalized user message = %#v", user)
	}
}

func TestAgentConvertsThrownRunFailureToLifecycle(t *testing.T) {
	agent := NewAgent(
		WithInitialState(AgentState{Model: loopModel()}),
		WithStreamFn(func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
			return nil, errors.New("provider exploded")
		}),
		WithClock(func() int64 { return 88 }),
	)
	var eventTypes []AgentEventType
	var ended AgentEndEvent
	agent.Subscribe(func(_ context.Context, event AgentEvent) error {
		eventTypes = append(eventTypes, event.Type())
		if value, ok := event.(AgentEndEvent); ok {
			ended = value
		}
		return nil
	})
	if err := agent.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("failure should be represented as events: %v", err)
	}
	want := "agent_start,turn_start,message_start,message_end,message_start,message_end,turn_end,agent_end"
	if got := joinEventTypes(eventTypes); got != want {
		t.Fatalf("event sequence = %s, want %s", got, want)
	}
	if len(ended.Messages) != 1 {
		t.Fatalf("agent_end messages = %#v", ended.Messages)
	}
	failure := ended.Messages[0].(*ai.AssistantMessage)
	if failure.StopReason != ai.StopReasonError || failure.ErrorMessage == nil || *failure.ErrorMessage != "provider exploded" || failure.Timestamp != 88 {
		t.Fatalf("failure message = %#v", failure)
	}
	state := agent.State()
	if len(state.Messages) != 2 || state.ErrorMessage == nil || *state.ErrorMessage != "provider exploded" {
		t.Fatalf("failure state = %#v", state)
	}
}

func TestAgentContinueDrainsAssistantTailSteeringOneAtATime(t *testing.T) {
	initial := loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "waiting"})
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "first response"}),
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "second response"}),
	}}
	agent := NewAgent(
		WithInitialState(AgentState{Model: loopModel(), Messages: AgentMessages{initial}}),
		WithStreamFn(responses.stream),
	)
	agent.Steer(loopUser("first steering"))
	agent.Steer(loopUser("second steering"))
	if err := agent.Continue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(responses.contexts) != 2 {
		t.Fatalf("provider calls = %d", len(responses.contexts))
	}
	if got := len(responses.contexts[0].Messages); got != 2 {
		t.Fatalf("first request messages = %d, want 2", got)
	}
	if got := len(responses.contexts[1].Messages); got != 4 {
		t.Fatalf("second request messages = %d, want 4", got)
	}
	if agent.HasQueuedMessages() {
		t.Fatal("steering queue was not drained")
	}
}

func TestAgentStateReturnsIndependentCollections(t *testing.T) {
	type customLabels []string
	type customMeta map[string]customLabels
	type customMessage struct {
		Role string     `json:"role"`
		Meta customMeta `json:"meta"`
	}
	levelName := "high"
	levelMap := map[ai.ModelThinkingLevel]*string{ai.ModelThinkingHigh: &levelName}
	tiers := []ai.ModelCostTier{{InputTokensAbove: 100}}
	message := loopAssistant(ai.StopReasonStop,
		&ai.TextContent{Text: "original"},
		&ai.ToolCall{ID: "call-1", Name: "tool", Arguments: map[string]any{
			"nested": map[string]any{"value": "original"},
			"labels": []string{"original"},
			"lookup": map[string]string{"value": "original"},
		}},
		&ai.UnknownContentBlock{Raw: json.RawMessage(`{"type":"future","value":"original"}`)},
	)
	customOriginal := &customMessage{Role: "custom", Meta: customMeta{"labels": customLabels{"original"}}}
	agent := NewAgent(WithInitialState(AgentState{
		Model: &ai.Model{
			ID: "model", ThinkingLevelMap: &levelMap,
			Cost: ai.ModelCost{Tiers: &tiers},
		},
		Messages: AgentMessages{message, customOriginal},
	}))
	customOriginal.Meta["labels"][0] = "input mutation"
	state := agent.State()
	state.Messages = append(state.Messages, loopUser("external"))
	state.Tools = append(state.Tools, AgentToolFunc{})
	state.PendingToolCalls["external"] = struct{}{}
	state.Model.ID = "external"
	*(*state.Model.ThinkingLevelMap)[ai.ModelThinkingHigh] = "external"
	(*state.Model.Cost.Tiers)[0].InputTokensAbove = 999
	assistant := state.Messages[0].(*ai.AssistantMessage)
	assistant.Content[0].(*ai.TextContent).Text = "external"
	assistant.Content[1].(*ai.ToolCall).Arguments["nested"].(map[string]any)["value"] = "external"
	assistant.Content[1].(*ai.ToolCall).Arguments["labels"].([]string)[0] = "external"
	assistant.Content[1].(*ai.ToolCall).Arguments["lookup"].(map[string]string)["value"] = "external"
	assistant.Content[2].(*ai.UnknownContentBlock).Raw[1] = 'X'
	custom := state.Messages[1].(*customMessage)
	custom.Meta["labels"][0] = "external"
	current := agent.State()
	if len(current.Messages) != 2 || len(current.Tools) != 0 || len(current.PendingToolCalls) != 0 || current.Model.ID != "model" {
		t.Fatalf("state snapshot aliases internal state: %#v", current)
	}
	if got := *(*current.Model.ThinkingLevelMap)[ai.ModelThinkingHigh]; got != "high" {
		t.Fatalf("thinking level map was aliased: %q", got)
	}
	if got := (*current.Model.Cost.Tiers)[0].InputTokensAbove; got != 100 {
		t.Fatalf("cost tiers were aliased: %v", got)
	}
	currentAssistant := current.Messages[0].(*ai.AssistantMessage)
	if got := currentAssistant.Content[0].(*ai.TextContent).Text; got != "original" {
		t.Fatalf("assistant text was aliased: %q", got)
	}
	if got := currentAssistant.Content[1].(*ai.ToolCall).Arguments["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("tool arguments were aliased: %#v", got)
	}
	if got := currentAssistant.Content[1].(*ai.ToolCall).Arguments["labels"].([]string)[0]; got != "original" {
		t.Fatalf("typed tool argument slice was aliased: %#v", got)
	}
	if got := currentAssistant.Content[1].(*ai.ToolCall).Arguments["lookup"].(map[string]string)["value"]; got != "original" {
		t.Fatalf("typed tool argument map was aliased: %#v", got)
	}
	if got := string(currentAssistant.Content[2].(*ai.UnknownContentBlock).Raw); got != `{"type":"future","value":"original"}` {
		t.Fatalf("unknown content block was aliased: %s", got)
	}
	if got := current.Messages[1].(*customMessage).Meta["labels"][0]; got != "original" {
		t.Fatalf("custom message was aliased: %#v", got)
	}
}

func TestAgentStreamingStateOwnsProviderSnapshot(t *testing.T) {
	providerMutated := make(chan struct{})
	releaseProvider := make(chan struct{})
	stream := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		return func(yield func(ai.AssistantMessageEvent, error) bool) {
			block := &ai.TextContent{Text: ""}
			partial := loopAssistant(ai.StopReasonStop, block)
			if !yield(ai.StartEvent{Partial: partial}, nil) {
				return
			}
			block.Text = "provider mutation"
			close(providerMutated)
			<-releaseProvider
			yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "done"})}, nil)
		}, nil
	}
	agent := NewAgent(WithInitialState(AgentState{Model: loopModel()}), WithStreamFn(stream))
	done := make(chan error, 1)
	go func() { done <- agent.Prompt(context.Background(), "go") }()
	<-providerMutated
	state := agent.State()
	streaming := state.StreamingMessage.(*ai.AssistantMessage)
	if got := streaming.Content[0].(*ai.TextContent).Text; got != "" {
		t.Fatalf("state observed provider mutation after event: %q", got)
	}
	streaming.Content[0].(*ai.TextContent).Text = "caller mutation"
	stateAgain := agent.State()
	if got := stateAgain.StreamingMessage.(*ai.AssistantMessage).Content[0].(*ai.TextContent).Text; got != "" {
		t.Fatalf("state snapshot was caller-mutable: %q", got)
	}
	close(releaseProvider)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAgentInitialStateDefaultsAndHighLevelQueueHook(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{loopAssistant(ai.StopReasonStop)}}
	externalMessage := loopUser("external steering")
	getterCalls := 0
	agent := NewAgent(
		WithInitialState(AgentState{Model: loopModel()}),
		WithStreamFn(responses.stream),
		WithGetSteeringMessages(func(context.Context) (AgentMessages, error) {
			getterCalls++
			if getterCalls == 1 {
				return AgentMessages{externalMessage}, nil
			}
			return AgentMessages{}, nil
		}),
	)
	state := agent.State()
	if state.ThinkingLevel != ThinkingOff || state.Tools == nil || state.Messages == nil {
		t.Fatalf("initial state defaults = %#v", state)
	}
	if err := agent.Prompt(context.Background(), loopUser("prompt")); err != nil {
		t.Fatal(err)
	}
	if len(responses.contexts) != 1 || len(responses.contexts[0].Messages) != 2 {
		t.Fatalf("provider context = %#v", responses.contexts)
	}
	if getterCalls != 2 {
		t.Fatalf("steering getter calls = %d, want 2", getterCalls)
	}
}

func joinEventTypes(values []AgentEventType) string {
	strings := make([]string, len(values))
	for index, value := range values {
		strings[index] = string(value)
	}
	return joinStrings(strings)
}

func joinInts(values []int) string {
	strings := make([]string, len(values))
	for index, value := range values {
		if value == 1 {
			strings[index] = "1"
		} else {
			strings[index] = "2"
		}
	}
	return joinStrings(strings)
}
