package providers

import (
	"context"

	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
)

const (
	anthropicAuthTokenEnv  = "ANTHROPIC_AUTH_TOKEN"
	anthropicOAuthTokenEnv = "ANTHROPIC_OAUTH_TOKEN"
	anthropicAPIKeyEnv     = "ANTHROPIC_API_KEY"
)

// anthropicAPIKeyAuth ports upstream anthropicApiKeyAuth()
// (ai/src/providers/anthropic.ts): a stored credential still wins outright,
// then ANTHROPIC_AUTH_TOKEN resolves to a bearer Authorization header AHEAD of
// the ANTHROPIC_OAUTH_TOKEN/ANTHROPIC_API_KEY api-key envs.
type anthropicAPIKeyAuth struct{}

func (anthropicAPIKeyAuth) Name() string { return "Anthropic API key" }

func (anthropicAPIKeyAuth) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	key, err := interaction.Prompt(ctx, auth.AuthPrompt{Type: auth.PromptSecret, Message: "Enter Anthropic API key"})
	if err != nil {
		return nil, err
	}
	return auth.APIKeyCredential(key), nil
}

func (anthropicAPIKeyAuth) Resolve(
	ctx context.Context,
	authContext auth.AuthContext,
	credential *auth.Credential,
) (*auth.AuthResult, error) {
	if credential != nil && credential.Key != nil && *credential.Key != "" {
		key := *credential.Key
		return &auth.AuthResult{Auth: auth.ModelAuth{APIKey: &key}, Env: credential.Clone().Env, Source: "stored credential"}, nil
	}
	if token, ok := authContext.Env(ctx, anthropicAuthTokenEnv); ok {
		bearer := "Bearer " + token
		return &auth.AuthResult{
			Auth:   auth.ModelAuth{Headers: map[string]*string{"Authorization": &bearer}},
			Source: anthropicAuthTokenEnv,
		}, nil
	}
	for _, name := range []string{anthropicOAuthTokenEnv, anthropicAPIKeyEnv} {
		if value, ok := authContext.Env(ctx, name); ok {
			key := value
			return &auth.AuthResult{Auth: auth.ModelAuth{APIKey: &key}, Source: name}, nil
		}
	}
	return nil, nil
}

var anthropicProvider = Provider{
	ID:   "anthropic",
	Name: "Anthropic",
	Auth: AuthAPIKey,
	Methods: auth.ProviderAuth{
		APIKey: anthropicAPIKeyAuth{},
		OAuth:  oauth.NewAnthropic(nil),
	},
}

func Anthropic() Provider { return registered("anthropic") }
