// Package browser assembles Orb's engine with an isolated, in-memory workspace.
// It has no JavaScript, DOM, terminal, native storage, or process-host dependency.
package browser

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/api"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/platforms/memory"
)

const (
	workspaceRoot     = "/workspace"
	maxWorkspaceBytes = 1 << 20
	maxWorkspaceFiles = 128
)

type Config struct {
	API     string `json:"api"`
	Model   string `json:"model"`
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
}

type Session struct {
	Agent     *engine.Agent
	Workspace *memory.FileSystem
}

// Providers is the light registry the Wasm runtime links: OpenAI Completions
// (which OpenRouter also rides) and Anthropic Messages.
func Providers() *api.Registry {
	return api.NewRegistry(api.OpenAICompletions(), api.AnthropicMessages())
}

// providerAPIs maps a Config.API choice to its provider id and wire API.
var providerAPIs = map[string]struct {
	provider ai.ProviderID
	api      ai.API
}{
	"openrouter":                    {"openrouter", ai.APIOpenAICompletions},
	string(ai.APIOpenAICompletions): {"openai", ai.APIOpenAICompletions},
	string(ai.APIAnthropicMessages): {"anthropic", ai.APIAnthropicMessages},
}

func New(config Config) (*Session, error) {
	selected, ok := providerAPIs[config.API]
	if !ok {
		return nil, fmt.Errorf("unsupported Wasm provider %q", config.API)
	}
	endpoint, err := url.Parse(config.BaseURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("provide an HTTP(S) API base URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("provide a model ID")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("provide an API key; browser sessions cannot use terminal credentials")
	}
	model := &ai.Model{ID: config.Model, Name: config.Model, API: selected.api, Provider: selected.provider,
		BaseURL: config.BaseURL, Input: ai.InputModalities{ai.InputText}, ContextWindow: 32768, MaxTokens: 4096}
	workspace := memory.New(memory.Options{Root: workspaceRoot, MaxBytes: maxWorkspaceBytes, MaxFiles: maxWorkspaceFiles})
	operations := tools.FileSystemToolsOptions(workspace)
	maxTokens, retries := float64(4096), 0
	agent := engine.NewAgent(Providers().StreamSimple,
		engine.WithInitialState(engine.AgentState{
			Model: model, ThinkingLevel: engine.ThinkingOff,
			SystemPrompt: "You are Orb running in a Wasm sandbox. Use read, write and edit on the in-memory /workspace. There is no shell or access to the user's disk. Files are lost when this debug session is reset.",
			Tools: []engine.AgentTool{
				tools.NewReadTool(workspaceRoot, operations.Read),
				tools.NewWriteTool(workspaceRoot, operations.Write),
				tools.NewEditTool(workspaceRoot, operations.Edit),
			},
		}),
		engine.WithSimpleStreamOptions(ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
			APIKey: &config.APIKey, MaxTokens: &maxTokens, MaxRetries: &retries, Env: ai.ProviderEnv{},
		}}),
	)
	return &Session{Agent: agent, Workspace: workspace}, nil
}
