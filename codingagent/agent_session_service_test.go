package codingagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/codingagent/config"
	"github.com/OrdalieTech/orb/codingagent/extensions"
	extensionhost "github.com/OrdalieTech/orb/codingagent/extensions/host"
)

// sessionCallbackRecorder mimics the SDK proxy's mirrors: onEvent drives
// subscribe listeners, the delta callbacks maintain session.messages, onStats
// maintains getSessionStats().
type sessionCallbackRecorder struct {
	mu        sync.Mutex
	events    []string
	messages  []json.RawMessage
	snapshots int
	stats     []extensionhost.AgentSessionStats
}

func (recorder *sessionCallbackRecorder) callbacks() extensionhost.AgentSessionCallbacks {
	return extensionhost.AgentSessionCallbacks{
		OnEvent: func(payload any) {
			raw, _ := payload.(json.RawMessage)
			var envelope struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &envelope)
			recorder.mu.Lock()
			recorder.events = append(recorder.events, envelope.Type)
			recorder.mu.Unlock()
		},
		OnMessagesSnapshot: func(messages any) {
			raw, _ := messages.(json.RawMessage)
			var decoded []json.RawMessage
			_ = json.Unmarshal(raw, &decoded)
			recorder.mu.Lock()
			recorder.messages = decoded
			recorder.snapshots++
			recorder.mu.Unlock()
		},
		OnMessageAppended: func(message any) {
			raw, _ := message.(json.RawMessage)
			recorder.mu.Lock()
			recorder.messages = append(recorder.messages, append(json.RawMessage(nil), raw...))
			recorder.mu.Unlock()
		},
		OnMessageUpdated: func(index int, message any) {
			raw, _ := message.(json.RawMessage)
			recorder.mu.Lock()
			if index >= 0 && index < len(recorder.messages) {
				recorder.messages[index] = append(json.RawMessage(nil), raw...)
			} else {
				recorder.messages = append(recorder.messages, append(json.RawMessage(nil), raw...))
			}
			recorder.mu.Unlock()
		},
		OnStats: func(stats extensionhost.AgentSessionStats) {
			recorder.mu.Lock()
			recorder.stats = append(recorder.stats, stats)
			recorder.mu.Unlock()
		},
	}
}

func (recorder *sessionCallbackRecorder) eventCount() int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return len(recorder.events)
}

type decodedMirrorMessage struct {
	Role         string `json:"role"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
	ToolName     string `json:"toolName"`
	IsError      bool   `json:"isError"`
	Content      []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (recorder *sessionCallbackRecorder) decodedMessages(t *testing.T) []decodedMirrorMessage {
	t.Helper()
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	result := make([]decodedMirrorMessage, 0, len(recorder.messages))
	for _, raw := range recorder.messages {
		var message decodedMirrorMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("mirror message %s: %v", raw, err)
		}
		result = append(result, message)
	}
	return result
}

func mirrorText(message decodedMirrorMessage) string {
	var builder strings.Builder
	for _, block := range message.Content {
		builder.WriteString(block.Text)
	}
	return builder.String()
}

func serviceAssistant(provider *faux.Provider, content any, stop ai.StopReason, tokens int64) *ai.AssistantMessage {
	message := faux.AssistantMessage(content, faux.AssistantMessageOptions{StopReason: stop})
	model := provider.GetModel()
	message.API = model.API
	message.Provider = model.Provider
	message.Model = model.ID
	message.Usage = ai.Usage{Input: tokens, Output: tokens, TotalTokens: 2 * tokens}
	return message
}

func synthesizedTestModel() *ai.Model {
	contextWindow := float64(100000)
	return &ai.Model{
		ID:            "synth-model",
		Name:          "Synthesized",
		API:           "faux",
		Provider:      "faux",
		ContextWindow: contextWindow,
		MaxTokens:     100,
	}
}

type recordedToolCall struct {
	name          string
	toolCallID    string
	params        any
	eventsAlready int
}

func newServiceFixture(t *testing.T, provider *faux.Provider) (*ExtensionAgentSessionService, string, string) {
	t.Helper()
	cwd, agentDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	service := NewExtensionAgentSessionService(ExtensionAgentSessionServiceOptions{
		CWD: cwd, AgentDir: agentDir, StreamFn: provider.StreamSimple,
	})
	return service, cwd, agentDir
}

func storePutSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`)
}

// TestExtensionAgentSessionServiceEndToEndFlow drives
// create→prompt→events→stats→dispose with a JS-tool callback round trip, an
// off-catalog synthesized model, and events-before-terminal ordering.
func TestExtensionAgentSessionServiceEndToEndFlow(t *testing.T) {
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{
		serviceAssistant(provider, faux.ToolCall("store_put", map[string]any{"key": "alpha"}, faux.ToolCallOptions{ID: "call-1"}), ai.StopReasonToolUse, 7),
		serviceAssistant(provider, "done", ai.StopReasonStop, 5),
	})
	service, cwd, _ := newServiceFixture(t, provider)
	recorder := &sessionCallbackRecorder{}

	var calls []recordedToolCall
	var callMu sync.Mutex
	request := extensionhost.AgentSessionCreateRequest{
		ExtensionID: "ext-1",
		Options: extensionhost.AgentSessionOptions{
			CWD:          cwd,
			Model:        synthesizedTestModel(),
			ExcludeTools: []string{"workflow", "workflow_control"},
			CustomTools: []extensionhost.AgentSessionTool{
				{Builtin: "read"},
				{Name: "store_put", Description: "store a value", Parameters: storePutSchema()},
			},
			ResourceLoader: &extensionhost.AgentSessionResourceLoader{CWD: cwd, NoExtensions: true},
		},
		ExecuteTool: func(_ context.Context, toolName, toolCallID string, params any, _ func(json.RawMessage)) (json.RawMessage, error) {
			callMu.Lock()
			calls = append(calls, recordedToolCall{
				name: toolName, toolCallID: toolCallID, params: params, eventsAlready: recorder.eventCount(),
			})
			callMu.Unlock()
			return json.RawMessage(`{"content":[{"type":"text","text":"stored:ALPHA"}],"details":{"ok":true}}`), nil
		},
	}

	handle, created, err := service.CreateSession(context.Background(), request, recorder.callbacks())
	if err != nil {
		t.Fatal(err)
	}
	// Contract point 9: model resolution rides the create result.
	if created.Model == nil || created.Model.ID != "synth-model" || string(created.Model.Provider) != "faux" {
		t.Fatalf("create result model = %#v", created.Model)
	}
	if created.ModelFallbackMessage != "" {
		t.Fatalf("unexpected fallback: %q", created.ModelFallbackMessage)
	}

	if err := handle.Prompt(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}

	// JS-tool callback round trip with schema-validated params (D14).
	callMu.Lock()
	if len(calls) != 1 {
		callMu.Unlock()
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	call := calls[0]
	callMu.Unlock()
	if call.name != "store_put" || call.toolCallID != "call-1" {
		t.Fatalf("tool call = %#v", call)
	}
	// The wire value is the schema-parsed params; when the validated value is
	// structurally identical to the provider-emitted arguments it travels as
	// their order-preserving raw JSON (upstream passes the parsed JS object
	// straight through, member order intact).
	raw, ok := call.params.(json.RawMessage)
	if !ok {
		t.Fatalf("validated params = %#v, want order-preserving raw JSON", call.params)
	}
	params := map[string]any{}
	if err := json.Unmarshal(raw, &params); err != nil || params["key"] != "alpha" || len(params) != 1 {
		t.Fatalf("validated params = %s (must be exactly the schema-parsed value)", raw)
	}
	// Contract point 1/2: callbacks stream during the turn — by the time the
	// tool executed, message events were already mirrored.
	if call.eventsAlready == 0 {
		t.Fatal("no events mirrored before tool execution: mirrors are not live during the turn")
	}

	messages := recorder.decodedMessages(t)
	if len(messages) < 4 {
		t.Fatalf("mirror has %d messages, want user+assistant+toolResult+assistant", len(messages))
	}
	if messages[0].Role != "user" || mirrorText(messages[0]) != "go" {
		t.Fatalf("mirror[0] = %#v", messages[0])
	}
	var sawToolResult, sawFinal bool
	for _, message := range messages {
		if message.Role == "toolResult" && message.ToolName == "store_put" && mirrorText(message) == "stored:ALPHA" && !message.IsError {
			sawToolResult = true
		}
		if message.Role == "assistant" && mirrorText(message) == "done" {
			sawFinal = true
		}
	}
	if !sawToolResult || !sawFinal {
		t.Fatalf("mirror missing tool result (%v) or final assistant (%v): %#v", sawToolResult, sawFinal, messages)
	}

	// Stats mirror is live and final at prompt resolution.
	recorder.mu.Lock()
	statsCount := len(recorder.stats)
	finalStats := extensionhost.AgentSessionStats{}
	if statsCount > 0 {
		finalStats = recorder.stats[statsCount-1]
	}
	recorder.mu.Unlock()
	if statsCount == 0 || finalStats.Tokens.Total == 0 {
		t.Fatalf("stats mirror = %d updates, final %#v", statsCount, finalStats)
	}
	direct, err := handle.SessionStats(context.Background())
	if err != nil || direct != finalStats {
		t.Fatalf("SessionStats = %#v (%v), want %#v", direct, err, finalStats)
	}

	// Messages() serves the same upstream-shaped mirror.
	rawMessages, err := handle.Messages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var direct2 []json.RawMessage
	if err := json.Unmarshal(rawMessages, &direct2); err != nil {
		t.Fatal(err)
	}
	if len(direct2) != len(messages) {
		t.Fatalf("Messages() = %d entries, mirror has %d", len(direct2), len(messages))
	}

	if err := handle.SetActiveToolsByName(context.Background(), []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if err := handle.AppendSessionInfo(context.Background(), "workflow:test"); err != nil {
		t.Fatal(err)
	}

	if err := handle.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := handle.Dispose(context.Background()); err != nil {
		t.Fatal(err) // idempotent
	}
	if err := handle.Prompt(context.Background(), "again", nil); err == nil {
		t.Fatal("prompt after dispose succeeded")
	}
}

// TestExtensionAgentSessionServiceValidatesBeforeExecute proves the D14 gate:
// schema-invalid params never reach the host-JS execute; the model sees a
// validation-error tool result instead.
func TestExtensionAgentSessionServiceValidatesBeforeExecute(t *testing.T) {
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{
		serviceAssistant(provider, faux.ToolCall("store_put", map[string]any{}, faux.ToolCallOptions{ID: "call-1"}), ai.StopReasonToolUse, 3),
		serviceAssistant(provider, "recovered", ai.StopReasonStop, 3),
	})
	service, cwd, _ := newServiceFixture(t, provider)
	recorder := &sessionCallbackRecorder{}
	executed := 0
	request := extensionhost.AgentSessionCreateRequest{
		Options: extensionhost.AgentSessionOptions{
			CWD:   cwd,
			Model: synthesizedTestModel(),
			CustomTools: []extensionhost.AgentSessionTool{
				{Builtin: "read"},
				{Name: "store_put", Parameters: storePutSchema()},
			},
		},
		ExecuteTool: func(context.Context, string, string, any, func(json.RawMessage)) (json.RawMessage, error) {
			executed++
			return json.RawMessage(`{"content":[]}`), nil
		},
	}
	handle, _, err := service.CreateSession(context.Background(), request, recorder.callbacks())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Dispose(context.Background()) }()
	if err := handle.Prompt(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if executed != 0 {
		t.Fatalf("execute ran %d times for schema-invalid params", executed)
	}
	var sawValidationError bool
	for _, message := range recorder.decodedMessages(t) {
		if message.Role == "toolResult" && message.IsError && strings.Contains(mirrorText(message), `Validation failed for tool "store_put"`) {
			sawValidationError = true
		}
	}
	if !sawValidationError {
		t.Fatalf("no validation-error tool result mirrored: %#v", recorder.decodedMessages(t))
	}
}

// TestExtensionAgentSessionServiceTerminateEndsTurn proves `terminate: true`
// in a tool result ends the turn (the structured_output contract).
func TestExtensionAgentSessionServiceTerminateEndsTurn(t *testing.T) {
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{
		serviceAssistant(provider, faux.ToolCall("structured_output", map[string]any{"value": "v"}, faux.ToolCallOptions{ID: "call-1"}), ai.StopReasonToolUse, 3),
		serviceAssistant(provider, "must-not-run", ai.StopReasonStop, 3),
	})
	service, cwd, _ := newServiceFixture(t, provider)
	recorder := &sessionCallbackRecorder{}
	request := extensionhost.AgentSessionCreateRequest{
		Options: extensionhost.AgentSessionOptions{
			CWD:   cwd,
			Model: synthesizedTestModel(),
			CustomTools: []extensionhost.AgentSessionTool{{
				Name:       "structured_output",
				Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
			}},
		},
		ExecuteTool: func(context.Context, string, string, any, func(json.RawMessage)) (json.RawMessage, error) {
			return json.RawMessage(`{"content":[{"type":"text","text":"captured"}],"terminate":true}`), nil
		},
	}
	handle, _, err := service.CreateSession(context.Background(), request, recorder.callbacks())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Dispose(context.Background()) }()
	if err := handle.Prompt(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if pending := provider.PendingResponseCount(); pending != 1 {
		t.Fatalf("pending faux responses = %d, want 1 (terminate must end the turn)", pending)
	}
	messages := recorder.decodedMessages(t)
	last := messages[len(messages)-1]
	if last.Role != "toolResult" || mirrorText(last) != "captured" {
		t.Fatalf("last mirrored message = %#v, want the terminating tool result", last)
	}
}

// TestExtensionAgentSessionServiceProviderLimitMirrors proves contract point
// 4: provider quota/limit failures never fail Prompt; they land in the mirror
// as a final assistant message with stopReason "error" and the verbatim
// errorMessage.
func TestExtensionAgentSessionServiceProviderLimitMirrors(t *testing.T) {
	provider := testFaux(100000)
	limitText := "Monthly usage limit reached: please check your billing"
	provider.SetResponses([]faux.ResponseStep{runtimeError(provider, limitText)})
	service, cwd, _ := newServiceFixture(t, provider)
	recorder := &sessionCallbackRecorder{}
	request := extensionhost.AgentSessionCreateRequest{
		Options: extensionhost.AgentSessionOptions{CWD: cwd, Model: synthesizedTestModel()},
	}
	handle, _, err := service.CreateSession(context.Background(), request, recorder.callbacks())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Dispose(context.Background()) }()
	if err := handle.Prompt(context.Background(), "go", nil); err != nil {
		t.Fatalf("provider limit surfaced as prompt error: %v", err)
	}
	messages := recorder.decodedMessages(t)
	last := messages[len(messages)-1]
	if last.Role != "assistant" || last.StopReason != "error" || last.ErrorMessage != limitText {
		t.Fatalf("mirrored limit message = %#v, want stopReason error + verbatim %q", last, limitText)
	}
}

// TestExtensionAgentSessionServiceToolAllowDenyMapping proves builtin markers
// never restrict the default built-in set (upstream createAgentSession always
// builds its defaults; same-named customTools only shadow them), callback
// tools become active alongside them, and ExcludeTools always wins.
func TestExtensionAgentSessionServiceToolAllowDenyMapping(t *testing.T) {
	provider := testFaux(100000)
	service, cwd, _ := newServiceFixture(t, provider)
	recorder := &sessionCallbackRecorder{}
	request := extensionhost.AgentSessionCreateRequest{
		Options: extensionhost.AgentSessionOptions{
			CWD:          cwd,
			Model:        provider.GetModel(),
			ExcludeTools: []string{"bash", "workflow"},
			CustomTools: []extensionhost.AgentSessionTool{
				{Builtin: "read"},
				{Builtin: "bash"},
				{Name: "store_put", Parameters: storePutSchema()},
			},
		},
		ExecuteTool: func(context.Context, string, string, any, func(json.RawMessage)) (json.RawMessage, error) {
			return json.RawMessage(`{"content":[]}`), nil
		},
	}
	handle, _, err := service.CreateSession(context.Background(), request, recorder.callbacks())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Dispose(context.Background()) }()
	session := handle.(*extensionAgentSessionHandle).session
	active := session.GetActiveToolNames()
	activeSet := map[string]bool{}
	for _, name := range active {
		activeSet[name] = true
	}
	// Markers {read,bash} do not narrow the default set: edit and write stay
	// active even without markers, exactly like upstream. ExcludeTools is the
	// only subtraction (bash), and non-default built-ins (grep) stay out.
	if !activeSet["read"] || !activeSet["edit"] || !activeSet["write"] || !activeSet["store_put"] {
		t.Fatalf("active tools = %#v, want default built-ins + store_put", active)
	}
	if activeSet["bash"] || activeSet["grep"] {
		t.Fatalf("active tools = %#v: excluded or non-default built-ins leaked", active)
	}
}

// TestExtensionAgentSessionServiceNoRecursiveExtensions proves the
// anti-recursion guarantee: skills still load for the child session while no
// extension (the shared loader's or otherwise) rides in — the child registry
// carries only the synthetic <sdk:*> custom-tool entries. It also proves
// ReloadResources maps to the real reload path of the shared loader
// (contract point 8).
func TestExtensionAgentSessionServiceNoRecursiveExtensions(t *testing.T) {
	provider := testFaux(100000)
	service, cwd, agentDir := newServiceFixture(t, provider)
	skillDir := filepath.Join(agentDir, "skills", "hello")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: hello\ndescription: A test skill\n---\nSay hello.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	loaderRef := &extensionhost.AgentSessionResourceLoader{CWD: cwd, AgentDir: agentDir, NoExtensions: true}
	create := func() *extensionAgentSessionHandle {
		t.Helper()
		recorder := &sessionCallbackRecorder{}
		handle, _, err := service.CreateSession(context.Background(), extensionhost.AgentSessionCreateRequest{
			Options: extensionhost.AgentSessionOptions{
				CWD:            cwd,
				Model:          provider.GetModel(),
				ResourceLoader: loaderRef,
				CustomTools: []extensionhost.AgentSessionTool{
					{Name: "store_put", Parameters: storePutSchema()},
				},
			},
			ExecuteTool: func(context.Context, string, string, any, func(json.RawMessage)) (json.RawMessage, error) {
				return json.RawMessage(`{"content":[]}`), nil
			},
		}, recorder.callbacks())
		if err != nil {
			t.Fatal(err)
		}
		return handle.(*extensionAgentSessionHandle)
	}

	first := create()
	defer func() { _ = first.Dispose(context.Background()) }()
	commandNames := map[string]bool{}
	for _, command := range first.session.Commands() {
		commandNames[command.Name] = true
	}
	if !commandNames["skill:hello"] {
		t.Fatalf("skill did not load for the child session: %#v", commandNames)
	}
	for _, extension := range first.result.ExtensionRegistry.Extensions() {
		if !strings.HasPrefix(extension.Path, "<sdk:") {
			t.Fatalf("child session loaded an extension: %q (noExtensions must hold)", extension.Path)
		}
	}

	// ReloadResources → real reload: a skill added afterwards is visible to
	// the next create through the same shared loader.
	secondSkill := filepath.Join(agentDir, "skills", "goodbye")
	if err := os.MkdirAll(secondSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondSkill, "SKILL.md"), []byte("---\nname: goodbye\ndescription: Another skill\n---\nSay goodbye.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := service.ReloadResources(context.Background(), *loaderRef); err != nil {
		t.Fatal(err)
	}
	second := create()
	defer func() { _ = second.Dispose(context.Background()) }()
	reloaded := map[string]bool{}
	for _, command := range second.session.Commands() {
		reloaded[command.Name] = true
	}
	if !reloaded["skill:goodbye"] {
		t.Fatalf("ReloadResources did not refresh the shared loader: %#v", reloaded)
	}
}

func blockingFauxResponse(provider *faux.Provider, started chan<- struct{}) faux.ResponseStep {
	var once sync.Once
	return faux.Factory(func(ctx context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	})
}

// TestExtensionAgentSessionServiceCancelAbortsPrompt mirrors the
// service_cancel path: cancelling the request context aborts the turn and
// Prompt reports the cancellation (the dispatch layer maps it to the
// structured "cancelled" code).
func TestExtensionAgentSessionServiceCancelAbortsPrompt(t *testing.T) {
	provider := testFaux(100000)
	started := make(chan struct{})
	provider.SetResponses([]faux.ResponseStep{blockingFauxResponse(provider, started)})
	service, cwd, _ := newServiceFixture(t, provider)
	recorder := &sessionCallbackRecorder{}
	handle, _, err := service.CreateSession(context.Background(), extensionhost.AgentSessionCreateRequest{
		Options: extensionhost.AgentSessionOptions{CWD: cwd, Model: provider.GetModel()},
	}, recorder.callbacks())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Dispose(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- handle.Prompt(ctx, "block", nil) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("cancelled prompt error = %v, want context cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled prompt did not settle")
	}
}

// TestExtensionAgentSessionServiceDisposeAbortsRunningPrompt proves contract
// point 7: Dispose (with reset()'s bounded context) aborts a running prompt.
func TestExtensionAgentSessionServiceDisposeAbortsRunningPrompt(t *testing.T) {
	provider := testFaux(100000)
	started := make(chan struct{})
	provider.SetResponses([]faux.ResponseStep{blockingFauxResponse(provider, started)})
	service, cwd, _ := newServiceFixture(t, provider)
	recorder := &sessionCallbackRecorder{}
	handle, _, err := service.CreateSession(context.Background(), extensionhost.AgentSessionCreateRequest{
		Options: extensionhost.AgentSessionOptions{CWD: cwd, Model: provider.GetModel()},
	}, recorder.callbacks())
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- handle.Prompt(context.Background(), "block", nil) }()
	<-started

	disposeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := handle.Dispose(disposeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("dispose did not abort the running prompt")
	}
}

// TestExtensionAgentSessionServiceSessionStorageAndInfoNames proves contract
// point 6 (pre-create appendSessionInfo names apply post-create) and the
// persisted SessionManager thin-handle mapping.
func TestExtensionAgentSessionServiceSessionStorageAndInfoNames(t *testing.T) {
	provider := testFaux(100000)
	service, cwd, _ := newServiceFixture(t, provider)
	sessionDir := t.TempDir()
	recorder := &sessionCallbackRecorder{}
	handle, _, err := service.CreateSession(context.Background(), extensionhost.AgentSessionCreateRequest{
		Options: extensionhost.AgentSessionOptions{
			CWD:   cwd,
			Model: provider.GetModel(),
			Session: &extensionhost.AgentSessionStorage{
				Persisted: true, SessionDir: sessionDir, CWD: cwd,
				SessionInfoNames: []string{"workflow:pre-create"},
			},
		},
	}, recorder.callbacks())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Dispose(context.Background()) }()
	session := handle.(*extensionAgentSessionHandle).session
	if session.Manager().GetSessionDir() != sessionDir {
		t.Fatalf("session dir = %q, want %q", session.Manager().GetSessionDir(), sessionDir)
	}
	if !session.Manager().IsPersisted() {
		t.Fatal("session is not persisted")
	}
	name := session.Manager().GetSessionName()
	if name == nil || *name != "workflow:pre-create" {
		t.Fatalf("session name = %v, want pre-create appendSessionInfo applied", name)
	}
}

// TestExtensionAgentSessionServiceResolvesModelRuntime proves contract point
// 5: the ModelRuntime ref resolves to the extensions.ModelRegistry behind it,
// and that registry supplies model resolution and the available-model set.
func TestExtensionAgentSessionServiceResolvesModelRuntime(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	registryDir := t.TempDir()
	modelsJSON := `{"providers":{"svc-fixture":{"name":"Svc Fixture","baseUrl":"http://127.0.0.1:1","api":"openai-completions","apiKey":"sk-test","models":[{"id":"svc-model","name":"Svc Model","contextWindow":32000,"maxTokens":4096}]}}}`
	if err := os.WriteFile(filepath.Join(registryDir, "models.json"), []byte(modelsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewOfflineModelRegistry(registryDir)
	if err != nil {
		t.Fatal(err)
	}
	// No StreamFn override: the resolved registry must drive the session.
	service := NewExtensionAgentSessionService(ExtensionAgentSessionServiceOptions{CWD: cwd, AgentDir: agentDir})
	recorder := &sessionCallbackRecorder{}
	resolved := 0
	handle, created, err := service.CreateSession(context.Background(), extensionhost.AgentSessionCreateRequest{
		Options: extensionhost.AgentSessionOptions{
			CWD:          cwd,
			ModelRuntime: &extensionhost.ModelRuntimeRef{Handle: "orb-model-runtime-1"},
		},
		ResolveModelRuntime: func() (extensions.ModelRegistry, error) {
			resolved++
			return registry, nil
		},
	}, recorder.callbacks())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Dispose(context.Background()) }()
	if resolved != 1 {
		t.Fatalf("ResolveModelRuntime called %d times", resolved)
	}
	// Model resolution fell through to the resolved registry's available set.
	if created.Model == nil || created.Model.ID != "svc-model" || string(created.Model.Provider) != "svc-fixture" {
		t.Fatalf("create result model = %#v, want svc-fixture/svc-model from the resolved registry", created.Model)
	}
	session := handle.(*extensionAgentSessionHandle).session
	available := session.AvailableModels()
	if len(available) != 1 || available[0].ID != "svc-model" {
		t.Fatalf("available models = %#v, want the resolved registry's set", available)
	}
}
