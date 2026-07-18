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

func TestGoogleGenerativeAILiveToolCallRoundTrip(t *testing.T) {
	if os.Getenv("PI_GO_LIVE_TESTS") != "1" {
		t.Skip("set PI_GO_LIVE_TESTS=1 to run the Google live smoke test")
	}
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Fatal("PI_GO_LIVE_TESTS=1 requires GEMINI_API_KEY")
	}
	modelID := os.Getenv("PI_GO_GOOGLE_MODEL")
	if modelID == "" {
		modelID = "gemini-2.5-flash"
	}
	model := googleTestModel(modelID)
	tools := []ai.Tool{{
		Name: "echo", Description: "Return the supplied text unchanged.",
		Parameters: jsonschema.Schema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
	}}
	messages := ai.MessageList{&ai.UserMessage{
		Content:   ai.NewUserText("Call the echo tool exactly once with text pi-go-live, then wait for its result."),
		Timestamp: time.Now().UnixMilli(),
	}}
	maxTokens := float64(256)
	timeoutMS := int64(60_000)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	first, err := StreamGoogleGenerativeAIWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &GoogleOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey, MaxTokens: &maxTokens, TimeoutMS: &timeoutMS},
		ToolChoice:    GoogleToolChoiceAny,
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
	if call == nil || call.Name != "echo" || call.Arguments["text"] != "pi-go-live" {
		t.Fatalf("tool call = %#v, want echo with pi-go-live", call)
	}
	messages = append(messages, toolRequest, &ai.ToolResultMessage{
		ToolCallID: call.ID, ToolName: call.Name,
		Content:   ai.ToolResultContent{&ai.TextContent{Text: "pi-go-live"}},
		Timestamp: time.Now().UnixMilli(),
	})
	second, err := StreamGoogleGenerativeAIWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &GoogleOptions{
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
