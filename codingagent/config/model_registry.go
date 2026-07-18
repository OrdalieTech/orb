package config

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/ai/auth/oauth"
	aimodels "github.com/OrdalieTech/pi-go/ai/models"
	"github.com/OrdalieTech/pi-go/ai/providers"
)

type ModelRegistry struct {
	agentDir      string
	config        *ModelConfig
	all           []ai.Model
	errors        []string
	authProviders map[string]*aiauth.Credential
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
	authProviders := readStoredCredentials(filepath.Join(registry.agentDir, "auth.json"))
	all = filterCredentialModels(all, authProviders)
	registry.config, registry.all, registry.errors, registry.authProviders = config, all, errors, authProviders
	return nil
}

func filterCredentialModels(models []ai.Model, credentials map[string]*aiauth.Credential) []ai.Model {
	availableIDs, filter := oauth.CopilotAvailableModelIDs(credentials["github-copilot"])
	if !filter {
		return models
	}
	available := make(map[string]struct{}, len(availableIDs))
	for _, id := range availableIDs {
		available[id] = struct{}{}
	}
	result := make([]ai.Model, 0, len(models))
	for _, model := range models {
		if model.Provider == "github-copilot" {
			if _, ok := available[model.ID]; !ok {
				continue
			}
		}
		result = append(result, model)
	}
	return result
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
	authContext := registryAuthContext{env: env}
	if storedCredential, stored := registry.authProviders[provider]; stored {
		credential := resolveStoredCredential(storedCredential)
		if definition, known := providers.Get(ai.ProviderID(provider)); known {
			switch credential.Type {
			case aiauth.CredentialAPIKey:
				if definition.Methods.APIKey == nil {
					return false
				}
				result, err := definition.Methods.APIKey.Resolve(context.Background(), authContext, credential)
				return err == nil && result != nil
			case aiauth.CredentialOAuth:
				return definition.Methods.OAuth != nil
			}
			return false
		}
		return credential.Type == aiauth.CredentialAPIKey && credential.Key != nil && *credential.Key != ""
	}
	if registry.config.HasConfiguredAPIKey(provider, env) {
		return true
	}
	if definition, known := providers.Get(ai.ProviderID(provider)); known && definition.Methods.APIKey != nil {
		result, err := definition.Methods.APIKey.Resolve(context.Background(), authContext, nil)
		return err == nil && result != nil
	}
	for _, name := range providerAPIKeyEnv[provider] {
		if env[name] != "" || lookupNonEmptyEnv(name) {
			return true
		}
	}
	return false
}

type registryAuthContext struct{ env map[string]string }

func (authContext registryAuthContext) Env(ctx context.Context, name string) (string, bool) {
	if value := authContext.env[name]; value != "" {
		return value, true
	}
	return (aiauth.EnvironmentContext{}).Env(ctx, name)
}

func (authContext registryAuthContext) FileExists(ctx context.Context, path string) bool {
	return (aiauth.EnvironmentContext{}).FileExists(ctx, path)
}

// ResolveConfiguredAPIKey resolves only a models.json provider override. Stored
// credentials and ambient provider sources are handled by the auth layer.
func (registry *ModelRegistry) ResolveConfiguredAPIKey(ctx context.Context, provider string, env map[string]string) (*string, error) {
	return registry.config.ResolveAPIKey(ctx, provider, env)
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
	"anthropic":              {"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
	"azure-openai-responses": {"AZURE_OPENAI_API_KEY"},
	"cerebras":               {"CEREBRAS_API_KEY"},
	"cloudflare-ai-gateway":  {"CLOUDFLARE_API_TOKEN"},
	"cloudflare-workers-ai":  {"CLOUDFLARE_API_TOKEN"},
	"deepseek":               {"DEEPSEEK_API_KEY"},
	"fireworks":              {"FIREWORKS_API_KEY"},
	"google":                 {"GEMINI_API_KEY"},
	"google-vertex":          {"GOOGLE_CLOUD_API_KEY"},
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
