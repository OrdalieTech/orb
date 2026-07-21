package providers

import (
	"context"

	"github.com/OrdalieTech/pigo/ai/auth"
)

const (
	cloudflareAPIKey    = "CLOUDFLARE_API_KEY"
	cloudflareAccountID = "CLOUDFLARE_ACCOUNT_ID"
	cloudflareGatewayID = "CLOUDFLARE_GATEWAY_ID"
)

type cloudflareAuthKind uint8

const (
	cloudflareWorkersAuth cloudflareAuthKind = iota
	cloudflareGatewayAuth
)

type cloudflareAPIKeyAuth struct {
	kind cloudflareAuthKind
}

var cloudflareWorkersAIProvider = Provider{
	ID: "cloudflare-workers-ai", Name: "Cloudflare Workers AI", Auth: AuthAPIKey,
	Methods: auth.ProviderAuth{APIKey: cloudflareWorkersAIAuth()},
}

var cloudflareAIGatewayProvider = Provider{
	ID: "cloudflare-ai-gateway", Name: "Cloudflare AI Gateway", Auth: AuthAPIKey,
	Methods: auth.ProviderAuth{APIKey: cloudflareAIGatewayAuth()},
}

func cloudflareWorkersAIAuth() auth.APIKeyAuth {
	return cloudflareAPIKeyAuth{kind: cloudflareWorkersAuth}
}

func cloudflareAIGatewayAuth() auth.APIKeyAuth {
	return cloudflareAPIKeyAuth{kind: cloudflareGatewayAuth}
}

func (cloudflareAPIKeyAuth) Name() string { return "Cloudflare API key" }

func (method cloudflareAPIKeyAuth) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	key, err := interaction.Prompt(ctx, auth.AuthPrompt{Type: auth.PromptSecret, Message: "Enter Cloudflare API key"})
	if err != nil {
		return nil, err
	}
	accountID, err := interaction.Prompt(ctx, auth.AuthPrompt{Type: auth.PromptText, Message: "Enter Cloudflare account ID"})
	if err != nil {
		return nil, err
	}
	credential := auth.APIKeyCredential(key)
	credential.Env = map[string]string{cloudflareAccountID: accountID}
	if method.kind == cloudflareGatewayAuth {
		gatewayID, promptErr := interaction.Prompt(ctx, auth.AuthPrompt{Type: auth.PromptText, Message: "Enter Cloudflare AI Gateway ID"})
		if promptErr != nil {
			return nil, promptErr
		}
		credential.Env[cloudflareGatewayID] = gatewayID
	}
	return credential, nil
}

func (method cloudflareAPIKeyAuth) Resolve(
	ctx context.Context,
	authContext auth.AuthContext,
	credential *auth.Credential,
) (*auth.AuthResult, error) {
	apiKey := cloudflareResolvedValue(ctx, authContext, credential, cloudflareAPIKey)
	accountID := cloudflareResolvedValue(ctx, authContext, credential, cloudflareAccountID)
	if apiKey == "" || accountID == "" {
		return nil, nil
	}
	env := map[string]string{cloudflareAccountID: accountID}
	source := cloudflareAPIKey
	if credential != nil {
		source = "stored credential"
	}
	if method.kind == cloudflareGatewayAuth {
		gatewayID := cloudflareResolvedValue(ctx, authContext, credential, cloudflareGatewayID)
		if gatewayID == "" {
			return nil, nil
		}
		env[cloudflareGatewayID] = gatewayID
		bearer := "Bearer " + apiKey
		return &auth.AuthResult{
			Auth: auth.ModelAuth{Headers: map[string]*string{
				"cf-aig-authorization": &bearer,
				"Authorization":        nil,
				"x-api-key":            nil,
			}},
			Env: env, Source: source,
		}, nil
	}
	return &auth.AuthResult{
		Auth: auth.ModelAuth{APIKey: &apiKey},
		Env:  env, Source: source,
	}, nil
}

func cloudflareResolvedValue(
	ctx context.Context,
	authContext auth.AuthContext,
	credential *auth.Credential,
	name string,
) string {
	if credential != nil {
		if name == cloudflareAPIKey && credential.Key != nil {
			return *credential.Key
		}
		if name != cloudflareAPIKey && credential.Env != nil {
			if value, exists := credential.Env[name]; exists {
				return value
			}
		}
	}
	value, _ := authContext.Env(ctx, name)
	return value
}
