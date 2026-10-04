package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestReadAnthropicSSEAcceptsAllLineEndings(t *testing.T) {
	for _, separator := range []string{"\n", "\r\n", "\r"} {
		t.Run(strings.ReplaceAll(separator, "\r", "CR"), func(t *testing.T) {
			input := strings.Join([]string{
				": heartbeat",
				"event: content_block_delta",
				`data: {"type":"content_block_delta",`,
				`data: "index":0}`,
				"",
			}, separator)
			var eventName, data string
			err := readAnthropicSSE(strings.NewReader(input), func(name string, raw []byte, _ []string) error {
				eventName, data = name, string(raw)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if eventName != "content_block_delta" || data != "{\"type\":\"content_block_delta\",\n\"index\":0}" {
				t.Fatalf("decoded event = %q %q", eventName, data)
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
	var authorizations, betas []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/oauth/token":
			exchanges++
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
	if authorizations[1] != "Bearer federated" || !strings.Contains(betas[1], "oauth-2025-04-20") {
		t.Fatalf("authorization = %q, beta = %q", authorizations, betas)
	}
}
