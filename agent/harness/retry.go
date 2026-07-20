package harness

import (
	"regexp"

	"github.com/OrdalieTech/pi-go/ai"
)

var nonRetryableProviderLimitPatterns = compilePatterns([]string{
	`GoUsageLimitError`, `FreeUsageLimitError`, `Monthly usage limit reached`, `available balance`,
	`insufficient_quota`, `out of budget`, `quota exceeded`, `billing`,
})

var retryableProviderPatterns = compilePatterns([]string{
	`overloaded`, `rate.?limit`, `too many requests`, `429`, `500`, `502`, `503`, `504`, `524`,
	`service.?unavailable`, `server.?error`, `internal.?error`, `provider.?returned.?error`,
	`network.?error`, `connection.?error`, `connection.?refused`, `connection.?lost`, `other side closed`,
	`fetch failed`, `upstream.?connect`, `reset before headers`, `socket hang up`,
	`socket connection was closed`, `timed? out`, `timeout`, `terminated`, `websocket.?closed`,
	`websocket.?error`, `ended without`, `stream ended before message_stop`,
	`stream ended before a terminal response event`, `http2 request did not get a response`, `retry delay`,
	`you can retry your request`, `try your request again`, `please retry your request`, `ResourceExhausted`,
})

var overflowPatterns = compilePatterns([]string{
	`prompt is too long`, `request_too_large`, `input is too long for requested model`,
	`exceeds the context window`, `exceeds (the )?(model'?s )?maximum context length`,
	`input token count.*exceeds the maximum`, `maximum prompt length is [0-9]+`,
	`reduce the length of the messages`, `range of input length should be`, `maximum context length is [0-9]+ tokens`,
	`exceeds (the )?maximum allowed input length of [0-9,]+ tokens?`,
	`input \([0-9]+ tokens\) is longer than the model'?s context length \([0-9]+ tokens\)`,
	`exceeds the limit of [0-9]+`, `exceeds the available context size`, `greater than the context length`,
	`context window exceeds limit`, `exceeded model token limit`,
	`too large for model with [0-9]+ maximum context length`,
	`prompt has [0-9,]+ tokens?, but the configured context size is [0-9,]+ tokens?`,
	`model_context_window_exceeded`, `prompt too long; exceeded (max )?context length`,
	`context[_ ]length[_ ]exceeded`, `too many tokens`, `token limit exceeded`,
	`^4(00|13)[[:space:]]*(status code)?[[:space:]]*\(no body\)`,
})

var nonOverflowPatterns = compilePatterns([]string{
	`^(Throttling error|Service unavailable):`, `rate limit`, `too many requests`,
})

func IsRetryableAssistantError(message *ai.AssistantMessage) bool {
	if message == nil || message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || *message.ErrorMessage == "" {
		return false
	}
	text := *message.ErrorMessage
	if matchesAny(nonRetryableProviderLimitPatterns, text) {
		return false
	}
	return matchesAny(retryableProviderPatterns, text)
}

func IsContextOverflow(message *ai.AssistantMessage, contextWindow float64) bool {
	if message == nil {
		return false
	}
	if message.StopReason == ai.StopReasonError && message.ErrorMessage != nil {
		text := *message.ErrorMessage
		if !matchesAny(nonOverflowPatterns, text) && matchesAny(overflowPatterns, text) {
			return true
		}
	}
	inputTokens := message.Usage.Input + message.Usage.CacheRead
	if contextWindow > 0 && message.StopReason == ai.StopReasonStop && float64(inputTokens) > contextWindow {
		return true
	}
	return contextWindow > 0 && message.StopReason == ai.StopReasonLength && message.Usage.Output == 0 && float64(inputTokens) >= contextWindow*0.99
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		compiled = append(compiled, regexp.MustCompile(`(?i)`+pattern))
	}
	return compiled
}

func matchesAny(patterns []*regexp.Regexp, value string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}
