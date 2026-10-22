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
		`{"error":{"code":"server_busy"}}`, "Our servers are currently busy, please try again", "Selected model is at capacity", "The upstream provider returned an error",
		"The service is temporarily unavailable", "Unable to forward upstream stream",
		"stream disconnected before completion: stream closed before response.completed", "upstream_stream_read_error",
		"request to https://oauth2.googleapis.com/token failed, reason: ", "HTTP 503 Service", "error 429",
	} {
		if !IsRetryableAssistantError(failed(text)) {
			t.Fatalf("not retryable: %q", text)
		}
	}
	for _, text := range []string{"429 quota exceeded", "429 subscription_sharing_usage_limit_exceeded", "429 subscription_sharing_workplace_individual_usage_limit_exceeded",
		"bad request req_15003abc", "body of 45020 bytes rejected"} {
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
	// HTTP 413 is a request too large from any provider; a bodyless 400 only from Cerebras.
	for _, text := range []string{"413 status code (no body)", `413 {"message":"failed to parse request","code":""}`, `github-copilot API error (413): {"message":"failed to parse request"}`} {
		if !IsContextOverflow(failed(text), 200000) {
			t.Fatalf("413 not classified as overflow: %s", text)
		}
	}
	bodyless := failed("400 status code (no body)")
	if IsContextOverflow(bodyless, 200000) {
		t.Fatal("non-Cerebras bodyless 400 classified as overflow")
	}
	bodyless.Provider = "cerebras"
	if !IsContextOverflow(bodyless, 200000) {
		t.Fatal("Cerebras bodyless 400 was not classified as overflow")
	}
	if IsContextOverflow(failed("400 the upstream returned 413 once"), 200000) {
		t.Fatal("a 413 inside the body classified as overflow")
	}
	silent := &AssistantMessage{StopReason: StopReasonStop, Usage: Usage{Input: 101}}
	if !IsContextOverflow(silent, 100) {
		t.Fatal("silent overflow not detected")
	}
}
