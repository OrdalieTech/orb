package api

import (
	"cmp"
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
		if len(requests) == 1 && (strict["prefers"] || strict["requires"]) {
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
	var session *string
	run := func(tools []ai.Tool) *ai.AssistantMessage {
		requests = nil
		key := "key"
		stream, err := StreamAnthropicMessagesWithOptions(context.Background(), model, ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hi")}}, Tools: &tools},
			&AnthropicMessagesOptions{StreamOptions: ai.StreamOptions{APIKey: &key, SessionID: session}})
		if err != nil {
			t.Fatal(err)
		}
		message, _ := ai.Collect(stream)
		return message
	}
	if message := run([]ai.Tool{tool("prefers", ai.ConstrainedSamplingPrefer), tool("requires", ai.ConstrainedSamplingRequire)}); message == nil || message.StopReason != ai.StopReasonStop ||
		message.Diagnostics == nil || (*message.Diagnostics)[0].Type != "anthropic_strict_tools_fallback" {
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
	// A session's later requests send them non-strict right away, until its resources are cleaned (#11626).
	id := t.Name()
	session = &id
	t.Cleanup(func() { _ = ai.CleanupSessionResources(id) })
	preferred := []ai.Tool{tool("prefers", ai.ConstrainedSamplingPrefer)}
	for _, want := range []int{2, 1} {
		if message := run(preferred); message.StopReason != ai.StopReasonStop || len(requests) != want || requests[len(requests)-1]["prefers"] {
			t.Fatalf("session requests = %v, want %d", requests, want)
		}
	}
	_ = ai.CleanupSessionResources(id)
	if run(preferred); len(requests) != 2 || !requests[0]["prefers"] {
		t.Fatalf("after cleanup requests = %v", requests)
	}
	status = http.StatusForbidden
	if run(preferred); len(requests) != 1 {
		t.Fatalf("a 403 was retried: %v", requests)
	}
}

// Anthropic, and Claude on Bedrock, reject an error tool result holding an image: the image follows
// the turn's tool results.
func TestAnthropicMovesErrorResultImagesAfterTheToolResults(t *testing.T) {
	model := anthropicTestModel()
	calls := ai.AssistantContent{&ai.ToolCall{ID: "a", Name: "shot", Arguments: map[string]any{}}, &ai.ToolCall{ID: "b", Name: "read", Arguments: map[string]any{}}}
	compat, _ := getAnthropicCompat(model)
	payload, _, err := buildAnthropicMessagesPayload(model, compat, ai.Context{Messages: ai.MessageList{
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
	compat, _ := getAnthropicCompat(model)
	payload, _, err := buildAnthropicMessagesPayload(model, compat, ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hi")}}},
		&AnthropicMessagesOptions{ThinkingEnabled: &off}, false)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(map[string]any{"thinking": payload.Thinking, "output_config": payload.OutputConfig, "messages": len(payload.Messages)})
	if string(got) != `{"messages":1,"output_config":null,"thinking":{"type":"disabled"}}` {
		t.Fatalf("payload = %s", got)
	}
}

// anthropicThreadServer is a fake Anthropic endpoint for message threads: it records each request's
// body and betas and answers from queue, else with a reply whose id is msg_<request number>.
type anthropicThreadServer struct {
	t        *testing.T
	requests []anthropicThreadRequest
	queue    []func(http.ResponseWriter, anthropicThreadRequest)
	options  AnthropicMessagesOptions
	model    *ai.Model
}

type anthropicThreadRequest struct {
	Model    string            `json:"model"`
	Messages []json.RawMessage `json:"messages"`
	System   json.RawMessage   `json:"system"`
	Tools    json.RawMessage   `json:"tools"`
	Thread   *struct {
		Type              string `json:"type"`
		PreviousMessageID string `json:"previous_message_id"`
	} `json:"thread"`
	betas string
}

func (request anthropicThreadRequest) thread() string {
	if request.Thread == nil {
		return ""
	}
	return strings.TrimSpace(request.Thread.Type + " " + request.Thread.PreviousMessageID)
}

func anthropicThreadReply(writer http.ResponseWriter, id, model string, toolCall bool) { // a model other than the request's is a fallback reply
	writer.Header().Set("Content-Type", "text/event-stream")
	events := `{"type":"message_start","message":{"id":"` + id + `","model":"` + model + `","usage":{"input_tokens":1,"output_tokens":0}}}` +
		"\n" + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` +
		"\n" + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}` +
		"\n" + `{"type":"content_block_stop","index":0}`
	stop := "end_turn"
	if toolCall {
		stop = "tool_use"
		events += "\n" + `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}}` +
			"\n" + `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"value\":\"x\"}"}}` +
			"\n" + `{"type":"content_block_stop","index":1}`
	}
	events += "\n" + `{"type":"message_delta","delta":{"stop_reason":"` + stop + `"},"usage":{"output_tokens":1}}` + "\n" + `{"type":"message_stop"}`
	for _, event := range strings.Split(events, "\n") {
		var head struct{ Type string }
		_ = json.Unmarshal([]byte(event), &head)
		_, _ = io.WriteString(writer, "event: "+head.Type+"\ndata: "+event+"\n\n")
	}
}

func anthropicThreadError(status int, message, code string) func(http.ResponseWriter, anthropicThreadRequest) {
	return func(writer http.ResponseWriter, _ anthropicThreadRequest) {
		details := ""
		if code != "" {
			details = `,"details":{"error_code":"` + code + `"}`
		}
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, `{"type":"error","error":{"type":"invalid_request_error","message":"`+message+`"`+details+`}}`)
	}
}

func newAnthropicThreadServer(t *testing.T) *anthropicThreadServer {
	harness := &anthropicThreadServer{t: t}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body anthropicThreadRequest
		_ = json.NewDecoder(request.Body).Decode(&body)
		body.betas = request.Header.Get("anthropic-beta")
		harness.requests = append(harness.requests, body)
		if len(harness.queue) > 0 {
			next := harness.queue[0]
			harness.queue = harness.queue[1:]
			next(writer, body)
			return
		}
		anthropicThreadReply(writer, fmt.Sprintf("msg_%d", len(harness.requests)), body.Model, false)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = ai.CleanupSessionResources() })
	key, session, retries := "key", t.Name(), 0
	harness.options = AnthropicMessagesOptions{StreamOptions: ai.StreamOptions{APIKey: &key, SessionID: &session, HTTPClient: server.Client(), MaxRetries: &retries}}
	harness.model = anthropicTestModel()
	harness.model.Reasoning, harness.model.BaseURL, harness.model.Compat = false, server.URL, json.RawMessage(`{"supportsMessageThreads":true}`)
	return harness
}

func (harness *anthropicThreadServer) turn(messages *ai.MessageList, system string, tools []ai.Tool, edit func(*ai.Model, *AnthropicMessagesOptions)) *ai.AssistantMessage {
	harness.t.Helper()
	model, options := *harness.model, harness.options
	if edit != nil {
		edit(&model, &options)
	}
	if tools == nil {
		tools = []ai.Tool{{Name: "read", Description: "read", Parameters: ai.JSONSchema(`{"type":"object","properties":{"value":{"type":"string"}}}`)}}
	}
	stream, err := StreamAnthropicMessagesWithOptions(context.Background(), &model, ai.Context{SystemPrompt: &system, Messages: *messages, Tools: &tools}, &options)
	if err != nil {
		harness.t.Fatal(err)
	}
	message, _ := ai.Collect(stream)
	*messages = append(*messages, message)
	return message
}

func anthropicThreadUser(text string) *ai.UserMessage {
	return &ai.UserMessage{Content: ai.NewUserText(text), Timestamp: 1}
}

// pi 1.5.1 message threads (#11545): a session's request continues the thread with only the new
// messages, and anything that changed earlier in the request sends the full request.
func TestAnthropicMessageThreadsContinueOnlyUnchangedConversations(t *testing.T) {
	if compat, _ := getAnthropicCompat(anthropicTestModel()); !compat.supportsMessageThreads {
		t.Fatal("api.anthropic.com does not default to message threads")
	}
	if compat, _ := getAnthropicCompat(&ai.Model{Provider: "openrouter", BaseURL: "https://openrouter.ai/api"}); compat.supportsMessageThreads {
		t.Fatal("a gateway defaults to message threads")
	}

	h := newAnthropicThreadServer(t)
	messages := ai.MessageList{anthropicThreadUser("first")}
	h.turn(&messages, "system", nil, nil)
	messages = append(messages, anthropicThreadUser("second"))
	if result := h.turn(&messages, "system", nil, nil); result.StopReason != ai.StopReasonStop {
		t.Fatalf("continued turn = %#v", result)
	}
	create, next := h.requests[0], h.requests[1]
	if create.thread() != "create" || create.System == nil || create.Tools == nil || !strings.Contains(create.betas, "message-threads-2026-08-12") {
		t.Fatalf("first request = %+v", create)
	}
	if next.thread() != "continue msg_1" || next.System != nil || next.Tools != nil || next.betas != create.betas ||
		len(next.Messages) != 1 || string(next.Messages[0]) != `{"role":"user","content":[{"type":"text","text":"second","cache_control":{"type":"ephemeral"}}]}` {
		t.Fatalf("continue request = %+v %s", next, next.Messages)
	}

	for _, test := range []struct {
		name   string
		system string
		tools  []ai.Tool
		edit   func(*ai.Model, *AnthropicMessagesOptions)
	}{
		{name: "system prompt", system: "other"},
		{name: "tools", tools: []ai.Tool{{Name: "other", Description: "other", Parameters: ai.JSONSchema(`{"type":"object"}`)}}},
		{name: "model", edit: func(model *ai.Model, _ *AnthropicMessagesOptions) { model.ID = "claude-other" }},
		{name: "cache retention", edit: func(_ *ai.Model, options *AnthropicMessagesOptions) {
			long := ai.CacheRetentionLong
			options.CacheRetention = &long
		}},
	} {
		h.requests = nil
		system := cmp.Or(test.system, "system")
		messages = append(messages, anthropicThreadUser(test.name))
		h.turn(&messages, system, test.tools, test.edit)
		messages = append(messages, anthropicThreadUser("again"))
		h.turn(&messages, "system", nil, nil)
		if h.requests[0].thread() != "create" || len(h.requests[0].Messages) != len(messages)-3 || !strings.HasPrefix(h.requests[1].thread(), "create") {
			t.Fatalf("%s change: threads %q %q", test.name, h.requests[0].thread(), h.requests[1].thread())
		}
	}

	// After cleanup, and with caching off, there is nothing to continue.
	_ = ai.CleanupSessionResources(t.Name())
	h.requests = nil
	messages = append(messages, anthropicThreadUser("after cleanup"))
	h.turn(&messages, "system", nil, nil)
	none := ai.CacheRetentionNone
	h.turn(&messages, "system", nil, func(_ *ai.Model, options *AnthropicMessagesOptions) { options.CacheRetention = &none })
	if h.requests[0].thread() != "create" || h.requests[1].Thread != nil || strings.Contains(h.requests[1].betas, "message-threads") {
		t.Fatalf("after cleanup %q, without caching %q %q", h.requests[0].thread(), h.requests[1].thread(), h.requests[1].betas)
	}
}

// A reply's conversion for the next request must match how that request sends it: tool calls (with
// Claude Code names under OAuth) and per-message effort markers.
func TestAnthropicMessageThreadsContinueAfterToolCallsAndEffortChanges(t *testing.T) {
	h := newAnthropicThreadServer(t)
	h.queue = append(h.queue, func(writer http.ResponseWriter, _ anthropicThreadRequest) {
		anthropicThreadReply(writer, "msg_tool", "claude-test", true)
	})
	oauth := func(_ *ai.Model, options *AnthropicMessagesOptions) {
		key := "sk-ant-oat-test"
		options.APIKey = &key
	}
	messages := ai.MessageList{anthropicThreadUser("read it")}
	if first := h.turn(&messages, "system", nil, oauth); first.StopReason != ai.StopReasonToolUse {
		t.Fatalf("tool turn = %#v", first)
	}
	messages = append(messages, &ai.ToolResultMessage{ToolCallID: "toolu_1", ToolName: "read", Content: ai.ToolResultContent{&ai.TextContent{Text: "result"}}, Timestamp: 1})
	h.turn(&messages, "system", nil, oauth)
	if next := h.requests[1]; next.thread() != "continue msg_tool" || len(next.Messages) != 1 || !strings.Contains(string(next.Messages[0]), `"tool_use_id":"toolu_1"`) {
		t.Fatalf("after tool calls: %q %s", next.thread(), next.Messages)
	}

	h.requests = nil
	effort := func(level AnthropicEffort) func(*ai.Model, *AnthropicMessagesOptions) {
		return func(model *ai.Model, options *AnthropicMessagesOptions) {
			enabled := true
			model.Reasoning, model.Compat = true, json.RawMessage(`{"supportsMessageThreads":true,"forceAdaptiveThinking":true,"supportsMidConvoEffort":true}`)
			options.ThinkingEnabled, options.Effort = &enabled, &level
		}
	}
	messages = ai.MessageList{anthropicThreadUser("first")}
	h.turn(&messages, "system", nil, effort(AnthropicEffortHigh))
	messages = append(messages, anthropicThreadUser("second"))
	h.turn(&messages, "system", nil, effort(AnthropicEffortLow))
	if next := h.requests[1]; next.thread() != "continue msg_1" || len(next.Messages) != 2 || !strings.Contains(string(next.Messages[1]), `"effort":"low"`) {
		t.Fatalf("after an effort change: %q %s", next.thread(), next.Messages)
	}
}

// Thread rejections are resent once in full; other errors are passed on.
func TestAnthropicMessageThreadsFallBackToTheFullRequest(t *testing.T) {
	for _, test := range []struct {
		name         string
		respond      func(http.ResponseWriter, anthropicThreadRequest)
		resent, last string // thread of the resend ("-" when none) and of the turn after
		failed       bool
	}{
		{name: "expired", respond: anthropicThreadError(404, "not found", "thread_not_found"), resent: "create", last: "continue msg_3"},
		{name: "in flight", respond: anthropicThreadError(400, "in flight", "thread_continue_in_flight"), resent: "create", last: "continue msg_3"},
		{name: "unsupported", respond: anthropicThreadError(400, "rejected", "thread_unsupported_request"), resent: "", last: ""},
		{name: "unknown field", respond: anthropicThreadError(400, "thread: Extra inputs are not permitted", ""), resent: "", last: ""},
		{name: "not about the thread", respond: anthropicThreadError(400, "prompt is too long", ""), resent: "-", last: "create", failed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAnthropicThreadServer(t)
			messages := ai.MessageList{anthropicThreadUser("first")}
			h.turn(&messages, "system", nil, nil)
			messages = append(messages, anthropicThreadUser("second"))
			h.queue = append(h.queue, test.respond)
			result := h.turn(&messages, "system", nil, nil)
			if test.failed {
				messages = messages[:len(messages)-1]
				if result.StopReason != ai.StopReasonError || result.Diagnostics != nil || len(h.requests) != 2 {
					t.Fatalf("failed turn = %#v after %d requests", result, len(h.requests))
				}
			} else if result.StopReason != ai.StopReasonStop || result.Diagnostics == nil || (*result.Diagnostics)[0].Type != "anthropic_thread_fallback" ||
				len(h.requests) != 3 || len(h.requests[2].Messages) != 3 || h.requests[2].thread() != test.resent {
				t.Fatalf("resent %q after %d requests: %#v", h.requests[len(h.requests)-1].thread(), len(h.requests), result)
			}
			messages = append(messages, anthropicThreadUser("third"))
			h.turn(&messages, "system", nil, nil)
			if last := h.requests[len(h.requests)-1]; last.thread() != test.last || last.betas != h.requests[0].betas {
				t.Fatalf("next turn thread = %q, betas %q", last.thread(), last.betas)
			}
		})
	}
}

// Non-strict schemas keep root $defs and definitions so property $refs resolve (#9557), and still
// drop root keywords Anthropic rejects.
func TestAnthropicNonStrictToolSchemaKeepsSharedDefinitions(t *testing.T) {
	tools := []ai.Tool{{Name: "t", Description: "t", Parameters: ai.JSONSchema(`{"type":"object","anyOf":[{"required":["a"]}],"properties":{"a":{"$ref":"#/$defs/A"},"b":{"$ref":"#/definitions/B"}},"$defs":{"A":{"type":"object"}},"definitions":{"B":{"type":"string"}}}`)}}
	converted, err := convertAnthropicTools(tools, false, false, false, false, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(converted[0].InputSchema), `{"type":"object","properties":{"a":{"$ref":"#/$defs/A"},"b":{"$ref":"#/definitions/B"}},"required":[],"$defs":{"A":{"type":"object"}},"definitions":{"B":{"type":"string"}}}`; got != want {
		t.Fatalf("input_schema = %s, want %s", got, want)
	}
}
