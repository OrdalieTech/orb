package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestGoogleCrossModelToolReplay(t *testing.T) {
	const signed = "AAAAAAAAAAAAAAAAAAAAAA=="
	for _, api := range []ai.API{ai.APIGoogleGenerativeAI, ai.APIGoogleVertex} {
		for _, tc := range []struct {
			name, model, source, signature, want string
		}{
			{"foreign", "gemini-3.8-flash", "glm-5.3", "", "skip_thought_signature_validator"},
			{"foreign signature", "gemini-3.8-flash", "glm-5.3", signed, "skip_thought_signature_validator"},
			{"pro foreign", "gemini-3.1-pro", "glm-5.3", "", "skip_thought_signature_validator"},
			{"same signed", "gemini-3.8-flash", "gemini-3.8-flash", signed, signed},
			{"same unsigned", "gemini-3.8-flash", "gemini-3.8-flash", "", ""},
			{"older model", "gemini-2.5-flash", "glm-5.3", "", ""},
		} {
			t.Run(string(api)+"/"+tc.name, func(t *testing.T) {
				model := googleTestModel(tc.model)
				model.API = api
				call := &ai.ToolCall{ID: "call", Name: "exec_code", Arguments: map[string]any{"code": "1+1"}}
				if tc.signature != "" {
					call.ThoughtSignature = &tc.signature
				}
				sourceProvider := model.Provider
				if tc.source != tc.model {
					sourceProvider = "tensorx"
				}
				request := ai.Context{Messages: ai.MessageList{
					&ai.UserMessage{Content: ai.NewUserText("Calculate 1+1.")},
					&ai.AssistantMessage{API: api, Provider: sourceProvider, Model: tc.source, StopReason: ai.StopReasonToolUse, Content: ai.AssistantContent{call}},
					&ai.ToolResultMessage{ToolCallID: "call", ToolName: "exec_code", Content: ai.ToolResultContent{&ai.TextContent{Text: "2"}}},
				}}
				previous := googleHTTPClient
				googleHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					body, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					var wire struct {
						Contents []GoogleContent `json:"contents"`
					}
					if err := json.Unmarshal(body, &wire); err != nil {
						t.Fatal(err)
					}
					part := wire.Contents[1].Parts[0]
					got := ""
					if part.ThoughtSignature != nil {
						got = *part.ThoughtSignature
					}
					if got != tc.want || part.FunctionCall == nil || part.FunctionCall.Name != "exec_code" || wire.Contents[2].Parts[0].FunctionResponse == nil {
						t.Fatalf("unexpected tool replay: %s", body)
					}
					return googleTestResponse("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"2\"}]},\"finishReason\":\"STOP\"}]}\n\n"), nil
				})}
				t.Cleanup(func() { googleHTTPClient = previous })
				key := "test-key"
				stream, err := StreamSimple(context.Background(), model, request, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{APIKey: &key}})
				if err != nil {
					t.Fatal(err)
				}
				result, err := ai.Collect(stream)
				if err != nil || result.StopReason != ai.StopReasonStop {
					t.Fatalf("completion = %v, %v", result, err)
				}
				if tc.signature == "" && call.ThoughtSignature != nil || tc.signature != "" && *call.ThoughtSignature != tc.signature {
					t.Fatal("canonical history mutated")
				}
			})
		}
	}
}
