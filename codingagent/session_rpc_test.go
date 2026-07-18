package codingagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

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

func TestSetThinkingLevelWithoutModelMatchesUpstream(t *testing.T) {
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
	if runtime.agent.State().ThinkingLevel != ai.ModelThinkingHigh || settings.GetDefaultThinkingLevel() != ai.ModelThinkingHigh {
		t.Fatalf("thinking = %q, default = %q", runtime.agent.State().ThinkingLevel, settings.GetDefaultThinkingLevel())
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
