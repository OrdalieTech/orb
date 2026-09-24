package runner_test

import (
	"context"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/jsonschema"
)

const productionCompatSystemPrompt = "Stable production system instructions."

type productionContextCapture struct {
	mu       sync.Mutex
	contexts []ai.Context
}

type productionConversionCapture struct {
	mu       sync.Mutex
	messages []ai.MessageList
}

func (capture *productionConversionCapture) convert(ctx context.Context, messages engine.AgentMessages) (ai.MessageList, error) {
	converted, err := productionConvertToLLM(ctx, messages)
	if err != nil {
		return nil, err
	}
	capture.mu.Lock()
	capture.messages = append(capture.messages, append(ai.MessageList(nil), converted...))
	capture.mu.Unlock()
	return converted, nil
}

func (capture *productionConversionCapture) snapshot() []ai.MessageList {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]ai.MessageList(nil), capture.messages...)
}

func (capture *productionContextCapture) stream(next engine.StreamFn) engine.StreamFn {
	return func(
		ctx context.Context,
		model *ai.Model,
		request ai.Context,
		options *ai.SimpleStreamOptions,
	) (ai.AssistantMessageEventStream, error) {
		copy := request
		copy.Messages = append(ai.MessageList(nil), request.Messages...)
		if request.Tools != nil {
			tools := append([]ai.Tool(nil), (*request.Tools)...)
			copy.Tools = &tools
		}
		capture.mu.Lock()
		capture.contexts = append(capture.contexts, copy)
		capture.mu.Unlock()
		return next(ctx, model, request, options)
	}
}

func (capture *productionContextCapture) snapshot() []ai.Context {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]ai.Context(nil), capture.contexts...)
}

// productionConvertToLLM matches the production consumer's compatibility
// boundary: all standard ai.Message values are forwarded and application-only
// messages are handled separately.
func productionConvertToLLM(_ context.Context, messages engine.AgentMessages) (ai.MessageList, error) {
	converted := make(ai.MessageList, 0, len(messages))
	for _, message := range messages {
		if standard, ok := message.(ai.Message); ok {
			converted = append(converted, standard)
		}
	}
	return converted, nil
}

func TestProductionConsumerFauxMultiRoundKeepsSystemPromptStable(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(
			faux.ToolCall("compat_tool", map[string]any{}, faux.ToolCallOptions{ID: "call-1"}),
			faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse},
		),
		faux.AssistantMessage("complete"),
	})
	capture := &productionContextCapture{}
	conversions := &productionConversionCapture{}
	agent := engine.NewAgent(
		capture.stream(provider.StreamSimple),
		engine.WithInitialState(engine.AgentState{
			SystemPrompt: productionCompatSystemPrompt,
			Model:        provider.GetModel(),
			Tools:        []engine.AgentTool{productionCompatTool()},
		}),
		engine.WithConvertToLLM(conversions.convert),
		engine.WithToolExecution(engine.ToolExecutionSequential),
	)

	if err := agent.Prompt(t.Context(), "use the tool"); err != nil {
		t.Fatal(err)
	}
	if provider.State().CallCount != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.State().CallCount)
	}
	contexts := capture.snapshot()
	if len(contexts) != 2 {
		t.Fatalf("stream contexts = %d, want 2", len(contexts))
	}
	converted := conversions.snapshot()
	if len(converted) != 2 {
		t.Fatalf("converted contexts = %d, want 2", len(converted))
	}
	t.Run("prompt normalization", func(t *testing.T) {
		for _, request := range contexts {
			assertProductionLegacyContext(t, request)
			assertNoSystemDeclarations(t, request.Messages)
		}
		for _, messages := range converted {
			assertSingleSystemDeclaration(t, messages)
		}
	})
	state := agent.State()
	if len(state.Messages) == 0 {
		t.Fatal("agent state has no messages")
	}
	last, ok := state.Messages[len(state.Messages)-1].(*ai.AssistantMessage)
	if !ok || ai.ContentText(last.Content, "") != "complete" {
		t.Fatalf("final message = %#v", state.Messages[len(state.Messages)-1])
	}
}

func productionCompatTool() engine.AgentToolFunc {
	return engine.AgentToolFunc{
		AgentToolSpec: engine.AgentToolSpec{
			Name:        "compat_tool",
			Description: "Production compatibility tool",
			Parameters:  jsonschema.Schema(`{"type":"object","properties":{}}`),
		},
		Run: func(context.Context, string, any, engine.AgentToolUpdateCallback) (engine.AgentToolResult, error) {
			return engine.AgentToolResult{
				Content: ai.ToolResultContent{&ai.TextContent{Text: "tool complete"}},
			}, nil
		},
	}
}

func assertProductionLegacyContext(t *testing.T, request ai.Context) {
	t.Helper()
	if request.SystemPrompt == nil {
		t.Fatal("legacy system prompt is nil")
	}
	if *request.SystemPrompt != productionCompatSystemPrompt {
		t.Fatalf("legacy system prompt = %q, want %q", *request.SystemPrompt, productionCompatSystemPrompt)
	}
	if request.Tools == nil || len(*request.Tools) != 1 || (*request.Tools)[0].Name != "compat_tool" {
		t.Fatalf("legacy tools = %#v", request.Tools)
	}
}

func assertSingleSystemDeclaration(t *testing.T, messages ai.MessageList) {
	t.Helper()
	count := 0
	for _, message := range messages {
		if system, ok := message.(*ai.SystemMessage); ok {
			count++
			if got := ai.SystemMessageText(system); got != productionCompatSystemPrompt {
				t.Fatalf("system declaration = %q, want %q", got, productionCompatSystemPrompt)
			}
		}
	}
	if count != 1 {
		t.Fatalf("system declarations = %d, want 1", count)
	}
}

func assertNoSystemDeclarations(t *testing.T, messages ai.MessageList) {
	t.Helper()
	for _, message := range messages {
		if _, ok := message.(*ai.SystemMessage); ok {
			t.Fatalf("legacy StreamFn received transcript system declaration: %#v", message)
		}
	}
}
