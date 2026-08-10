package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/codingagent/config"
	"github.com/OrdalieTech/orb/codingagent/extensions"
	"github.com/OrdalieTech/orb/codingagent/session"
)

func startServicesFixture(t *testing.T) (*Manager, *extensions.Runner, string, string) {
	t.Helper()
	runtime := requireRuntime(t)
	cwd := t.TempDir()
	agentDir := t.TempDir()
	manager := NewManager(Options{
		AgentDir: agentDir, CWD: cwd, Version: "test", Runtime: &runtime,
		RequestTimeout: 30 * time.Second, ShutdownTimeout: time.Second,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
	})
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	modelRegistry, err := config.NewOfflineModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(cwd)
	result := manager.RegisterInto(context.Background(), registry, []string{fixturePath(t, "services.mjs")})
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd, ModelRegistry: modelRegistry})
	return manager, runner, cwd, agentDir
}

func runServiceTool(t *testing.T, runner *extensions.Runner, name string, params map[string]any, into any) {
	t.Helper()
	definition := runner.ToolDefinition(name)
	if definition == nil {
		t.Fatalf("%s was not registered", name)
	}
	final, err := definition.Execute(context.Background(), "call-"+name, params, nil, runner.CreateContext())
	if err != nil {
		t.Fatalf("%s failed: %v", name, err)
	}
	payload := toolText(final)
	if err := json.Unmarshal([]byte(payload), into); err != nil {
		t.Fatalf("%s result %q: %v", name, payload, err)
	}
}

func TestRealHostServicesHandshakeAndCapabilityGate(t *testing.T) {
	_, runner, cwd, agentDir := startServicesFixture(t)
	var result struct {
		TransportBound bool     `json:"transportBound"`
		Capabilities   []string `json:"capabilities"`
		Hello          struct {
			SessionsRoot string `json:"sessionsRoot"`
		} `json:"hello"`
		Gate struct {
			Threw   bool   `json:"threw"`
			Name    string `json:"name"`
			Message string `json:"message"`
		} `json:"gate"`
		ReloadOk any `json:"reloadOk"`
	}
	runServiceTool(t, runner, "svc_probe", map[string]any{}, &result)
	if !result.TransportBound {
		t.Fatal("SDK transport was not bound at handshake")
	}
	negotiated := strings.Join(result.Capabilities, ",")
	for _, capability := range []string{"sdk_v1", "agent_session_v1", "model_runtime_v1", "state_v1"} {
		if !strings.Contains(negotiated, capability) {
			t.Fatalf("capability %s missing from negotiated set %q", capability, negotiated)
		}
	}
	sessionDir, err := session.DefaultSessionDirPath(cwd, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Hello.SessionsRoot != filepath.Dir(sessionDir) {
		t.Fatalf("hello.sessionsRoot = %q, want %q", result.Hello.SessionsRoot, filepath.Dir(sessionDir))
	}
	if !result.Gate.Threw || result.Gate.Name != "OrbUnsupportedCapability" {
		t.Fatalf("capability gate = %#v", result.Gate)
	}
	if !strings.Contains(result.Gate.Message, "time_travel_v9") {
		t.Fatalf("gate diagnostic does not name the capability: %q", result.Gate.Message)
	}
	if ok, isBool := result.ReloadOk.(bool); !isBool || !ok {
		t.Fatalf("DefaultResourceLoader.reload over sdk_resource_reload = %#v", result.ReloadOk)
	}
}

func TestRealHostModelRuntimeLifecycleOverProtocol(t *testing.T) {
	_, runner, _, _ := startServicesFixture(t)
	dir := t.TempDir()
	modelsJSON := `{"providers":{"svc-fixture":{"name":"Svc Fixture","baseUrl":"http://127.0.0.1:1","api":"openai-completions","models":[{"id":"svc-model","name":"Svc Model","contextWindow":32000,"maxTokens":4096}]}}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(modelsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Handle                string `json:"handle"`
		RuntimeFieldSurvives  bool   `json:"runtimeFieldSurvives"`
		AllCount              int    `json:"allCount"`
		HasFixtureModel       bool   `json:"hasFixtureModel"`
		AvailableIsArray      bool   `json:"availableIsArray"`
		HasConfiguredAuthType string `json:"hasConfiguredAuthType"`
		AfterDispose          *struct {
			Code string `json:"code"`
		} `json:"afterDispose"`
	}
	runServiceTool(t, runner, "svc_model_runtime", map[string]any{"dir": dir}, &result)
	if !strings.HasPrefix(result.Handle, "orb-model-runtime-") {
		t.Fatalf("handle = %q, want Go-minted orb-model-runtime-N", result.Handle)
	}
	if !result.RuntimeFieldSurvives {
		t.Fatal("ModelRegistry.runtime private-field read did not survive")
	}
	if result.AllCount == 0 || !result.HasFixtureModel {
		t.Fatalf("catalog = %d models, hasFixtureModel=%v", result.AllCount, result.HasFixtureModel)
	}
	if !result.AvailableIsArray || result.HasConfiguredAuthType != "boolean" {
		t.Fatalf("runtime surface: availableIsArray=%v hasConfiguredAuth=%s", result.AvailableIsArray, result.HasConfiguredAuthType)
	}
	if result.AfterDispose == nil || result.AfterDispose.Code != "unknown_handle" {
		t.Fatalf("post-dispose error = %#v, want code unknown_handle", result.AfterDispose)
	}
}

func TestRealHostContextModelRuntimeFacade(t *testing.T) {
	_, runner, _, _ := startServicesFixture(t)
	var result struct {
		HasRuntime          bool   `json:"hasRuntime"`
		RuntimeID           string `json:"runtimeId"`
		SnapshotIsArray     bool   `json:"snapshotIsArray"`
		RegistryGetAllCount int    `json:"registryGetAllCount"`
		AvailableCount      *int   `json:"availableCount"`
		AvailableError      *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"availableError"`
	}
	runServiceTool(t, runner, "svc_context_runtime", map[string]any{}, &result)
	if !result.HasRuntime || result.RuntimeID != "context:ext-1" {
		t.Fatalf("ctx.modelRegistry.runtime = %#v", result)
	}
	if !result.SnapshotIsArray {
		t.Fatal("getAvailableSnapshot() is not backed by the state snapshot")
	}
	if result.AvailableError != nil {
		t.Fatalf("context getAvailable over model_runtime_v1 failed: %#v", result.AvailableError)
	}
	if result.AvailableCount == nil {
		t.Fatal("context getAvailable returned nothing")
	}
	if result.RegistryGetAllCount == 0 {
		t.Fatal("new ModelRegistry(ctx.modelRegistry.runtime).getAll() is empty (builtin catalog expected)")
	}
}

func TestRealHostAgentSessionStubReportsNotWired(t *testing.T) {
	_, runner, _, _ := startServicesFixture(t)
	var result struct {
		Created bool   `json:"created"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	runServiceTool(t, runner, "svc_session_stub", map[string]any{}, &result)
	if result.Created {
		t.Fatal("stub service created a session")
	}
	if result.Code != "agent_session_unimplemented" {
		t.Fatalf("stub error code = %q", result.Code)
	}
	if !strings.Contains(result.Message, "not wired") || !strings.Contains(result.Message, "SetAgentSessionService") {
		t.Fatalf("stub error message is not the precise not-yet-wired diagnostic: %q", result.Message)
	}
}

// fakeAgentSessionService drives the protocol layer the way the runner lane
// will: mirrors emitted during Prompt, a JS tool callback, and a blocking
// prompt for cancellation.
type fakeAgentSessionService struct {
	bigEventBytes int

	mu          sync.Mutex
	options     AgentSessionOptions
	extensionID string
	toolResult  string
	toolErr     error
	activeTools []string
	appended    []string
	prompts     []string
	aborts      int
	disposes    int
}

type fakeAgentSessionHandle struct {
	service   *fakeAgentSessionService
	request   AgentSessionCreateRequest
	callbacks AgentSessionCallbacks
}

func (service *fakeAgentSessionService) CreateSession(_ context.Context, request AgentSessionCreateRequest, callbacks AgentSessionCallbacks) (AgentSessionHandle, AgentSessionCreateResult, error) {
	service.mu.Lock()
	service.options = request.Options
	service.extensionID = request.ExtensionID
	service.mu.Unlock()
	return &fakeAgentSessionHandle{service: service, request: request, callbacks: callbacks},
		AgentSessionCreateResult{ModelFallbackMessage: "fake-fallback"}, nil
}

func (service *fakeAgentSessionService) ReloadResources(context.Context, AgentSessionResourceLoader) error {
	return nil
}

func (handle *fakeAgentSessionHandle) Prompt(ctx context.Context, text string, _ json.RawMessage) error {
	service := handle.service
	service.mu.Lock()
	service.prompts = append(service.prompts, text)
	service.mu.Unlock()
	if text == "block" {
		<-ctx.Done()
		return ctx.Err()
	}
	callbacks := handle.callbacks
	callbacks.OnEvent(map[string]any{"type": "turn_start"})
	callbacks.OnMessageAppended(map[string]any{"role": "user", "content": text})
	callbacks.OnMessagesSnapshot([]any{
		map[string]any{"role": "user", "content": text},
		map[string]any{"role": "assistant", "stopReason": "stop"},
	})
	callbacks.OnMessageUpdated(1, map[string]any{"role": "assistant", "stopReason": "stop", "final": true})
	callbacks.OnStats(AgentSessionStats{Tokens: AgentSessionTokens{Input: 1, Output: 2, Total: 3}, Cost: 0.25})
	if service.bigEventBytes > 0 {
		callbacks.OnMessagesSnapshot([]any{
			map[string]any{"role": "assistant", "big": strings.Repeat("x", service.bigEventBytes)},
		})
	}
	if handle.request.ExecuteTool != nil {
		raw, err := handle.request.ExecuteTool(ctx, "store_put", "call-1", map[string]any{"key": "alpha"}, nil)
		service.mu.Lock()
		service.toolResult, service.toolErr = string(raw), err
		service.mu.Unlock()
	}
	callbacks.OnEvent(map[string]any{"type": "turn_end"})
	return nil
}

func (handle *fakeAgentSessionHandle) Messages(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`[{"role":"user"},{"role":"assistant","stopReason":"stop"}]`), nil
}

func (handle *fakeAgentSessionHandle) Abort(context.Context) error {
	handle.service.mu.Lock()
	handle.service.aborts++
	handle.service.mu.Unlock()
	return nil
}

func (handle *fakeAgentSessionHandle) SessionStats(context.Context) (AgentSessionStats, error) {
	return AgentSessionStats{Tokens: AgentSessionTokens{Input: 1, Output: 2, Total: 3}, Cost: 0.25}, nil
}

func (handle *fakeAgentSessionHandle) SetActiveToolsByName(_ context.Context, names []string) error {
	handle.service.mu.Lock()
	handle.service.activeTools = append([]string(nil), names...)
	handle.service.mu.Unlock()
	return nil
}

func (handle *fakeAgentSessionHandle) AppendSessionInfo(_ context.Context, name string) error {
	handle.service.mu.Lock()
	handle.service.appended = append(handle.service.appended, name)
	handle.service.mu.Unlock()
	return nil
}

func (handle *fakeAgentSessionHandle) Dispose(context.Context) error {
	handle.service.mu.Lock()
	handle.service.disposes++
	handle.service.mu.Unlock()
	return nil
}

func TestRealHostAgentSessionRoundTripWithService(t *testing.T) {
	manager, runner, cwd, agentDir := startServicesFixture(t)
	fake := &fakeAgentSessionService{bigEventBytes: 5 << 20}
	manager.SetAgentSessionService(fake)
	var result struct {
		SessionDir            string   `json:"sessionDir"`
		SessionDirWritable    bool     `json:"sessionDirWritable"`
		ModelFallbackMessage  string   `json:"modelFallbackMessage"`
		SubscribeEvents       []string `json:"subscribeEvents"`
		EventsAtPromptResolve int      `json:"eventsAtPromptResolve"`
		Stats                 struct {
			Tokens struct {
				Total int64 `json:"total"`
			} `json:"tokens"`
			Cost float64 `json:"cost"`
		} `json:"stats"`
		MessageCount int `json:"messageCount"`
		BigLen       int `json:"bigLen"`
	}
	runServiceTool(t, runner, "svc_session_flow", map[string]any{}, &result)

	expectedDir, err := session.DefaultSessionDirPath(cwd, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionDir != expectedDir || !result.SessionDirWritable {
		t.Fatalf("SessionManager.create dir = %q writable=%v, want %q (Go scheme, pre-wire mkdir)", result.SessionDir, result.SessionDirWritable, expectedDir)
	}
	if result.ModelFallbackMessage != "fake-fallback" {
		t.Fatalf("modelFallbackMessage = %q", result.ModelFallbackMessage)
	}
	if len(result.SubscribeEvents) != 2 || result.SubscribeEvents[0] != "turn_start" || result.SubscribeEvents[1] != "turn_end" {
		t.Fatalf("subscribe events = %#v", result.SubscribeEvents)
	}
	// Callback events emitted during Prompt must be delivered before the
	// prompt's terminal result.
	if result.EventsAtPromptResolve != 2 {
		t.Fatalf("events delivered at prompt resolution = %d, want 2", result.EventsAtPromptResolve)
	}
	if result.Stats.Tokens.Total != 3 || result.Stats.Cost != 0.25 {
		t.Fatalf("stats mirror = %#v", result.Stats)
	}
	// The last messages_snapshot exceeded the 4 MiB frame cap and traveled as
	// agent_session_chunk frames; the mirror must hold it intact.
	if result.MessageCount != 1 || result.BigLen != 5<<20 {
		t.Fatalf("chunked mirror: messages=%d bigLen=%d, want 1/%d", result.MessageCount, result.BigLen, 5<<20)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.toolErr != nil || !strings.Contains(fake.toolResult, "stored:ALPHA:call-1") {
		t.Fatalf("execute_session_tool round trip = %q, %v (prepareArguments must run host-side)", fake.toolResult, fake.toolErr)
	}
	if len(fake.activeTools) != 1 || fake.activeTools[0] != "read" {
		t.Fatalf("setActiveToolsByName = %#v", fake.activeTools)
	}
	if len(fake.appended) != 1 || fake.appended[0] != "workflow:test" {
		t.Fatalf("appendSessionInfo = %#v", fake.appended)
	}
	if fake.aborts != 1 || fake.disposes != 1 {
		t.Fatalf("aborts=%d disposes=%d, want 1/1 (dispose must dedupe)", fake.aborts, fake.disposes)
	}
	if len(fake.prompts) != 1 || fake.prompts[0] != "go" {
		t.Fatalf("prompts = %#v", fake.prompts)
	}
	options := fake.options
	if options.Model == nil || options.Model.ID != "synth-model" || string(options.Model.Provider) != "faux" {
		t.Fatalf("off-catalog model did not cross: %#v", options.Model)
	}
	if len(options.ExcludeTools) != 2 || options.ExcludeTools[0] != "workflow" {
		t.Fatalf("excludeTools = %#v", options.ExcludeTools)
	}
	if options.ModelRuntime == nil || options.ModelRuntime.Handle != "context:ext-1" {
		t.Fatalf("modelRuntime ref = %#v", options.ModelRuntime)
	}
	if options.ResourceLoader == nil || !options.ResourceLoader.NoExtensions {
		t.Fatalf("resourceLoader = %#v (noExtensions is the anti-recursion guarantee)", options.ResourceLoader)
	}
	if options.Session == nil || !options.Session.Persisted || options.Session.SessionDir != expectedDir {
		t.Fatalf("session storage = %#v", options.Session)
	}
	if options.Settings == nil || options.Settings.CWD != cwd {
		t.Fatalf("settings = %#v", options.Settings)
	}
	var builtins, callbacks []string
	for _, tool := range options.CustomTools {
		if tool.Builtin != "" {
			builtins = append(builtins, tool.Builtin)
		} else {
			callbacks = append(callbacks, tool.Name)
		}
	}
	if len(builtins) != 1 || builtins[0] != "read" || len(callbacks) != 1 || callbacks[0] != "store_put" {
		t.Fatalf("customTools split = builtins %#v, callbacks %#v", builtins, callbacks)
	}
}

func TestRealHostAgentSessionCancelPropagatesServiceCancel(t *testing.T) {
	manager, runner, _, _ := startServicesFixture(t)
	fake := &fakeAgentSessionService{}
	manager.SetAgentSessionService(fake)
	var result struct {
		Cancel struct {
			Resolved bool   `json:"resolved"`
			Code     string `json:"code"`
		} `json:"cancel"`
		AfterDispose *struct {
			Code string `json:"code"`
		} `json:"afterDispose"`
	}
	runServiceTool(t, runner, "svc_session_cancel", map[string]any{}, &result)
	if result.Cancel.Resolved || result.Cancel.Code != "cancelled" {
		t.Fatalf("aborted prompt = %#v, want structured code cancelled", result.Cancel)
	}
	if result.AfterDispose == nil || result.AfterDispose.Code != "unknown_handle" {
		t.Fatalf("post-dispose call = %#v, want code unknown_handle", result.AfterDispose)
	}
}
