package config

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
	aimodels "github.com/OrdalieTech/pi-go/ai/models"
)

type ModelRegistry struct {
	agentDir string
	config   *ModelConfig
	all      []ai.Model
	errors   []string
}

func NewModelRegistry(agentDir string) (*ModelRegistry, error) {
	normalized, err := NormalizePath(agentDir)
	if err != nil {
		return nil, err
	}
	registry := &ModelRegistry{agentDir: normalized}
	if err := registry.Reload(); err != nil {
		return nil, err
	}
	return registry, nil
}

// Reload re-reads both the dynamic model cache and models.json.
func (registry *ModelRegistry) Reload() error {
	builtin, err := aimodels.Builtin()
	if err != nil {
		return err
	}
	stored, err := aimodels.LoadStore(filepath.Join(registry.agentDir, "models-store.json"))
	if err != nil {
		return err
	}
	config, err := LoadModelConfig(filepath.Join(registry.agentDir, "models.json"))
	if err != nil {
		return err
	}
	all := builtin.Merge(stored).Models()
	errors := make([]string, 0)
	if config.Error() != "" {
		errors = append(errors, config.Error())
	}
	for _, providerID := range config.providerIDs() {
		partial := &ModelConfig{Providers: map[string]ModelProviderConfig{providerID: config.Providers[providerID]}}
		updated, applyErr := ApplyModelConfig(all, partial)
		if applyErr != nil {
			errors = append(errors, `Provider "`+providerID+`": `+applyErr.Error())
			continue
		}
		all = updated
	}
	registry.config, registry.all, registry.errors = config, all, errors
	return nil
}

func (registry *ModelRegistry) Error() string { return strings.Join(registry.errors, "\n\n") }

func (registry *ModelRegistry) Models() []ai.Model {
	return append([]ai.Model(nil), registry.all...)
}

func (registry *ModelRegistry) Find(provider, id string) (ai.Model, bool) {
	index := slices.IndexFunc(registry.all, func(model ai.Model) bool {
		return model.ID == id && string(model.Provider) == provider
	})
	if index < 0 {
		return ai.Model{}, false
	}
	return registry.all[index], true
}

func (registry *ModelRegistry) HasConfiguredAuth(provider string, env map[string]string) bool {
	if registry.config.HasConfiguredAPIKey(provider, env) {
		return true
	}
	for _, name := range providerAPIKeyEnv[provider] {
		if env[name] != "" || lookupNonEmptyEnv(name) {
			return true
		}
	}
	return false
}

func (registry *ModelRegistry) Available(env map[string]string) []ai.Model {
	result := make([]ai.Model, 0, len(registry.all))
	for _, model := range registry.all {
		if registry.HasConfiguredAuth(string(model.Provider), env) {
			result = append(result, model)
		}
	}
	return result
}

func (registry *ModelRegistry) ResolveAPIKey(ctx context.Context, provider string, env map[string]string) (*string, error) {
	key, err := registry.config.ResolveAPIKey(ctx, provider, env)
	if err != nil || key != nil {
		return key, err
	}
	for _, name := range providerAPIKeyEnv[provider] {
		if value := env[name]; value != "" {
			return &value, nil
		}
		if value := getenv(name); value != "" {
			return &value, nil
		}
	}
	return nil, nil
}

func (registry *ModelRegistry) ResolveModelHeaders(ctx context.Context, model ai.Model, env map[string]string, apiKeys ...*string) (*map[string]string, error) {
	return registry.config.ResolveModelHeaders(ctx, model, env, apiKeys...)
}

var providerAPIKeyEnv = map[string][]string{
	"amazon-bedrock":         {"AWS_BEARER_TOKEN_BEDROCK", "AWS_ACCESS_KEY_ID"},
	"anthropic":              {"ANTHROPIC_API_KEY"},
	"azure-openai-responses": {"AZURE_OPENAI_API_KEY"},
	"cerebras":               {"CEREBRAS_API_KEY"},
	"cloudflare-ai-gateway":  {"CLOUDFLARE_API_TOKEN"},
	"cloudflare-workers-ai":  {"CLOUDFLARE_API_TOKEN"},
	"deepseek":               {"DEEPSEEK_API_KEY"},
	"fireworks":              {"FIREWORKS_API_KEY"},
	"google":                 {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
	"google-vertex":          {"GOOGLE_APPLICATION_CREDENTIALS"},
	"groq":                   {"GROQ_API_KEY"},
	"huggingface":            {"HUGGINGFACE_API_KEY", "HF_TOKEN"},
	"kimi-coding":            {"KIMI_API_KEY"},
	"minimax":                {"MINIMAX_API_KEY"},
	"minimax-cn":             {"MINIMAX_API_KEY"},
	"mistral":                {"MISTRAL_API_KEY"},
	"moonshotai":             {"MOONSHOT_API_KEY"},
	"moonshotai-cn":          {"MOONSHOT_API_KEY"},
	"nvidia":                 {"NVIDIA_API_KEY"},
	"openai":                 {"OPENAI_API_KEY"},
	"opencode":               {"OPENCODE_API_KEY"},
	"opencode-go":            {"OPENCODE_API_KEY"},
	"openrouter":             {"OPENROUTER_API_KEY"},
	"together":               {"TOGETHER_API_KEY"},
	"vercel-ai-gateway":      {"AI_GATEWAY_API_KEY"},
	"xai":                    {"XAI_API_KEY"},
	"xiaomi":                 {"XIAOMI_API_KEY"},
	"xiaomi-token-plan-ams":  {"XIAOMI_API_KEY"},
	"xiaomi-token-plan-cn":   {"XIAOMI_API_KEY"},
	"xiaomi-token-plan-sgp":  {"XIAOMI_API_KEY"},
	"zai":                    {"ZAI_API_KEY"},
	"zai-coding-cn":          {"ZAI_API_KEY"},
}

var getenv = func(name string) string {
	return strings.TrimSpace(environmentValue(name))
}

func lookupNonEmptyEnv(name string) bool { return getenv(name) != "" }
