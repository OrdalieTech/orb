package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
)

func isolateSDKAgentDir(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvAgentDir, t.TempDir())
}

func sdkServiceProviderFactory(api extensions.API) error {
	api.RegisterProviderConfig("sdk-service", extensions.ProviderConfig{
		Name: "SDK service", BaseURL: "https://sdk.invalid/v1", APIKey: "sdk-key",
		API: ai.APIOpenAIResponses,
		Models: []extensions.ProviderModelConfig{{
			ID: "sdk-model", Name: "SDK model", API: ai.APIOpenAIResponses,
			BaseURL: "https://sdk.invalid/v1", Input: ai.InputModalities{ai.InputText},
			ContextWindow: 1000, MaxTokens: 100,
		}},
	})
	return nil
}

func TestSDKServiceFactoryPublishesNativeExtensionProvidersBeforeSessionCreation(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	services, err := CreateAgentSessionServices(CreateAgentSessionServicesOptions{
		CWD: cwd, AgentDir: agentDir,
		ResourceLoaderOptions: &DefaultResourceLoaderOptions{
			ExtensionFactories: []extensions.Factory{sdkServiceProviderFactory},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := services.ModelRegistry.Find("sdk-service", "sdk-model"); !ok {
		t.Fatal("cwd services returned before native extension providers were published")
	}
}

func TestSDKDefaultModelSelectionIncludesResourceLoaderProviders(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	loader, err := NewDefaultResourceLoader(DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, NoSkills: true, NoPromptTemplates: true, NoContextFiles: true,
		AppendSystemPrompt: []string{}, ExtensionFactories: []extensions.Factory{sdkServiceProviderFactory},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	result, err := NewAgentSession(AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, ResourceLoader: loader, NoTools: "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	model := result.Session.State().Model
	if model == nil || model.Provider != "sdk-service" || model.ID != "sdk-model" {
		t.Fatalf("selected model = %#v", model)
	}
}

func TestSDKServiceFactoryAppliesExtensionFlagsAndReportsInvalidValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME", "OPENCODE_CONFIG_DIR", "GEMINI_CLI_HOME", "COPILOT_HOME", "COPILOT_SKILLS_DIRS"} {
		t.Setenv(name, "")
	}
	cwd, agentDir := t.TempDir(), t.TempDir()
	services, err := CreateAgentSessionServices(CreateAgentSessionServicesOptions{
		CWD: cwd, AgentDir: agentDir,
		ResourceLoaderOptions: &DefaultResourceLoaderOptions{
			ExtensionFactories: []extensions.Factory{func(api extensions.API) error {
				api.RegisterFlag("enabled", extensions.Flag{Type: extensions.FlagBoolean})
				api.RegisterFlag("label", extensions.Flag{Type: extensions.FlagString})
				api.RegisterFlag("bad", extensions.Flag{Type: extensions.FlagString})
				return nil
			}},
		},
		ExtensionFlagValues: map[string]any{
			"enabled": false, "label": "sdk", "bad": true, "unknown": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := extensions.NewRunner(services.ExtensionRegistry, extensions.RunnerOptions{CWD: cwd})
	flags := runner.FlagValues()
	if flags["enabled"] != true || flags["label"] != "sdk" {
		t.Fatalf("extension flags = %#v", flags)
	}
	wantDiagnostics := []AgentSessionRuntimeDiagnostic{
		{Type: "error", Message: `Extension flag "--bad" requires a value`},
		{Type: "error", Message: "Unknown option: --unknown"},
	}
	if !reflect.DeepEqual(services.Diagnostics, wantDiagnostics) {
		t.Fatalf("diagnostics = %#v", services.Diagnostics)
	}
}

func TestNewAgentSessionReloadReadsDynamicProviderSettingsPerRequest(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	agentDir := t.TempDir()
	settingsPath := filepath.Join(agentDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{
  "transport": "sse",
  "websocketConnectTimeoutMs": 20,
  "thinkingBudgets": {"minimal": 1},
  "retry": {"provider": {"timeoutMs": 10, "maxRetries": 1, "maxRetryDelayMs": 30}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	var capturedMu sync.Mutex
	captured := make([]ai.SimpleStreamOptions, 0, 2)
	stream := func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		copy := *options
		capturedMu.Lock()
		captured = append(captured, copy)
		capturedMu.Unlock()
		response := runtimeAssistant(provider, "ok", 10)
		return func(yield func(ai.AssistantMessageEvent, error) bool) {
			yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: response}, nil)
		}, nil
	}
	result, err := NewAgentSession(AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: stream, NoTools: "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	if err := result.Session.PromptSync(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{
  "transport": "websocket",
  "websocketConnectTimeoutMs": 22,
  "thinkingBudgets": {"minimal": 2},
  "retry": {"provider": {"timeoutMs": 11, "maxRetries": 2, "maxRetryDelayMs": 31}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := result.Session.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := result.Session.PromptSync(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}

	capturedMu.Lock()
	got := append([]ai.SimpleStreamOptions(nil), captured...)
	capturedMu.Unlock()
	if len(got) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(got))
	}
	if got[1].TimeoutMS == nil || *got[1].TimeoutMS != 11 ||
		got[1].WebSocketConnectTimeoutMS == nil || *got[1].WebSocketConnectTimeoutMS != 22 ||
		got[1].MaxRetries == nil || *got[1].MaxRetries != 2 {
		t.Fatalf("dynamic provider settings after reload = %#v", got[1])
	}
	if got[1].Transport == nil || *got[1].Transport != ai.TransportSSE ||
		got[1].MaxRetryDelayMS == nil || *got[1].MaxRetryDelayMS != 30 ||
		got[1].ThinkingBudgets == nil || got[1].ThinkingBudgets.Minimal == nil || *got[1].ThinkingBudgets.Minimal != 1 {
		t.Fatalf("construction-time provider settings changed on reload = %#v", got[1])
	}
}

func TestNewAgentSessionReloadRebuildsSettingsBoundTools(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	agentDir := t.TempDir()
	settingsPath := filepath.Join(agentDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"shellCommandPrefix":"export ORB_RELOAD_PREFIX=old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	result, err := NewAgentSession(AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	runBash := func() string {
		t.Helper()
		var bash engine.AgentTool
		for _, candidate := range result.Session.State().Tools {
			if candidate.Spec().Name == "bash" {
				bash = candidate
				break
			}
		}
		if bash == nil {
			t.Fatal("active bash tool is missing")
		}
		output, executeErr := bash.Execute(context.Background(), "reload", map[string]any{
			"command": `printf '%s' "$ORB_RELOAD_PREFIX"`,
		}, nil)
		if executeErr != nil {
			t.Fatal(executeErr)
		}
		if len(output.Content) != 1 {
			t.Fatalf("bash output = %#v", output.Content)
		}
		text, ok := output.Content[0].(*ai.TextContent)
		if !ok {
			t.Fatalf("bash output block = %T", output.Content[0])
		}
		return text.Text
	}

	if got := runBash(); got != "old" {
		t.Fatalf("initial bash prefix = %q", got)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"shellCommandPrefix":"export ORB_RELOAD_PREFIX=new"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := result.Session.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := runBash(); got != "new" {
		t.Fatalf("reloaded bash prefix = %q", got)
	}
}

func TestNewAgentSessionPrompt(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "world", 10)})

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	var texts []string
	result.Session.Subscribe(func(event any) {
		if end, ok := event.(SessionAgentEndEvent); ok {
			for _, msg := range end.Messages {
				if assistant := asAssistant(msg); assistant != nil {
					for _, block := range assistant.Content {
						if text, ok := block.(*ai.TextContent); ok {
							texts = append(texts, text.Text)
						}
					}
				}
			}
		}
	})

	if err := result.Session.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if len(texts) == 0 {
		t.Fatal("no text received")
	}
}

func TestNewAgentSessionRefreshesStateBetweenToolTurns(t *testing.T) {
	cwd := t.TempDir()
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	modelA, modelB := *provider.GetModel(), *provider.GetModel()
	modelA.ID, modelB.ID, modelA.Reasoning, modelB.Reasoning = "before", "after", true, true
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("switch", map[string]any{}, faux.ToolCallOptions{ID: "call"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage("done"),
	})
	var calls []struct {
		model, prompt, tools, thinking string
	}
	stream := func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		toolNames := []string{}
		if request.Tools != nil {
			for _, tool := range *request.Tools {
				toolNames = append(toolNames, tool.Name)
			}
		}
		prompt := ""
		if request.SystemPrompt != nil {
			prompt = *request.SystemPrompt
		}
		thinking := ""
		if options.Reasoning != nil {
			thinking = string(*options.Reasoning)
		}
		calls = append(calls, struct{ model, prompt, tools, thinking string }{model.ID, prompt, strings.Join(toolNames, ","), thinking})
		return provider.StreamSimple(ctx, model, request, options)
	}
	registry := extensions.NewRegistry(cwd)
	var result *AgentSessionResult
	if err := registry.Register("<inline:refresh>", func(api extensions.API) error {
		api.On(extensions.EventBeforeAgentStart, func(_ context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
			prompt := "overridden"
			return extensions.BeforeAgentStartResult{SystemPrompt: &prompt}, nil
		})
		api.RegisterTool(extensions.ToolDefinition{Name: "switch", Parameters: ai.JSONSchema(`{"type":"object"}`), Execute: func(context.Context, string, any, engine.AgentToolUpdateCallback, extensions.Context) (engine.AgentToolResult, error) {
			if err := result.Session.SetActiveToolsByName([]string{"after"}); err != nil {
				return engine.AgentToolResult{}, err
			}
			result.Session.Agent().SetModel(&modelB)
			result.Session.Agent().SetThinkingLevel(ai.ModelThinkingHigh)
			return engine.AgentToolResult{}, nil
		}})
		api.RegisterTool(extensions.ToolDefinition{Name: "after", Parameters: ai.JSONSchema(`{"type":"object"}`)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err = NewAgentSession(AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Model: &modelA, ThinkingLevel: ai.ModelThinkingLow,
		StreamFn: stream, ExtensionRegistry: registry, NoTools: "builtin", Resources: &Resources{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	if err := result.Session.SetActiveToolsByName([]string{"switch"}); err != nil {
		t.Fatal(err)
	}
	if err := result.Session.PromptSync(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if got, want := calls, []struct{ model, prompt, tools, thinking string }{{"before", "overridden", "switch", "low"}, {"after", "overridden", "after", "high"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("provider calls = %#v, want %#v", got, want)
	}
}

func TestNewAgentSessionActivatesRehydratedHarnessStorage(t *testing.T) {
	input, err := os.ReadFile(filepath.Join("session", "testdata", "harness-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	storage, err := harness.RehydrateJSONLSession(input, filepath.Join(t.TempDir(), "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cwd, agentDir := t.TempDir(), t.TempDir()
	manager, err := sessionstore.FromHarnessStorage(storage, sessionstore.WithCwdOverride(cwd))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "runtime reply", 10)})
	result, err := NewAgentSession(AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, SessionManager: manager, Settings: settings,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &Resources{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	state := result.Session.State()
	// The v3 fixture leaf sits on a compacted branch: the projected context is
	// the compaction summary plus the branch summary.
	if len(state.Messages) != 2 {
		t.Fatalf("rehydrated message count = %d, want 2", len(state.Messages))
	}
	toolNames := make([]string, len(state.Tools))
	for index := range state.Tools {
		toolNames[index] = state.Tools[index].Spec().Name
	}
	if got := strings.Join(toolNames, ","); got != "" {
		t.Fatalf("rehydrated active tools = %q, want explicit empty state", got)
	}

	before := len(storage.EntriesByType("message"))
	if err := result.Session.PromptSync(context.Background(), "runtime write"); err != nil {
		t.Fatal(err)
	}
	if got := len(storage.EntriesByType("message")); got != before+3 {
		t.Fatalf("storage message count after runtime prompt = %d, want %d", got, before+3)
	}

	leaf, err := storage.LeafID()
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.AppendEntry(harness.SessionTreeEntry{
		Type: "message", ID: "storage-runtime-user", ParentID: leaf, Timestamp: "2026-02-03T04:05:30.000Z",
		Message: json.RawMessage(`{"role":"user","content":[{"type":"text","text":"storage write"}],"timestamp":6}`),
	}); err != nil {
		t.Fatal(err)
	}
	forking := result.Session.GetUserMessagesForForking()
	if got := forking[len(forking)-1].EntryID; got != "storage-runtime-user" {
		t.Fatalf("runtime did not observe storage write: last user id = %q", got)
	}
}

func TestNewAgentSessionThinkingLevelClamped(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	model := provider.GetModel()

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn:      provider.StreamSimple,
		Model:         model,
		ThinkingLevel: ai.ModelThinkingHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	state := result.Session.State()
	supported := ai.SupportedThinkingLevels(state.Model)
	found := false
	for _, level := range supported {
		if level == state.ThinkingLevel {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("thinking level %s not in supported %v", state.ThinkingLevel, supported)
	}
}

func TestSubscribeChan(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "chan-test", 10)})

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	ch, cancel := result.Session.SubscribeChan(64)
	defer cancel()

	var settled bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for event := range ch {
			if _, ok := event.(AgentSettledEvent); ok {
				settled = true
				cancel()
				return
			}
		}
	}()

	if err := result.Session.Prompt(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for events")
	}
	if !settled {
		t.Fatal("did not receive settled event")
	}
}

func TestSubscribeChanPreservesSaturatedEventOrder(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	ch, cancel := result.Session.SubscribeChan(1)
	defer cancel()
	for value := 0; value < 256; value++ {
		result.Session.emit(value)
	}
	result.Session.emit(AgentSettledEvent{})

	for want := 0; want < 256; want++ {
		select {
		case got := <-ch:
			if got != want {
				t.Fatalf("event %d = %#v", want, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", want)
		}
	}
	select {
	case got := <-ch:
		if _, ok := got.(AgentSettledEvent); !ok {
			t.Fatalf("terminal event = %T, want AgentSettledEvent", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal event")
	}
}

func TestPromptSync(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "sync", 10)})

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	if err := result.Session.PromptSync(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	messages := result.Session.State().Messages
	found := false
	for _, msg := range messages {
		if assistant := asAssistant(msg); assistant != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("expected assistant message after PromptSync")
	}
}

func TestNewAgentSessionRestoresFromExistingSession(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "first", 10)})
	model := provider.GetModel()

	sm, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	result1, err := NewAgentSession(AgentSessionOptions{
		StreamFn:       provider.StreamSimple,
		Model:          model,
		SessionManager: sm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := result1.Session.Prompt(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	result1.Session.Dispose()

	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "second", 10)})
	result2, err := NewAgentSession(AgentSessionOptions{
		StreamFn:       provider.StreamSimple,
		Model:          model,
		SessionManager: sm,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result2.Session.Dispose()

	messages := result2.Session.State().Messages
	if len(messages) < 2 {
		t.Fatalf("expected restored messages, got %d", len(messages))
	}
}

func TestNewAgentSessionInitializesMissingThinkingEntryFromSettings(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	model := provider.GetModel()
	model.Reasoning = true
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(userMessage("existing session")); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(t.TempDir(), config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	settings.SetDefaultThinkingLevel(ai.ModelThinkingHigh)

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn:       provider.StreamSimple,
		Model:          model,
		SessionManager: manager,
		Settings:       settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	if result.Session.State().ThinkingLevel != ai.ModelThinkingHigh {
		t.Fatalf("thinking level = %q", result.Session.State().ThinkingLevel)
	}
	branch := manager.GetBranch()
	if len(branch) == 0 || branch[len(branch)-1].Type != "thinking_level_change" || branch[len(branch)-1].ThinkingLevel != string(ai.ModelThinkingHigh) {
		t.Fatalf("missing initialized thinking entry: %#v", branch)
	}
}

func TestSubscribeChanConcurrentCancel(t *testing.T) {
	isolateSDKAgentDir(t)
	// Multiple goroutines calling cancel must not panic.
	provider := testFaux(100000)
	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	ch, cancel := result.Session.SubscribeChan(4)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cancel()
		}()
	}
	wg.Wait()

	// Channel must be closed exactly once.
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("expected closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed")
	}
}

func TestNewAgentSessionModelRegistryRestore(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "first", 10)})
	model := provider.GetModel()

	sm, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	result1, err := NewAgentSession(AgentSessionOptions{
		StreamFn:       provider.StreamSimple,
		Model:          model,
		SessionManager: sm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := result1.Session.Prompt(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	result1.Session.Dispose()

	// Resume without explicit Model — should restore from session via registry.
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "second", 10)})
	registry, err := config.NewModelRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result2, err := NewAgentSession(AgentSessionOptions{
		StreamFn:       provider.StreamSimple,
		SessionManager: sm,
		ModelRegistry:  registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result2.Session.Dispose()

	// Faux provider isn't in the real registry, so restoration should fail.
	// A fallback message should be present.
	if result2.ModelFallbackMessage == "" {
		t.Fatal("expected fallback message when faux model can't be restored")
	}
}

func TestNewAgentSessionNoToolsBuiltinRetainsCustom(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)

	registry := extensions.NewRegistry(".")
	_ = registry.Register("<test:custom>", func(api extensions.API) error {
		api.RegisterTool(extensions.ToolDefinition{
			Name:        "custom_test",
			Description: "test tool",
		})
		return nil
	})

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn:          provider.StreamSimple,
		Model:             provider.GetModel(),
		NoTools:           "builtin",
		ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	state := result.Session.State()
	foundCustom := false
	for _, tool := range state.Tools {
		name := tool.Spec().Name
		if name == "custom_test" {
			foundCustom = true
		}
		if name == "read" || name == "bash" || name == "edit" || name == "write" {
			t.Fatalf("builtin tool %q should be suppressed with noTools=builtin", name)
		}
	}
	if !foundCustom {
		t.Fatal("custom_test tool should be active when noTools=builtin")
	}
}

func TestNewAgentSessionNoToolsAllSuppressesCustom(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)

	registry := extensions.NewRegistry(".")
	_ = registry.Register("<test:custom>", func(api extensions.API) error {
		api.RegisterTool(extensions.ToolDefinition{
			Name:        "custom_test",
			Description: "test tool",
		})
		return nil
	})

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn:          provider.StreamSimple,
		Model:             provider.GetModel(),
		NoTools:           "all",
		ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	if len(result.Session.State().Tools) != 0 {
		names := make([]string, 0)
		for _, tool := range result.Session.State().Tools {
			names = append(names, tool.Spec().Name)
		}
		t.Fatalf("expected 0 tools with noTools=all, got %v", names)
	}
}

func TestNewAgentSessionConvertsPersistedCodingAgentMessages(t *testing.T) {
	provider := testFaux(100000)
	model := provider.GetModel()
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := manager.AppendMessage(userMessage("before compaction"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendCompaction("persisted summary", firstID, 42); err != nil {
		t.Fatal(err)
	}

	var request ai.Context
	stream := func(_ context.Context, _ *ai.Model, got ai.Context, _ *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		request = got
		response := runtimeAssistant(provider, "ok", 10)
		return func(yield func(ai.AssistantMessageEvent, error) bool) {
			yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: response}, nil)
		}, nil
	}
	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn:       stream,
		Model:          model,
		SessionManager: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	if err := result.Session.PromptSync(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(request.Messages)
	if err != nil {
		t.Fatal(err)
	}
	var messages []struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(encoded, &messages); err != nil {
		t.Fatal(err)
	}
	want := CompactionSummaryPrefix + "persisted summary" + CompactionSummarySuffix
	found := false
	for _, message := range messages {
		for _, block := range message.Content {
			found = found || block.Text == want
		}
	}
	if !found {
		t.Fatalf("provider request omitted projected compaction summary: %s", encoded)
	}
}

func TestReloadEnablesToolsNewlyAddedToDefaultTools(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	agentDir := t.TempDir()
	settingsPath := filepath.Join(agentDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"defaultTools":["+grep"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	result, err := NewAgentSession(AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	active := func() []string {
		var names []string
		for _, tool := range result.Session.State().Tools {
			names = append(names, tool.Spec().Name)
		}
		return names
	}
	if got := active(); !slices.Contains(got, "grep") || slices.Contains(got, "find") {
		t.Fatalf("initial tools = %v", got)
	}
	// write is turned off during the session; find is newly added and grep removed.
	if err := result.Session.SetActiveToolsByName([]string{"read", "bash", "edit", "grep"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"defaultTools":["+find"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := result.Session.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := active(); !slices.Contains(got, "find") || !slices.Contains(got, "grep") || slices.Contains(got, "write") {
		t.Fatalf("reloaded tools = %v", got)
	}
}

func TestResumeRestoresLoadoutAndActivatesToolsThatRegisterLate(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "first", 10)})
	model := provider.GetModel()
	cwd := t.TempDir()
	sm, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	late := extensions.ToolDefinition{Name: "late", Exposure: extensions.ToolDeferred, Parameters: ai.JSONSchema(`{"type":"object"}`)}
	start := func(registerNow bool) (*AgentSessionResult, *extensions.API) {
		var bound extensions.API
		registry := extensions.NewRegistry(cwd)
		if err := registry.Register("<inline:late>", func(api extensions.API) error {
			bound = api
			api.On(extensions.EventBeforeAgentStart, func(_ context.Context, event extensions.Event, _ extensions.Context) (any, error) {
				event.(extensions.BeforeAgentStartEvent).SystemPromptOptions.Sections["servers"] = "late servers"
				return nil, nil
			})
			if registerNow {
				api.RegisterTool(late)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		result, err := NewAgentSession(AgentSessionOptions{
			CWD: cwd, AgentDir: t.TempDir(), SessionManager: sm, Model: model,
			StreamFn: provider.StreamSimple, ExtensionRegistry: registry, Resources: &Resources{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result, &bound
	}
	active := func(result *AgentSessionResult) []string {
		var names []string
		for _, tool := range result.Session.State().Tools {
			names = append(names, tool.Spec().Name)
		}
		return names
	}

	first, _ := start(true)
	if slices.Contains(active(first), "late") {
		t.Fatal("deferred tool active on registration")
	}
	if err := first.Session.SetActiveToolsByName([]string{"read", "late"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Session.Prompt(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	transcript, _ := ConvertToLLM(context.Background(), first.Session.State().Messages)
	current := ai.CurrentSystemMessage(transcript)
	if current == nil || !strings.Contains(ai.SystemMessageText(current), "<servers>\nlate servers\n</servers>") {
		t.Fatalf("section missing from the transcript: %#v", current)
	}
	first.Session.Dispose()

	second, api := start(false)
	defer second.Session.Dispose()
	if got := active(second); !reflect.DeepEqual(got, []string{"read"}) {
		t.Fatalf("restored loadout = %v", got)
	}
	(*api).RegisterTool(late)
	if got := active(second); !reflect.DeepEqual(got, []string{"read", "late"}) {
		t.Fatalf("late tool did not turn on when it registered: %v", got)
	}
}
