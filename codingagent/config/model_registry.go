package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/ai"
	aiapi "github.com/OrdalieTech/pigo/ai/api"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
	aimodels "github.com/OrdalieTech/pigo/ai/models"
	"github.com/OrdalieTech/pigo/ai/providers"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

type ModelRegistry struct {
	reloadMu sync.Mutex
	opMu     sync.Mutex
	mu       sync.RWMutex

	agentDir      string
	config        *ModelConfig
	base          []ai.Model
	all           []ai.Model
	errors        []string
	authProviders map[string]*aiauth.Credential

	providerConfigs     map[string]extensions.ProviderConfig
	nativeProviders     map[string]extensions.Provider
	configOrder         []string
	nativeOrder         []string
	providerVersions    map[string]uint64
	nextProviderVersion uint64
	allowModelNetwork   bool
	revision            uint64
}

func NewModelRegistry(agentDir string) (*ModelRegistry, error) {
	normalized, err := NormalizePath(agentDir)
	if err != nil {
		return nil, err
	}
	_, offline := os.LookupEnv("PI_OFFLINE")
	registry := &ModelRegistry{
		agentDir: normalized, providerConfigs: make(map[string]extensions.ProviderConfig),
		nativeProviders:   make(map[string]extensions.Provider),
		providerVersions:  make(map[string]uint64),
		allowModelNetwork: !offline,
	}
	if err := registry.Reload(); err != nil {
		return nil, err
	}
	return registry, nil
}

// Reload re-reads both the dynamic model cache and models.json.
func (registry *ModelRegistry) Reload() error {
	registry.reloadMu.Lock()
	defer registry.reloadMu.Unlock()
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
	base := builtin.Merge(stored).Models()
	authProviders := cloneCredentials(readStoredCredentials(filepath.Join(registry.agentDir, "auth.json")))
	return registry.refreshSnapshot(base, config, authProviders)
}

// RefreshAuth re-reads credentials without reloading models.json or the model
// store. Login and logout use this path so unrelated config edits remain behind
// the explicit reload boundary.
func (registry *ModelRegistry) RefreshAuth() error {
	registry.reloadMu.Lock()
	defer registry.reloadMu.Unlock()
	registry.mu.RLock()
	base := append([]ai.Model(nil), registry.base...)
	config := registry.config
	registry.mu.RUnlock()
	authProviders := cloneCredentials(readStoredCredentials(filepath.Join(registry.agentDir, "auth.json")))
	return registry.refreshSnapshot(base, config, authProviders)
}

func (registry *ModelRegistry) refreshSnapshot(base []ai.Model, config *ModelConfig, authProviders map[string]*aiauth.Credential) error {
	registry.opMu.Lock()
	registry.mu.RLock()
	providerConfigs := cloneProviderConfigs(registry.providerConfigs)
	nativeProviders := cloneNativeProviders(registry.nativeProviders)
	configOrder := append([]string(nil), registry.configOrder...)
	nativeOrder := append([]string(nil), registry.nativeOrder...)
	providerVersions := cloneUint64Map(registry.providerVersions)
	revision := registry.revision
	registry.mu.RUnlock()
	registry.opMu.Unlock()
	var refreshErr error
	providerConfigs, refreshErr = registry.refreshProviderSnapshots(
		config, providerConfigs, nativeProviders, configOrder, nativeOrder, authProviders, registry.allowModelNetwork,
	)
	if refreshErr != nil {
		return refreshErr
	}
	registry.opMu.Lock()
	defer registry.opMu.Unlock()
	registry.mu.RLock()
	if registry.revision != revision {
		latestConfigs := cloneProviderConfigs(registry.providerConfigs)
		latestNative := cloneNativeProviders(registry.nativeProviders)
		for id, latestVersion := range registry.providerVersions {
			if providerVersions[id] == latestVersion {
				continue
			}
			delete(providerConfigs, id)
			delete(nativeProviders, id)
			if providerConfig, ok := latestConfigs[id]; ok {
				providerConfigs[id] = providerConfig
			}
			if nativeProvider, ok := latestNative[id]; ok {
				nativeProviders[id] = nativeProvider
			}
		}
		configOrder = append([]string(nil), registry.configOrder...)
		nativeOrder = append([]string(nil), registry.nativeOrder...)
	}
	registry.mu.RUnlock()
	all, compositionErrors := composeRegisteredProviders(base, config, providerConfigs, nativeProviders, configOrder, nativeOrder, authProviders)
	for _, id := range append(append([]string(nil), configOrder...), nativeOrder...) {
		if compositionErr := providerCompositionError(id, compositionErrors); compositionErr != nil {
			registry.mu.Lock()
			registry.errors = append(configLoadErrors(config), compositionErrors...)
			registry.revision++
			registry.mu.Unlock()
			return fmt.Errorf("provider %q: %w", id, compositionErr)
		}
	}
	errors := make([]string, 0)
	if config.Error() != "" {
		errors = append(errors, config.Error())
	}
	errors = append(errors, compositionErrors...)
	registry.mu.Lock()
	registry.config, registry.base, registry.all, registry.errors, registry.authProviders = config, base, all, errors, authProviders
	registry.providerConfigs = providerConfigs
	registry.revision++
	registry.mu.Unlock()
	return nil
}

func (registry *ModelRegistry) refreshProviderSnapshots(
	config *ModelConfig,
	providerConfigs map[string]extensions.ProviderConfig,
	nativeProviders map[string]extensions.Provider,
	configOrder, nativeOrder []string,
	authProviders map[string]*aiauth.Credential,
	allowNetwork bool,
) (map[string]extensions.ProviderConfig, error) {
	for _, id := range nativeOrder {
		provider := nativeProviders[id]
		if provider.RefreshModels != nil {
			methods := providerAuthFromLayers(id, config, providerConfigs, nativeProviders)
			credential, credentialErr := registry.resolveRefreshCredential(context.Background(), id, methods, authProviders[id], nil, config)
			if credentialErr != nil {
				return providerConfigs, fmt.Errorf("refresh native provider %q auth: %w", id, credentialErr)
			}
			if credential == nil {
				continue
			}
			if err := provider.RefreshModels(registry.refreshContext(id, credential, allowNetwork, false)); err != nil {
				return providerConfigs, fmt.Errorf("refresh native provider %q: %w", id, err)
			}
		}
	}
	for _, id := range configOrder {
		providerConfig, ok := providerConfigs[id]
		if !ok || providerConfig.RefreshModels == nil {
			continue
		}
		methods := providerAuthFromLayers(id, config, providerConfigs, nativeProviders)
		credential, credentialErr := registry.resolveRefreshCredential(context.Background(), id, methods, authProviders[id], &providerConfig, config)
		if credentialErr != nil {
			return providerConfigs, fmt.Errorf("refresh provider %q auth: %w", id, credentialErr)
		}
		if credential == nil {
			continue
		}
		models, refreshErr := providerConfig.RefreshModels(registry.refreshContext(id, credential, allowNetwork, false))
		if refreshErr != nil {
			return providerConfigs, fmt.Errorf("refresh provider %q: %w", id, refreshErr)
		}
		providerConfig.Models = models
		providerConfig.Defined["models"] = true
		providerConfigs[id] = providerConfig
	}
	return providerConfigs, nil
}

func (registry *ModelRegistry) refreshRegisteredProviders(allowNetwork bool) {
	registry.opMu.Lock()
	registry.mu.RLock()
	base, config := append([]ai.Model(nil), registry.base...), registry.config
	providerConfigs, nativeProviders := cloneProviderConfigs(registry.providerConfigs), cloneNativeProviders(registry.nativeProviders)
	configOrder, nativeOrder := append([]string(nil), registry.configOrder...), append([]string(nil), registry.nativeOrder...)
	authProviders := cloneCredentials(registry.authProviders)
	revision := registry.revision
	registry.mu.RUnlock()
	registry.opMu.Unlock()
	providerConfigs, err := registry.refreshProviderSnapshots(
		config, providerConfigs, nativeProviders, configOrder, nativeOrder, authProviders, allowNetwork,
	)
	if err != nil {
		registry.opMu.Lock()
		registry.mu.Lock()
		if registry.revision == revision {
			registry.errors = append(configLoadErrors(config), "Availability refresh: "+err.Error())
			registry.revision++
		}
		registry.mu.Unlock()
		registry.opMu.Unlock()
		return
	}
	all, compositionErrors := composeRegisteredProviders(base, config, providerConfigs, nativeProviders, configOrder, nativeOrder, authProviders)
	registry.opMu.Lock()
	registry.mu.Lock()
	if registry.revision == revision {
		registry.providerConfigs = providerConfigs
		registry.all = all
		registry.errors = append(configLoadErrors(config), compositionErrors...)
		registry.revision++
	}
	registry.mu.Unlock()
	registry.opMu.Unlock()
}

func hasRefreshProvider(configs map[string]extensions.ProviderConfig, native map[string]extensions.Provider) bool {
	for _, config := range configs {
		if config.RefreshModels != nil {
			return true
		}
	}
	for _, provider := range native {
		if provider.RefreshModels != nil {
			return true
		}
	}
	return false
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

func (registry *ModelRegistry) Error() string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return strings.Join(registry.errors, "\n\n")
}

func (registry *ModelRegistry) Models() []ai.Model {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return append([]ai.Model(nil), registry.all...)
}

func (registry *ModelRegistry) Find(provider, id string) (ai.Model, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	index := slices.IndexFunc(registry.all, func(model ai.Model) bool {
		return model.ID == id && string(model.Provider) == provider
	})
	if index < 0 {
		return ai.Model{}, false
	}
	return registry.all[index], true
}

func (registry *ModelRegistry) HasConfiguredAuth(provider string, env map[string]string) bool {
	registry.mu.RLock()
	storedCredential := registry.authProviders[provider].Clone()
	config := registry.config
	methods := registry.providerAuthLocked(provider)
	registry.mu.RUnlock()
	authContext := registryAuthContext{env: env}
	if storedCredential != nil {
		credential := resolveStoredCredential(storedCredential)
		if methods.APIKey != nil || methods.OAuth != nil {
			switch credential.Type {
			case aiauth.CredentialAPIKey:
				if methods.APIKey == nil {
					return false
				}
				if checker, ok := methods.APIKey.(aiauth.APIKeyCheck); ok {
					result, err := checker.Check(context.Background(), authContext, credential)
					return err == nil && result != nil
				}
				result, err := methods.APIKey.Resolve(context.Background(), authContext, credential)
				return err == nil && result != nil
			case aiauth.CredentialOAuth:
				return methods.OAuth != nil
			}
			return false
		}
		return credential.Type == aiauth.CredentialAPIKey && credential.Key != nil && *credential.Key != ""
	}
	if configuredProviderAPIKey(config, registry.RegisteredProviderConfig, provider, env) {
		return true
	}
	if methods.APIKey != nil {
		if checker, ok := methods.APIKey.(aiauth.APIKeyCheck); ok {
			result, err := checker.Check(context.Background(), authContext, nil)
			return err == nil && result != nil
		}
		result, err := methods.APIKey.Resolve(context.Background(), authContext, nil)
		return err == nil && result != nil
	}
	for _, name := range providerAPIKeyEnvironmentNames(provider) {
		if env[name] != "" || lookupNonEmptyEnv(name) {
			return true
		}
	}
	return false
}

func (registry *ModelRegistry) GetProviderAuthStatus(provider string, env map[string]string) extensions.AuthStatus {
	registry.mu.RLock()
	credential := registry.authProviders[provider].Clone()
	registered, registeredOK := registry.providerConfigs[provider]
	static := registry.config.Providers[provider]
	methods := registry.providerAuthLocked(provider)
	registry.mu.RUnlock()
	if credential != nil {
		return extensions.AuthStatus{Configured: true, Source: "stored"}
	}
	value, configuredValue, fallback := "", false, false
	if static.APIKey != nil {
		value, configuredValue = *static.APIKey, true
	}
	if registeredOK && registered.Defined["apiKey"] {
		value, configuredValue, fallback = registered.APIKey, true, true
	}
	if configuredValue {
		if IsCommandConfigValue(value) {
			return extensions.AuthStatus{Configured: true, Source: "models_json_command"}
		}
		names := GetConfigValueEnvVarNames(value)
		if len(names) > 0 {
			if len(GetMissingConfigValueEnvVarNames(value, env)) > 0 {
				return extensions.AuthStatus{}
			}
			return extensions.AuthStatus{Configured: true, Source: "environment", Label: strings.Join(names, ", ")}
		}
		source := "models_json_key"
		if fallback {
			source = "fallback"
		}
		return extensions.AuthStatus{Configured: true, Source: source}
	}
	authContext := registryAuthContext{env: env}
	if methods.APIKey == nil {
		return extensions.AuthStatus{}
	}
	if checker, ok := methods.APIKey.(aiauth.APIKeyCheck); ok {
		check, err := checker.Check(context.Background(), authContext, nil)
		if err != nil || check == nil {
			return extensions.AuthStatus{}
		}
		return extensions.AuthStatus{Configured: true, Source: "environment", Label: check.Source}
	}
	resolved, err := methods.APIKey.Resolve(context.Background(), authContext, nil)
	if err != nil || resolved == nil {
		return extensions.AuthStatus{}
	}
	return extensions.AuthStatus{Configured: true, Source: "environment", Label: resolved.Source}
}

func (registry *ModelRegistry) IsUsingOAuth(provider string) bool {
	registry.mu.RLock()
	credential := registry.authProviders[provider].Clone()
	methods := registry.providerAuthLocked(provider)
	registry.mu.RUnlock()
	return credential != nil && credential.Type == aiauth.CredentialOAuth && methods.OAuth != nil
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
	registry.mu.RLock()
	registered, registeredOK := registry.providerConfigs[provider]
	config := registry.config
	registry.mu.RUnlock()
	if registeredOK && registered.Defined["apiKey"] {
		value, err := ResolveConfigValue(ctx, registered.APIKey, env)
		if err != nil {
			return nil, fmt.Errorf("API key for provider %q: %w", provider, err)
		}
		return &value, nil
	}
	return config.ResolveAPIKey(ctx, provider, env)
}

func (registry *ModelRegistry) Available(env map[string]string) []ai.Model {
	models, _ := registry.AvailableWithError(env)
	return models
}

func (registry *ModelRegistry) AvailableWithError(env map[string]string) ([]ai.Model, error) {
	registry.mu.RLock()
	models := append([]ai.Model(nil), registry.all...)
	credential := registry.authProviders["github-copilot"].Clone()
	credentials := cloneCredentials(registry.authProviders)
	native := cloneNativeProviders(registry.nativeProviders)
	nativeOrder := append([]string(nil), registry.nativeOrder...)
	registry.mu.RUnlock()
	models = filterCredentialModels(models, map[string]*aiauth.Credential{"github-copilot": credential})
	result := make([]ai.Model, 0, len(models))
	configured := make(map[string]bool)
	for _, model := range models {
		id := string(model.Provider)
		available, checked := configured[id]
		if !checked {
			available = registry.HasConfiguredAuth(id, env)
			configured[id] = available
		}
		if available {
			result = append(result, model)
		}
	}
	for _, id := range nativeOrder {
		provider, ok := native[id]
		if !ok {
			continue
		}
		if provider.FilterModels == nil || !configured[id] {
			continue
		}
		filtered, err := provider.FilterModels(providerModels(result, id), credentials[id])
		if err != nil {
			return nil, fmt.Errorf("filter provider %q models: %w", id, err)
		}
		result = replaceProviderModels(result, id, filtered)
	}
	return result, nil
}

func (registry *ModelRegistry) ResolveAPIKey(ctx context.Context, provider string, env map[string]string) (*string, error) {
	key, err := registry.ResolveConfiguredAPIKey(ctx, provider, env)
	if err != nil || key != nil {
		return key, err
	}
	registry.mu.RLock()
	methods := registry.providerAuthLocked(provider)
	registry.mu.RUnlock()
	if methods.APIKey != nil {
		resolved, err := methods.APIKey.Resolve(ctx, registryAuthContext{env: env}, nil)
		if err != nil || resolved == nil {
			return nil, err
		}
		return resolved.Auth.APIKey, nil
	}
	for _, name := range providerAPIKeyEnvironmentNames(provider) {
		if value := env[name]; value != "" {
			return &value, nil
		}
		if value := getenv(name); value != "" {
			return &value, nil
		}
	}
	return nil, nil
}

func (registry *ModelRegistry) ResolveProviderAuth(ctx context.Context, provider string, env map[string]string) (*aiauth.AuthResult, error) {
	registry.mu.RLock()
	methods := registry.providerAuthLocked(provider)
	credentials := cloneCredentials(registry.authProviders)
	registry.mu.RUnlock()
	return aiauth.ResolveProviderAuth(ctx, provider, methods, aiauth.NewMemoryStore(credentials), registryAuthContext{env: env}, nil)
}

func (registry *ModelRegistry) ResolveModelHeaders(ctx context.Context, model ai.Model, env map[string]string, apiKeys ...*string) (*map[string]string, error) {
	registry.mu.RLock()
	registered, registeredOK := registry.providerConfigs[string(model.Provider)]
	_, nativeRegistered := registry.nativeProviders[string(model.Provider)]
	config := registry.config
	registry.mu.RUnlock()
	if !registeredOK && !nativeRegistered {
		return config.ResolveModelHeaders(ctx, model, env, apiKeys...)
	}
	providerID := string(model.Provider)
	static := config.Providers[providerID]
	raw := make(map[string]string)
	if model.Headers != nil {
		for name, value := range *model.Headers {
			setHeader(raw, name, value)
		}
	}
	for name, value := range static.Headers {
		setHeader(raw, name, value)
	}
	if registeredOK {
		for name, value := range registered.Headers {
			setHeader(raw, name, value)
		}
	}
	authHeader := static.AuthHeader
	if registeredOK && registered.Defined["authHeader"] {
		authHeader = registered.AuthHeader
	}
	if authHeader != nil && *authHeader {
		if len(apiKeys) == 0 || apiKeys[0] == nil || *apiKeys[0] == "" {
			return nil, fmt.Errorf("authHeader requires a resolved API key")
		}
		setHeader(raw, "Authorization", "Bearer "+*apiKeys[0])
	}
	if override, ok := static.ModelOverrides[model.ID]; ok {
		for name, value := range override.Headers {
			setHeader(raw, name, value)
		}
	}
	if index := slices.IndexFunc(static.Models, func(definition ModelDefinition) bool { return definition.ID == model.ID }); index >= 0 {
		for name, value := range static.Models[index].Headers {
			setHeader(raw, name, value)
		}
	}
	if registeredOK {
		if index := slices.IndexFunc(registered.Models, func(definition extensions.ProviderModelConfig) bool { return definition.ID == model.ID }); index >= 0 {
			for name, value := range registered.Models[index].Headers {
				setHeader(raw, name, value)
			}
		}
	}
	headers := make(map[string]string, len(raw))
	for name, value := range raw {
		resolved, err := ResolveConfigValue(ctx, value, env)
		if err != nil {
			return nil, fmt.Errorf("model %q header %q: %w", providerID+"/"+model.ID, name, err)
		}
		setHeader(headers, name, resolved)
	}
	if len(headers) == 0 {
		return nil, nil
	}
	return &headers, nil
}

func (registry *ModelRegistry) RegisterProviderConfig(id string, incoming extensions.ProviderConfig) error {
	if id == "" {
		return fmt.Errorf("provider id must not be empty")
	}
	incoming = normalizeProviderConfig(incoming)
	registry.opMu.Lock()
	defer registry.opMu.Unlock()
	registry.mu.RLock()
	base, config := append([]ai.Model(nil), registry.base...), registry.config
	configs, native := cloneProviderConfigs(registry.providerConfigs), cloneNativeProviders(registry.nativeProviders)
	configOrder, nativeOrder := append([]string(nil), registry.configOrder...), append([]string(nil), registry.nativeOrder...)
	credentials := cloneCredentials(registry.authProviders)
	registry.mu.RUnlock()
	if err := validateProviderConfig(id, incoming, base, config); err != nil {
		return err
	}
	merged := mergeProviderConfig(configs[id], incoming)
	if _, exists := configs[id]; !exists {
		configOrder = removeProviderID(configOrder, id)
		configOrder = append(configOrder, id)
	}
	configs[id] = merged
	delete(native, id)
	nativeOrder = removeProviderID(nativeOrder, id)
	all, errs := composeRegisteredProviders(base, config, configs, native, configOrder, nativeOrder, credentials)
	if err := providerCompositionError(id, errs); err != nil {
		return err
	}
	registry.mu.Lock()
	registry.providerConfigs, registry.nativeProviders = configs, native
	registry.configOrder, registry.nativeOrder = configOrder, nativeOrder
	registry.all, registry.errors = all, append(configLoadErrors(config), errs...)
	registry.bumpProviderVersionLocked(id)
	registry.revision++
	registry.mu.Unlock()
	if hasRefreshProvider(configs, native) {
		go registry.refreshRegisteredProviders(false)
	}
	return nil
}

func (registry *ModelRegistry) RegisterProvider(provider extensions.Provider) error {
	if provider.ID == "" {
		return fmt.Errorf("native provider id must not be empty")
	}
	if provider.Name == "" {
		return fmt.Errorf("native provider %q requires name", provider.ID)
	}
	if provider.Auth.APIKey == nil && provider.Auth.OAuth == nil {
		return fmt.Errorf("native provider %q requires auth", provider.ID)
	}
	if provider.GetModels == nil {
		return fmt.Errorf("native provider %q requires getModels", provider.ID)
	}
	if provider.Stream == nil {
		return fmt.Errorf("native provider %q requires stream", provider.ID)
	}
	if provider.StreamSimple == nil {
		return fmt.Errorf("native provider %q requires streamSimple", provider.ID)
	}
	registry.opMu.Lock()
	defer registry.opMu.Unlock()
	registry.mu.RLock()
	base, config := append([]ai.Model(nil), registry.base...), registry.config
	configs, native := cloneProviderConfigs(registry.providerConfigs), cloneNativeProviders(registry.nativeProviders)
	configOrder, nativeOrder := append([]string(nil), registry.configOrder...), append([]string(nil), registry.nativeOrder...)
	credentials := cloneCredentials(registry.authProviders)
	registry.mu.RUnlock()
	if _, exists := native[provider.ID]; !exists {
		nativeOrder = removeProviderID(nativeOrder, provider.ID)
		nativeOrder = append(nativeOrder, provider.ID)
	}
	native[provider.ID] = provider
	delete(configs, provider.ID)
	configOrder = removeProviderID(configOrder, provider.ID)
	all, errs := composeRegisteredProviders(base, config, configs, native, configOrder, nativeOrder, credentials)
	if err := providerCompositionError(provider.ID, errs); err != nil {
		return err
	}
	registry.mu.Lock()
	registry.providerConfigs, registry.nativeProviders = configs, native
	registry.configOrder, registry.nativeOrder = configOrder, nativeOrder
	registry.all, registry.errors = all, append(configLoadErrors(config), errs...)
	registry.bumpProviderVersionLocked(provider.ID)
	registry.revision++
	registry.mu.Unlock()
	if hasRefreshProvider(configs, native) {
		go registry.refreshRegisteredProviders(false)
	}
	return nil
}

func (registry *ModelRegistry) UnregisterProvider(id string) error {
	registry.opMu.Lock()
	defer registry.opMu.Unlock()
	registry.mu.RLock()
	base, config := append([]ai.Model(nil), registry.base...), registry.config
	configs, native := cloneProviderConfigs(registry.providerConfigs), cloneNativeProviders(registry.nativeProviders)
	configOrder, nativeOrder := append([]string(nil), registry.configOrder...), append([]string(nil), registry.nativeOrder...)
	credentials := cloneCredentials(registry.authProviders)
	registry.mu.RUnlock()
	delete(configs, id)
	delete(native, id)
	configOrder = removeProviderID(configOrder, id)
	nativeOrder = removeProviderID(nativeOrder, id)
	all, errs := composeRegisteredProviders(base, config, configs, native, configOrder, nativeOrder, credentials)
	registry.mu.Lock()
	registry.providerConfigs, registry.nativeProviders = configs, native
	registry.configOrder, registry.nativeOrder = configOrder, nativeOrder
	registry.all, registry.errors = all, append(configLoadErrors(config), errs...)
	registry.bumpProviderVersionLocked(id)
	registry.revision++
	registry.mu.Unlock()
	if hasRefreshProvider(configs, native) {
		go registry.refreshRegisteredProviders(false)
	}
	return nil
}

func (registry *ModelRegistry) bumpProviderVersionLocked(id string) {
	registry.nextProviderVersion++
	registry.providerVersions[id] = registry.nextProviderVersion
}

func (registry *ModelRegistry) Provider(id string) (extensions.Provider, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if provider, ok := registry.nativeProviders[id]; ok {
		static := registry.config.Providers[id]
		if static.Name != nil {
			provider.Name = *static.Name
		}
		if static.BaseURL != nil {
			provider.BaseURL = *static.BaseURL
		}
		provider.Auth = registry.providerAuthLocked(id)
		models := providerModels(registry.all, id)
		provider.GetModels = func() ([]ai.Model, error) { return append([]ai.Model(nil), models...), nil }
		return provider, true
	}
	config, registered := registry.providerConfigs[id]
	definition, builtin := providers.Get(ai.ProviderID(id))
	staticConfig, configured := registry.config.Providers[id]
	modelBacked := slices.ContainsFunc(registry.all, func(model ai.Model) bool { return string(model.Provider) == id })
	if !registered && !builtin && !configured && !modelBacked {
		return extensions.Provider{}, false
	}
	provider := extensions.Provider{ID: id, Name: config.Name, BaseURL: config.BaseURL, Headers: cloneStringMap(config.Headers), Config: config}
	if provider.Name == "" && builtin {
		provider.Name = definition.Name
	}
	if provider.BaseURL == "" && builtin {
		provider.BaseURL = definition.BaseURL
	}
	if staticConfig.Name != nil && !config.Defined["name"] {
		provider.Name = *staticConfig.Name
	}
	if staticConfig.BaseURL != nil && !config.Defined["baseUrl"] {
		provider.BaseURL = *staticConfig.BaseURL
	}
	provider.Auth = registry.providerAuthLocked(id)
	models := providerModels(registry.all, id)
	provider.GetModels = func() ([]ai.Model, error) { return append([]ai.Model(nil), models...), nil }
	return provider, true
}

func (registry *ModelRegistry) ProviderDisplayName(id string) string {
	provider, ok := registry.Provider(id)
	if ok && provider.Name != "" {
		return provider.Name
	}
	return id
}

func (registry *ModelRegistry) ProviderAuth(id string) aiauth.ProviderAuth {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return registry.providerAuthLocked(id)
}

func (registry *ModelRegistry) RegisteredProviderConfig(id string) (extensions.ProviderConfig, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	config, ok := registry.providerConfigs[id]
	return cloneProviderConfig(config), ok
}

func (registry *ModelRegistry) RegisteredNativeProvider(id string) (extensions.Provider, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	provider, ok := registry.nativeProviders[id]
	return provider, ok
}

func (registry *ModelRegistry) RegisteredProviderIDs() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]string, 0, len(registry.providerConfigs)+len(registry.nativeProviders))
	seen := make(map[string]struct{})
	for _, id := range registry.configOrder {
		if _, ok := registry.providerConfigs[id]; ok {
			result = append(result, id)
			seen[id] = struct{}{}
		}
	}
	for _, id := range registry.nativeOrder {
		if _, ok := registry.nativeProviders[id]; ok {
			if _, duplicate := seen[id]; !duplicate {
				result = append(result, id)
			}
		}
	}
	return result
}

// ProviderIDs returns the complete composed provider order: built-ins, native
// extension providers, models.json providers, then extension provider configs.
func (registry *ModelRegistry) ProviderIDs() []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	appendID := func(id string) {
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	for _, provider := range providers.List() {
		appendID(string(provider.ID))
	}

	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for _, id := range registry.nativeOrder {
		if _, exists := registry.nativeProviders[id]; exists {
			appendID(id)
		}
	}
	for _, id := range registry.config.providerIDs() {
		appendID(id)
	}
	for _, id := range registry.configOrder {
		if _, exists := registry.providerConfigs[id]; exists {
			appendID(id)
		}
	}
	return result
}

func (registry *ModelRegistry) StreamSimple(
	ctx context.Context,
	model *ai.Model,
	request ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, fmt.Errorf("ai: model is nil")
	}
	registry.mu.RLock()
	native, nativeOK := registry.nativeProviders[string(model.Provider)]
	config, configOK := registry.providerConfigs[string(model.Provider)]
	registry.mu.RUnlock()
	if nativeOK {
		baseModels, err := native.GetModels()
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(baseModels, func(candidate ai.Model) bool { return candidate.API == model.API }) {
			if native.StreamSimple != nil {
				return native.StreamSimple(ctx, model, request, options)
			}
			if native.Stream != nil {
				return native.Stream(ctx, model, request, options)
			}
		}
	}
	if configOK && config.Stream != nil && config.API == model.API {
		return config.Stream(ctx, model, request, options)
	}
	return aiapi.StreamSimple(ctx, model, request, options)
}

func (registry *ModelRegistry) providerAuthLocked(id string) aiauth.ProviderAuth {
	return providerAuthFromLayers(id, registry.config, registry.providerConfigs, registry.nativeProviders)
}

func (registry *ModelRegistry) refreshContext(id string, credential *aiauth.Credential, allowNetwork, force bool) extensions.RefreshModelsContext {
	return extensions.RefreshModelsContext{
		Credential: credential.Clone(), AllowNetwork: allowNetwork, Force: force,
		Signal: context.Background(), Store: newProviderModelStore(filepath.Join(registry.agentDir, "models-store.json"), id),
	}
}

func (registry *ModelRegistry) resolveRefreshCredential(
	ctx context.Context,
	id string,
	methods aiauth.ProviderAuth,
	stored *aiauth.Credential,
	registered *extensions.ProviderConfig,
	config *ModelConfig,
) (*aiauth.Credential, error) {
	if stored != nil && stored.Type == aiauth.CredentialOAuth {
		if methods.OAuth == nil {
			return nil, nil
		}
		return stored.Clone(), nil
	}
	if registered != nil && registered.Defined["apiKey"] {
		key, err := ResolveConfigValue(ctx, registered.APIKey, nil)
		if err != nil {
			return nil, err
		}
		if key == "" {
			return nil, nil
		}
		return aiauth.APIKeyCredential(key), nil
	}
	if config != nil {
		key, err := config.ResolveAPIKey(ctx, id, nil)
		if err != nil {
			return nil, err
		}
		if key != nil && *key != "" {
			return aiauth.APIKeyCredential(*key), nil
		}
	}
	if methods.APIKey == nil {
		return nil, nil
	}
	var credential *aiauth.Credential
	if stored != nil && stored.Type == aiauth.CredentialAPIKey {
		credential = stored.Clone()
	}
	resolved, err := methods.APIKey.Resolve(ctx, registryAuthContext{}, credential)
	if err != nil || resolved == nil {
		return nil, err
	}
	result := &aiauth.Credential{Type: aiauth.CredentialAPIKey, Env: cloneStringMap(resolved.Env)}
	if resolved.Auth.APIKey != nil {
		key := *resolved.Auth.APIKey
		result.Key = &key
	}
	return result, nil
}

func providerAPIKeyEnvironmentNames(id string) []string {
	provider, ok := providers.Get(ai.ProviderID(id))
	if !ok {
		return nil
	}
	return provider.APIKeyEnv
}

func removeProviderID(values []string, id string) []string {
	return slices.DeleteFunc(values, func(value string) bool { return value == id })
}

var getenv = func(name string) string {
	return strings.TrimSpace(environmentValue(name))
}

func lookupNonEmptyEnv(name string) bool { return getenv(name) != "" }

// RequestAuth mirrors agent.RequestAuth without importing the agent package.
type RequestAuth struct {
	APIKey  *string
	Headers ai.ProviderHeaders
	Env     ai.ProviderEnv
	BaseURL *string
}

// DefaultRequestAuthResolver returns a resolver that checks stored
// credentials, models.json overrides, and built-in provider auth methods
// (env vars, ADC, etc.) for a given provider. This is the canonical
// implementation used by both the CLI and the SDK. When credentials is
// nil, the registry's own auth.json data is used.
func (registry *ModelRegistry) DefaultRequestAuthResolver(credentials aiauth.CredentialStore) func(context.Context, ai.ProviderID) (*RequestAuth, error) {
	var credentialsErr error
	if credentials == nil {
		credentials, credentialsErr = NewAuthStorage(filepath.Join(registry.agentDir, "auth.json"))
	}
	return func(ctx context.Context, providerID ai.ProviderID) (*RequestAuth, error) {
		if credentialsErr != nil {
			return nil, credentialsErr
		}
		stored, err := credentials.Read(ctx, string(providerID))
		if err != nil {
			return nil, err
		}
		provider, knownProvider := providers.Get(providerID)
		if stored != nil && knownProvider {
			resolved, err := aiauth.ResolveProviderAuth(
				ctx, string(providerID), provider.Methods, credentials,
				aiauth.EnvironmentContext{}, nil,
			)
			if err != nil || resolved == nil {
				return nil, err
			}
			return registryRequestAuth(resolved), nil
		}
		if stored != nil {
			if stored.Type == aiauth.CredentialAPIKey {
				return &RequestAuth{APIKey: stored.Key, Env: cloneRuntimeEnv(stored.Env)}, nil
			}
			return nil, nil
		}
		configured, err := registry.ResolveConfiguredAPIKey(ctx, string(providerID), nil)
		if err != nil || configured != nil {
			return &RequestAuth{APIKey: configured}, err
		}
		if knownProvider {
			resolved, err := aiauth.ResolveProviderAuth(
				ctx, string(providerID), provider.Methods, credentials,
				aiauth.EnvironmentContext{}, nil,
			)
			if err != nil || resolved == nil {
				return nil, err
			}
			return registryRequestAuth(resolved), nil
		}
		key, err := registry.ResolveAPIKey(ctx, string(providerID), nil)
		if err != nil || key == nil {
			return nil, err
		}
		return &RequestAuth{APIKey: key}, nil
	}
}

// DefaultModelHeadersResolver returns a resolver for per-request headers
// from models.json configuration.
func (registry *ModelRegistry) DefaultModelHeadersResolver() func(context.Context, *ai.Model, *string, ai.ProviderEnv) (*map[string]string, error) {
	return func(ctx context.Context, model *ai.Model, apiKey *string, env ai.ProviderEnv) (*map[string]string, error) {
		return registry.ResolveModelHeaders(ctx, *model, map[string]string(env), apiKey)
	}
}

// FallbackRequestAuthResolver resolves auth using only stored credentials
// and built-in provider auth methods, without a ModelRegistry.
func FallbackRequestAuthResolver(credentials aiauth.CredentialStore) func(context.Context, ai.ProviderID) (*RequestAuth, error) {
	if credentials == nil {
		credentials = aiauth.NewMemoryStore(nil)
	}
	return func(ctx context.Context, providerID ai.ProviderID) (*RequestAuth, error) {
		stored, err := credentials.Read(ctx, string(providerID))
		if err != nil {
			return nil, err
		}
		provider, knownProvider := providers.Get(providerID)
		if stored != nil && knownProvider {
			resolved, err := aiauth.ResolveProviderAuth(
				ctx, string(providerID), provider.Methods, credentials,
				aiauth.EnvironmentContext{}, nil,
			)
			if err != nil || resolved == nil {
				return nil, err
			}
			return registryRequestAuth(resolved), nil
		}
		if stored != nil && stored.Type == aiauth.CredentialAPIKey {
			return &RequestAuth{APIKey: stored.Key, Env: cloneRuntimeEnv(stored.Env)}, nil
		}
		if knownProvider {
			resolved, err := aiauth.ResolveProviderAuth(
				ctx, string(providerID), provider.Methods, credentials,
				aiauth.EnvironmentContext{}, nil,
			)
			if err != nil || resolved == nil {
				return nil, err
			}
			return registryRequestAuth(resolved), nil
		}
		return nil, nil
	}
}

func registryRequestAuth(resolved *aiauth.AuthResult) *RequestAuth {
	if resolved == nil {
		return nil
	}
	return &RequestAuth{
		APIKey: resolved.Auth.APIKey, Headers: cloneProviderHeaders(resolved.Auth.Headers),
		Env: cloneRuntimeEnv(resolved.Env), BaseURL: resolved.Auth.BaseURL,
	}
}

func cloneProviderHeaders(source ai.ProviderHeaders) ai.ProviderHeaders {
	if source == nil {
		return nil
	}
	result := make(ai.ProviderHeaders, len(source))
	for name, value := range source {
		if value == nil {
			result[name] = nil
			continue
		}
		copy := *value
		result[name] = &copy
	}
	return result
}

func cloneRuntimeEnv(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}
