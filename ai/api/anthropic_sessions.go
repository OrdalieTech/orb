package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// Per-session Claude state, released by ai.CleanupSessionResources.
var anthropicSessions = struct {
	sync.Mutex
	// threads holds where each session's next request can continue its message thread; a nil entry
	// means the endpoint rejected threads for the session.
	threads map[string]*anthropicThread
	// overStrictToolLimits holds sessions whose strict tools Claude (Anthropic or Bedrock) rejected for
	// exceeding its limits. Their later requests send strict "prefer" tools non-strict right away: the
	// tool set rarely changes, and going back to strict would rewrite the tools and miss the cache.
	overStrictToolLimits map[string]struct{}
}{threads: map[string]*anthropicThread{}, overStrictToolLimits: map[string]struct{}{}}

func init() {
	ai.RegisterSessionResourceCleanup(func(sessionID string) error {
		anthropicSessions.Lock()
		defer anthropicSessions.Unlock()
		if sessionID == "" {
			clear(anthropicSessions.threads)
			clear(anthropicSessions.overStrictToolLimits)
		} else {
			delete(anthropicSessions.threads, sessionID)
			delete(anthropicSessions.overStrictToolLimits, sessionID)
		}
		return nil
	})
}

func strictToolLimitsExceeded(options *ai.StreamOptions) bool {
	anthropicSessions.Lock()
	defer anthropicSessions.Unlock()
	_, exceeded := anthropicSessions.overStrictToolLimits[streamSessionID(options)]
	return exceeded
}

func markStrictToolLimitsExceeded(options *ai.StreamOptions) {
	if id := streamSessionID(options); id != "" {
		anthropicSessions.Lock()
		anthropicSessions.overStrictToolLimits[id] = struct{}{}
		anthropicSessions.Unlock()
	}
}

func streamSessionID(options *ai.StreamOptions) string {
	if options == nil || options.SessionID == nil {
		return ""
	}
	return *options.SessionID
}

// anthropicThread is a reply a message thread can continue from: the hash of the full request that
// produced it plus the reply, as anthropicThreadHash computes it over prefixCount messages.
type anthropicThread struct {
	prefixCount int
	prefixHash  [sha256.Size]byte
	replyID     string
}

// anthropicThreadSession is the session whose requests use message threads, "" when they don't apply.
func anthropicThreadSession(compat resolvedAnthropicCompat, options *ai.StreamOptions) string {
	if !compat.supportsMessageThreads || resolveCacheRetention(options) == ai.CacheRetentionNone {
		return ""
	}
	return streamSessionID(options)
}

// anthropicThreadRejection is a refusal about the message thread rather than the request: the thread
// can't be continued (newThread), or the endpoint can't use threads.
type anthropicThreadRejection struct {
	error
	reason    string
	newThread bool
}

func classifyAnthropicThreadRejection(err error, status int, contents []byte) error {
	if !bytes.Contains(contents, []byte("thread")) {
		return err
	}
	var body struct {
		Error struct {
			Details struct {
				ErrorCode string `json:"error_code"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(contents, &body)
	switch code := body.Error.Details.ErrorCode; code {
	case "thread_not_found", "thread_already_continued", "thread_continue_in_flight", "thread_fingerprint_mismatch":
		return anthropicThreadRejection{err, code, true}
	case "thread_unsupported_request":
		return anthropicThreadRejection{err, code, false}
	case "":
		// An endpoint that doesn't know the field: "thread: Extra inputs are not permitted".
		if status == http.StatusBadRequest {
			return anthropicThreadRejection{err, "400", false}
		}
	}
	return err
}

// postAnthropicThread sends body, the full request, as a message thread request: a continue with
// only the new messages when the session's last reply still prefixes body, a create otherwise. A
// thread rejection is resent once in full, as a new thread or, when the endpoint can't use threads,
// without one, which turns threads off for the session. It reports whether a thread request answered.
func postAnthropicThread(
	ctx context.Context,
	model *ai.Model,
	session string,
	body jsonwire.RawObject,
	output *ai.AssistantMessage,
	post func(payload any) (*http.Response, error),
) (*http.Response, bool, error) {
	anthropicSessions.Lock()
	state, known := anthropicSessions.threads[session]
	anthropicSessions.Unlock()
	if known && state == nil {
		response, err := post(body)
		return response, false, err
	}
	request := withAnthropicThread(body, `{"type":"create"}`)
	if state != nil {
		raw, _ := body.Get("messages")
		messages := jsonwire.Elements(raw)
		if len(messages) > state.prefixCount && anthropicThreadHash(model, body, messages[:state.prefixCount]) == state.prefixHash {
			anthropicSessions.Lock()
			// Each reply can be continued once; deleting also keeps a concurrent request from trying.
			delete(anthropicSessions.threads, session)
			anthropicSessions.Unlock()
			replyID, _ := ai.Marshal(state.replyID)
			rest, _ := ai.Marshal(messages[state.prefixCount:])
			request = withAnthropicThread(body, `{"type":"continue","previous_message_id":`+string(replyID)+`}`)
			request.Set("messages", rest)
			request.Delete("system")
			request.Delete("tools")
		}
	}
	response, err := post(request)
	var rejection anthropicThreadRejection
	if err == nil || ctx.Err() != nil || !errors.As(err, &rejection) {
		return response, err == nil, err
	}
	details, _ := ai.Marshal(map[string]string{"reason": rejection.reason})
	appendDiagnostic(output, ai.AssistantMessageDiagnostic{
		Type: "anthropic_thread_fallback", Timestamp: openAINowUnixMilli(), Error: &ai.DiagnosticErrorInfo{Message: err.Error()}, Details: details,
	})
	if rejection.newThread {
		response, err = post(withAnthropicThread(body, `{"type":"create"}`))
		return response, err == nil, err
	}
	// With the threads beta, a request without thread is a normal request.
	if response, err = post(body); err == nil {
		anthropicSessions.Lock()
		anthropicSessions.threads[session] = nil
		anthropicSessions.Unlock()
	}
	return response, false, err
}

func withAnthropicThread(body jsonwire.RawObject, thread string) jsonwire.RawObject {
	request := slices.Clone(body)
	request.Set("thread", json.RawMessage(thread))
	return request
}

// recordAnthropicThread records that the session's next request can continue after output, the
// clean reply to the thread request whose full body was body.
func recordAnthropicThread(model *ai.Model, session string, body jsonwire.RawObject, output *ai.AssistantMessage, oauth, allowEmptySignature bool) {
	// A fallback model's reply is not stored by the server, so it cannot be continued.
	if output.ResponseID == nil || output.ResponseModel != nil {
		return
	}
	// The reply as the next request will send it; transformMessages adds placeholder results for
	// trailing tool calls, which are not part of it.
	reply, err := convertAnthropicMessages(transformMessages(ai.MessageList{output}, model, normalizeAnthropicToolCallID),
		oauth, nil, allowEmptySignature, nil, func(name string) string { return name }, false, "")
	if err != nil {
		return
	}
	raw, _ := body.Get("messages")
	prefix := jsonwire.Elements(raw)
	for _, message := range reply {
		if message.Role == "assistant" {
			encoded, _ := ai.Marshal(message)
			prefix = append(prefix, encoded)
		}
	}
	anthropicSessions.Lock()
	anthropicSessions.threads[session] = &anthropicThread{len(prefix), anthropicThreadHash(model, body, prefix), *output.ResponseID}
	anthropicSessions.Unlock()
}

// anthropicThreadHash covers what a continue builds on: the endpoint and the fields that must stay the
// same within a thread, as sent (a continue omits system and tools, so only this notices a change to
// them), and the messages the server already holds.
func anthropicThreadHash(model *ai.Model, body jsonwire.RawObject, messages []json.RawMessage) [sha256.Size]byte {
	hash := sha256.New()
	hash.Write([]byte(string(model.Provider) + "\x00" + model.BaseURL))
	for _, name := range []string{"model", "system", "tools", "thinking", "output_config", "tool_choice", "betas", "fallbacks"} {
		value, _ := body.Get(name)
		hash.Write(append([]byte{0}, value...))
	}
	for _, message := range messages {
		var value any
		_ = json.Unmarshal(message, &value)
		encoded, _ := json.Marshal(normalizeAnthropicThreadMessage(value))
		hash.Write(append([]byte{0}, encoded...))
	}
	return [sha256.Size]byte(hash.Sum(nil))
}

// normalizeAnthropicThreadMessage drops what moves between requests without changing a message's
// meaning: cache markers, and the newest user message's string content sent as one cached text block.
func normalizeAnthropicThreadMessage(value any) any {
	switch value := value.(type) {
	case map[string]any:
		delete(value, "cache_control")
		for key, field := range value {
			if text, ok := field.(string); ok && key == "content" {
				value[key] = []any{map[string]any{"type": "text", "text": text}}
			} else {
				value[key] = normalizeAnthropicThreadMessage(field)
			}
		}
	case []any:
		for index, item := range value {
			value[index] = normalizeAnthropicThreadMessage(item)
		}
	}
	return value
}
