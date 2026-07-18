package api

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

func TestAnthropicMessagesLiveToolCallRoundTrip(t *testing.T) {
	if os.Getenv("PI_GO_LIVE_TESTS") != "1" {
		t.Skip("set PI_GO_LIVE_TESTS=1 to run the Anthropic live smoke test")
	}
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Fatal("PI_GO_LIVE_TESTS=1 requires ANTHROPIC_API_KEY")
	}

	modelID := os.Getenv("PI_GO_ANTHROPIC_MODEL")
	if modelID == "" {
		modelID = "claude-haiku-4-5"
	}
	model := &ai.Model{
		ID: modelID, Name: modelID, API: ai.APIAnthropicMessages, Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", Input: ai.InputModalities{ai.InputText},
		ContextWindow: 200_000, MaxTokens: 4_096,
	}
	tools := []ai.Tool{{
		Name:        "echo",
		Description: "Return the supplied text unchanged.",
		Parameters:  jsonschema.Schema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
	}}
	messages := ai.MessageList{&ai.UserMessage{
		Content:   ai.NewUserText("Call the echo tool exactly once with text pi-go-live, then wait for its result."),
		Timestamp: time.Now().UnixMilli(),
	}}
	maxTokens := float64(256)
	timeoutMS := int64(60_000)
	required := "echo"

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	first, err := StreamAnthropicMessagesWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &AnthropicMessagesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey, MaxTokens: &maxTokens, TimeoutMS: &timeoutMS},
		ToolChoice:    &AnthropicToolChoice{Type: "tool", Name: &required},
	})
	if err != nil {
		t.Fatal(err)
	}
	toolRequest, err := ai.Collect(first)
	if err != nil {
		t.Fatal(err)
	}
	if toolRequest.StopReason != ai.StopReasonToolUse {
		t.Fatalf("first stop reason = %q, want %q: %s", toolRequest.StopReason, ai.StopReasonToolUse, assistantError(toolRequest))
	}
	var call *ai.ToolCall
	for _, block := range toolRequest.Content {
		if candidate, ok := block.(*ai.ToolCall); ok {
			call = candidate
			break
		}
	}
	if call == nil {
		t.Fatal("first response contained no tool call")
	}
	if call.Name != "echo" || call.Arguments["text"] != "pi-go-live" {
		t.Fatalf("tool call = %q %#v, want echo with pi-go-live", call.Name, call.Arguments)
	}

	messages = append(messages, toolRequest, &ai.ToolResultMessage{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    ai.ToolResultContent{&ai.TextContent{Text: "pi-go-live"}},
		Timestamp:  time.Now().UnixMilli(),
	})
	second, err := StreamAnthropicMessagesWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &AnthropicMessagesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey, MaxTokens: &maxTokens, TimeoutMS: &timeoutMS},
	})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := ai.Collect(second)
	if err != nil {
		t.Fatal(err)
	}
	if answer.StopReason == ai.StopReasonError || answer.StopReason == ai.StopReasonAborted {
		t.Fatalf("second response failed with %q: %s", answer.StopReason, assistantError(answer))
	}
	var text strings.Builder
	for _, block := range answer.Content {
		if content, ok := block.(*ai.TextContent); ok {
			text.WriteString(content.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		t.Fatal("second response contained no text")
	}
}
