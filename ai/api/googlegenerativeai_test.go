package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestGoogleStreamGeneratesUniqueMissingAndDuplicateToolIDs(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash")
	apiKey := "key"
	previousClient := googleHTTPClient
	previousNow := openAINowUnixMilli
	openAINowUnixMilli = func() int64 { return 123 }
	googleToolCallCounter.Store(0)
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return googleTestResponse("data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"echo\",\"args\":{}}},{\"functionCall\":{\"id\":\"same\",\"name\":\"echo\",\"args\":{}}},{\"functionCall\":{\"id\":\"same\",\"name\":\"echo\",\"args\":{}}}]},\"finishReason\":\"STOP\"}]}\n\n"), nil
	})}
	t.Cleanup(func() {
		googleHTTPClient = previousClient
		openAINowUnixMilli = previousNow
		googleToolCallCounter.Store(0)
	})
	requestContext := ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("call echo")},
	}}
	stream, err := StreamGoogleGenerativeAIWithOptions(context.Background(), model, requestContext, &GoogleOptions{StreamOptions: ai.StreamOptions{APIKey: &apiKey}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 3)
	for _, block := range message.Content {
		if call, ok := block.(*ai.ToolCall); ok {
			ids = append(ids, call.ID)
		}
	}
	if len(ids) != 3 || ids[0] != "echo_123_1" || ids[1] != "same" || ids[2] != "echo_123_2" {
		errorMessage := ""
		if message.ErrorMessage != nil {
			errorMessage = *message.ErrorMessage
		}
		t.Fatalf("tool call IDs = %v (stop=%s error=%q)", ids, message.StopReason, errorMessage)
	}
}

func TestGoogleCanceledContextEmitsAborted(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash")
	apiKey := "key"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream, err := StreamGoogleGenerativeAIWithOptions(ctx, model, ai.Context{}, &GoogleOptions{StreamOptions: ai.StreamOptions{APIKey: &apiKey}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonAborted {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if message.ErrorMessage == nil || *message.ErrorMessage != "Request aborted" {
		t.Fatalf("error message = %v", message.ErrorMessage)
	}
}

// TestGoogleStreamUnknownFinishReasonFailsStream_OTm1 pins the streaming
// consequence of google-shared.ts mapStopReason throwing: an unknown
// finishReason turns the stream into an error event carrying the exact
// upstream message. (OT-m1)
func TestGoogleStreamUnknownFinishReasonFailsStream_OTm1(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash")
	apiKey := "key"
	previousClient := googleHTTPClient
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return googleTestResponse("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"BRAND_NEW_REASON\"}]}\n\n"), nil
	})}
	t.Cleanup(func() { googleHTTPClient = previousClient })
	stream, err := StreamGoogleGenerativeAIWithOptions(context.Background(), model, ai.Context{
		Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello")}},
	}, &GoogleOptions{StreamOptions: ai.StreamOptions{APIKey: &apiKey}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	errorMessage := ""
	if message.ErrorMessage != nil {
		errorMessage = *message.ErrorMessage
	}
	if message.StopReason != ai.StopReasonError || errorMessage != "Unhandled stop reason: BRAND_NEW_REASON" {
		t.Fatalf("stop=%q error=%q", message.StopReason, errorMessage)
	}
	if message.RawStopReason == nil || *message.RawStopReason != "BRAND_NEW_REASON" {
		t.Fatalf("raw stop reason = %v", message.RawStopReason)
	}
}

func googleTestModel(id string) *ai.Model {
	return &ai.Model{
		ID: id, Name: id, API: ai.APIGoogleGenerativeAI, Provider: "google",
		BaseURL: "https://generativelanguage.googleapis.com/v1beta", Reasoning: true,
		Input: ai.InputModalities{ai.InputText, ai.InputImage}, ContextWindow: 1_000_000, MaxTokens: 65_536,
	}
}

func googleTestResponse(sse string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK",
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(strings.NewReader(sse)),
	}
}

// A non-stop finish reason survives an accompanying tool call; only STOP is
// upgraded to toolUse.
func TestGoogleStopReasonKeepsLengthWithToolCalls(t *testing.T) {
	for _, test := range []struct {
		finishReason string
		want         ai.StopReason
	}{
		{"STOP", ai.StopReasonToolUse},
		{"MAX_TOKENS", ai.StopReasonLength},
		{"MALFORMED_FUNCTION_CALL", ai.StopReasonError},
	} {
		model := &ai.Model{ID: "gemini-3-pro", API: ai.APIGoogleGenerativeAI, Provider: "google"}
		output := newAssistantMessage(model)
		processor := googleStreamProcessor{model: model, output: output}
		if _, err := processor.process(googleGenerateContentResponse{Candidates: []googleCandidate{{
			FinishReason: test.finishReason,
			Content: &GoogleContent{Parts: []GooglePart{{
				FunctionCall: &GoogleFunctionCall{ID: "call-1", Name: "echo", Args: json.RawMessage(`{"value":"x"}`)},
			}}},
		}}}); err != nil {
			t.Fatal(err)
		}
		if output.StopReason != test.want {
			t.Fatalf("%s stop reason = %q, want %q", test.finishReason, output.StopReason, test.want)
		}
	}
}
