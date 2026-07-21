package api

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
)

func TestOpenAIResponsesLiveToolCallRoundTrip(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TESTS") != "1" {
		t.Skip("set PIGO_LIVE_TESTS=1 to run the OpenAI live smoke test")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Fatal("PIGO_LIVE_TESTS=1 requires OPENAI_API_KEY")
	}

	modelID := os.Getenv("PIGO_OPENAI_MODEL")
	if modelID == "" {
		modelID = "gpt-4o-mini"
	}
	model := &ai.Model{
		ID:            modelID,
		Name:          modelID,
		API:           ai.APIOpenAIResponses,
		Provider:      "openai",
		BaseURL:       "https://api.openai.com/v1",
		Input:         ai.InputModalities{ai.InputText},
		ContextWindow: 128_000,
		MaxTokens:     4_096,
	}
	tools := []ai.Tool{{
		Name:        "echo",
		Description: "Return the supplied text unchanged.",
		Parameters:  jsonschema.Schema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
	}}
	userText := "Call the echo tool exactly once with text pigo-live. After receiving its result, answer with that result."
	messages := ai.MessageList{&ai.UserMessage{Content: ai.NewUserText(userText), Timestamp: time.Now().UnixMilli()}}
	maxTokens := float64(256)
	timeoutMS := int64(60_000)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	first, err := StreamOpenAIResponsesWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &OpenAIResponsesOptions{
		StreamOptions: ai.StreamOptions{MaxTokens: &maxTokens, TimeoutMS: &timeoutMS},
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
	if call.Name != "echo" || call.Arguments["text"] != "pigo-live" {
		t.Fatalf("tool call = %q %#v, want echo with pigo-live", call.Name, call.Arguments)
	}

	toolResult := &ai.ToolResultMessage{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    ai.ToolResultContent{&ai.TextContent{Text: "pigo-live"}},
		Timestamp:  time.Now().UnixMilli(),
	}
	messages = append(messages, toolRequest, toolResult)
	second, err := StreamOpenAIResponsesWithOptions(ctx, model, ai.Context{Messages: messages}, &OpenAIResponsesOptions{
		StreamOptions: ai.StreamOptions{MaxTokens: &maxTokens, TimeoutMS: &timeoutMS},
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

func assistantError(message *ai.AssistantMessage) string {
	if message == nil || message.ErrorMessage == nil {
		return ""
	}
	return *message.ErrorMessage
}
