package codingagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func isolateSDKAgentDir(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvAgentDir, t.TempDir())
}

func TestNewAgentSessionMinimal(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "hello", 10)})

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	if result.Session == nil {
		t.Fatal("session is nil")
	}
	if result.ModelFallbackMessage != "" {
		t.Fatalf("unexpected fallback: %s", result.ModelFallbackMessage)
	}
}

func TestSDKPublicSessionControlsMatchUpstream(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "accepted", 10)})
	result, err := NewAgentSession(AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &Resources{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	if result.Session.Agent() == nil || result.Session.Agent().State().Model.ID != provider.GetModel().ID {
		t.Fatalf("direct agent = %#v", result.Session.Agent())
	}
	if got := result.Session.GetActiveToolNames(); !reflect.DeepEqual(got, DefaultActiveToolNames) {
		t.Fatalf("active tools = %#v", got)
	}
	if err := result.Session.SetActiveToolsByName([]string{"read", "missing"}); err != nil {
		t.Fatal(err)
	}
	if got := result.Session.GetActiveToolNames(); !reflect.DeepEqual(got, []string{"read"}) {
		t.Fatalf("filtered active tools = %#v", got)
	}

	if err := result.Session.SendCustomMessage(context.Background(), CustomMessage{CustomType: "sdk", Content: nil, Display: true}, nil); err != nil {
		t.Fatal(err)
	}
	state := result.Session.State()
	custom, ok := state.Messages[len(state.Messages)-1].(*harness.CustomMessage)
	if !ok || custom.CustomType != "sdk" || !reflect.DeepEqual(custom.Content, []any{}) {
		t.Fatalf("custom message = %#v", state.Messages[len(state.Messages)-1])
	}

	var preflight []bool
	if err := result.Session.PromptWithOptions(context.Background(), "hello", &PromptOptions{
		PreflightResult: func(success bool) { preflight = append(preflight, success) },
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preflight, []bool{true}) {
		t.Fatalf("preflight = %#v", preflight)
	}
}

func TestSDKPromptOptionsReportUnknownModelPreflightRejection(t *testing.T) {
	t.Setenv(config.EnvAgentDir, t.TempDir())
	result, err := NewAgentSession(AgentSessionOptions{Resources: &Resources{}})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	var preflight []bool
	err = result.Session.PromptWithOptions(context.Background(), "hello", &PromptOptions{
		PreflightResult: func(success bool) { preflight = append(preflight, success) },
	})
	if err == nil || !strings.HasPrefix(err.Error(), "No API key found for the selected model.") {
		t.Fatalf("prompt error = %v", err)
	}
	if !reflect.DeepEqual(preflight, []bool{false}) {
		t.Fatalf("preflight = %#v", preflight)
	}
}

func TestSDKServiceFactoriesReuseCWDServices(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	services, err := CreateAgentSessionServices(CreateAgentSessionServicesOptions{CWD: cwd, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	if services.CWD != cwd || services.AgentDir != agentDir || services.SettingsManager == nil || services.ModelRegistry == nil || services.Resources == nil || services.ExtensionRegistry == nil {
		t.Fatalf("services = %#v", services)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	result, err := CreateAgentSessionFromServices(CreateAgentSessionFromServicesOptions{
		Services: services, SessionManager: manager, Model: provider.GetModel(), NoTools: "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	if result.Services != services || result.Session.Manager() != manager || result.Session.State().Model.ID != provider.GetModel().ID {
		t.Fatalf("session result = %#v", result)
	}
}

func TestSDKServiceFactoryForwardsResourceReloadOptions(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	trustCalls := 0
	services, err := CreateAgentSessionServices(CreateAgentSessionServicesOptions{
		CWD: cwd, AgentDir: agentDir,
		ResourceLoaderOptions: &DefaultResourceLoaderOptions{
			ExtensionFactories: []extensions.Factory{func(extensions.API) error { return nil }},
		},
		ResourceLoaderReloadOptions: &ResourceLoaderReloadOptions{
			ResolveProjectTrust: func(_ context.Context, registry *extensions.Registry) (bool, error) {
				trustCalls++
				if !registry.HasPath("<inline:sdk-1>") {
					t.Fatal("resource reload options ran before extension discovery")
				}
				return true, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if trustCalls != 1 || !services.SettingsManager.IsProjectTrusted() {
		t.Fatalf("trust calls=%d trusted=%t", trustCalls, services.SettingsManager.IsProjectTrusted())
	}
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

func TestNewAgentSessionForwardsStreamSettings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		settings    map[string]any
		wantTimeout int64
	}{
		{
			name: "http idle fallback",
			settings: map[string]any{
				"httpIdleTimeoutMs":         1234,
				"websocketConnectTimeoutMs": 5678,
				"thinkingBudgets":           map[string]any{"minimal": 1, "low": 2, "medium": 3, "high": 4},
			},
			wantTimeout: 1234,
		},
		{
			name:        "disabled idle uses sdk sentinel",
			settings:    map[string]any{"httpIdleTimeoutMs": 0},
			wantTimeout: 2147483647,
		},
		{
			name: "provider timeout wins",
			settings: map[string]any{
				"httpIdleTimeoutMs": 1234,
				"retry":             map[string]any{"provider": map[string]any{"timeoutMs": 42}},
			},
			wantTimeout: 42,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cwd := t.TempDir()
			agentDir := t.TempDir()
			encoded, err := json.Marshal(test.settings)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), encoded, 0o644); err != nil {
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
			provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "ok", 10)})
			var captured ai.SimpleStreamOptions
			stream := func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
				if options != nil {
					captured = *options
				}
				return provider.StreamSimple(ctx, model, request, options)
			}
			result, err := NewAgentSession(AgentSessionOptions{
				CWD: cwd, AgentDir: agentDir, Model: provider.GetModel(), StreamFn: stream,
				Settings: settings, SessionManager: manager, NoTools: "all",
			})
			if err != nil {
				t.Fatal(err)
			}
			defer result.Session.Dispose()
			if err := result.Session.PromptSync(context.Background(), "hello"); err != nil {
				t.Fatal(err)
			}
			if captured.TimeoutMS == nil || *captured.TimeoutMS != test.wantTimeout {
				t.Fatalf("timeout = %#v, want %d", captured.TimeoutMS, test.wantTimeout)
			}
			if test.name == "http idle fallback" {
				if captured.WebSocketConnectTimeoutMS == nil || *captured.WebSocketConnectTimeoutMS != 5678 {
					t.Fatalf("websocket timeout = %#v", captured.WebSocketConnectTimeoutMS)
				}
				budgets := captured.ThinkingBudgets
				if budgets == nil || budgets.Minimal == nil || *budgets.Minimal != 1 || budgets.High == nil || *budgets.High != 4 {
					t.Fatalf("thinking budgets = %#v", budgets)
				}
			}
		})
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
	if err := os.WriteFile(settingsPath, []byte(`{"shellCommandPrefix":"export PI_GO_RELOAD_PREFIX=old"}`), 0o644); err != nil {
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
		var bash agent.AgentTool
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
			"command": `printf '%s' "$PI_GO_RELOAD_PREFIX"`,
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
	if err := os.WriteFile(settingsPath, []byte(`{"shellCommandPrefix":"export PI_GO_RELOAD_PREFIX=new"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := result.Session.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := runBash(); got != "new" {
		t.Fatalf("reloaded bash prefix = %q", got)
	}
}

func TestBuildBuiltInToolsHonorsImageAutoResizeSetting(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"images":{"autoResize":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 2001, 1))); err != nil {
		t.Fatal(err)
	}
	imageBytes := encoded.Bytes()
	if err := os.WriteFile(filepath.Join(cwd, "fixture.png"), imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	builtIns, err := buildBuiltInTools(cwd, settings)
	if err != nil {
		t.Fatal(err)
	}
	var read agent.AgentTool
	for _, tool := range builtIns {
		if tool.Spec().Name == "read" {
			read = tool
			break
		}
	}
	if read == nil {
		t.Fatal("read tool was not built")
	}
	result, err := read.Execute(context.Background(), "call", map[string]any{"path": "fixture.png"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 2 {
		t.Fatalf("read content = %#v", result.Content)
	}
	image, ok := result.Content[1].(*ai.ImageContent)
	if !ok || image.Data != base64.StdEncoding.EncodeToString(imageBytes) || image.MimeType != "image/png" {
		t.Fatalf("image content = %#v", result.Content[1])
	}
}

func TestNewAgentSessionResolvesCWD(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	provider := testFaux(100000)
	result, err := NewAgentSession(AgentSessionOptions{
		AgentDir: t.TempDir(),
		CWD:      "project",
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	if got := result.Session.Manager().GetCWD(); got != project {
		t.Fatalf("session cwd = %q, want %q", got, project)
	}
	if got := result.Session.State().SystemPrompt; !strings.HasSuffix(got, "Current working directory: "+project) {
		t.Fatalf("system prompt uses unresolved cwd: %q", got)
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

func TestNewAgentSessionWithExplicitSessionManager(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "ok", 10)})

	sm, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn:       provider.StreamSimple,
		Model:          provider.GetModel(),
		SessionManager: sm,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	if err := result.Session.Prompt(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if len(result.Session.State().Messages) == 0 {
		t.Fatal("expected messages after prompt")
	}
}

func TestNewAgentSessionActivatesRehydratedHarnessStorage(t *testing.T) {
	input, err := os.ReadFile(filepath.Join("..", "conformance", "fixtures", "F6Harness", "session.jsonl"))
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
	if len(state.Messages) != 3 {
		t.Fatalf("rehydrated message count = %d, want 3", len(state.Messages))
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
	if got := len(storage.EntriesByType("message")); got != before+2 {
		t.Fatalf("storage message count after runtime prompt = %d, want %d", got, before+2)
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

func TestNewAgentSessionNoModel(t *testing.T) {
	isolateSDKAgentDir(t)
	packageDir := t.TempDir()
	t.Setenv("PI_PACKAGE_DIR", packageDir)
	result, err := NewAgentSession(AgentSessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	state := result.Session.State()
	if state.Model == nil || state.Model.Provider != "unknown" || state.Model.ID != "unknown" || state.Model.API != "unknown" {
		t.Fatalf("expected upstream unknown model sentinel, got %v", state.Model)
	}
	if state.ThinkingLevel != ai.ModelThinkingOff {
		t.Fatalf("expected off thinking, got %s", state.ThinkingLevel)
	}
	want := "No models available. Use /login to log into a provider via OAuth or API key. See:\n  " +
		filepath.Join(packageDir, "docs", "providers.md") + "\n  " + filepath.Join(packageDir, "docs", "models.md")
	if result.ModelFallbackMessage != want {
		t.Fatalf("fallback = %q, want %q", result.ModelFallbackMessage, want)
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

func TestSubscribeChanDrainOnCancel(t *testing.T) {
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

	ch, cancel := result.Session.SubscribeChan(2)
	cancel()

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("channel not closed after cancel")
	}
}

func TestAgentSessionTypeAlias(t *testing.T) {
	isolateSDKAgentDir(t)
	// Compile-time proof that AgentSession = SessionRuntime
	var session *AgentSession
	var runtime *SessionRuntime
	session = runtime
	_ = session
	runtime = session
	_ = runtime
}

func TestSubscribeChanRaceRegression(t *testing.T) {
	isolateSDKAgentDir(t)
	// Regression: SubscribeChan must not panic when cancel races with event delivery.
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "race", 10)})

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	// Rapid subscribe/cancel cycles under race detector.
	for i := 0; i < 100; i++ {
		ch, cancel := result.Session.SubscribeChan(1)
		go cancel()
		go func() {
			for range ch { //nolint:revive
			}
		}()
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

func TestNewAgentSessionWithTools(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		Tools:    []string{"read", "grep"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	state := result.Session.State()
	if len(state.Tools) != 2 {
		names := make([]string, len(state.Tools))
		for i, t := range state.Tools {
			names[i] = t.Spec().Name
		}
		t.Fatalf("expected 2 tools, got %d: %v", len(state.Tools), names)
	}
}

func TestNewAgentSessionNoToolsAll(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		NoTools:  "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	if len(result.Session.State().Tools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(result.Session.State().Tools))
	}
}

func TestNewAgentSessionExcludeTools(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn:     provider.StreamSimple,
		Model:        provider.GetModel(),
		ExcludeTools: []string{"write", "edit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	for _, tool := range result.Session.State().Tools {
		name := tool.Spec().Name
		if name == "write" || name == "edit" {
			t.Fatalf("tool %s should be excluded", name)
		}
	}
}

func TestNewAgentSessionDefaultBuildsTools(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "ok", 10)})

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	state := result.Session.State()
	if len(state.Tools) < 4 {
		t.Fatalf("expected at least 4 default tools, got %d", len(state.Tools))
	}
	names := make(map[string]bool)
	for _, tool := range state.Tools {
		names[tool.Spec().Name] = true
	}
	for _, required := range []string{"read", "bash", "edit", "write"} {
		if !names[required] {
			t.Fatalf("missing default tool %q", required)
		}
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

func TestNewAgentSessionModelRegistryUsedForCycleModel(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "ok", 10)})
	model := provider.GetModel()

	agentDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(agentDir, "auth.json"),
		[]byte(`{"anthropic":{"type":"api_key","key":"test-key"},"openai":{"type":"api_key","key":"test-key"}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	registry, err := config.NewModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}

	available := registry.Available(nil)
	if len(available) < 2 {
		t.Fatalf("expected at least 2 available models with anthropic+openai auth, got %d", len(available))
	}

	testKey := "test-key"
	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn:      provider.StreamSimple,
		Model:         model,
		ModelRegistry: registry,
		GetAPIKey: func(_ context.Context, _ ai.ProviderID) (*string, error) {
			return &testKey, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	initial := result.Session.State().Model
	if initial == nil {
		t.Fatal("expected initial model")
	}

	if _, err := result.Session.CycleModel(context.Background()); err != nil {
		t.Fatal(err)
	}
	cycled := result.Session.State().Model
	if cycled == nil {
		t.Fatal("expected model after cycle")
	}
	if cycled.ID == initial.ID && cycled.Provider == initial.Provider {
		t.Fatal("CycleModel did not change model despite multiple available")
	}
}

func TestNewAgentSessionPersistentSessionManagerFailsOnBadPath(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)

	// Use a nonexistent path that cannot be created.
	_, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		AgentDir: "/dev/null/impossible",
	})
	if err == nil {
		t.Fatal("expected error for bad session dir, got nil")
	}
}

func TestSubscribeChanBarrierRace(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "race", 10)})

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	// Barrier-based regression: ensure cancel during active emission never panics.
	var wg sync.WaitGroup
	ready := make(chan struct{})
	for i := 0; i < 20; i++ {
		ch, cancel := result.Session.SubscribeChan(1)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-ready
			cancel()
		}()
		go func() {
			defer wg.Done()
			<-ready
			for range ch { //nolint:revive
			}
		}()
	}
	close(ready) // barrier — all goroutines start simultaneously
	wg.Wait()
}

func TestSubscribeChanHighIterationRace(t *testing.T) {
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

	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		ch, cancel := result.Session.SubscribeChan(1)
		wg.Add(2)
		go func() {
			defer wg.Done()
			cancel()
		}()
		go func() {
			defer wg.Done()
			for range ch { //nolint:revive
			}
		}()
	}
	wg.Wait()
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

func TestNewAgentSessionCustomToolUsesPerToolSDKSource(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		CustomTools: []extensions.ToolDefinition{
			{Name: "alpha", Description: "alpha tool"},
			{Name: "beta", Description: "beta tool"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	registered := result.Session.ExtensionRunner().AllRegisteredTools()
	if len(registered) != 2 {
		t.Fatalf("registered tools = %d", len(registered))
	}
	for index, path := range []string{"<sdk:alpha>", "<sdk:beta>"} {
		if registered[index].SourceInfo.Path != path || registered[index].SourceInfo.Source != "sdk" {
			t.Fatalf("tool %d source = %#v", index, registered[index].SourceInfo)
		}
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

func TestPreferredAvailableModelProviderOrder(t *testing.T) {
	isolateSDKAgentDir(t)
	// PreferredAvailableModel should pick the first model matching
	// defaultModelProviderOrder, not just index 0.
	anthropicModel := ai.Model{Provider: "anthropic", ID: "claude-opus-4-8", Name: "Claude Opus"}
	openaiModel := ai.Model{Provider: "openai", ID: "gpt-5.5", Name: "GPT 5.5"}
	unknownModel := ai.Model{Provider: "unknown-provider", ID: "whatever", Name: "Whatever"}

	// openai appears first in slice, but anthropic is higher in provider order
	got := PreferredAvailableModel([]ai.Model{openaiModel, unknownModel, anthropicModel})
	if got == nil {
		t.Fatal("expected a model")
	}
	if got.Provider != "anthropic" || got.ID != "claude-opus-4-8" {
		t.Fatalf("expected anthropic/claude-opus-4-8 (higher provider order), got %s/%s", got.Provider, got.ID)
	}

	// Only unknown provider — falls back to index 0
	got = PreferredAvailableModel([]ai.Model{unknownModel})
	if got == nil {
		t.Fatal("expected fallback model")
	}
	if got.Provider != "unknown-provider" {
		t.Fatalf("expected unknown-provider fallback, got %s", got.Provider)
	}

	// Empty slice returns nil
	if PreferredAvailableModel(nil) != nil {
		t.Fatal("expected nil for empty slice")
	}
}

func TestNewAgentSessionSettingsReceivesAgentDir(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "ok", 10)})
	agentDir := t.TempDir()

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		AgentDir: agentDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	// Verify session was created successfully with the custom AgentDir.
	// The Settings manager should have loaded without error (it uses
	// WithAgentDir internally to locate global settings).
	if result.Session == nil {
		t.Fatal("session is nil")
	}
}

func TestNewAgentSessionGetRequestAuthThreaded(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "ok", 10)})

	var called bool
	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		GetRequestAuth: func(_ context.Context, _ ai.ProviderID) (*agent.RequestAuth, error) {
			called = true
			return &agent.RequestAuth{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	if err := result.Session.PromptSync(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("GetRequestAuth was not called during prompt")
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

func TestNewAgentSessionThreadsRuntimeSettingsToProvider(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsJSON := []byte(`{
  "transport": "sse",
  "steeringMode": "all",
  "followUpMode": "all",
  "retry": {"provider": {"timeoutMs": 50, "maxRetries": 2, "maxRetryDelayMs": 75}}
}`)
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), settingsJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("sdk-runtime-settings"))
	if err != nil {
		t.Fatal(err)
	}

	provider := testFaux(100000)
	var received *ai.SimpleStreamOptions
	stream := func(_ context.Context, _ *ai.Model, _ ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		copy := *options
		received = &copy
		response := runtimeAssistant(provider, "ok", 10)
		return func(yield func(ai.AssistantMessageEvent, error) bool) {
			yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: response}, nil)
		}, nil
	}
	result, err := NewAgentSession(AgentSessionOptions{
		AgentDir:       agentDir,
		CWD:            root,
		StreamFn:       stream,
		Model:          provider.GetModel(),
		Settings:       settings,
		SessionManager: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	if result.Session.SteeringMode() != agent.QueueAll || result.Session.FollowUpMode() != agent.QueueAll {
		t.Fatalf("queue modes = %q/%q", result.Session.SteeringMode(), result.Session.FollowUpMode())
	}
	if err := result.Session.PromptSync(context.Background(), "settings"); err != nil {
		t.Fatal(err)
	}
	if received == nil || received.Transport == nil || *received.Transport != ai.TransportSSE {
		t.Fatalf("transport options = %#v", received)
	}
	if received.TimeoutMS == nil || *received.TimeoutMS != 50 || received.MaxRetries == nil || *received.MaxRetries != 2 {
		t.Fatalf("provider retry options = %#v", received)
	}
	if received.MaxRetryDelayMS == nil || *received.MaxRetryDelayMS != 75 {
		t.Fatalf("max retry delay = %#v", received.MaxRetryDelayMS)
	}
	if received.SessionID == nil || *received.SessionID != manager.GetSessionID() {
		t.Fatalf("session id = %#v, want %q", received.SessionID, manager.GetSessionID())
	}
}

func TestNewAgentSessionThreadsShellSettingsToBashTool(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"shellCommandPrefix":"export PI_GO_SDK_PREFIX=threaded"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	result, err := NewAgentSession(AgentSessionOptions{
		AgentDir: agentDir,
		CWD:      root,
		StreamFn: provider.StreamSimple,
		Model:    provider.GetModel(),
		Tools:    []string{"bash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	tool := result.Session.State().Tools[0]
	got, err := tool.Execute(context.Background(), "call", map[string]any{"command": "printf %s \"$PI_GO_SDK_PREFIX\""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Content) != 1 || got.Content[0].(*ai.TextContent).Text != "threaded" {
		t.Fatalf("bash result = %#v", got.Content)
	}
}

func TestDefaultRegistryResolverUsesAuthJSON(t *testing.T) {
	isolateSDKAgentDir(t)
	agentDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(agentDir, "auth.json"),
		[]byte(`{"anthropic":{"type":"api_key","key":"sk-stored-test"}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	resolver := registry.DefaultRequestAuthResolver(nil)
	result, err := resolver(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.APIKey == nil {
		t.Fatal("expected non-nil API key from auth.json")
	}
	if *result.APIKey != "sk-stored-test" {
		t.Fatalf("expected sk-stored-test, got %s", *result.APIKey)
	}
}

func TestDefaultRegistryResolverReturnsNilForUnknown(t *testing.T) {
	isolateSDKAgentDir(t)
	registry, err := config.NewModelRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolver := registry.DefaultRequestAuthResolver(nil)
	result, err := resolver(context.Background(), "unknown-provider-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("expected nil for unknown provider, got %+v", result)
	}
}

func BenchmarkModelRegistryCreation(b *testing.B) {
	dir := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := config.NewModelRegistry(dir)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNewAgentSessionMinimal(b *testing.B) {
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "ok", 10)})
	model := provider.GetModel()
	agentDir := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := NewAgentSession(AgentSessionOptions{
			StreamFn: provider.StreamSimple,
			Model:    model,
			AgentDir: agentDir,
		})
		if err != nil {
			b.Fatal(err)
		}
		result.Session.Dispose()
	}
}
