package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/conformance/runner"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

type f3Fixture struct {
	SchemaVersion int      `json:"schemaVersion"`
	FixedNow      int64    `json:"fixedNow"`
	Cases         []f3Case `json:"cases"`
}

type f3Case struct {
	Name              string               `json:"name"`
	Trace             string               `json:"trace"`
	API               ai.API               `json:"api"`
	Provider          ai.ProviderID        `json:"provider"`
	SystemPrompt      string               `json:"systemPrompt"`
	Prompt            json.RawMessage      `json:"prompt"`
	Responses         []json.RawMessage    `json:"responses"`
	Tools             []f3Tool             `json:"tools"`
	ToolExecution     string               `json:"toolExecution"`
	ToolBehavior      string               `json:"toolBehavior"`
	TokensPerSecond   float64              `json:"tokensPerSecond"`
	TokenSize         f3TokenSize          `json:"tokenSize"`
	Steering          *f3Steering          `json:"steering"`
	Abort             *f3Abort             `json:"abort"`
	ThinkingRoundTrip *f3ThinkingRoundTrip `json:"thinkingRoundTrip"`
	Expected          f3Expected           `json:"expected"`
	EventCount        int                  `json:"eventCount"`
}

type f3Tool struct {
	Name        string          `json:"name"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type f3TokenSize struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type f3Steering struct {
	Trigger  string            `json:"trigger"`
	Messages []json.RawMessage `json:"messages"`
}

type f3Abort struct {
	Trigger string `json:"trigger"`
}

type f3ThinkingRoundTrip struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type f3Expected struct {
	ProviderCalls    int64    `json:"providerCalls"`
	PendingResponses int      `json:"pendingResponses"`
	ToolEndOrder     []string `json:"toolEndOrder"`
	ToolResultOrder  []string `json:"toolResultOrder"`
}

func TestF3AgentLoopMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "F3")
	if manifest.Family != "F3" || manifest.Generator != "conformance/extract/f3-agent.ts" {
		t.Fatalf("unexpected F3 manifest: %+v", manifest)
	}

	var fixture f3Fixture
	runner.LoadJSON(t, "F3", "cases.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Cases) != 7 {
		t.Fatalf("F3 fixture header = version %d, cases %d", fixture.SchemaVersion, len(fixture.Cases))
	}

	for _, fixtureCase := range fixture.Cases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Name, func(t *testing.T) {
			runF3Case(t, fixture.FixedNow, fixtureCase)
		})
	}
}

func runF3Case(t *testing.T, fixedNow int64, fixtureCase f3Case) {
	t.Helper()

	prompt := mustF3Message(t, fixtureCase.Prompt)
	responses := make([]faux.ResponseStep, len(fixtureCase.Responses))
	for index, raw := range fixtureCase.Responses {
		message := mustF3Message(t, raw)
		assistantMessage, ok := message.(*ai.AssistantMessage)
		if !ok {
			t.Fatalf("response %d has type %T, want *ai.AssistantMessage", index, message)
		}
		normalizeF3ToolArguments(t, assistantMessage)
		responses[index] = assistantMessage
	}
	if fixtureCase.ThinkingRoundTrip != nil {
		if len(responses) < 2 {
			t.Fatal("thinking round-trip fixture needs two responses")
		}
		finalResponse, ok := responses[1].(*ai.AssistantMessage)
		if !ok {
			t.Fatalf("thinking round-trip response has type %T", responses[1])
		}
		expected := *fixtureCase.ThinkingRoundTrip
		responses[1] = faux.Factory(func(_ context.Context, requestContext ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			for _, message := range requestContext.Messages {
				assistantMessage, ok := message.(*ai.AssistantMessage)
				if !ok {
					continue
				}
				for _, content := range assistantMessage.Content {
					thinking, ok := content.(*ai.ThinkingContent)
					if ok && thinking.Thinking == expected.Thinking && thinking.ThinkingSignature != nil && *thinking.ThinkingSignature == expected.Signature {
						return finalResponse, nil
					}
				}
			}
			return nil, errors.New("Anthropic thinking signature was not preserved across turns")
		})
	}
	minTokenSize, maxTokenSize := fixtureCase.TokenSize.Min, fixtureCase.TokenSize.Max
	apiName := fixtureCase.API
	if apiName == "" {
		apiName = "faux"
	}
	providerName := fixtureCase.Provider
	if providerName == "" {
		providerName = "faux"
	}
	provider := faux.New(faux.Options{
		API:             apiName,
		Provider:        providerName,
		TokensPerSecond: fixtureCase.TokensPerSecond,
		TokenSize: faux.TokenSize{
			Min: &minTokenSize,
			Max: &maxTokenSize,
		},
		Now: func() int64 { return fixedNow },
	})
	provider.SetResponses(responses)

	releaseFirst := make(chan struct{})
	var releaseFirstOnce sync.Once
	releaseSecondTerminate := make(chan struct{})
	var releaseSecondTerminateOnce sync.Once
	var steeringReady atomic.Bool
	var steeringDelivered atomic.Bool
	tools := make([]agent.AgentTool, len(fixtureCase.Tools))
	for index, fixtureTool := range fixtureCase.Tools {
		fixtureTool := fixtureTool
		tools[index] = agent.AgentToolFunc{
			AgentToolSpec: agent.AgentToolSpec{
				Name:        fixtureTool.Name,
				Label:       fixtureTool.Label,
				Description: fixtureTool.Description,
				Parameters:  jsonschema.Schema(bytes.Clone(fixtureTool.Parameters)),
			},
			Run: func(ctx context.Context, _ string, params any, _ agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
				value, err := f3StringArgument(params, "value")
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				switch fixtureCase.ToolBehavior {
				case "parallel-second-finishes-first":
					if value == "first" {
						select {
						case <-releaseFirst:
						case <-ctx.Done():
							return agent.AgentToolResult{}, ctx.Err()
						}
					}
					return f3ToolResult("echo", value, false), nil
				case "queue-steering":
					steeringReady.Store(true)
					return f3ToolResult("echo", value, false), nil
				case "throw":
					return agent.AgentToolResult{}, errors.New("fixture tool exploded")
				case "terminate-all":
					if value == "second" {
						select {
						case <-releaseSecondTerminate:
						case <-ctx.Done():
							return agent.AgentToolResult{}, ctx.Err()
						}
					}
					return f3ToolResult("finished", value, true), nil
				case "echo":
					return f3ToolResult("echo", value, false), nil
				case "none":
					return agent.AgentToolResult{}, errors.New("scenario configured a tool with no behavior")
				default:
					return agent.AgentToolResult{}, fmt.Errorf("unknown F3 tool behavior %q", fixtureCase.ToolBehavior)
				}
			},
		}
	}

	steeringMessages := make(agent.AgentMessages, 0)
	if fixtureCase.Steering != nil {
		for _, raw := range fixtureCase.Steering.Messages {
			steeringMessages = append(steeringMessages, mustF3Message(t, raw))
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var trace bytes.Buffer
	var sinkMu sync.Mutex
	toolEndOrder := make([]string, 0)
	toolResultOrder := make([]string, 0)
	eventCount := 0
	aborted := false
	sink := func(_ context.Context, event agent.AgentEvent) error {
		sinkMu.Lock()
		defer sinkMu.Unlock()
		encoded, err := agent.MarshalAgentEvent(event)
		if err != nil {
			return err
		}
		trace.Write(encoded)
		trace.WriteByte('\n')
		eventCount++

		switch typed := event.(type) {
		case agent.ToolExecutionEndEvent:
			toolEndOrder = append(toolEndOrder, typed.ToolCallID)
			if typed.ToolCallID == "parallel-2" {
				releaseFirstOnce.Do(func() { close(releaseFirst) })
			}
			if typed.ToolCallID == "terminate-1" {
				releaseSecondTerminateOnce.Do(func() { close(releaseSecondTerminate) })
			}
		case agent.MessageEndEvent:
			if result, ok := typed.Message.(*ai.ToolResultMessage); ok {
				toolResultOrder = append(toolResultOrder, result.ToolCallID)
			}
		case agent.MessageUpdateEvent:
			if fixtureCase.Abort != nil && !aborted && isF3TextDelta(typed.AssistantMessageEvent) {
				aborted = true
				cancel()
			}
		}
		return nil
	}

	config := agent.AgentLoopConfig{
		Model:         provider.GetModel(),
		StreamFn:      provider.StreamSimple,
		ToolExecution: agent.ToolExecutionMode(fixtureCase.ToolExecution),
		Now:           func() int64 { return fixedNow },
	}
	if fixtureCase.Steering != nil {
		config.GetSteeringMessages = func(context.Context) (agent.AgentMessages, error) {
			if !steeringReady.Load() || !steeringDelivered.CompareAndSwap(false, true) {
				return agent.AgentMessages{}, nil
			}
			return append(agent.AgentMessages(nil), steeringMessages...), nil
		}
	}

	_, err := agent.RunLoop(ctx, agent.AgentMessages{prompt}, agent.AgentContext{
		SystemPrompt: fixtureCase.SystemPrompt,
		Tools:        tools,
	}, config, sink)
	if err != nil {
		t.Fatalf("RunLoop: %v", err)
	}

	if got := provider.State().CallCount; got != fixtureCase.Expected.ProviderCalls {
		t.Errorf("provider calls = %d, want %d", got, fixtureCase.Expected.ProviderCalls)
	}
	if got := provider.PendingResponseCount(); got != fixtureCase.Expected.PendingResponses {
		t.Errorf("pending responses = %d, want %d", got, fixtureCase.Expected.PendingResponses)
	}
	if !slices.Equal(toolEndOrder, fixtureCase.Expected.ToolEndOrder) {
		t.Errorf("tool end order = %v, want %v", toolEndOrder, fixtureCase.Expected.ToolEndOrder)
	}
	if !slices.Equal(toolResultOrder, fixtureCase.Expected.ToolResultOrder) {
		t.Errorf("tool result order = %v, want %v", toolResultOrder, fixtureCase.Expected.ToolResultOrder)
	}
	if eventCount != fixtureCase.EventCount {
		t.Errorf("event count = %d, want %d", eventCount, fixtureCase.EventCount)
	}

	want, err := runner.ReadFixture("F3", fixtureCase.Trace)
	if err != nil {
		t.Fatal(err)
	}
	if diff := runner.ByteDiff(want, trace.Bytes()); diff != "" {
		t.Fatal(diff)
	}
}

func mustF3Message(t testing.TB, raw json.RawMessage) ai.Message {
	t.Helper()
	message, err := ai.UnmarshalMessage(raw)
	if err != nil {
		t.Fatalf("decode F3 message: %v", err)
	}
	return message
}

func normalizeF3ToolArguments(t testing.TB, message *ai.AssistantMessage) {
	t.Helper()
	for _, block := range message.Content {
		toolCall, ok := block.(*ai.ToolCall)
		if !ok {
			continue
		}
		arguments, err := ai.Marshal(toolCall.Arguments)
		if err != nil {
			t.Fatalf("marshal F3 tool arguments: %v", err)
		}
		if err := ai.SetToolCallArgumentsJSON(toolCall, arguments); err != nil {
			t.Fatalf("normalize F3 tool arguments: %v", err)
		}
	}
}

func f3StringArgument(params any, name string) (string, error) {
	object, ok := params.(map[string]any)
	if !ok {
		return "", fmt.Errorf("F3 tool arguments have type %T", params)
	}
	value, ok := object[name].(string)
	if !ok {
		return "", fmt.Errorf("F3 tool argument %q has type %T", name, object[name])
	}
	return value, nil
}

func f3ToolResult(prefix, value string, terminate bool) agent.AgentToolResult {
	result := agent.AgentToolResult{
		Content: ai.ToolResultContent{&ai.TextContent{Text: prefix + ":" + value}},
		Details: map[string]any{"value": value},
	}
	if terminate {
		result.Terminate = &terminate
	}
	return result
}

func isF3TextDelta(event ai.AssistantMessageEvent) bool {
	switch event.(type) {
	case ai.TextDeltaEvent, *ai.TextDeltaEvent:
		return true
	default:
		return false
	}
}
