package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/ai"
)

const (
	openAICodexWebSocketBeta       = "responses_websockets=2026-02-06"
	openAICodexWebSocketIdleTTL    = 5 * time.Minute
	openAICodexWebSocketMaximumAge = 55 * time.Minute
	defaultCodexWebSocketTimeout   = 15 * time.Second
)

type openAICodexSocket interface {
	WriteText([]byte) error
	ReadMessage(context.Context, time.Duration) ([]byte, error)
	Close(uint16, string) error
	IsOpen() bool
}

var (
	openAICodexConnectWebSocket = func(ctx context.Context, endpoint string, headers http.Header, timeout time.Duration) (openAICodexSocket, error) {
		return connectCodexWebSocket(ctx, endpoint, headers, timeout)
	}
	openAICodexWebSocketNow = time.Now
	openAICodexWebSockets   = newOpenAICodexWebSocketState()
)

func init() {
	ai.RegisterSessionResourceCleanup(func(sessionID string) error {
		if sessionID == "" {
			CloseOpenAICodexWebSocketSessions()
		} else {
			CloseOpenAICodexWebSocketSessions(sessionID)
		}
		return nil
	})
}

type OpenAICodexWebSocketDebugStats struct {
	Requests                int
	ConnectionsCreated      int
	ConnectionsReused       int
	CachedContextRequests   int
	StoreTrueRequests       int
	FullContextRequests     int
	DeltaRequests           int
	LastInputItems          int
	LastDeltaInputItems     *int
	LastPreviousResponseID  *string
	WebSocketFailures       int
	SSEFallbacks            int
	WebSocketFallbackActive bool
	LastWebSocketError      *string
}

type openAICodexWebSocketState struct {
	mu        sync.Mutex
	sessions  map[string]*openAICodexWebSocketEntry
	stats     map[string]*OpenAICodexWebSocketDebugStats
	fallbacks map[string]bool
}

type openAICodexWebSocketEntry struct {
	socket       openAICodexSocket
	busy         bool
	createdAt    time.Time
	idleTimer    *time.Timer
	continuation *openAICodexContinuation
}

type openAICodexContinuation struct {
	other         []byte
	input         []json.RawMessage
	responseID    string
	responseItems []json.RawMessage
}

type openAICodexWebSocketLease struct {
	sessionID string
	entry     *openAICodexWebSocketEntry
	socket    openAICodexSocket
	reused    bool
}

func newOpenAICodexWebSocketState() *openAICodexWebSocketState {
	return &openAICodexWebSocketState{
		sessions:  make(map[string]*openAICodexWebSocketEntry),
		stats:     make(map[string]*OpenAICodexWebSocketDebugStats),
		fallbacks: make(map[string]bool),
	}
}

func GetOpenAICodexWebSocketDebugStats(sessionID string) *OpenAICodexWebSocketDebugStats {
	openAICodexWebSockets.mu.Lock()
	defer openAICodexWebSockets.mu.Unlock()
	stats := openAICodexWebSockets.stats[sessionID]
	if stats == nil {
		return nil
	}
	copy := *stats
	copy.LastDeltaInputItems = cloneInt(stats.LastDeltaInputItems)
	copy.LastPreviousResponseID = cloneStringValue(stats.LastPreviousResponseID)
	copy.LastWebSocketError = cloneStringValue(stats.LastWebSocketError)
	return &copy
}

func ResetOpenAICodexWebSocketDebugStats(sessionID ...string) {
	openAICodexWebSockets.mu.Lock()
	defer openAICodexWebSockets.mu.Unlock()
	if len(sessionID) > 0 && sessionID[0] != "" {
		delete(openAICodexWebSockets.stats, sessionID[0])
		delete(openAICodexWebSockets.fallbacks, sessionID[0])
		return
	}
	clear(openAICodexWebSockets.stats)
	clear(openAICodexWebSockets.fallbacks)
}

func CloseOpenAICodexWebSocketSessions(sessionID ...string) {
	openAICodexWebSockets.mu.Lock()
	entries := make([]*openAICodexWebSocketEntry, 0)
	if len(sessionID) > 0 && sessionID[0] != "" {
		if entry := openAICodexWebSockets.sessions[sessionID[0]]; entry != nil {
			entries = append(entries, entry)
			delete(openAICodexWebSockets.sessions, sessionID[0])
		}
	} else {
		for _, entry := range openAICodexWebSockets.sessions {
			entries = append(entries, entry)
		}
		clear(openAICodexWebSockets.sessions)
	}
	for _, entry := range entries {
		if entry.idleTimer != nil {
			entry.idleTimer.Stop()
		}
	}
	openAICodexWebSockets.mu.Unlock()
	for _, entry := range entries {
		if entry.socket != nil {
			_ = entry.socket.Close(1000, "debug_close")
		}
	}
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStringValue(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func openAICodexWebSocketFallbackActive(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	openAICodexWebSockets.mu.Lock()
	defer openAICodexWebSockets.mu.Unlock()
	return openAICodexWebSockets.fallbacks[sessionID]
}

func recordOpenAICodexWebSocketFailure(sessionID string, failure error) {
	if sessionID == "" {
		return
	}
	message := failure.Error()
	openAICodexWebSockets.mu.Lock()
	defer openAICodexWebSockets.mu.Unlock()
	openAICodexWebSockets.fallbacks[sessionID] = true
	stats := openAICodexWebSocketStatsLocked(sessionID)
	stats.WebSocketFailures++
	stats.LastWebSocketError = &message
	stats.WebSocketFallbackActive = true
}

func recordOpenAICodexSSEFallback(sessionID string) {
	if sessionID == "" {
		return
	}
	openAICodexWebSockets.mu.Lock()
	defer openAICodexWebSockets.mu.Unlock()
	stats := openAICodexWebSocketStatsLocked(sessionID)
	stats.SSEFallbacks++
	stats.WebSocketFallbackActive = openAICodexWebSockets.fallbacks[sessionID]
}

func openAICodexWebSocketStatsLocked(sessionID string) *OpenAICodexWebSocketDebugStats {
	stats := openAICodexWebSockets.stats[sessionID]
	if stats == nil {
		stats = &OpenAICodexWebSocketDebugStats{}
		openAICodexWebSockets.stats[sessionID] = stats
	}
	return stats
}

func acquireOpenAICodexWebSocket(
	ctx context.Context,
	endpoint string,
	headers http.Header,
	sessionID string,
	connectTimeout time.Duration,
) (*openAICodexWebSocketLease, error) {
	if sessionID == "" {
		socket, err := openAICodexConnectWebSocket(ctx, endpoint, headers, connectTimeout)
		if err != nil {
			return nil, err
		}
		return &openAICodexWebSocketLease{socket: socket}, nil
	}
	now := openAICodexWebSocketNow()
	var stale openAICodexSocket
	openAICodexWebSockets.mu.Lock()
	entry := openAICodexWebSockets.sessions[sessionID]
	if entry != nil && entry.idleTimer != nil {
		entry.idleTimer.Stop()
		entry.idleTimer = nil
	}
	if entry != nil && !entry.busy && entry.socket != nil && entry.socket.IsOpen() && now.Sub(entry.createdAt) < openAICodexWebSocketMaximumAge {
		entry.busy = true
		openAICodexWebSockets.mu.Unlock()
		return &openAICodexWebSocketLease{sessionID: sessionID, entry: entry, socket: entry.socket, reused: true}, nil
	}
	if entry != nil && entry.busy {
		openAICodexWebSockets.mu.Unlock()
		socket, err := openAICodexConnectWebSocket(ctx, endpoint, headers, connectTimeout)
		if err != nil {
			return nil, err
		}
		return &openAICodexWebSocketLease{sessionID: sessionID, socket: socket}, nil
	}
	if entry != nil {
		delete(openAICodexWebSockets.sessions, sessionID)
		stale = entry.socket
	}
	entry = &openAICodexWebSocketEntry{busy: true, createdAt: now}
	openAICodexWebSockets.sessions[sessionID] = entry
	openAICodexWebSockets.mu.Unlock()
	if stale != nil {
		_ = stale.Close(1000, "connection_age_limit")
	}
	socket, err := openAICodexConnectWebSocket(ctx, endpoint, headers, connectTimeout)
	openAICodexWebSockets.mu.Lock()
	if err != nil {
		if openAICodexWebSockets.sessions[sessionID] == entry {
			delete(openAICodexWebSockets.sessions, sessionID)
		}
		openAICodexWebSockets.mu.Unlock()
		return nil, err
	}
	entry.socket = socket
	if openAICodexWebSockets.sessions[sessionID] != entry {
		openAICodexWebSockets.mu.Unlock()
		return &openAICodexWebSocketLease{sessionID: sessionID, socket: socket}, nil
	}
	openAICodexWebSockets.mu.Unlock()
	return &openAICodexWebSocketLease{sessionID: sessionID, entry: entry, socket: socket}, nil
}

func (lease *openAICodexWebSocketLease) release(keep bool) {
	if lease == nil || lease.socket == nil {
		return
	}
	if lease.entry == nil {
		_ = lease.socket.Close(1000, "done")
		return
	}
	openAICodexWebSockets.mu.Lock()
	current := openAICodexWebSockets.sessions[lease.sessionID]
	if current != lease.entry || !keep || !lease.socket.IsOpen() {
		if current == lease.entry {
			delete(openAICodexWebSockets.sessions, lease.sessionID)
		}
		if lease.entry.idleTimer != nil {
			lease.entry.idleTimer.Stop()
			lease.entry.idleTimer = nil
		}
		openAICodexWebSockets.mu.Unlock()
		_ = lease.socket.Close(1000, "done")
		return
	}
	lease.entry.busy = false
	entry := lease.entry
	entry.idleTimer = time.AfterFunc(openAICodexWebSocketIdleTTL, func() {
		openAICodexWebSockets.mu.Lock()
		if openAICodexWebSockets.sessions[lease.sessionID] != entry || entry.busy {
			openAICodexWebSockets.mu.Unlock()
			return
		}
		delete(openAICodexWebSockets.sessions, lease.sessionID)
		openAICodexWebSockets.mu.Unlock()
		_ = entry.socket.Close(1000, "idle_timeout")
	})
	openAICodexWebSockets.mu.Unlock()
}

func (lease *openAICodexWebSocketLease) continuation() *openAICodexContinuation {
	if lease == nil || lease.entry == nil {
		return nil
	}
	openAICodexWebSockets.mu.Lock()
	defer openAICodexWebSockets.mu.Unlock()
	if lease.entry.continuation == nil {
		return nil
	}
	copy := *lease.entry.continuation
	copy.other = append([]byte(nil), copy.other...)
	copy.input = cloneRawMessages(copy.input)
	copy.responseItems = cloneRawMessages(copy.responseItems)
	return &copy
}

func (lease *openAICodexWebSocketLease) setContinuation(continuation *openAICodexContinuation) {
	if lease == nil || lease.entry == nil {
		return
	}
	openAICodexWebSockets.mu.Lock()
	lease.entry.continuation = continuation
	openAICodexWebSockets.mu.Unlock()
}

func cloneRawMessages(source []json.RawMessage) []json.RawMessage {
	if source == nil {
		return nil
	}
	result := make([]json.RawMessage, len(source))
	for index, raw := range source {
		result[index] = append(json.RawMessage(nil), raw...)
	}
	return result
}

func processOpenAICodexWebSocket(
	ctx context.Context,
	model *ai.Model,
	requestBody []byte,
	headers http.Header,
	options *OpenAICodexResponsesOptions,
	output *ai.AssistantMessage,
	sink func(ai.AssistantMessageEvent) bool,
) (started bool, resultErr error) {
	streamOptions := codexStreamOptions(options)
	sessionID := rawCodexSessionID(streamOptions)
	lease, err := acquireOpenAICodexWebSocket(
		ctx,
		resolveOpenAICodexWebSocketURL(model.BaseURL),
		headers,
		sessionID,
		codexWebSocketConnectTimeout(streamOptions),
	)
	if err != nil {
		return false, err
	}
	keep := true
	defer func() {
		if resultErr != nil {
			lease.setContinuation(nil)
			keep = false
		}
		if ctx.Err() != nil {
			keep = false
		}
		lease.release(keep)
	}()
	transport := codexTransport(streamOptions)
	useCachedContext := transport == ai.TransportAuto || transport == ai.TransportWebSocketCached
	wireBody := requestBody
	if useCachedContext {
		wireBody = buildOpenAICodexCachedRequest(requestBody, lease.continuation())
	}
	recordOpenAICodexWebSocketRequest(sessionID, lease.reused, useCachedContext, wireBody)
	if err := lease.socket.WriteText(openAICodexWebSocketEnvelope(wireBody)); err != nil {
		return false, err
	}
	processorOptions := &OpenAIResponsesOptions{}
	if options != nil {
		processorOptions.StreamOptions = options.StreamOptions
		processorOptions.ServiceTier = options.ServiceTier
	}
	pending := make([]ai.AssistantMessageEvent, 0)
	processor := newOpenAIResponsesProcessor(model, output, processorOptions, func(event ai.AssistantMessageEvent) bool {
		if !started {
			pending = append(pending, event)
			return true
		}
		return sink(event)
	})
	for {
		message, err := lease.socket.ReadMessage(ctx, codexWebSocketIdleTimeout(streamOptions))
		if err != nil {
			return started, err
		}
		var raw json.RawMessage
		if err := json.Unmarshal(message, &raw); err != nil {
			return started, &codexProtocolError{message: "Invalid Codex WebSocket JSON: " + err.Error()}
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return started, &codexProtocolError{message: "Invalid Codex WebSocket JSON: " + err.Error()}
		}
		if envelope.Type == "error" || envelope.Type == "response.failed" {
			return started, handleOpenAICodexEvent(processor, raw)
		}
		err = handleOpenAICodexEvent(processor, raw)
		if !started {
			started = true
			if !sink(ai.StartEvent{Partial: output}) {
				return true, nil
			}
			for _, event := range pending {
				if !sink(event) {
					return true, nil
				}
			}
			pending = nil
		}
		if errors.Is(err, errCodexTerminal) {
			break
		}
		if err != nil {
			return started, err
		}
	}
	if useCachedContext && output.ResponseID != nil {
		continuation, err := makeOpenAICodexContinuation(model, requestBody, *output.ResponseID, output)
		if err != nil {
			return started, err
		}
		lease.setContinuation(continuation)
	}
	return started, nil
}

func recordOpenAICodexWebSocketRequest(sessionID string, reused, cached bool, body []byte) {
	if sessionID == "" {
		return
	}
	_, input, _ := openAICodexBodySnapshot(body)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	_, hasPrevious := fields["previous_response_id"]
	storeTrue := bytes.Equal(bytes.TrimSpace(fields["store"]), []byte("true"))
	openAICodexWebSockets.mu.Lock()
	defer openAICodexWebSockets.mu.Unlock()
	stats := openAICodexWebSocketStatsLocked(sessionID)
	stats.Requests++
	if reused {
		stats.ConnectionsReused++
	} else {
		stats.ConnectionsCreated++
	}
	if cached {
		stats.CachedContextRequests++
	}
	if storeTrue {
		stats.StoreTrueRequests++
	}
	stats.LastInputItems = len(input)
	if hasPrevious {
		stats.DeltaRequests++
		count := len(input)
		stats.LastDeltaInputItems = &count
		var previous string
		_ = json.Unmarshal(fields["previous_response_id"], &previous)
		stats.LastPreviousResponseID = &previous
	} else {
		stats.FullContextRequests++
		stats.LastDeltaInputItems = nil
		stats.LastPreviousResponseID = nil
	}
}

func makeOpenAICodexContinuation(
	model *ai.Model,
	requestBody []byte,
	responseID string,
	output *ai.AssistantMessage,
) (*openAICodexContinuation, error) {
	other, input, err := openAICodexBodySnapshot(requestBody)
	if err != nil {
		return nil, err
	}
	items, err := convertResponsesMessages(model, ai.Context{Messages: ai.MessageList{output}}, nil, false)
	if err != nil {
		return nil, err
	}
	responseItems := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		encoded, err := ai.Marshal(item)
		if err != nil {
			return nil, err
		}
		var kind struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(encoded, &kind)
		if kind.Type != "function_call_output" {
			responseItems = append(responseItems, encoded)
		}
	}
	return &openAICodexContinuation{other: other, input: input, responseID: responseID, responseItems: responseItems}, nil
}

func buildOpenAICodexCachedRequest(body []byte, continuation *openAICodexContinuation) []byte {
	if continuation == nil || continuation.responseID == "" {
		return body
	}
	other, current, err := openAICodexBodySnapshot(body)
	if err != nil || !bytes.Equal(other, continuation.other) {
		return body
	}
	baseline := make([]json.RawMessage, 0, len(continuation.input)+len(continuation.responseItems))
	baseline = append(baseline, continuation.input...)
	baseline = append(baseline, continuation.responseItems...)
	if len(current) < len(baseline) {
		return body
	}
	for index := range baseline {
		if !bytes.Equal(bytes.TrimSpace(current[index]), bytes.TrimSpace(baseline[index])) {
			return body
		}
	}
	delta, err := ai.Marshal(current[len(baseline):])
	if err != nil {
		return body
	}
	updated, err := replaceOpenAICodexInput(body, delta)
	if err != nil || len(updated) == 0 || updated[len(updated)-1] != '}' {
		return body
	}
	previous, err := ai.Marshal(continuation.responseID)
	if err != nil {
		return body
	}
	result := make([]byte, 0, len(updated)+len(previous)+24)
	result = append(result, updated[:len(updated)-1]...)
	result = append(result, `,"previous_response_id":`...)
	result = append(result, previous...)
	result = append(result, '}')
	return result
}

func openAICodexBodySnapshot(body []byte) ([]byte, []json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, nil, err
	}
	var input []json.RawMessage
	if raw := fields["input"]; raw != nil {
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, nil, err
		}
	}
	delete(fields, "input")
	delete(fields, "previous_response_id")
	other, err := ai.Marshal(fields)
	return other, input, err
}

func replaceOpenAICodexInput(body, input []byte) ([]byte, error) {
	marker := []byte(`,"input":`)
	markerIndex := bytes.Index(body, marker)
	if markerIndex < 0 {
		return nil, errors.New("codex WebSocket request has no input field")
	}
	valueStart := markerIndex + len(marker)
	valueEnd, err := openAICodexJSONValueEnd(body, valueStart)
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0, len(body)-valueEnd+valueStart+len(input))
	result = append(result, body[:valueStart]...)
	result = append(result, input...)
	result = append(result, body[valueEnd:]...)
	return result, nil
}

func openAICodexJSONValueEnd(data []byte, start int) (int, error) {
	for start < len(data) && strings.ContainsRune(" \t\r\n", rune(data[start])) {
		start++
	}
	if start >= len(data) || (data[start] != '[' && data[start] != '{') {
		return 0, errors.New("codex WebSocket input is not an array")
	}
	depth := 0
	inString, escaped := false, false
	for index := start; index < len(data); index++ {
		value := data[index]
		if inString {
			if escaped {
				escaped = false
			} else if value == '\\' {
				escaped = true
			} else if value == '"' {
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return index + 1, nil
			}
		}
	}
	return 0, errors.New("unterminated Codex WebSocket input")
}

func openAICodexWebSocketEnvelope(body []byte) []byte {
	if len(body) > 0 && body[0] == '{' {
		result := make([]byte, 0, len(body)+25)
		result = append(result, `{"type":"response.create",`...)
		result = append(result, body[1:]...)
		return result
	}
	return body
}

func buildOpenAICodexWebSocketHeaders(
	model *ai.Model,
	options *ai.StreamOptions,
	token, accountID, requestID string,
) http.Header {
	headers := buildOpenAICodexHeaders(model, options, token, accountID)
	headers.Del("Accept")
	headers.Del("Content-Type")
	headers.Del("OpenAI-Beta")
	headers.Set("OpenAI-Beta", openAICodexWebSocketBeta)
	headers.Set("x-client-request-id", requestID)
	headers.Set("session-id", requestID)
	return headers
}

func resolveOpenAICodexWebSocketURL(baseURL string) string {
	resolved := resolveOpenAICodexURL(baseURL)
	if strings.HasPrefix(resolved, "https:") {
		return "wss:" + strings.TrimPrefix(resolved, "https:")
	}
	if strings.HasPrefix(resolved, "http:") {
		return "ws:" + strings.TrimPrefix(resolved, "http:")
	}
	return resolved
}

func newOpenAICodexRequestID() (string, error) {
	return ai.UUIDv7()
}

func rawCodexSessionID(options *ai.StreamOptions) string {
	if options == nil || options.SessionID == nil {
		return ""
	}
	return *options.SessionID
}

func codexWebSocketRequestID(options *ai.StreamOptions) (string, error) {
	if options != nil && options.SessionID != nil {
		if clamped, ok := clampOpenAIPromptCacheKey(options.SessionID).(string); ok && clamped != "" {
			return clamped, nil
		}
	}
	return newOpenAICodexRequestID()
}

func codexTransport(options *ai.StreamOptions) ai.Transport {
	if options == nil || options.Transport == nil || *options.Transport == "" {
		return ai.TransportAuto
	}
	return *options.Transport
}

func codexWebSocketConnectTimeout(options *ai.StreamOptions) time.Duration {
	if options == nil || options.WebSocketConnectTimeoutMS == nil {
		return defaultCodexWebSocketTimeout
	}
	return time.Duration(*options.WebSocketConnectTimeoutMS) * time.Millisecond
}

func codexWebSocketIdleTimeout(options *ai.StreamOptions) time.Duration {
	if options == nil || options.TimeoutMS == nil {
		return 0
	}
	return time.Duration(*options.TimeoutMS) * time.Millisecond
}
