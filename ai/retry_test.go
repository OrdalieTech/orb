package ai

import "testing"

func TestRetryAndOverflowClassification(t *testing.T) {
	failed := func(text string) *AssistantMessage {
		return &AssistantMessage{StopReason: StopReasonError, ErrorMessage: &text}
	}
	for _, text := range []string{
		"overloaded_error", "Provider finish_reason: network_error", "stream ended before a terminal response event",
		// DNS transport failures (upstream 33e40c3e) — Node wording plus Go's.
		"The pending stream has been canceled (caused by: getaddrinfo ENOTFOUND bedrock-runtime.us-east-1.amazonaws.com)",
		"connect ENOTFOUND api.example.com",
		"EAI_AGAIN api.example.com",
		"getaddrinfo failed for api.example.com",
		"dial tcp: lookup api.example.com: no such host",
	} {
		if !IsRetryableAssistantError(failed(text)) {
			t.Fatalf("not retryable: %q", text)
		}
	}
	if IsRetryableAssistantError(failed("429 quota exceeded")) {
		t.Fatal("quota exhaustion retried")
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
	silent := &AssistantMessage{StopReason: StopReasonStop, Usage: Usage{Input: 101}}
	if !IsContextOverflow(silent, 100) {
		t.Fatal("silent overflow not detected")
	}
}
