package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/ai/providers"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/gofrs/flock"
)

func normalizeProviderConfig(config extensions.ProviderConfig) extensions.ProviderConfig {
	config = cloneProviderConfig(config)
	if config.Defined == nil {
		config.Defined = make(map[string]bool)
		if config.Name != "" {
			config.Defined["name"] = true
		}
		if config.BaseURL != "" {
			config.Defined["baseUrl"] = true
		}
		if config.APIKey != "" {
			config.Defined["apiKey"] = true
		}
		if config.API != ai.APIUnknown {
			config.Defined["api"] = true
		}
		if config.Stream != nil {
			config.Defined["streamSimple"] = true
		}
		if config.Headers != nil {
			config.Defined["headers"] = true
		}
		if config.AuthHeader != nil {
			config.Defined["authHeader"] = true
		}
		if config.Models != nil {
			config.Defined["models"] = true
		}
		if config.RefreshModels != nil {
			config.Defined["refreshModels"] = true
		}
		if config.OAuth != nil {
			config.Defined["oauth"] = true
		}
	}
	return config
}

func mergeProviderConfig(previous, incoming extensions.ProviderConfig) extensions.ProviderConfig {
	result := cloneProviderConfig(previous)
	if result.Defined == nil {
		result.Defined = make(map[string]bool)
	}
	if result.RegistrationValues == nil {
		result.RegistrationValues = make(map[string]any)
	}
	for name := range incoming.Defined {
		result.Defined[name] = true
		if value, ok := incoming.RegistrationValues[name]; ok {
			result.RegistrationValues[name] = value
		} else {
			delete(result.RegistrationValues, name)
		}
		switch name {
		case "name":
			result.Name = incoming.Name
		case "baseUrl":
			result.BaseURL = incoming.BaseURL
		case "apiKey":
			result.APIKey = incoming.APIKey
		case "api":
			result.API = incoming.API
		case "streamSimple":
			result.Stream = incoming.Stream
		case "headers":
			result.Headers = cloneStringMap(incoming.Headers)
		case "authHeader":
			result.AuthHeader = cloneBool(incoming.AuthHeader)
		case "models":
			result.Models = cloneProviderModels(incoming.Models)
		case "refreshModels":
			result.RefreshModels = incoming.RefreshModels
		case "oauth":
			result.OAuth = incoming.OAuth
		}
	}
	return result
}

func validateProviderConfig(id string, config extensions.ProviderConfig, base []ai.Model, modelsConfig *ModelConfig) error {
	if config.Stream != nil && config.API == ai.APIUnknown {
		return fmt.Errorf("provider %q: api is required when streamSimple is provided", id)
	}
	configured := base
	if static, ok := modelsConfig.Providers[id]; ok {
		var err error
		configured, err = ApplyModelConfig(base, &ModelConfig{Providers: map[string]ModelProviderConfig{id: static}})
		if err != nil {
			return err
		}
	}
	_, err := applyRegisteredConfig(configured, id, config)
	if err != nil {
		return err
	}
	return nil
}

func composeRegisteredProviders(
	base []ai.Model,
	modelsConfig *ModelConfig,
	configs map[string]extensions.ProviderConfig,
	native map[string]extensions.Provider,
	configOrder, nativeOrder []string,
	credentials map[string]*aiauth.Credential,
) ([]ai.Model, []string) {
	all := append([]ai.Model(nil), base...)
	errorsList := make([]string, 0)
	providerIDs := make([]string, 0, len(modelsConfig.Providers))
	for _, providerID := range modelsConfig.providerIDs() {
		if _, overridden := native[providerID]; !overridden {
			providerIDs = append(providerIDs, providerID)
		}
	}
	invalidConfig := make(map[string]struct{})
	for _, providerID := range providerIDs {
		partial := &ModelConfig{Providers: map[string]ModelProviderConfig{providerID: modelsConfig.Providers[providerID]}}
		updated, err := ApplyModelConfig(all, partial)
		if err != nil {
			errorsList = append(errorsList, `Provider "`+providerID+`": `+err.Error())
			invalidConfig[providerID] = struct{}{}
			continue
		}
		all = updated
	}
	for _, id := range nativeOrder {
		provider, ok := native[id]
		if !ok {
			continue
		}
		models, err := provider.GetModels()
		if err != nil {
			errorsList = append(errorsList, `Provider "`+id+`": `+err.Error())
			continue
		}
		for index := range models {
			models[index].Provider = ai.ProviderID(id)
			if models[index].BaseURL == "" {
				models[index].BaseURL = provider.BaseURL
			}
		}
		if static, configured := modelsConfig.Providers[id]; configured {
			models, err = applyProviderConfig(id, models, static, true)
			if err != nil {
				errorsList = append(errorsList, `Provider "`+id+`": `+err.Error())
				continue
			}
		}
		all = replaceProviderModels(all, id, models)
	}
	for _, id := range configOrder {
		config, ok := configs[id]
		if !ok {
			continue
		}
		if _, invalid := invalidConfig[id]; invalid {
			continue
		}
		updated, err := applyRegisteredConfig(all, id, config)
		if err == nil && config.OAuth != nil && config.OAuth.ModifyModels != nil && credentials[id] != nil && credentials[id].Type == aiauth.CredentialOAuth {
			models, modifyErr := config.OAuth.ModifyModels(providerModels(updated, id), extensionCredentials(credentials[id]))
			if modifyErr != nil {
				err = modifyErr
			} else {
				updated = replaceProviderModels(updated, id, models)
			}
		}
		if err != nil {
			errorsList = append(errorsList, `Provider "`+id+`": `+err.Error())
			continue
		}
		if static, exists := modelsConfig.Providers[id]; exists {
			for index := range updated {
				if string(updated[index].Provider) != id {
					continue
				}
				if override, exists := static.ModelOverrides[updated[index].ID]; exists {
					updated[index] = applyModelOverride(updated[index], override)
				}
			}
		}
		all = updated
	}
	return all, errorsList
}

func applyRegisteredConfig(all []ai.Model, id string, config extensions.ProviderConfig) ([]ai.Model, error) {
	base := providerModels(all, id)
	models := append([]ai.Model(nil), base...)
	if config.Defined["models"] {
		models = make([]ai.Model, 0, len(config.Models))
		for _, definition := range config.Models {
			defaults := matchingModel(base, definition.ID)
			model, err := registeredModel(id, definition, config, defaults)
			if err != nil {
				return nil, err
			}
			models = append(models, model)
		}
	} else if config.Defined["baseUrl"] {
		for index := range models {
			models[index].BaseURL = config.BaseURL
		}
	}
	return replaceProviderModels(all, id, models), nil
}

func registeredModel(id string, definition extensions.ProviderModelConfig, config extensions.ProviderConfig, defaults *ai.Model) (ai.Model, error) {
	model := ai.Model{ID: definition.ID, Name: definition.Name, API: definition.API, Provider: ai.ProviderID(id), BaseURL: definition.BaseURL,
		Reasoning: definition.Reasoning, ThinkingLevelMap: cloneThinkingMap(definition.ThinkingLevelMap), Input: append(ai.InputModalities(nil), definition.Input...),
		Cost: definition.Cost, ContextWindow: definition.ContextWindow, MaxTokens: definition.MaxTokens,
		Compat: append(json.RawMessage(nil), definition.Compat...)}
	if model.API == ai.APIUnknown {
		model.API = config.API
	}
	if model.BaseURL == "" {
		model.BaseURL = config.BaseURL
	}
	if defaults != nil {
		if model.API == ai.APIUnknown {
			model.API = defaults.API
		}
		if model.BaseURL == "" {
			model.BaseURL = defaults.BaseURL
		}
	}
	if model.ID == "" {
		return ai.Model{}, fmt.Errorf("provider %q: model id must not be empty", id)
	}
	if model.API == ai.APIUnknown {
		return ai.Model{}, fmt.Errorf("provider %s, model %s: no api specified", id, model.ID)
	}
	if model.BaseURL == "" {
		return ai.Model{}, fmt.Errorf("provider %s: baseUrl is required when defining custom models", id)
	}
	return model, nil
}

func matchingModel(models []ai.Model, id string) *ai.Model {
	for index := range models {
		if models[index].ID == id {
			return &models[index]
		}
	}
	return firstModel(models)
}

func providerModels(all []ai.Model, id string) []ai.Model {
	result := make([]ai.Model, 0)
	for _, model := range all {
		if string(model.Provider) == id {
			result = append(result, model)
		}
	}
	return result
}

func replaceProviderModels(all []ai.Model, id string, replacement []ai.Model) []ai.Model {
	first := slices.IndexFunc(all, func(model ai.Model) bool { return string(model.Provider) == id })
	result := make([]ai.Model, 0, len(all)+len(replacement))
	if first < 0 {
		result = append(result, all...)
		return append(result, replacement...)
	}
	for index, model := range all {
		if index == first {
			result = append(result, replacement...)
		}
		if string(model.Provider) != id {
			result = append(result, model)
		}
	}
	return result
}

func firstModel(models []ai.Model) *ai.Model {
	if len(models) == 0 {
		return nil
	}
	return &models[0]
}

func providerCompositionError(id string, values []string) error {
	prefix := `Provider "` + id + `": `
	for _, value := range values {
		if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
			return errors.New(value[len(prefix):])
		}
	}
	return nil
}

func configLoadErrors(config *ModelConfig) []string {
	if config != nil && config.Error() != "" {
		return []string{config.Error()}
	}
	return nil
}

func cloneProviderConfig(config extensions.ProviderConfig) extensions.ProviderConfig {
	config.Headers = cloneStringMap(config.Headers)
	config.AuthHeader = cloneBool(config.AuthHeader)
	config.Models = cloneProviderModels(config.Models)
	config.Defined = cloneBoolMap(config.Defined)
	config.RegistrationValues = cloneAnyMap(config.RegistrationValues)
	return config
}

func cloneProviderConfigs(source map[string]extensions.ProviderConfig) map[string]extensions.ProviderConfig {
	result := make(map[string]extensions.ProviderConfig, len(source))
	for id, config := range source {
		result[id] = cloneProviderConfig(config)
	}
	return result
}

func cloneNativeProviders(source map[string]extensions.Provider) map[string]extensions.Provider {
	result := make(map[string]extensions.Provider, len(source))
	for id, provider := range source {
		result[id] = provider
	}
	return result
}

func cloneProviderModels(source []extensions.ProviderModelConfig) []extensions.ProviderModelConfig {
	if source == nil {
		return nil
	}
	result := append([]extensions.ProviderModelConfig(nil), source...)
	for index := range result {
		result[index].Input = append(ai.InputModalities(nil), result[index].Input...)
		result[index].Headers = cloneStringMap(result[index].Headers)
		result[index].Compat = append(json.RawMessage(nil), result[index].Compat...)
		result[index].ThinkingLevelMap = cloneThinkingMap(result[index].ThinkingLevelMap)
	}
	return result
}

func cloneCredentials(source map[string]*aiauth.Credential) map[string]*aiauth.Credential {
	result := make(map[string]*aiauth.Credential, len(source))
	for id, credential := range source {
		result[id] = credential.Clone()
	}
	return result
}

func cloneUint64Map(source map[string]uint64) map[string]uint64 {
	result := make(map[string]uint64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	if source == nil {
		return nil
	}
	result := make(map[string]bool, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func configuredProviderAPIKey(config *ModelConfig, registered func(string) (extensions.ProviderConfig, bool), id string, env map[string]string) bool {
	if runtimeConfig, ok := registered(id); ok && runtimeConfig.Defined["apiKey"] {
		return runtimeConfig.APIKey != "" && (runtimeConfig.APIKey[0] == '!' || len(missingConfigEnv(runtimeConfig.APIKey, env)) == 0)
	}
	return config.HasConfiguredAPIKey(id, env)
}

type registeredAPIKeyAuth struct {
	name         string
	value        string
	valueDefined bool
	inherited    aiauth.APIKeyAuth
	providerID   string
	headers      map[string]string
	authHeader   bool
}

func (method registeredAPIKeyAuth) Name() string { return method.name }

func (method registeredAPIKeyAuth) Login(ctx context.Context, interaction aiauth.AuthInteraction) (*aiauth.Credential, error) {
	key, err := interaction.Prompt(ctx, aiauth.AuthPrompt{Type: aiauth.PromptSecret, Message: "Enter " + method.name})
	if err != nil {
		return nil, err
	}
	return aiauth.APIKeyCredential(key), nil
}

func (method registeredAPIKeyAuth) Check(ctx context.Context, authContext aiauth.AuthContext, credential *aiauth.Credential) (*aiauth.AuthCheck, error) {
	if credential != nil {
		if inherited, ok := method.inherited.(aiauth.APIKeyCheck); ok {
			return inherited.Check(ctx, authContext, credential)
		}
		if credential.Key != nil && *credential.Key != "" {
			return &aiauth.AuthCheck{Source: "stored credential", Type: aiauth.CredentialAPIKey}, nil
		}
		if method.inherited != nil {
			resolved, err := method.inherited.Resolve(ctx, authContext, credential)
			if err != nil || resolved == nil {
				return nil, err
			}
			return &aiauth.AuthCheck{Source: resolved.Source, Type: aiauth.CredentialAPIKey}, nil
		}
		return nil, nil
	}
	if method.valueDefined || method.value != "" {
		if IsCommandConfigValue(method.value) {
			return &aiauth.AuthCheck{Source: "configured API key", Type: aiauth.CredentialAPIKey}, nil
		}
		for _, name := range GetConfigValueEnvVarNames(method.value) {
			if value, ok := authContext.Env(ctx, name); !ok || value == "" {
				return nil, nil
			}
		}
		return &aiauth.AuthCheck{Source: "configured API key", Type: aiauth.CredentialAPIKey}, nil
	}
	if inherited, ok := method.inherited.(aiauth.APIKeyCheck); ok {
		return inherited.Check(ctx, authContext, nil)
	}
	if method.inherited == nil {
		return nil, nil
	}
	resolved, err := method.inherited.Resolve(ctx, authContext, nil)
	if err != nil || resolved == nil {
		return nil, err
	}
	return &aiauth.AuthCheck{Source: resolved.Source, Type: aiauth.CredentialAPIKey}, nil
}

func (method registeredAPIKeyAuth) Resolve(ctx context.Context, authContext aiauth.AuthContext, credential *aiauth.Credential) (*aiauth.AuthResult, error) {
	result, err := method.resolve(ctx, authContext, credential)
	if err != nil || result == nil {
		return result, err
	}
	return configureAuthResult(ctx, authContext, method.providerID, method.headers, method.authHeader, credential, result)
}

func (method registeredAPIKeyAuth) resolve(ctx context.Context, authContext aiauth.AuthContext, credential *aiauth.Credential) (*aiauth.AuthResult, error) {
	if credential != nil {
		if method.inherited != nil {
			return method.inherited.Resolve(ctx, authContext, credential)
		}
		if credential.Key == nil || *credential.Key == "" {
			return nil, nil
		}
		key := *credential.Key
		return &aiauth.AuthResult{Auth: aiauth.ModelAuth{APIKey: &key}, Env: cloneStringMap(credential.Env), Source: "stored credential"}, nil
	}
	if method.valueDefined || method.value != "" {
		env := make(map[string]string)
		for _, name := range GetConfigValueEnvVarNames(method.value) {
			if value, ok := authContext.Env(ctx, name); ok {
				env[name] = value
			}
		}
		key, err := ResolveConfigValue(ctx, method.value, env)
		if err != nil {
			return nil, err
		}
		if method.inherited != nil {
			return method.inherited.Resolve(ctx, authContext, aiauth.APIKeyCredential(key))
		}
		return &aiauth.AuthResult{Auth: aiauth.ModelAuth{APIKey: &key}, Source: "configured API key"}, nil
	}
	if method.inherited == nil {
		return nil, nil
	}
	return method.inherited.Resolve(ctx, authContext, nil)
}

func providerAuthFromLayers(
	id string,
	modelsConfig *ModelConfig,
	configs map[string]extensions.ProviderConfig,
	native map[string]extensions.Provider,
) aiauth.ProviderAuth {
	methods := aiauth.ProviderAuth{}
	if provider, ok := native[id]; ok {
		methods = provider.Auth
	} else if provider, ok := providers.Get(ai.ProviderID(id)); ok {
		methods = provider.Methods
	}
	static, staticOK := modelsConfig.Providers[id]
	config, configOK := configs[id]
	hasLayers := staticOK || configOK
	if !hasLayers {
		return methods
	}
	oauth := methods.OAuth
	if configOK && config.OAuth != nil {
		oauth = extensionOAuth{provider: config.OAuth}
	}
	headers, authHeader := providerAuthConfiguration(static, staticOK, config, configOK)
	if oauth != nil && (len(headers) > 0 || authHeader) {
		oauth = configuredOAuth{OAuth: oauth, providerID: id, headers: headers, authHeader: authHeader}
	}
	value, valueDefined := "", false
	if staticOK && static.APIKey != nil {
		value, valueDefined = *static.APIKey, true
	}
	if configOK && config.Defined["apiKey"] {
		value, valueDefined = config.APIKey, true
	}
	apiKey := methods.APIKey
	if apiKey == nil && !valueDefined && oauth != nil {
		apiKey = nil
	} else {
		name := providerDisplayNameFromLayers(id, modelsConfig, configs, native) + " API key"
		if methods.APIKey != nil && methods.APIKey.Name() != "" {
			name = methods.APIKey.Name()
		}
		apiKey = registeredAPIKeyAuth{
			name: name, value: value, valueDefined: valueDefined, inherited: methods.APIKey,
			providerID: id, headers: headers, authHeader: authHeader,
		}
	}
	return aiauth.ProviderAuth{APIKey: apiKey, OAuth: oauth}
}

func providerAuthConfiguration(
	static ModelProviderConfig,
	staticOK bool,
	config extensions.ProviderConfig,
	configOK bool,
) (map[string]string, bool) {
	var headers map[string]string
	merge := func(values map[string]string) {
		if len(values) == 0 {
			return
		}
		if headers == nil {
			headers = make(map[string]string, len(values))
		}
		for name, value := range values {
			setHeader(headers, name, value)
		}
	}
	authHeader := false
	if staticOK {
		merge(static.Headers)
		if static.AuthHeader != nil {
			authHeader = *static.AuthHeader
		}
	}
	if configOK {
		merge(config.Headers)
		if config.Defined["authHeader"] && config.AuthHeader != nil {
			authHeader = *config.AuthHeader
		}
	}
	return headers, authHeader
}

func configureAuthResult(
	ctx context.Context,
	authContext aiauth.AuthContext,
	providerID string,
	rawHeaders map[string]string,
	authHeader bool,
	credential *aiauth.Credential,
	result *aiauth.AuthResult,
) (*aiauth.AuthResult, error) {
	env := make(map[string]string)
	if credential != nil {
		for name, value := range credential.Env {
			env[name] = value
		}
	}
	for name, value := range result.Env {
		env[name] = value
	}
	for _, raw := range rawHeaders {
		for _, name := range GetConfigValueEnvVarNames(raw) {
			if _, exists := env[name]; exists {
				continue
			}
			if value, ok := authContext.Env(ctx, name); ok {
				env[name] = value
			}
		}
	}
	headers, err := ResolveHeadersOrThrow(rawHeaders, fmt.Sprintf("provider %q", providerID), env)
	if err != nil {
		return nil, err
	}
	copy := *result
	copy.Auth, err = withConfiguredModelAuth(result.Auth, headers, authHeader)
	if err != nil {
		return nil, err
	}
	return &copy, nil
}

func withConfiguredModelAuth(auth aiauth.ModelAuth, configured map[string]string, authHeader bool) (aiauth.ModelAuth, error) {
	headers := cloneProviderHeaders(auth.Headers)
	if len(configured) > 0 && headers == nil {
		headers = make(ai.ProviderHeaders, len(configured))
	}
	for name, value := range configured {
		setProviderHeader(headers, name, &value)
	}
	if authHeader {
		if auth.APIKey == nil || *auth.APIKey == "" {
			return aiauth.ModelAuth{}, errors.New("authHeader requires a resolved API key")
		}
		if headers == nil {
			headers = make(ai.ProviderHeaders, 1)
		}
		value := "Bearer " + *auth.APIKey
		setProviderHeader(headers, "Authorization", &value)
	}
	auth.Headers = headers
	return auth, nil
}

func setProviderHeader(headers ai.ProviderHeaders, name string, value *string) {
	for existing := range headers {
		if strings.EqualFold(existing, name) {
			delete(headers, existing)
		}
	}
	if value == nil {
		headers[name] = nil
		return
	}
	copy := *value
	headers[name] = &copy
}

func providerDisplayNameFromLayers(
	id string,
	modelsConfig *ModelConfig,
	configs map[string]extensions.ProviderConfig,
	native map[string]extensions.Provider,
) string {
	if config, ok := configs[id]; ok && config.Defined["name"] {
		return config.Name
	}
	if modelsConfig != nil {
		if config, ok := modelsConfig.Providers[id]; ok && config.Name != nil {
			return *config.Name
		}
	}
	if provider, ok := native[id]; ok && provider.Name != "" {
		return provider.Name
	}
	if provider, ok := providers.Get(ai.ProviderID(id)); ok {
		return provider.Name
	}
	if config, ok := configs[id]; ok && config.Name != "" {
		return config.Name
	}
	return id
}

type extensionOAuth struct {
	provider *extensions.OAuthProvider
}

func (method extensionOAuth) Name() string { return method.provider.Name }

func (method extensionOAuth) Login(ctx context.Context, interaction aiauth.AuthInteraction) (*aiauth.Credential, error) {
	credentials, err := method.provider.Login(ctx, extensions.OAuthLoginCallbacks{
		Signal: ctx,
		OnAuth: func(info extensions.OAuthAuthInfo) {
			interaction.Notify(aiauth.AuthEvent{Type: aiauth.EventAuthURL, URL: info.URL, Instructions: info.Instructions})
		},
		OnDeviceCode: func(info extensions.OAuthDeviceCodeInfo) {
			interaction.Notify(aiauth.AuthEvent{Type: aiauth.EventDeviceCode, UserCode: info.UserCode, VerificationURI: info.VerificationURI, IntervalSeconds: info.IntervalSeconds, ExpiresInSeconds: info.ExpiresInSeconds})
		},
		OnPrompt: func(prompt extensions.OAuthPrompt) (string, error) {
			return interaction.Prompt(ctx, aiauth.AuthPrompt{Type: aiauth.PromptText, Message: prompt.Message, Placeholder: prompt.Placeholder})
		},
		OnProgress: func(message string) {
			interaction.Notify(aiauth.AuthEvent{Type: aiauth.EventProgress, Message: message})
		},
		OnManualCodeInput: func() (string, error) {
			return interaction.Prompt(ctx, aiauth.AuthPrompt{Type: aiauth.PromptManualCode, Message: "Enter authorization code"})
		},
		OnSelect: func(prompt extensions.OAuthSelectPrompt) (*string, error) {
			options := make([]aiauth.PromptOption, len(prompt.Options))
			for index, option := range prompt.Options {
				options[index] = aiauth.PromptOption{ID: option.ID, Label: option.Label}
			}
			selected, err := interaction.Prompt(ctx, aiauth.AuthPrompt{Type: aiauth.PromptSelect, Message: prompt.Message, Options: options})
			if err != nil {
				return nil, err
			}
			return &selected, nil
		},
	})
	if err != nil {
		return nil, err
	}
	return oauthCredential(credentials), nil
}

func (method extensionOAuth) Refresh(ctx context.Context, credential *aiauth.Credential) (*aiauth.Credential, error) {
	refreshed, err := method.provider.RefreshToken(ctx, extensionCredentials(credential))
	if err != nil {
		return nil, err
	}
	return oauthCredential(refreshed), nil
}

func (method extensionOAuth) ToAuth(credential *aiauth.Credential) (aiauth.ModelAuth, error) {
	key, err := method.provider.GetAPIKey(extensionCredentials(credential))
	if err != nil {
		return aiauth.ModelAuth{}, err
	}
	return aiauth.ModelAuth{APIKey: &key}, nil
}

type configuredOAuth struct {
	aiauth.OAuth
	providerID string
	headers    map[string]string
	authHeader bool
}

func (method configuredOAuth) LoginLabel() string {
	labeled, ok := method.OAuth.(aiauth.OAuthLoginLabel)
	if !ok {
		return ""
	}
	return labeled.LoginLabel()
}

func (method configuredOAuth) ToAuth(credential *aiauth.Credential) (aiauth.ModelAuth, error) {
	auth, err := method.OAuth.ToAuth(credential)
	if err != nil {
		return aiauth.ModelAuth{}, err
	}
	var env map[string]string
	if credential != nil {
		env = credential.Env
	}
	headers, err := ResolveHeadersOrThrow(method.headers, fmt.Sprintf("provider %q", method.providerID), env)
	if err != nil {
		return aiauth.ModelAuth{}, err
	}
	return withConfiguredModelAuth(auth, headers, method.authHeader)
}

func oauthCredential(credentials extensions.OAuthCredentials) *aiauth.Credential {
	result := aiauth.OAuthCredential(credentials.Refresh, credentials.Access, credentials.Expires)
	for name, value := range credentials.Extra {
		encoded, _ := json.Marshal(value)
		result.SetExtra(name, encoded)
	}
	return result
}

func extensionCredentials(credential *aiauth.Credential) extensions.OAuthCredentials {
	if credential == nil {
		return extensions.OAuthCredentials{}
	}
	result := extensions.OAuthCredentials{Refresh: credential.Refresh, Access: credential.Access, Expires: credential.Expires}
	if len(credential.Extra) > 0 {
		result.Extra = make(map[string]any, len(credential.Extra))
		for name, raw := range credential.Extra {
			var value any
			_ = json.Unmarshal(raw, &value)
			result.Extra[name] = value
		}
	}
	return result
}

var providerStoreMu sync.Mutex

type providerModelStore struct {
	path string
	id   string
}

func newProviderModelStore(path, id string) extensions.ProviderModelStore {
	return providerModelStore{path: path, id: id}
}

func (store providerModelStore) Read(_ context.Context) (entry *extensions.ProviderModelsStoreEntry, err error) {
	unlock, err := lockProviderStore(store.path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	values, err := readProviderStore(store.path)
	if err != nil {
		return nil, err
	}
	stored, ok := values[store.id]
	if !ok {
		return nil, nil
	}
	return &stored, nil
}

func (store providerModelStore) Write(_ context.Context, entry extensions.ProviderModelsStoreEntry) (err error) {
	unlock, err := lockProviderStore(store.path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	values, err := readProviderStore(store.path)
	if err != nil {
		return err
	}
	values[store.id] = entry
	return writeProviderStore(store.path, values)
}

func (store providerModelStore) Delete(_ context.Context) (err error) {
	unlock, err := lockProviderStore(store.path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	values, err := readProviderStore(store.path)
	if err != nil {
		return err
	}
	delete(values, store.id)
	return writeProviderStore(store.path, values)
}

func lockProviderStore(path string) (func() error, error) {
	providerStoreMu.Lock()
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		providerStoreMu.Unlock()
		return nil, err
	}
	return func() error {
		err := lock.Unlock()
		providerStoreMu.Unlock()
		return err
	}, nil
}

func readProviderStore(path string) (map[string]extensions.ProviderModelsStoreEntry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]extensions.ProviderModelsStoreEntry), nil
	}
	if err != nil {
		return nil, err
	}
	var values map[string]extensions.ProviderModelsStoreEntry
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func writeProviderStore(path string, values map[string]extensions.ProviderModelsStoreEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".models-store-extension-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
