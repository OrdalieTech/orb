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

func TestPiMessagesLiveToolCallRoundTrip(t *testing.T) {
	if os.Getenv("PI_GO_LIVE_TESTS") != "1" {
		t.Skip("set PI_GO_LIVE_TESTS=1 to run the pi-messages live smoke test")
	}
	baseURL := os.Getenv("PI_GO_PI_MESSAGES_BASE_URL")
	apiKey := os.Getenv("PI_GO_PI_MESSAGES_API_KEY")
	modelID := os.Getenv("PI_GO_PI_MESSAGES_MODEL")
	if baseURL == "" || apiKey == "" || modelID == "" {
		t.Fatal("PI_GO_LIVE_TESTS=1 requires PI_GO_PI_MESSAGES_BASE_URL, PI_GO_PI_MESSAGES_API_KEY, and PI_GO_PI_MESSAGES_MODEL")
	}
	model := &ai.Model{
		ID: modelID, Name: modelID, API: ai.APIPiMessages, Provider: "pi-messages-gateway",
		BaseURL: baseURL, Input: ai.InputModalities{ai.InputText}, ContextWindow: 128_000, MaxTokens: 4_096,
	}
	tools := []ai.Tool{{
		Name: "echo", Description: "Return the supplied text unchanged.",
		Parameters: jsonschema.Schema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
	}}
	messages := ai.MessageList{&ai.UserMessage{
		Content:   ai.NewUserText("Call the echo tool exactly once with text pi-go-live, then wait for its result."),
		Timestamp: time.Now().UnixMilli(),
	}}
	maxTokens := float64(256)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	first, err := StreamPiMessagesWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &PiMessagesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey, MaxTokens: &maxTokens},
		ToolChoice:    "required",
	})
	if err != nil {
		t.Fatal(err)
	}
	toolRequest, err := ai.Collect(first)
	if err != nil {
		t.Fatal(err)
	}
	if toolRequest.StopReason != ai.StopReasonToolUse {
		t.Fatalf("first stop reason = %q, want toolUse: %s", toolRequest.StopReason, piMessagesAssistantError(toolRequest))
	}
	var call *ai.ToolCall
	for _, block := range toolRequest.Content {
		if candidate, ok := block.(*ai.ToolCall); ok {
			call = candidate
			break
		}
	}
	if call == nil || call.Name != "echo" || call.Arguments["text"] != "pi-go-live" {
		t.Fatalf("tool call = %#v, want echo with pi-go-live", call)
	}
	messages = append(messages, toolRequest, &ai.ToolResultMessage{
		ToolCallID: call.ID, ToolName: call.Name,
		Content: ai.ToolResultContent{&ai.TextContent{Text: "pi-go-live"}}, IsError: false,
		Timestamp: time.Now().UnixMilli(),
	})
	second, err := StreamPiMessagesWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &PiMessagesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey, MaxTokens: &maxTokens},
	})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := ai.Collect(second)
	if err != nil {
		t.Fatal(err)
	}
	if answer.StopReason == ai.StopReasonError || answer.StopReason == ai.StopReasonAborted {
		t.Fatalf("second response failed with %q: %s", answer.StopReason, piMessagesAssistantError(answer))
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

func piMessagesAssistantError(message *ai.AssistantMessage) string {
	if message == nil || message.ErrorMessage == nil {
		return ""
	}
	return *message.ErrorMessage
}
