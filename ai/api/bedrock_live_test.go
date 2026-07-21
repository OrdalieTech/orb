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

func TestBedrockConverseStreamLiveToolCallRoundTrip(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TESTS") != "1" {
		t.Skip("set PIGO_LIVE_TESTS=1 to run the Amazon Bedrock live smoke test")
	}
	if !hasLiveBedrockCredentials() {
		t.Fatal("PIGO_LIVE_TESTS=1 requires a Bedrock bearer token, AWS profile, IAM keys, ECS task role, or web identity")
	}

	modelID := os.Getenv("PIGO_BEDROCK_MODEL")
	if modelID == "" {
		modelID = "global.anthropic.claude-sonnet-4-5-20250929-v1:0"
	}
	model := &ai.Model{
		ID: modelID, Name: modelID, API: ai.APIBedrockConverse, Provider: "amazon-bedrock",
		BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", Reasoning: true,
		Input: ai.InputModalities{ai.InputText}, ContextWindow: 200_000, MaxTokens: 4_096,
	}
	tools := []ai.Tool{{
		Name:        "echo",
		Description: "Return the supplied text unchanged.",
		Parameters:  jsonschema.Schema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
	}}
	messages := ai.MessageList{&ai.UserMessage{
		Content:   ai.NewUserText("Call the echo tool exactly once with text pigo-live, then wait for its result."),
		Timestamp: time.Now().UnixMilli(),
	}}
	maxTokens := float64(256)
	timeoutMS := int64(60_000)
	required := &BedrockToolChoice{Type: "tool", Name: "echo"}
	region := os.Getenv("PIGO_BEDROCK_REGION")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	first, err := StreamBedrockConverseWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &BedrockConverseStreamOptions{
		StreamOptions: ai.StreamOptions{MaxTokens: &maxTokens, TimeoutMS: &timeoutMS}, Region: region, ToolChoice: required,
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
	if call == nil || call.Name != "echo" || call.Arguments["text"] != "pigo-live" {
		t.Fatalf("tool call = %#v, want echo with pigo-live", call)
	}

	messages = append(messages, toolRequest, &ai.ToolResultMessage{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    ai.ToolResultContent{&ai.TextContent{Text: "pigo-live"}},
		Timestamp:  time.Now().UnixMilli(),
	})
	second, err := StreamBedrockConverseWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &BedrockConverseStreamOptions{
		StreamOptions: ai.StreamOptions{MaxTokens: &maxTokens, TimeoutMS: &timeoutMS}, Region: region,
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

func hasLiveBedrockCredentials() bool {
	if os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" || os.Getenv("AWS_PROFILE") != "" {
		return true
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != "" {
		return true
	}
	return os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" ||
		os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" ||
		os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != ""
}
