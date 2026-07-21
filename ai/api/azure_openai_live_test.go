package api

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
)

func TestAzureOpenAIResponsesLiveToolCallRoundTrip(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TESTS") != "1" {
		t.Skip("set PIGO_LIVE_TESTS=1 to run the Azure OpenAI live smoke test")
	}
	apiKey := os.Getenv("AZURE_OPENAI_API_KEY")
	if apiKey == "" || (os.Getenv("AZURE_OPENAI_BASE_URL") == "" && os.Getenv("AZURE_OPENAI_RESOURCE_NAME") == "") {
		t.Fatal("PIGO_LIVE_TESTS=1 requires AZURE_OPENAI_API_KEY and AZURE_OPENAI_BASE_URL or AZURE_OPENAI_RESOURCE_NAME")
	}
	modelID := os.Getenv("PIGO_AZURE_OPENAI_MODEL")
	if modelID == "" {
		modelID = "gpt-4o-mini"
	}
	model := &ai.Model{
		ID: modelID, Name: modelID, API: ai.APIAzureOpenAIResponses, Provider: "azure-openai-responses",
		Input: ai.InputModalities{ai.InputText}, ContextWindow: 128_000, MaxTokens: 4_096,
	}
	tools := []ai.Tool{{
		Name: "echo", Description: "Return the supplied text unchanged.",
		Parameters: jsonschema.Schema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
	}}
	messages := ai.MessageList{&ai.UserMessage{
		Content: ai.NewUserText("Call the echo tool exactly once with text pigo-live. Do not answer until the tool result arrives."), Timestamp: time.Now().UnixMilli(),
	}}
	maxTokens := float64(256)
	timeoutMS := int64(60_000)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	first, err := StreamAzureOpenAIResponsesWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &AzureOpenAIResponsesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey, MaxTokens: &maxTokens, TimeoutMS: &timeoutMS},
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
	if call.Name != "echo" || call.Arguments["text"] != "pigo-live" {
		t.Fatalf("tool call = %q %#v, want echo with pigo-live", call.Name, call.Arguments)
	}
	messages = append(messages, toolRequest, &ai.ToolResultMessage{
		ToolCallID: call.ID, ToolName: call.Name,
		Content: ai.ToolResultContent{&ai.TextContent{Text: "pigo-live"}}, Timestamp: time.Now().UnixMilli(),
	})
	second, err := StreamAzureOpenAIResponsesWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &AzureOpenAIResponsesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey, MaxTokens: &maxTokens, TimeoutMS: &timeoutMS},
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
