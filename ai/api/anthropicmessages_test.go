package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/OrdalieTech/orb/ai"
)

func TestScanSSEAcceptsAllLineEndings(t *testing.T) {
	for _, separator := range []string{"\n", "\r\n", "\r"} {
		t.Run(strings.ReplaceAll(separator, "\r", "CR"), func(t *testing.T) {
			input := strings.Join([]string{
				": heartbeat",
				"event: content_block_delta",
				`data: {"type":"content_block_delta",`,
				`data: "index":0}`,
				"", "",
			}, separator)
			var events []string
			// One byte per read splits every CRLF across reads.
			err := scanSSE(iotest.OneByteReader(strings.NewReader(input)), func(event *sseEvent) error {
				events = append(events, fmt.Sprintf("%s %q %t", event.name, event.data, event.eof))
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if want := `content_block_delta "{\"type\":\"content_block_delta\",\n\"index\":0}\n" false`; len(events) != 1 || events[0] != want {
				t.Fatalf("decoded events = %q, want %q", events, want)
			}
		})
	}
}

// Ports packages/ai/test/anthropic-tool-name-normalization.test.ts: Claude
// Code OAuth tool naming is a case-insensitive round-trip against CC's
// canonical casing, never a mapping between different tool names.
func TestAnthropicClaudeCodeToolNameNormalizationRoundTrip(t *testing.T) {
	toolsNamed := func(names ...string) *[]ai.Tool {
		list := make([]ai.Tool, len(names))
		for index, name := range names {
			list[index] = ai.Tool{Name: name}
		}
		return &list
	}
	// A user-defined tool matching a CC name round-trips: todowrite -> TodoWrite -> todowrite.
	if got := toClaudeCodeToolName("todowrite"); got != "TodoWrite" {
		t.Fatalf("toClaudeCodeToolName(todowrite) = %q, want TodoWrite", got)
	}
	if got := fromClaudeCodeToolName("TodoWrite", toolsNamed("todowrite")); got != "todowrite" {
		t.Fatalf("fromClaudeCodeToolName(TodoWrite) = %q, want todowrite", got)
	}
	// pi's built-in tools convert to CC casing outbound and back to the
	// original lowercase names inbound.
	for lower, canonical := range map[string]string{"read": "Read", "write": "Write", "edit": "Edit", "bash": "Bash"} {
		if got := toClaudeCodeToolName(lower); got != canonical {
			t.Fatalf("toClaudeCodeToolName(%s) = %q, want %s", lower, got, canonical)
		}
		if got := fromClaudeCodeToolName(canonical, toolsNamed(lower)); got != lower {
			t.Fatalf("fromClaudeCodeToolName(%s) = %q, want %s", canonical, got, lower)
		}
	}
	// find is not a CC tool name: it must pass through, never map to Glob.
	if got := toClaudeCodeToolName("find"); got != "find" {
		t.Fatalf("toClaudeCodeToolName(find) = %q, want find", got)
	}
	// The old find->Glob mapping broke here: Glob has no matching context tool.
	if got := fromClaudeCodeToolName("Glob", toolsNamed("find")); got != "Glob" {
		t.Fatalf("fromClaudeCodeToolName(Glob) with only a find tool = %q, want Glob", got)
	}
	// Custom tool names pass through unchanged in both directions.
	if got := toClaudeCodeToolName("my_custom_tool"); got != "my_custom_tool" {
		t.Fatalf("toClaudeCodeToolName(my_custom_tool) = %q", got)
	}
	if got := fromClaudeCodeToolName("my_custom_tool", toolsNamed("my_custom_tool")); got != "my_custom_tool" {
		t.Fatalf("fromClaudeCodeToolName(my_custom_tool) = %q", got)
	}
}

// The two tests below port packages/ai/test/github-copilot-anthropic.test.ts
// adaptive-thinking cases (the dynamic-header case lives in
// TestAnthropicCopilotDynamicHeaders).

func anthropicTestModel() *ai.Model {
	return &ai.Model{
		ID: "claude-test", Name: "Claude Test", API: ai.APIAnthropicMessages, Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 1.25}},
		ContextWindow: 200_000, MaxTokens: 4_096,
	}
}

func TestAnthropicWorkloadIdentityFederationExchangesAndCachesToken(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("identity-jwt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var exchanges int
	var exchange map[string]string
	var exchangeAgent, exchangeBeta string
	var authorizations, betas []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/oauth/token":
			exchanges++
			exchangeAgent, exchangeBeta = request.Header.Get("User-Agent"), request.Header.Get("anthropic-beta")
			_ = json.NewDecoder(request.Body).Decode(&exchange)
			_, _ = io.WriteString(writer, `{"access_token":"federated","token_type":"Bearer","expires_in":3600}`)
		case "/v1/messages":
			authorizations = append(authorizations, request.Header.Get("Authorization"))
			betas = append(betas, request.Header.Get("anthropic-beta"))
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		}
	}))
	defer server.Close()
	previousClient := anthropicHTTPClient
	anthropicHTTPClient = server.Client()
	t.Cleanup(func() { anthropicHTTPClient = previousClient })
	model := anthropicTestModel()
	model.BaseURL = server.URL
	env := ai.ProviderEnv{"ANTHROPIC_FEDERATION_RULE_ID": "fdrl_1", "ANTHROPIC_ORGANIZATION_ID": "org_1", "ANTHROPIC_IDENTITY_TOKEN_FILE": tokenFile, "ANTHROPIC_WORKSPACE_ID": "default"}
	for range 2 {
		stream, err := StreamAnthropicMessagesWithOptions(context.Background(), model, ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hi")}}}, &AnthropicMessagesOptions{StreamOptions: ai.StreamOptions{Env: env}})
		if err != nil {
			t.Fatal(err)
		}
		message, err := ai.Collect(stream)
		if err != nil || message.StopReason != ai.StopReasonStop {
			t.Fatalf("message = %#v, err = %v", message, err)
		}
	}
	if exchanges != 1 || exchange["assertion"] != "identity-jwt" || exchange["federation_rule_id"] != "fdrl_1" || exchange["workspace_id"] != "default" {
		t.Fatalf("exchanges = %d, body = %v", exchanges, exchange)
	}
	// What upstream's TypeScript SDK sends on the exchange.
	if exchangeAgent != "Anthropic/JS 0.124.0" || exchangeBeta != "oauth-2025-04-20,oidc-federation-2026-04-01" {
		t.Fatalf("exchange user agent = %q, beta = %q", exchangeAgent, exchangeBeta)
	}
	if authorizations[1] != "Bearer federated" || !strings.Contains(betas[1], "oauth-2025-04-20") {
		t.Fatalf("authorization = %q, beta = %q", authorizations, betas)
	}
}

// Anthropic refusing the strict tool set as too large gets one more request, with tools that only
// prefer strict sampling sent non-strict and tools that require it still strict (upstream 1.2.0).
// A 400 refusing too many strict tools is retried once with preferred-strict tools relaxed (upstream
// 1.2.0 #10409); another status, or a tool set with nothing to relax, is not.
func TestAnthropicRetriesTooManyStrictToolsWithPreferredOnesRelaxed(t *testing.T) {
	var requests []map[string]bool
	status := http.StatusBadRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Tools []struct {
				Name   string `json:"name"`
				Strict bool   `json:"strict"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		strict := map[string]bool{}
		for _, tool := range body.Tools {
			strict[tool.Name] = tool.Strict
		}
		requests = append(requests, strict)
		if len(requests) == 1 {
			writer.WriteHeader(status)
			_, _ = io.WriteString(writer, `{"type":"error","error":{"type":"invalid_request_error","message":"Too many strict tools (24). The maximum number of strict tools is 20."}}`)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	previousClient := anthropicHTTPClient
	anthropicHTTPClient = server.Client()
	t.Cleanup(func() { anthropicHTTPClient = previousClient })
	model := anthropicTestModel()
	model.BaseURL, model.Compat = server.URL, json.RawMessage(`{"supportsStrictTools":true}`)
	tool := func(name string, strict ai.ConstrainedSamplingStrict) ai.Tool {
		return ai.Tool{Name: name, Description: name, Parameters: ai.JSONSchema(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"],"additionalProperties":false}`),
			ConstrainedSampling: &ai.ConstrainedSamplingConfig{Type: ai.ConstrainedSamplingJSONSchema, Strict: strict}}
	}
	run := func(tools []ai.Tool) *ai.AssistantMessage {
		requests = nil
		key := "key"
		stream, err := StreamAnthropicMessagesWithOptions(context.Background(), model, ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hi")}}, Tools: &tools},
			&AnthropicMessagesOptions{StreamOptions: ai.StreamOptions{APIKey: &key}})
		if err != nil {
			t.Fatal(err)
		}
		message, _ := ai.Collect(stream)
		return message
	}
	if message := run([]ai.Tool{tool("prefers", ai.ConstrainedSamplingPrefer), tool("requires", ai.ConstrainedSamplingRequire)}); message == nil || message.StopReason != ai.StopReasonStop {
		t.Fatalf("the relaxed request failed: %#v", message)
	}
	if len(requests) != 2 || !requests[0]["prefers"] || !requests[0]["requires"] || requests[1]["prefers"] || !requests[1]["requires"] {
		t.Fatalf("strict tools per request = %v", requests)
	}
	if run([]ai.Tool{tool("requires", ai.ConstrainedSamplingRequire)}); len(requests) != 1 {
		t.Fatalf("an all-require tool set was retried: %v", requests)
	}
	// A preferred tool whose schema strict sampling rejects was sent non-strict already: nothing to relax.
	unbounded := tool("unbounded", ai.ConstrainedSamplingPrefer)
	unbounded.Parameters = ai.JSONSchema(`{"type":"object","properties":{"x":{"type":"integer","minimum":1}},"required":["x"],"additionalProperties":false}`)
	if run([]ai.Tool{unbounded, tool("requires", ai.ConstrainedSamplingRequire)}); len(requests) != 1 {
		t.Fatalf("a request with nothing to relax was retried: %v", requests)
	}
	status = http.StatusForbidden
	if run([]ai.Tool{tool("prefers", ai.ConstrainedSamplingPrefer)}); len(requests) != 1 {
		t.Fatalf("a 403 was retried: %v", requests)
	}
}

// Anthropic, and Claude on Bedrock, reject an error tool result holding an image: the image follows
// the turn's tool results.
func TestAnthropicMovesErrorResultImagesAfterTheToolResults(t *testing.T) {
	model := anthropicTestModel()
	calls := ai.AssistantContent{&ai.ToolCall{ID: "a", Name: "shot", Arguments: map[string]any{}}, &ai.ToolCall{ID: "b", Name: "read", Arguments: map[string]any{}}}
	payload, _, err := buildAnthropicMessagesPayload(model, ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("go")},
		&ai.AssistantMessage{Content: calls, API: model.API, Provider: model.Provider, Model: model.ID, StopReason: ai.StopReasonToolUse},
		&ai.ToolResultMessage{ToolCallID: "a", ToolName: "shot", IsError: true, Content: ai.ToolResultContent{&ai.TextContent{Text: "failed"}, &ai.ImageContent{Data: "png", MimeType: "image/png"}}},
		&ai.ToolResultMessage{ToolCallID: "b", ToolName: "read", Content: ai.ToolResultContent{&ai.TextContent{Text: "ok"}}},
	}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(payload.Messages[len(payload.Messages)-1].Content)
	want := `[{"type":"tool_result","tool_use_id":"a","content":"failed\n(image attached after the tool results)","is_error":true},` +
		`{"type":"tool_result","tool_use_id":"b","content":"ok","is_error":false},` +
		`{"type":"text","text":"Images from the error result of tool call a (shot):"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"png"},"cache_control":{"type":"ephemeral"}}]`
	if string(got) != want {
		t.Fatalf("user content =\n%s\nwant\n%s", got, want)
	}

	model = &ai.Model{ID: "anthropic.claude-test", API: ai.APIBedrockConverse, Provider: "amazon-bedrock", Input: ai.InputModalities{ai.InputText, ai.InputImage}}
	context := ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("go")},
		&ai.AssistantMessage{Content: calls[:1], API: model.API, Provider: model.Provider, Model: model.ID, StopReason: ai.StopReasonToolUse},
		&ai.ToolResultMessage{ToolCallID: "a", ToolName: "shot", IsError: true, Content: ai.ToolResultContent{&ai.ImageContent{Data: "iVBO", MimeType: "image/png"}}},
	}}
	messages, err := convertBedrockMessages(context, model, ai.CacheRetentionNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = json.Marshal(messages[len(messages)-1].Content)
	want = `[{"toolResult":{"toolUseId":"a","content":[{"text":"(image attached after the tool results)"}],"status":"error"}},` +
		`{"text":"Images from the error result of tool call a (shot):"},{"image":{"format":"png","source":{"bytes":"iVBO"}}}]`
	if string(got) != want {
		t.Fatalf("Bedrock user content =\n%s\nwant\n%s", got, want)
	}
}

// A model whose effort is set per message, with its thinking off, gets thinking disabled and no effort.
func TestAnthropicManagedEffortModelWithThinkingOff(t *testing.T) {
	model := anthropicTestModel()
	model.Compat = json.RawMessage(`{"supportsMidConvoEffort":true,"forceAdaptiveThinking":true}`)
	off := false
	payload, _, err := buildAnthropicMessagesPayload(model, ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hi")}}},
		&AnthropicMessagesOptions{ThinkingEnabled: &off}, false)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(map[string]any{"thinking": payload.Thinking, "output_config": payload.OutputConfig, "messages": len(payload.Messages)})
	if string(got) != `{"messages":1,"output_config":null,"thinking":{"type":"disabled"}}` {
		t.Fatalf("payload = %s", got)
	}
}
