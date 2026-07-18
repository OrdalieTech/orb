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

func TestMistralConversationsLiveToolCallRoundTrip(t *testing.T) {
	if os.Getenv("PI_GO_LIVE_TESTS") != "1" {
		t.Skip("set PI_GO_LIVE_TESTS=1 to run the Mistral live smoke test")
	}
	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		t.Fatal("PI_GO_LIVE_TESTS=1 requires MISTRAL_API_KEY")
	}
	modelID := os.Getenv("PI_GO_MISTRAL_MODEL")
	if modelID == "" {
		modelID = "mistral-small-latest"
	}
	model := &ai.Model{
		ID: modelID, Name: modelID, API: ai.APIMistralConversations, Provider: "mistral",
		BaseURL: "https://api.mistral.ai", Input: ai.InputModalities{ai.InputText},
		ContextWindow: 128_000, MaxTokens: 4_096,
	}
	tools := []ai.Tool{{
		Name: "echo", Description: "Return the supplied text unchanged.",
		Parameters: jsonschema.Schema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
	}}
	messages := ai.MessageList{&ai.UserMessage{
		Content: ai.NewUserText("Call the echo tool exactly once with text pi-go-live, then wait for its result."), Timestamp: time.Now().UnixMilli(),
	}}
	maxTokens := float64(256)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	first, err := StreamMistralConversationsWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &MistralConversationsOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey, MaxTokens: &maxTokens}, ToolChoice: "required",
	})
	if err != nil {
		t.Fatal(err)
	}
	toolRequest, err := ai.Collect(first)
	if err != nil {
		t.Fatal(err)
	}
	call := firstToolCall(toolRequest)
	if toolRequest.StopReason != ai.StopReasonToolUse || call == nil {
		t.Fatalf("first response stop reason = %q, tool call = %#v: %s", toolRequest.StopReason, call, assistantError(toolRequest))
	}
	if call.Name != "echo" || call.Arguments["text"] != "pi-go-live" {
		t.Fatalf("tool call = %q %#v, want echo with pi-go-live", call.Name, call.Arguments)
	}
	messages = append(messages, toolRequest, &ai.ToolResultMessage{
		ToolCallID: call.ID, ToolName: call.Name,
		Content: ai.ToolResultContent{&ai.TextContent{Text: "pi-go-live"}}, Timestamp: time.Now().UnixMilli(),
	})
	second, err := StreamMistralConversationsWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &MistralConversationsOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey, MaxTokens: &maxTokens},
	})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := ai.Collect(second)
	if err != nil {
		t.Fatal(err)
	}
	if answer.StopReason == ai.StopReasonError || answer.StopReason == ai.StopReasonAborted || assistantText(answer) == "" {
		t.Fatalf("second response failed with %q: %s", answer.StopReason, assistantError(answer))
	}
}

func firstToolCall(message *ai.AssistantMessage) *ai.ToolCall {
	for _, block := range message.Content {
		if call, ok := block.(*ai.ToolCall); ok {
			return call
		}
	}
	return nil
}

func assistantText(message *ai.AssistantMessage) string {
	var text strings.Builder
	for _, block := range message.Content {
		if content, ok := block.(*ai.TextContent); ok {
			text.WriteString(content.Text)
		}
	}
	return strings.TrimSpace(text.String())
}
