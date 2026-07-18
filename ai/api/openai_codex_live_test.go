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

func TestOpenAICodexLiveToolCallRoundTrip(t *testing.T) {
	if os.Getenv("PI_GO_LIVE_TESTS") != "1" {
		t.Skip("set PI_GO_LIVE_TESTS=1 to run the OpenAI Codex live smoke test")
	}
	accessToken := os.Getenv("PI_GO_OPENAI_CODEX_ACCESS_TOKEN")
	if accessToken == "" {
		t.Fatal("PI_GO_LIVE_TESTS=1 requires PI_GO_OPENAI_CODEX_ACCESS_TOKEN")
	}

	modelID := os.Getenv("PI_GO_OPENAI_CODEX_MODEL")
	if modelID == "" {
		modelID = "gpt-5.4-mini"
	}
	transport := ai.TransportAuto
	if configured := os.Getenv("PI_GO_OPENAI_CODEX_TRANSPORT"); configured != "" {
		transport = ai.Transport(configured)
		switch transport {
		case ai.TransportAuto, ai.TransportSSE, ai.TransportWebSocket, ai.TransportWebSocketCached:
		default:
			t.Fatalf("invalid PI_GO_OPENAI_CODEX_TRANSPORT %q", configured)
		}
	}
	model := &ai.Model{
		ID: modelID, Name: modelID, API: ai.APIOpenAICodexResponses, Provider: "openai-codex",
		BaseURL: defaultOpenAICodexBaseURL, Reasoning: true, Input: ai.InputModalities{ai.InputText},
		ContextWindow: 128_000, MaxTokens: 8_192,
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
	sessionID := "pi-go-live-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { CloseOpenAICodexWebSocketSessions(sessionID) })
	reasoningEffort, required := "low", "required"

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	first, err := StreamOpenAICodexResponsesWithOptions(ctx, model, ai.Context{Messages: messages, Tools: &tools}, &OpenAICodexResponsesOptions{
		StreamOptions:   ai.StreamOptions{APIKey: &accessToken, Transport: &transport, SessionID: &sessionID},
		ReasoningEffort: &reasoningEffort,
		ToolChoice:      &required,
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
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    ai.ToolResultContent{&ai.TextContent{Text: "pi-go-live"}},
		Timestamp:  time.Now().UnixMilli(),
	})
	second, err := StreamOpenAICodexResponsesWithOptions(ctx, model, ai.Context{Messages: messages}, &OpenAICodexResponsesOptions{
		StreamOptions:   ai.StreamOptions{APIKey: &accessToken, Transport: &transport, SessionID: &sessionID},
		ReasoningEffort: &reasoningEffort,
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
