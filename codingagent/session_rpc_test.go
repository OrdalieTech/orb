package codingagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent/config"
	modetheme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

func TestPromptPreflightRejectsUnknownModelSentinel(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	if err := runtime.PromptPreflight(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "No model selected.") {
		t.Fatalf("unknown-model preflight error = %v", err)
	}
}

func TestExportHTMLPrefersConfiguredCustomThemeOverCurrent(t *testing.T) {
	root, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	settings.SetTheme("configured-custom")
	source, err := os.ReadFile(filepath.Join("modes", "theme", "dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	custom := strings.Replace(string(source), `"name": "dark"`, `"name": "configured-custom"`, 1)
	custom = strings.Replace(custom, `"pageBg": "#18181e"`, `"pageBg": "#123456"`, 1)
	themePath := filepath.Join(root, "configured-custom.json")
	if err := os.WriteFile(themePath, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	loader, err := NewDefaultResourceLoader(DefaultResourceLoaderOptions{
		CWD: root, AgentDir: agentDir, SettingsManager: settings, NoThemes: true,
		AdditionalThemePaths: []string{themePath}, NoSkills: true, NoPromptTemplates: true, NoContextFiles: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	registry := modetheme.Load(modetheme.LoadOptions{CWD: root, AgentDir: agentDir, NoThemes: true, Mode: modetheme.TrueColor})
	dark, found := registry.Get("dark")
	if !found {
		t.Fatal("dark theme is missing")
	}
	previous := modetheme.Current()
	modetheme.SetCurrent(dark)
	t.Cleanup(func() { modetheme.SetCurrent(previous) })

	provider := testFaux(100_000)
	manager, err := sessionstore.Create(root, filepath.Join(root, "sessions"), sessionstore.WithSessionID("configured-theme-export"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(userMessage("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(runtimeAssistant(provider, "answer", 1)); err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{
		Model: provider.GetModel(), SystemPrompt: "test", Messages: agent.AgentMessages{}, Tools: []agent.AgentTool{},
	}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, ResourceLoader: loader,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	output := filepath.Join(root, "session.html")
	if _, err := runtime.ExportHTML(output); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "--exportPageBg: #123456;") {
		t.Fatalf("configured custom theme did not win over current dark theme")
	}
}

func TestSessionUsageStatsAndCostBreakdownIncludeAuxiliaryCalls(t *testing.T) {
	provider := faux.New()
	runtime, manager := newTestRuntime(t, provider, nil)
	defer runtime.Dispose()
	root, err := manager.AppendMessage(userMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	responseModel := "actual-model"
	assistant := runtimeAssistant(provider, "answer", 100)
	assistant.ResponseModel = &responseModel
	assistant.Usage.Cost.Total = 0.5
	if _, err := manager.AppendMessage(assistant); err != nil {
		t.Fatal(err)
	}
	usage := func(cost float64) *ai.Usage {
		return &ai.Usage{Input: 100, TotalTokens: 100, Cost: ai.Cost{Total: cost}}
	}
	if _, err := manager.AppendMessage(&ai.ToolResultMessage{ToolCallID: "call", ToolName: "nested", Content: ai.ToolResultContent{}, Usage: usage(1), Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendCompaction("summary", root, 100, sessionstore.OptionalEntryFields{Usage: usage(2)}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BranchWithSummary(nil, "branch", sessionstore.OptionalEntryFields{Usage: usage(3)}); err != nil {
		t.Fatal(err)
	}

	stats := runtime.GetSessionStats()
	if stats.Tokens.Input != 400 || stats.Tokens.Total != 400 || stats.Cost != 6.5 || stats.AssistantMessages != 1 || stats.ToolResults != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	want := []UsageCostBreakdownEntry{{Key: "Tools/summaries", Cost: 6, Tokens: 300}, {Key: string(assistant.Provider) + "/" + responseModel, Cost: 0.5, Tokens: 100}}
	if got := GetUsageCostBreakdown(manager.GetEntries()); !reflect.DeepEqual(got, want) {
		t.Fatalf("breakdown = %#v, want %#v", got, want)
	}

	message := func(provider string) json.RawMessage {
		encoded, err := ai.MarshalMessage(&ai.AssistantMessage{Provider: ai.ProviderID(provider), Model: "model", Usage: *usage(1)})
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	ties := GetUsageCostBreakdown([]sessionstore.SessionEntry{{Type: "message", Message: message("first")}, {Type: "message", Message: message("second")}})
	if len(ties) != 2 || ties[0].Key != "first/model" || ties[1].Key != "second/model" {
		t.Fatalf("stable ties = %#v", ties)
	}
}

func TestCycleModelUsesAuthenticatedScopeAndScopedThinking(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("scoped-models"))
	if err != nil {
		t.Fatal(err)
	}
	modelA := rpcTestModel("provider-a", "a")
	modelB := rpcTestModel("provider-b", "b")
	modelC := rpcTestModel("provider-c", "c")
	high := ai.ModelThinkingHigh
	keys := map[ai.ProviderID]bool{
		modelA.Provider: true,
		modelB.Provider: true,
		modelC.Provider: true,
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{
		Model: &modelA, ThinkingLevel: ai.ModelThinkingLow, Messages: agent.AgentMessages{},
	}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		GetAPIKey: func(_ context.Context, provider ai.ProviderID) (*string, error) {
			if !keys[provider] {
				return nil, nil
			}
			key := "key"
			return &key, nil
		},
		ScopedModels: []ScopedModel{
			{Model: modelA},
			{Model: modelB, ThinkingLevel: &high},
			{Model: modelC},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	result, err := runtime.CycleModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsScoped || result.Model.ID != "b" || result.ThinkingLevel != ai.ModelThinkingHigh {
		t.Fatalf("first scoped cycle = %#v", result)
	}
	result, err = runtime.CycleModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Model.ID != "c" || result.ThinkingLevel != ai.ModelThinkingHigh {
		t.Fatalf("inherited scoped cycle = %#v", result)
	}

	outside := rpcTestModel("provider-d", "outside")
	created.SetModel(&outside)
	result, err = runtime.CycleModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Model.ID != "b" {
		t.Fatalf("out-of-scope cycle = %#v, want second scoped model", result)
	}
	keys[modelB.Provider] = false
	result, err = runtime.CycleModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Model.ID != "c" {
		t.Fatalf("auth-filtered cycle = %#v", result)
	}
}

func TestCycleModelBackwardWrapsScopeAndFiltersAuth(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("scoped-models-backward"))
	if err != nil {
		t.Fatal(err)
	}
	modelA := rpcTestModel("provider-a", "a")
	modelB := rpcTestModel("provider-b", "b")
	modelC := rpcTestModel("provider-c", "c")
	high := ai.ModelThinkingHigh
	keys := map[ai.ProviderID]bool{
		modelA.Provider: true,
		modelB.Provider: true,
		modelC.Provider: true,
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{
		Model: &modelA, ThinkingLevel: ai.ModelThinkingLow, Messages: agent.AgentMessages{},
	}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		GetAPIKey: func(_ context.Context, provider ai.ProviderID) (*string, error) {
			if !keys[provider] {
				return nil, nil
			}
			key := "key"
			return &key, nil
		},
		ScopedModels: []ScopedModel{
			{Model: modelA},
			{Model: modelB, ThinkingLevel: &high},
			{Model: modelC},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	result, err := runtime.CycleModelBackward(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsScoped || result.Model.ID != "c" {
		t.Fatalf("backward wraparound cycle = %#v, want last scoped model", result)
	}
	result, err = runtime.CycleModelBackward(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Model.ID != "b" || result.ThinkingLevel != ai.ModelThinkingHigh {
		t.Fatalf("backward scoped-thinking cycle = %#v", result)
	}

	keys[modelA.Provider] = false
	result, err = runtime.CycleModelBackward(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Model.ID != "c" {
		t.Fatalf("auth-filtered backward cycle = %#v, want previous authenticated model", result)
	}
}

func TestCycleModelBackwardFromAbsentCurrentModelUsesIndexZero(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("absent-current-backward"))
	if err != nil {
		t.Fatal(err)
	}
	modelA := rpcTestModel("provider-a", "a")
	modelB := rpcTestModel("provider-b", "b")
	modelC := rpcTestModel("provider-c", "c")
	outside := rpcTestModel("provider-d", "outside")
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{
		Model: &outside, ThinkingLevel: ai.ModelThinkingLow, Messages: agent.AgentMessages{},
	}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			key := "key"
			return &key, nil
		},
		ScopedModels: []ScopedModel{{Model: modelA}, {Model: modelB}, {Model: modelC}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	result, err := runtime.CycleModelBackward(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Model.ID != "c" {
		t.Fatalf("backward from absent current = %#v, want (0-1+len)%%len wraparound", result)
	}
}

func TestCycleModelPreservesFullModelFields(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("model-field-preservation"))
	if err != nil {
		t.Fatal(err)
	}
	modelA := rpcTestModel("provider-a", "a")
	headers := map[string]string{"X-Custom": "yes"}
	low := "low-mapped"
	levelMap := map[ai.ModelThinkingLevel]*string{ai.ModelThinkingLow: &low, ai.ModelThinkingXHigh: nil}
	rich := rpcTestModel("provider-b", "rich")
	rich.Name = "Rich Model"
	rich.BaseURL = "https://example.test/v1"
	rich.Headers = &headers
	rich.ThinkingLevelMap = &levelMap
	rich.Input = ai.InputModalities{ai.InputText, ai.InputImage}
	rich.Cost = ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1.25, Output: 6.5, CacheRead: 0.5, CacheWrite: 2}}
	rich.Compat = json.RawMessage(`{"supportsStore":false}`)
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{
		Model: &modelA, ThinkingLevel: ai.ModelThinkingLow, Messages: agent.AgentMessages{},
	}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			key := "key"
			return &key, nil
		},
		ScopedModels: []ScopedModel{{Model: modelA}, {Model: rich}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	result, err := runtime.CycleModelBackward(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !reflect.DeepEqual(result.Model, rich) {
		t.Fatalf("cycled model = %#v, want full field preservation of %#v", result, rich)
	}
	state := runtime.agent.State()
	if state.Model == nil || !reflect.DeepEqual(*state.Model, rich) {
		t.Fatalf("agent model = %#v, want full field preservation", state.Model)
	}
}

func TestCycleModelReportsUnscopedCatalogCycle(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("available-models"))
	if err != nil {
		t.Fatal(err)
	}
	modelA := rpcTestModel("provider-a", "a")
	modelB := rpcTestModel("provider-b", "b")
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{
		Model: &modelA, ThinkingLevel: ai.ModelThinkingLow, Messages: agent.AgentMessages{},
	}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		AvailableModels: func() []ai.Model { return []ai.Model{modelA, modelB} },
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			key := "key"
			return &key, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	result, err := runtime.CycleModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.IsScoped || result.Model.ID != "b" {
		t.Fatalf("available-model cycle = %#v", result)
	}
}

func TestAvailableModelsUsesEmptyArrayShape(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("empty-models"))
	if err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	models := runtime.AvailableModels()
	if models == nil || len(models) != 0 {
		t.Fatalf("available models = %#v, want non-nil empty slice", models)
	}
}

func TestSetThinkingLevelWithUnknownModelMatchesUpstream(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("model-less-thinking"))
	if err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{ThinkingLevel: ai.ModelThinkingOff, Messages: agent.AgentMessages{}}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	if err := runtime.SetThinkingLevel(ai.ModelThinkingHigh); err != nil {
		t.Fatal(err)
	}
	if runtime.agent.State().ThinkingLevel != ai.ModelThinkingOff || settings.GetDefaultThinkingLevel() != "" {
		t.Fatalf("thinking = %q, default = %q", runtime.agent.State().ThinkingLevel, settings.GetDefaultThinkingLevel())
	}
	if levels := runtime.AvailableThinkingLevels(); !reflect.DeepEqual(levels, []ai.ModelThinkingLevel{ai.ModelThinkingOff}) {
		t.Fatalf("available thinking levels = %#v", levels)
	}
	if entries := manager.GetEntries(); len(entries) != 0 {
		t.Fatalf("unchanged thinking level persisted %#v", entries)
	}
}

func TestLastAssistantTextUsesECMAScriptTrim(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("assistant-text-trim"))
	if err != nil {
		t.Fatal(err)
	}
	message := &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "\u0085"}}, StopReason: ai.StopReasonStop}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{message}}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	text := runtime.GetLastAssistantText()
	if text == nil || *text != "\u0085" {
		t.Fatalf("assistant text = %#v", text)
	}
}

func TestManualCompactionWithoutModelUsesAuthGuidance(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("model-less-compaction"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(userMessage("work")); err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{userMessage("work")}}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	_, err = runtime.Compact(context.Background(), "")
	if err == nil || err.Error() != noModelSelectedError().Error() {
		t.Fatalf("compaction error = %v", err)
	}
}

func TestPromptPreflightCompactsAbortedHighUsageResponse(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"compaction":{"enabled":true,"reserveTokens":10,"keepRecentTokens":1},"retry":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("pre-prompt-compaction"))
	if err != nil {
		t.Fatal(err)
	}
	model := rpcTestModel("faux", "small")
	model.ContextWindow = 100
	messages := agent.AgentMessages{
		userMessage("old request"),
		&ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "old answer"}}, API: model.API, Provider: model.Provider, Model: model.ID, StopReason: ai.StopReasonStop, Usage: ai.Usage{TotalTokens: 40}, Timestamp: 1},
		userMessage("recent request"),
		&ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "partial"}}, API: model.API, Provider: model.Provider, Model: model.ID, StopReason: ai.StopReasonAborted, Usage: ai.Usage{TotalTokens: 95}, Timestamp: 2},
	}
	for _, message := range messages {
		if _, err := manager.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{Model: &model, Messages: messages}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			key := "key"
			return &key, nil
		},
		Complete: func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
			return &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "summary"}}, StopReason: ai.StopReasonStop}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	var events []any
	runtime.Subscribe(func(event any) { events = append(events, event) })

	if err := runtime.PromptPreflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	branch := manager.GetBranch()
	if len(branch) == 0 || branch[len(branch)-1].Type != "compaction" {
		t.Fatalf("branch = %#v", branch)
	}
	if len(events) != 2 {
		t.Fatalf("compaction events = %#v", events)
	}
	if _, ok := events[0].(CompactionStartEvent); !ok {
		t.Fatalf("first event = %T", events[0])
	}
	if _, ok := events[1].(CompactionEndEvent); !ok {
		t.Fatalf("second event = %T", events[1])
	}
}

func TestRPCTogglesKeepProjectOverridesEffective(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, config.ConfigDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, config.ConfigDirName, "settings.json"), []byte(`{"compaction":{"enabled":false},"retry":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("project-policy"))
	if err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	runtime.SetAutoCompactionEnabled(true)
	runtime.SetAutoRetryEnabled(true)
	if runtime.AutoCompactionEnabled() || runtime.AutoRetryEnabled() {
		t.Fatalf("effective policies = compaction %t, retry %t", runtime.AutoCompactionEnabled(), runtime.AutoRetryEnabled())
	}
}

func rpcTestModel(provider, id string) ai.Model {
	return ai.Model{
		ID: id, Name: id, API: ai.API("faux"), Provider: ai.ProviderID(provider),
		Reasoning: true, Input: ai.InputModalities{ai.InputText}, ContextWindow: 128_000, MaxTokens: 16_384,
	}
}
