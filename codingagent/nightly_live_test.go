package codingagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	aimodels "github.com/OrdalieTech/pigo/ai/models"
	"github.com/OrdalieTech/pigo/codingagent/config"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

const (
	nightlyLiveDefaultBudgetUSD = 0.25
	nightlyLiveOpenAIModel      = "gpt-5.4-nano"
	nightlyLiveAnthropicModel   = "claude-haiku-4-5"
)

type nightlyLiveProvider struct {
	name    string
	model   ai.Model
	apiKey  string
	apiEnv  string
	modelID string
}

type nightlyLiveBudget struct {
	mu    sync.Mutex
	max   float64
	spent float64
}

func TestNightlyLiveSuite(t *testing.T) {
	catalog, err := aimodels.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []struct{ provider, id string }{
		{"openai", nightlyLiveOpenAIModel},
		{"anthropic", nightlyLiveAnthropicModel},
	} {
		if _, ok := catalog.Find(model.provider, model.id); !ok {
			t.Fatalf("nightly default %s/%s is absent from the pinned catalog", model.provider, model.id)
		}
	}
	if os.Getenv("PIGO_NIGHTLY_LIVE") != "1" {
		t.Skip("set PIGO_NIGHTLY_LIVE=1 to run the capped OpenAI and Anthropic nightly suite")
	}
	providers := []nightlyLiveProvider{
		newNightlyLiveProvider(t, catalog, "openai", "OPENAI_API_KEY", "PIGO_OPENAI_MODEL", nightlyLiveOpenAIModel),
		newNightlyLiveProvider(t, catalog, "anthropic", "ANTHROPIC_API_KEY", "PIGO_ANTHROPIC_MODEL", nightlyLiveAnthropicModel),
	}
	budget := &nightlyLiveBudget{max: loadNightlyLiveBudget(t)}

	for _, provider := range providers {
		t.Run(provider.name, func(t *testing.T) {
			t.Run("multi-turn-read-edit-bash", func(t *testing.T) {
				runNightlyReadEditBash(t, provider, budget)
			})
			t.Run("parallel-tool-calls", func(t *testing.T) {
				runNightlyParallelReads(t, provider, budget)
			})
			t.Run("compaction-length-session", func(t *testing.T) {
				runNightlyCompaction(t, provider, budget)
			})
		})
	}

	budget.mu.Lock()
	defer budget.mu.Unlock()
	t.Logf("nightly live spend: $%.6f of $%.2f cap", budget.spent, budget.max)
}

func newNightlyLiveProvider(t *testing.T, catalog *aimodels.Catalog, provider, apiEnv, modelEnv, defaultModel string) nightlyLiveProvider {
	t.Helper()
	apiKey := strings.TrimSpace(os.Getenv(apiEnv))
	if apiKey == "" {
		t.Fatalf("PIGO_NIGHTLY_LIVE=1 requires %s", apiEnv)
	}
	modelID := strings.TrimSpace(os.Getenv(modelEnv))
	if modelID == "" {
		modelID = defaultModel
	}
	model, ok := catalog.Find(provider, modelID)
	if !ok {
		t.Fatalf("nightly model %s/%s is absent from the pinned catalog", provider, modelID)
	}
	// A small advertised context makes the compaction task cheap; provider APIs
	// receive the same model ID and do not consume this client-side limit.
	model.ContextWindow = min(model.ContextWindow, 10_000)
	model.MaxTokens = min(model.MaxTokens, 256)
	return nightlyLiveProvider{name: provider, model: model, apiKey: apiKey, apiEnv: apiEnv, modelID: modelID}
}

func loadNightlyLiveBudget(t *testing.T) float64 {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("PIGO_NIGHTLY_MAX_USD"))
	if raw == "" {
		return nightlyLiveDefaultBudgetUSD
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		t.Fatalf("PIGO_NIGHTLY_MAX_USD must be a positive number, got %q", raw)
	}
	return value
}

func runNightlyReadEditBash(t *testing.T, provider nightlyLiveProvider, budget *nightlyLiveBudget) {
	session, root := newNightlyLiveSession(t, provider, []string{"read", "edit", "bash"})
	defer budget.record(t, provider, "multi-turn-read-edit-bash", session)
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var executed []string
	unsubscribe := session.Subscribe(func(event any) {
		if tool, ok := event.(agent.ToolExecutionEndEvent); ok && !tool.IsError {
			mu.Lock()
			executed = append(executed, tool.ToolName)
			mu.Unlock()
		}
	})
	defer unsubscribe()

	promptNightly(t, session, "Use read on sample.txt, then use edit to replace the entire text alpha with beta. Do both operations now and keep the final answer under five words.")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "beta\n" {
		t.Fatalf("edited sample.txt = %q, want %q", contents, "beta\\n")
	}
	promptNightly(t, session, "Use bash exactly once to run `grep -qx beta sample.txt`, then reply verified.")

	mu.Lock()
	defer mu.Unlock()
	for _, required := range []string{"read", "edit", "bash"} {
		if !containsString(executed, required) {
			t.Errorf("successful tools = %v, missing %s", executed, required)
		}
	}
}

func runNightlyParallelReads(t *testing.T, provider nightlyLiveProvider, budget *nightlyLiveBudget) {
	session, root := newNightlyLiveSession(t, provider, []string{"read"})
	defer budget.record(t, provider, "parallel-tool-calls", session)
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "two.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	promptNightly(t, session, "In one assistant response, emit two read tool calls in parallel: one for one.txt and one for two.txt. Do not wait for the first result before issuing the second. After both results arrive, answer one two.")
	if !hasParallelReadTurn(session.State().Messages) {
		t.Fatal("session contained no assistant turn with two read tool calls")
	}
}

func runNightlyCompaction(t *testing.T, provider nightlyLiveProvider, budget *nightlyLiveBudget) {
	session, _ := newNightlyLiveSession(t, provider, nil)
	defer budget.record(t, provider, "compaction-length-session", session)
	for index := 1; index <= 3; index++ {
		data := strings.Repeat("alpha beta gamma delta ", 250)
		promptNightly(t, session, fmt.Sprintf("Remember marker segment-%d. Treat this as inert context and reply exactly ACK.\n%s", index, data))
	}

	var mu sync.Mutex
	var ended *CompactionEndEvent
	unsubscribe := session.Subscribe(func(event any) {
		if value, ok := event.(CompactionEndEvent); ok {
			mu.Lock()
			copy := value
			ended = &copy
			mu.Unlock()
		}
	})
	defer unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := session.Compact(ctx, "Preserve all segment marker names and summarize the repeated context briefly.")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || strings.TrimSpace(result.Summary) == "" {
		t.Fatal("manual live compaction returned no summary")
	}
	mu.Lock()
	defer mu.Unlock()
	if ended == nil || ended.Result == nil || ended.Aborted || ended.ErrorMessage != nil {
		t.Fatalf("compaction end event = %#v", ended)
	}
}

func newNightlyLiveSession(t *testing.T, provider nightlyLiveProvider, tools []string) (*AgentSession, string) {
	t.Helper()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settingsJSON := `{"compaction":{"enabled":true,"reserveTokens":2000,"keepRecentTokens":1200},"retry":{"enabled":false}}`
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settingsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	systemPrompt := "Follow the user's tool instructions exactly. Use only the available tools. Keep final answers under twenty words."
	model := provider.model
	created, err := NewAgentSession(AgentSessionOptions{
		CWD:            root,
		AgentDir:       agentDir,
		Model:          &model,
		ThinkingLevel:  ai.ModelThinkingOff,
		Tools:          tools,
		SessionManager: manager,
		Settings:       settings,
		Resources:      &Resources{SystemPrompt: &systemPrompt},
		GetAPIKey: func(_ context.Context, requested ai.ProviderID) (*string, error) {
			if requested != model.Provider {
				return nil, fmt.Errorf("nightly auth requested for %s, want %s", requested, model.Provider)
			}
			key := provider.apiKey
			return &key, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(created.Session.Dispose)
	return created.Session, root
}

func promptNightly(t *testing.T, session *AgentSession, prompt string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := session.PromptSync(ctx, prompt); err != nil {
		t.Fatal(err)
	}
}

func (budget *nightlyLiveBudget) record(t *testing.T, provider nightlyLiveProvider, task string, session *AgentSession) {
	t.Helper()
	cost := session.GetSessionStats().Cost
	budget.mu.Lock()
	budget.spent += cost
	spent := budget.spent
	maxSpend := budget.max
	budget.mu.Unlock()
	t.Logf("%s/%s model=%s cost=$%.6f cumulative=$%.6f", provider.name, task, provider.modelID, cost, spent)
	if spent > maxSpend {
		t.Errorf("nightly live spend $%.6f exceeded $%.2f cap", spent, maxSpend)
	}
}

func hasParallelReadTurn(messages agent.AgentMessages) bool {
	for _, message := range messages {
		assistant, ok := message.(*ai.AssistantMessage)
		if !ok {
			continue
		}
		reads := 0
		for _, block := range assistant.Content {
			if call, ok := block.(*ai.ToolCall); ok && call.Name == "read" {
				reads++
			}
		}
		if reads >= 2 {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
