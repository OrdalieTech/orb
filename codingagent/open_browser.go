package codingagent

import (
	"context"

	"github.com/OrdalieTech/pigo/ai"
)

// DefaultModelIDForProvider reports the upstream defaultModelPerProvider entry
// for a provider so the interactive login completion can name the missing
// default in its diagnostics (upstream interactive-mode.ts
// completeProviderAuthentication). It lives here only because this is the
// package-internal seam owned by the login work; model_resolver.go owns the
// table itself.
func DefaultModelIDForProvider(provider string) (string, bool) {
	id, exists := defaultModelPerProvider[provider]
	return id, exists
}

// ProviderAPIKey resolves the API key the runtime would send for provider, the
// seam behind the TUI's Anthropic subscription-auth warning (upstream
// modelRuntime.getAuth(...)?.auth.apiKey in
// maybeWarnAboutAnthropicSubscriptionAuth). It lives here with the other
// login-flow seams owned by this work.
func (runtime *SessionRuntime) ProviderAPIKey(ctx context.Context, provider ai.ProviderID) (string, error) {
	if runtime == nil {
		return "", nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if runtime.getRequestAuth != nil {
		resolved, err := runtime.getRequestAuth(ctx, provider)
		if err != nil || resolved == nil || resolved.APIKey == nil {
			return "", err
		}
		return *resolved.APIKey, nil
	}
	if runtime.getAPIKey != nil {
		key, err := runtime.getAPIKey(ctx, provider)
		if err != nil || key == nil {
			return "", err
		}
		return *key, nil
	}
	return "", nil
}

// WarnAnthropicExtraUsage reads the warnings.anthropicExtraUsage settings gate
// for the Anthropic subscription-auth warning (upstream
// settingsManager.getWarnings().anthropicExtraUsage; default true).
func (runtime *SessionRuntime) WarnAnthropicExtraUsage() bool {
	if runtime == nil || runtime.settings == nil {
		return true
	}
	return runtime.settings.GetWarningAnthropicExtraUsage()
}
