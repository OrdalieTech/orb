package providers

import (
	"context"
	"fmt"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/auth"
)

type bedrockAuth struct{}

func (bedrockAuth) Name() string { return "AWS credentials or bearer token" }

func (bedrockAuth) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	method, err := interaction.Prompt(ctx, auth.AuthPrompt{
		Type: auth.PromptSelect, Message: "Select Amazon Bedrock authentication method:",
		Options: []auth.PromptOption{
			{ID: "bearer-token", Label: "Bearer token"},
			{ID: "aws-profile", Label: "AWS profile"},
			{ID: "credential-chain", Label: "Existing AWS credential chain"},
		},
	})
	if err != nil {
		return nil, err
	}
	if method == "bearer-token" {
		key, err := interaction.Prompt(ctx, auth.AuthPrompt{Type: auth.PromptSecret, Message: "Enter Amazon Bedrock bearer token"})
		if err != nil {
			return nil, err
		}
		return auth.APIKeyCredential(key), nil
	}
	interaction.Notify(auth.AuthEvent{
		Type:    auth.EventInfo,
		Message: "Amazon Bedrock supports AWS profiles, IAM credentials, and role-based credentials.",
		Links: []auth.AuthInfoLink{{
			Label: "AWS credential provider chain",
			URL:   "https://docs.aws.amazon.com/sdkref/latest/guide/standardized-credentials.html",
		}},
	})
	if method == "aws-profile" {
		profile, err := interaction.Prompt(ctx, auth.AuthPrompt{Type: auth.PromptText, Message: "Enter AWS profile name"})
		if err != nil {
			return nil, err
		}
		return auth.APIKeyEnvCredential(map[string]string{"AWS_PROFILE": profile}, "AWS_PROFILE"), nil
	}
	if method != "credential-chain" {
		return nil, fmt.Errorf("Unknown Amazon Bedrock auth method: %s", method) //nolint:staticcheck // Exact upstream text.
	}
	if _, err := interaction.Prompt(ctx, auth.AuthPrompt{
		Type: auth.PromptText, Message: "Configure AWS credentials, then press Enter to continue",
	}); err != nil {
		return nil, err
	}
	return &auth.Credential{Type: auth.CredentialAPIKey}, nil
}

func (bedrockAuth) Resolve(
	ctx context.Context,
	authContext auth.AuthContext,
	credential *auth.Credential,
) (*auth.AuthResult, error) {
	if credential != nil && credential.Key != nil && *credential.Key != "" {
		key := *credential.Key
		return &auth.AuthResult{Auth: auth.ModelAuth{APIKey: &key}, Source: "stored credential"}, nil
	}
	if value, _ := authContext.Env(ctx, "AWS_BEARER_TOKEN_BEDROCK"); value != "" {
		return &auth.AuthResult{Source: "AWS_BEARER_TOKEN_BEDROCK"}, nil
	}
	profileStored := false
	if credential != nil && credential.Env != nil {
		profile, exists := credential.Env["AWS_PROFILE"]
		profileStored = exists
		if profile != "" {
			return &auth.AuthResult{Env: cloneProviderEnv(credential.Env), Source: "stored credential"}, nil
		}
	}
	if !profileStored {
		if value, _ := authContext.Env(ctx, "AWS_PROFILE"); value != "" {
			return &auth.AuthResult{Source: "AWS_PROFILE"}, nil
		}
	}
	if value, _ := authContext.Env(ctx, "AWS_ACCESS_KEY_ID"); value != "" {
		if secret, _ := authContext.Env(ctx, "AWS_SECRET_ACCESS_KEY"); secret != "" {
			return &auth.AuthResult{Source: "AWS access keys"}, nil
		}
	}
	if value, _ := authContext.Env(ctx, "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"); value != "" {
		return &auth.AuthResult{Source: "ECS task role"}, nil
	}
	if value, _ := authContext.Env(ctx, "AWS_CONTAINER_CREDENTIALS_FULL_URI"); value != "" {
		return &auth.AuthResult{Source: "ECS task role"}, nil
	}
	if value, _ := authContext.Env(ctx, "AWS_WEB_IDENTITY_TOKEN_FILE"); value != "" {
		return &auth.AuthResult{Source: "web identity token"}, nil
	}
	return nil, nil
}

var amazonBedrockProvider = Provider{
	ID:   "amazon-bedrock",
	Name: "Amazon Bedrock",
	API:  ai.APIBedrockConverse,
	Auth: AuthAPIKey,
	Env: []string{
		"AWS_BEARER_TOKEN_BEDROCK",
		"AWS_PROFILE",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_WEB_IDENTITY_TOKEN_FILE",
	},
	Methods: auth.ProviderAuth{APIKey: bedrockAuth{}},
}

func AmazonBedrock() Provider {
	return cloneProvider(amazonBedrockProvider)
}
