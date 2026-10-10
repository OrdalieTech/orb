package ai

import "testing"

func TestRetryAndOverflowClassification(t *testing.T) {
	failed := func(text string) *AssistantMessage {
		return &AssistantMessage{StopReason: StopReasonError, ErrorMessage: &text}
	}
	for _, text := range []string{
		"overloaded_error", "currently experiencing high demand", "HTTP 520", "Provider finish_reason: network_error", "stream ended before a terminal response event",
		// DNS transport failures (upstream 33e40c3e) — Node wording plus Go's.
		"The pending stream has been canceled (caused by: getaddrinfo ENOTFOUND bedrock-runtime.us-east-1.amazonaws.com)",
		"connect ENOTFOUND api.example.com",
		"EAI_AGAIN api.example.com",
		"getaddrinfo failed for api.example.com",
		"dial tcp: lookup api.example.com: no such host",
		`Post "https://chatgpt.com/backend-api/codex/responses": dial tcp [2a06:98c1::1]:443: connect: network is unreachable`,
		"read tcp 10.0.0.2:51234->1.2.3.4:443: read: connection reset by peer",
		"dial tcp: lookup chatgpt.com on [::1]:53: androiddns: dnsproxyd error -3 (no such process)",
		// Upstream gateway buffer exhaustion while retrying (fe10558eb).
		"Exceeded request buffer limit while retrying upstream",
		"subscription_sharing_usage_unavailable",
		// Busy or full providers (upstream 8b5708db, 3874b3e9).
		`{"error":{"code":"server_busy"}}`, "Our servers are currently busy, please try again", "Selected model is at capacity",
	} {
		if !IsRetryableAssistantError(failed(text)) {
			t.Fatalf("not retryable: %q", text)
		}
	}
	for _, text := range []string{"429 quota exceeded", "429 subscription_sharing_usage_limit_exceeded"} {
		if IsRetryableAssistantError(failed(text)) {
			t.Fatalf("limit retried: %q", text)
		}
	}
	for _, text := range []string{`{"code":"1261","message":"Prompt too long"}`, `{"code":"1261","message":"Prompt exceeds max length"}`} {
		if !IsContextOverflow(failed(text), 200000) {
			t.Fatalf("z.ai overflow not detected: %s", text)
		}
	}
	if !IsContextOverflow(failed("Range of input length should be [1, 999999]"), 200000) {
		t.Fatal("Qwen Token Plan overflow not detected")
	}
	if IsContextOverflow(failed("Rate limit exceeded: too many tokens"), 200000) {
		t.Fatal("rate limit classified as overflow")
	}
	if IsContextOverflow(failed("Input exceeds the model's maximum context length"), 200000) {
		t.Fatal("incomplete maximum-context phrase classified as overflow")
	}
	if !IsContextOverflow(failed(" Throttling error: too many tokens"), 200000) {
		t.Fatal("error text was trimmed before anchored upstream patterns")
	}
	bodyless := failed("413 status code (no body)")
	if IsContextOverflow(bodyless, 200000) {
		t.Fatal("non-Cerebras bodyless 413 classified as overflow")
	}
	bodyless.Provider = "cerebras"
	if !IsContextOverflow(bodyless, 200000) {
		t.Fatal("Cerebras bodyless 413 was not classified as overflow")
	}
	silent := &AssistantMessage{StopReason: StopReasonStop, Usage: Usage{Input: 101}}
	if !IsContextOverflow(silent, 100) {
		t.Fatal("silent overflow not detected")
	}
}
